package close

import (
	"io"
	"sync"
	"testing"

	tap "github.com/amarbel-llc/tap/go"
)

// TestLineWriterConcurrentWrite is a regression test for the panic that
// occurred at session close when nixgc.Reap passed the same lineWriter
// as both stdout and stderr to nix-store. os/exec spawns one goroutine
// per stream; without serialization, the two goroutines raced on buf
// and crashed with "slice bounds out of range [N:0]".
//
// Run under `go test -race` to catch the race directly. Without -race,
// the test still exercises the contended path enough to surface the
// original slice-bounds panic.
func TestLineWriterConcurrentWrite(t *testing.T) {
	tw := tap.NewWriter(io.Discard)
	tw.OutputBlock("concurrent linewriter", func(ob *tap.OutputBlockWriter) *tap.Diagnostics {
		lw := &lineWriter{ob: ob}

		const writers = 16
		const perWriter = 500
		// A line long enough that several goroutines have at least one
		// full line buffered at once, maximising the window between the
		// IndexByte read and the buf reassignment that originally
		// raced.
		line := []byte("the quick brown fox jumps over the lazy dog\n")

		var wg sync.WaitGroup
		wg.Add(writers)
		for i := 0; i < writers; i++ {
			go func() {
				defer wg.Done()
				for j := 0; j < perWriter; j++ {
					if _, err := lw.Write(line); err != nil {
						t.Errorf("Write returned error: %v", err)
						return
					}
				}
			}()
		}
		wg.Wait()
		lw.Flush()
		return nil
	})
}

// TestLineWriterConcurrentMixedSizes exercises the path where the
// writes don't align on line boundaries — fragments arrive without
// newlines, then a later write completes the line. Mirrors how os/exec
// hands chunks of stdout/stderr through.
func TestLineWriterConcurrentMixedSizes(t *testing.T) {
	tw := tap.NewWriter(io.Discard)
	tw.OutputBlock("concurrent linewriter (mixed)", func(ob *tap.OutputBlockWriter) *tap.Diagnostics {
		lw := &lineWriter{ob: ob}

		const writers = 8
		const perWriter = 250
		// Four chunks per logical line: head, middle, tail, newline.
		// The newline lives in its own write to maximise the chance of
		// observing a partial buffer being mutated by a peer.
		chunks := [][]byte{
			[]byte("head-"),
			[]byte("middle-"),
			[]byte("tail"),
			[]byte("\n"),
		}

		var wg sync.WaitGroup
		wg.Add(writers)
		for i := 0; i < writers; i++ {
			go func() {
				defer wg.Done()
				for j := 0; j < perWriter; j++ {
					for _, c := range chunks {
						if _, err := lw.Write(c); err != nil {
							t.Errorf("Write returned error: %v", err)
							return
						}
					}
				}
			}()
		}
		wg.Wait()
		lw.Flush()
		return nil
	})
}
