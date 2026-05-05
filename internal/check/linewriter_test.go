package check

import (
	"io"
	"sync"
	"testing"

	tap "github.com/amarbel-llc/tap/go"
)

// TestLineWriterConcurrentWrite mirrors the regression test in
// internal/close: pre-merge hooks may attach the same writer to both
// stdout and stderr, so lineWriter.Write must tolerate concurrent
// callers. Without the mutex, the race detector fires and (with the
// right scheduling) Write panics with "slice bounds out of range".
func TestLineWriterConcurrentWrite(t *testing.T) {
	tw := tap.NewWriter(io.Discard)
	tw.OutputBlock("concurrent linewriter", func(ob *tap.OutputBlockWriter) *tap.Diagnostics {
		lw := &lineWriter{ob: ob}

		const writers = 16
		const perWriter = 500
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

// TestLineWriterConcurrentMixedSizes exercises the partial-line path
// where chunks arrive without newlines, then a later write completes
// the line. This is the shape that triggered the original
// "[37:0]" panic in production.
func TestLineWriterConcurrentMixedSizes(t *testing.T) {
	tw := tap.NewWriter(io.Discard)
	tw.OutputBlock("concurrent linewriter (mixed)", func(ob *tap.OutputBlockWriter) *tap.Diagnostics {
		lw := &lineWriter{ob: ob}

		const writers = 8
		const perWriter = 250
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
