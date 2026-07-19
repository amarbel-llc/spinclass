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
// Returns ErrAlreadyRunning if a job is already in flight for wt.
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

	j := &Job{
		ID:        id,
		Kind:      kind,
		Status:    StatusRunning,
		GitSync:   gitSync,
		ServePID:  os.Getpid(),
		StartedAt: time.Now(),
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

	// Snapshot before the goroutine launches: the goroutine owns j from here
	// on (status, result, clown job id), so handing the caller the live
	// pointer would be a data race one field-read away. The snapshot carries
	// the identity fields the caller renders (ID, Kind, StartedAt).
	snapshot := *j

	go func() {
		defer clearRunning()
		defer func() { _ = logf.Close() }()

		// Allocate the matching clown job-wakeup entry (clown RFC-0009) when
		// running under clown, so the terminal emit below can wake the agent.
		// Inside the goroutine so a slow clown never delays Start's
		// immediate-return contract. Emit failures are logged, never fatal:
		// spinclass's job.json/job.log remain the system of record, clown is
		// only the wake layer.
		if clown.Enabled() {
			if cid, cerr := clown.StartJob(context.Background(), kind, clown.Source); cerr != nil {
				_, _ = fmt.Fprintf(logf, "[clown] job-start emit failed: %v\n", cerr)
			} else {
				j.ClownJobID = cid
				_ = Write(wt, j)
			}
		}

		text, isErr := fn(ctx, logf)

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
			if cerr := clown.FinishJob(context.Background(), j.ClownJobID, j.Status, msg, "spinclass session-job-status"); cerr != nil {
				_, _ = fmt.Fprintf(logf, "[clown] job-done emit failed: %v\n", cerr)
			}
		}
	}()

	return &snapshot, nil
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
// distinguish "finished" from "never started". session-job-wait selects on
// this so it can block on a running job without polling.
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
