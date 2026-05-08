package nixgc

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
)

// concurrentRunner mimics what os/exec does: spawns one goroutine per
// stream so outW and errW Writes happen concurrently. Each stream
// emits a fixed banner "perStream" times. The "deleting '" marker
// goes to stdout once per iteration; the "still alive" marker goes to
// stderr once per iteration.
type concurrentRunner struct {
	perStream int
	err       error
}

func (c concurrentRunner) Output(_ string, _ ...string) ([]byte, error) {
	return nil, nil
}

func (c concurrentRunner) CombinedOutput(_ string, _ ...string) ([]byte, error) {
	return nil, nil
}

func (c concurrentRunner) Run(_ context.Context, outW, errW io.Writer, _ string, _ ...string) error {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < c.perStream; i++ {
			_, _ = io.WriteString(outW, "deleting '/nix/store/aaa-path'\n")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < c.perStream; i++ {
			_, _ = io.WriteString(errW, "warning: cannot delete path '/nix/store/bbb' since it is still alive\n")
		}
	}()
	wg.Wait()
	return c.err
}

// TestReapConcurrentCaptureNoRace is a regression test for the data
// race on Reap's internal capture buffer. nix-store streams progress
// to both stdout and stderr; os/exec drains the two pipes in separate
// goroutines, so the io.MultiWriter(outW, &captured) and
// io.MultiWriter(errW, &captured) calls write to `captured`
// concurrently. Without serialization (bytes.Buffer is not goroutine-
// safe), `go test -race` flags this and the buffer can be corrupted.
//
// The test fakes the runner with two concurrent emitters and asserts
// that the post-Run counters come out exactly right. With a corrupted
// buffer the Reclaimed/Kept counts can diverge from the inputs.
func TestReapConcurrentCaptureNoRace(t *testing.T) {
	const perStream = 200
	defer overrideRunner(concurrentRunner{perStream: perStream})()

	// A closure with one path per "still alive" stderr line plus one
	// per "deleting '" stdout line keeps `unaccounted` at 0 so the
	// happy path is exercised end-to-end.
	closure := make([]string, 0, perStream*2)
	for i := 0; i < perStream*2; i++ {
		closure = append(closure, "/nix/store/p")
	}
	plan := Plan{Closure: closure}

	got := Reap(plan, io.Discard, io.Discard)

	if got.Reclaimed != perStream {
		t.Errorf("Reclaimed = %d, want %d", got.Reclaimed, perStream)
	}
	if got.Kept != perStream {
		t.Errorf("Kept = %d, want %d", got.Kept, perStream)
	}
	if len(got.Errors) != 0 {
		t.Errorf("unexpected Errors: %v", got.Errors)
	}
}

// TestReapConcurrentCaptureFanOut exercises a higher-contention shape
// (more goroutines per stream, but only via two streams since that's
// what os/exec produces) and asserts that all bytes survive.
func TestReapConcurrentCaptureFanOut(t *testing.T) {
	const perStream = 1000
	defer overrideRunner(concurrentRunner{perStream: perStream})()

	plan := Plan{Closure: []string{"/nix/store/p"}}

	// Capture stdout/stderr explicitly so we can assert byte
	// preservation downstream from the internal sync buffer too.
	var outW, errW strings.Builder
	got := Reap(plan, &outW, &errW)

	wantOut := strings.Repeat("deleting '/nix/store/aaa-path'\n", perStream)
	wantErr := strings.Repeat("warning: cannot delete path '/nix/store/bbb' since it is still alive\n", perStream)
	if outW.String() != wantOut {
		t.Errorf("stdout corrupted: len=%d want=%d", outW.Len(), len(wantOut))
	}
	if errW.String() != wantErr {
		t.Errorf("stderr corrupted: len=%d want=%d", errW.Len(), len(wantErr))
	}

	// Reap's Reclaimed/Kept counts derive from the internal capture
	// buffer; if the mutex is missing they can drop below the true
	// emission count.
	if got.Reclaimed != perStream {
		t.Errorf("Reclaimed = %d, want %d", got.Reclaimed, perStream)
	}
	if got.Kept != perStream {
		t.Errorf("Kept = %d, want %d", got.Kept, perStream)
	}
}
