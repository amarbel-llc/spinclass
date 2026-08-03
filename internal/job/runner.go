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

	// clearRunning runs exactly once per Start (an early-return path OR the
	// goroutine's defer, never both). Closing done last — after the goroutine
	// has written the final job record — lets WaitDone callers Read the
	// terminal status the moment they wake.
	clearRunning := func() {
		mu.Lock()
		delete(running, wt)
		mu.Unlock()
		cancel()
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
	// (spinclass#251). job.log stays the system of record and keeps driving
	// LastActivity's mtime signal, so this is purely additive: every failure
	// path here leaves the job running with an empty spool, exactly as before.
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
			j.Status = StatusCancelled
		case isErr:
			j.Status = StatusFailed
		default:
			j.Status = StatusSucceeded
		}
		_ = Write(wt, j)

		// Terminal wake emit, after the job record is durable (store before
		// wake — a woken agent reading session-job-status must see the
		// terminal state). Statuses map 1:1 onto RFC-0009 terminal types.
		if j.ClownJobID != "" {
			msg := kind + " " + j.Status
			if j.Status == StatusFailed {
				if line := firstFailureLine(text); line != "" {
					msg += ": " + line
				}
			}

			// Attach the rendered verdict ladder by reference so the wake
			// carries its own result (#251 piece 2b). Before this the terminal
			// record's only pointer was `result_ref: "spinclass
			// session-job-status"` — a wake that says "call my other tool",
			// which is exactly what made the retire-or-keep question in #251
			// hard to answer from usage.
			//
			// The two are alternatives, not companions: when the blob exists
			// it IS the result, so repeating a pointer to a tool that serves
			// the same bytes is noise. result_ref stays as the fallback for a
			// build with no madder pin, where there is no blob to point at.
			resource := storeResultBlob(wt, text, logf)
			resultRef := ""
			if resource == "" {
				resultRef = "spinclass session-job-status"
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

// TailLog returns up to n trailing lines of the worktree's job log, for
// surfacing live activity in session-job-status. Missing log -> "".
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
