package job

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"code.linenisgreat.com/spinclass/internal/clown"
	"code.linenisgreat.com/spinclass/internal/embeds"
	"code.linenisgreat.com/spinclass/internal/madder"
)

// Func is the unit of work a background job runs. It receives a context that
// is cancelled by Cancel and a writer for the pre-merge hook's live output
// (streamed to job.log). It returns the rendered result text (the same plain
// verdict lines — "✓ <desc>"/"✗ <desc>" from present.RenderPlain — the
// synchronous tool produces) and whether that result is an error.
type Func func(ctx context.Context, hookOutput io.Writer) (text string, isErr bool)

type runEntry struct {
	cancel context.CancelFunc
	done   chan struct{} // closed after the final job record is written
}

var (
	mu      sync.Mutex
	running = map[string]*runEntry{} // worktree abs path -> in-flight job
)

// ErrAlreadyRunning is returned by Start when a job is already in flight for
// the worktree within this serve process.
var ErrAlreadyRunning = fmt.Errorf("a job is already running for this session")

// Start launches fn in a background goroutine for worktree wt, writes a
// running Job record, and streams fn's hook output to job.log. It returns the
// created Job immediately. The goroutine outlives the caller (serve is a
// long-lived process); on completion it persists the final status + result.
//
// The returned Job.ID is the clown/ringmaster job id when running under clown,
// so it matches the id the completion wake reports (#243); the caller's id
// argument is the fallback used when clown is not enabled.
//
// Returns ErrAlreadyRunning if a job is already in flight for wt, and refuses
// to dispatch at all if the clown job-wakeup allocation fails while clown is
// enabled (see below).
func Start(wt, kind string, gitSync bool, id string, fn Func) (*Job, error) {
	mu.Lock()
	if _, ok := running[wt]; ok {
		mu.Unlock()
		return nil, ErrAlreadyRunning
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	running[wt] = &runEntry{cancel: cancel, done: done}
	mu.Unlock()

	// releaseFlock is set once the per-job liveness lock is acquired (below), and
	// released by clearRunning. Captured by reference: it is nil on the
	// early-return paths (which run before the acquire) and non-nil only for the
	// goroutine's terminal defer, so the nil guard in clearRunning covers both.
	// The assignment happens-before the goroutine launch, so the defer's read is
	// race-free.
	var releaseFlock func() error

	// observerDone is closed by the cancel-observer goroutine when it returns.
	// Set by reference (like releaseFlock) only when that goroutine launches, so
	// it is nil on the early-return paths and non-nil once there is an observer
	// to join. The assignment happens-before the job goroutine launches, so
	// clearRunning's read from that goroutine is race-free.
	var observerDone chan struct{}

	// clearRunning runs exactly once per Start (an early-return path OR the
	// goroutine's defer, never both). cancel() tears down the cancel observer,
	// whose `ringmaster wait` runs under this same ctx; clearRunning then JOINS
	// that observer (<-observerDone) before closing done, so a woken WaitDone
	// caller is guaranteed the observer's subprocess is already reaped. Without
	// the join the subprocess can outlive the (possibly instant) job body and
	// still be writing when a woken caller acts — in tests that is a t.TempDir
	// RemoveAll racing the stub's writes (ENOTEMPTY, seen only under load); in
	// production it is a job reporting done with a child still alive. Releasing
	// the flock after the goroutine body has written the final job record (defers
	// run LIFO, so this registered-first defer runs last) keeps the "lock held ⟺
	// job running" invariant the probe relies on. Closing done last lets WaitDone
	// callers Read the terminal status the moment they wake.
	clearRunning := func() {
		mu.Lock()
		delete(running, wt)
		mu.Unlock()
		cancel()
		if releaseFlock != nil {
			_ = releaseFlock()
		}
		if observerDone != nil {
			<-observerDone
		}
		close(done)
	}

	// Allocate the clown job-wakeup entry (clown RFC-0009) BEFORE launching the
	// goroutine, so the id handed back to the caller is the SAME id the
	// completion wake will carry (#243). Previously this ran inside the
	// goroutine and the two ids diverged: an agent that recorded the dispatch
	// id saw a wake bearing an unfamiliar one and reasonably concluded the
	// notification belonged to another session. The confusion was sharpened by
	// the two schemes rhyming — ringmaster mints `<label>-<hash>` and the label
	// is `kind`, so `merge-b009b2b1` sat next to a local `merge-<unix-ts>`.
	//
	// Dispatch-time allocation is what `ringmaster start` is for, and it costs
	// ~7ms (measured; almost entirely process startup) against a dispatch that
	// has already paid for PrepareMerge's network `git pull` and rebase. The
	// earlier "a slow clown must not delay Start's immediate-return contract"
	// rationale guarded a cost that is not there; ringmaster's own emit
	// deadline remains the backstop against a genuinely wedged binary.
	//
	// Not running under clown at all is fine — the job just gets no wake, the
	// pre-clown behaviour. But a FAILED allocation while clown IS enabled is a
	// hard refusal: the wake is how the agent learns an async job finished, so
	// dispatching without one produces a job that completes into silence, which
	// is worse than not dispatching. The error carries the local id we would
	// have used plus the identifying context, so the failure is debuggable
	// without digging for the job record that was never written.
	clownID := ""
	if clown.Enabled() {
		cid, cerr := clown.StartJob(context.Background(), kind, clown.Source)
		if cerr != nil {
			clearRunning()
			return nil, fmt.Errorf(
				"allocating the clown job-wakeup entry failed, so this %s job would run with no "+
					"completion wake; refusing to dispatch (local job id would have been %q, "+
					"worktree %q): %w",
				kind, id, wt, cerr,
			)
		}
		clownID = cid
		id = cid
	}

	j := &Job{
		ID:         id,
		ClownJobID: clownID,
		Kind:       kind,
		Status:     StatusRunning,
		GitSync:    gitSync,
		ServePID:   os.Getpid(),
		StartedAt:  time.Now(),
	}
	if err := Write(wt, j); err != nil {
		clearRunning()
		return nil, err
	}

	logf, err := os.Create(LogPath(wt))
	if err != nil {
		clearRunning()
		return nil, err
	}

	// Tee the hook's output into ringmaster's spool as well, so its native
	// surface (`ringmaster status --tail`, `ringmaster tail -f`) can show a
	// running job instead of the `spool_bytes: 0` it reported before
	// (spinclass#251). job.log stays the system of record, so this is purely
	// additive: every failure path here leaves the job running with an empty
	// spool, exactly as before.
	out := io.Writer(logf)
	var spoolf *os.File
	if clownID != "" {
		if p, perr := clown.SpoolPath(context.Background(), clownID); perr == nil {
			if f, ferr := os.Create(p); ferr == nil {
				spoolf = f
				out = io.MultiWriter(logf, spoolf)
			} else {
				_, _ = fmt.Fprintf(logf, "[clown] spool open failed: %v\n", ferr)
			}
		} else {
			_, _ = fmt.Fprintf(logf, "[clown] spool-path failed: %v\n", perr)
		}
	}

	// Hold ringmaster's per-job advisory lock for the job's lifetime (#26,
	// RFC-0016): the OS releases it when serve dies, so a crashed producer's
	// job is declared `gone` by the liveness probe instead of hanging in
	// `running` forever. In-process by necessity — a CLI shell-out would drop
	// the lock the instant it exits. Gated on an exact ProtocolVersion match:
	// on a skew the linked jobwake's lock-path derivation may disagree with the
	// running ringmaster's probe, so skip the flock (serve-start already warned
	// loudly). Any acquire failure — including ErrAlreadyLocked — is non-fatal:
	// the job still runs and wakes, it just carries no liveness signal.
	// Released by clearRunning after the terminal record is written, or by the
	// OS on crash. clown.FlockEnabled is the cached serve-start ProtocolVersion
	// verdict — a plain flag read, no per-dispatch shell-out.
	if clownID != "" && clown.FlockEnabled() {
		if rel, lerr := clown.AcquireJobLock(clownID); lerr != nil {
			_, _ = fmt.Fprintf(logf,
				"[clown] per-job liveness lock not acquired (job runs without a liveness signal): %v\n", lerr)
		} else {
			releaseFlock = rel
		}
	}

	// Observe an external cancellation (#22, RFC-0018). `ringmaster cancel`
	// records a non-terminal `cancel-requested` rather than signalling a
	// process, so the owning producer must observe it and tear down. This
	// goroutine blocks in clown.WaitForCancel (`ringmaster wait --on-cancel`)
	// and, on a cancel-requested, fires the job's context cancel — killing the
	// hook exactly as an in-process cancel does, after which fn returns and the
	// goroutine below writes the terminal `aborted`, keeping the producer the
	// sole terminal-writer. CLI-based so it survives a ProtocolVersion skew
	// (where the flock is off but the runtime cancel surface still works). Gated
	// on clownID (there is a ringmaster job to observe). It runs under the job's
	// own ctx, so when the job ends by any other path, clearRunning's cancel()
	// kills the `ringmaster wait` subprocess and this goroutine exits — no
	// separate cancel to track.
	if clownID != "" {
		observerDone = make(chan struct{})
		go func() {
			defer close(observerDone)
			canceled, werr := clown.WaitForCancel(ctx, clownID)
			if werr != nil {
				// ctx cancelled (the job ended first) or the runtime ringmaster
				// lacks --on-cancel — either way, nothing to observe.
				return
			}
			if canceled {
				_, _ = fmt.Fprintf(logf, "[clown] external cancel-requested observed; tearing down the hook\n")
				cancel()
			}
		}()
	}

	// Snapshot before the goroutine launches: the goroutine owns j from here
	// on (status, result, clown job id), so handing the caller the live
	// pointer would be a data race one field-read away. The snapshot carries
	// the identity fields the caller renders (ID, Kind, StartedAt).
	snapshot := *j

	go func() {
		defer clearRunning()
		defer func() { _ = logf.Close() }()
		defer func() {
			if spoolf != nil {
				_ = spoolf.Close()
			}
		}()

		text, isErr := fn(ctx, out)

		end := time.Now()
		j.EndedAt = &end
		j.ResultText = text
		j.ResultIsErr = isErr
		switch {
		case ctx.Err() != nil:
			// The context was cancelled — by the #22 observer on an external
			// cancel-requested, by session-job-cancel, or by the inactivity
			// watchdog. All are the producer tearing down in response to a
			// cancellation, so the terminal is `aborted` (RFC-0018).
			j.Status = StatusAborted
		case isErr:
			j.Status = StatusFailed
		default:
			j.Status = StatusSucceeded
		}
		_ = Write(wt, j)

		// Terminal wake emit, after the job record is durable (store before
		// wake — a woken agent inspecting the job via ringmaster must see the
		// terminal state). Statuses map 1:1 onto RFC-0009 terminal types.
		if j.ClownJobID != "" {
			msg := kind + " " + j.Status
			if j.Status == StatusFailed {
				if line := firstFailureLine(text); line != "" {
					msg += ": " + line
				}
			}

			// Attach the rendered verdict ladder by reference so the wake
			// carries its own result (#251 piece 2b). The two are alternatives,
			// not companions: when the blob exists it IS the result, so a pointer
			// alongside it is noise. result_ref stays as the fallback for a build
			// with no madder pin, where there is no blob to point at — it now
			// names ringmaster's own read surface (spinclass's session-job-status
			// was retired in #23), which resolves against this same job id.
			resource := storeResultBlob(wt, text, logf)
			resultRef := ""
			if resource == "" {
				resultRef = "ringmaster read " + j.ClownJobID
			}

			if cerr := clown.FinishJob(context.Background(), j.ClownJobID, j.Status, msg, resultRef, resource); cerr != nil {
				_, _ = fmt.Fprintf(logf, "[clown] job-done emit failed: %v\n", cerr)
			}
		}
	}()

	return &snapshot, nil
}

// storeResultBlob writes the job's rendered verdict ladder to the worktree's
// madder blob store and returns its `madder://blobs/<digest>` URI, for
// attachment to the terminal wake (#251 piece 2b).
//
// Returns "" whenever no blob could be produced — no madder pin (the default
// build leaves it dormant, FDR 0003), an uninitialised store, a spawn or write
// failure. That is a degrade, never an error: the job has already finished and
// its result is durable in job.json, so failing the wake over a missing
// attachment would trade a working notification for none. Failures are noted
// in job.log so a missing attachment is diagnosable rather than silent.
func storeResultBlob(wt, text string, logf io.Writer) string {
	if embeds.MadderBin() == "" {
		return ""
	}
	w, finish, err := madder.Write(wt, embeds.MadderBin())
	if err != nil {
		_, _ = fmt.Fprintf(logf, "[clown] result blob: madder write failed to start: %v\n", err)
		return ""
	}
	if _, err := io.WriteString(w, text); err != nil {
		_ = w.Close()
		_, _ = finish() // reap the subprocess even on write failure
		_, _ = fmt.Fprintf(logf, "[clown] result blob: write failed: %v\n", err)
		return ""
	}
	if err := w.Close(); err != nil {
		_, _ = finish()
		_, _ = fmt.Fprintf(logf, "[clown] result blob: close failed: %v\n", err)
		return ""
	}
	id, err := finish()
	if err != nil {
		_, _ = fmt.Fprintf(logf, "[clown] result blob: %v\n", err)
		return ""
	}
	if id == "" {
		return ""
	}
	return "madder://blobs/" + id
}

// firstFailureLine returns the first plain-verdict failure line ("✗ <desc>",
// as rendered by present.RenderPlain) of the rendered result text, for a
// one-line wake message that names what broke.
func firstFailureLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "✗ ") {
			return line
		}
	}
	return ""
}

// Cancel signals the in-flight job for wt to stop (cancels its context, which
// kills the hook subprocess). Returns false if no job is running for wt in
// this serve process.
func Cancel(wt string) bool {
	mu.Lock()
	e, ok := running[wt]
	mu.Unlock()
	if !ok {
		return false
	}
	e.cancel()
	return true
}

// IsRunning reports whether a job is in flight for wt in this serve process.
func IsRunning(wt string) bool {
	mu.Lock()
	_, ok := running[wt]
	mu.Unlock()
	return ok
}

// WaitDone returns a channel that is closed once the in-flight job for wt has
// finished and its terminal record is persisted (so a Read after the channel
// closes observes the final status). If no job is in flight in this serve
// process, it returns an already-closed channel — the caller should Read to
// distinguish "finished" from "never started". A blocking-join primitive: it
// lets a caller wait on a running job without polling. The retired
// session-job-wait tool selected on this; it survives as a synchronization
// point for the job package's own tests.
func WaitDone(wt string) <-chan struct{} {
	mu.Lock()
	defer mu.Unlock()
	if e, ok := running[wt]; ok {
		return e.done
	}
	ch := make(chan struct{})
	close(ch)
	return ch
}

// TailLog returns up to n trailing lines of the worktree's job log. Now a
// test-only helper for asserting job.log content: the live-activity surface it
// fed, session-job-status, was retired in #23, and ringmaster tails its own
// spool copy of the same output (#251). Missing log -> "".
func TailLog(wt string, n int) string {
	f, err := os.Open(LogPath(wt))
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	lines := make([]string, 0, n)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
	}
	return strings.Join(lines, "\n")
}
