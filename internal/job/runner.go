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
)

// Func is the unit of work a background job runs. It receives a context that
// is cancelled by Cancel and a writer for the pre-merge hook's live output
// (streamed to job.log). It returns the rendered result text (the same TAP
// payload the synchronous tool produces) and whether that result is an error.
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

	go func() {
		defer clearRunning()
		defer logf.Close()

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
	}()

	return j, nil
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
	defer f.Close()
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
