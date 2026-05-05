// Package tapblock provides helpers for streaming bytes into a
// tap.OutputBlockWriter. The package's only export today is LineWriter,
// which buffers an arbitrary byte stream and forwards it to the
// underlying block one complete line at a time.
package tapblock

import (
	"bytes"
	"sync"

	tap "github.com/amarbel-llc/tap/go"
)

// LineWriter splits incoming bytes on '\n' and forwards each complete
// line to a tap.OutputBlockWriter. Partial trailing content is
// buffered until a newline arrives or Flush is called.
//
// Write and Flush are safe to call concurrently. Callers commonly hand
// the same LineWriter to both stdout and stderr of an os/exec.Cmd:
// exec spawns one goroutine per stream, and without serialization the
// two goroutines race on buf and can panic with
// "slice bounds out of range".
type LineWriter struct {
	ob  *tap.OutputBlockWriter
	mu  sync.Mutex
	buf []byte
}

// NewLineWriter constructs a LineWriter that forwards complete lines
// to ob. ob must remain valid for the lifetime of the LineWriter
// (i.e. until the enclosing OutputBlock callback returns).
func NewLineWriter(ob *tap.OutputBlockWriter) *LineWriter {
	return &LineWriter{ob: ob}
}

// Write appends p to the internal buffer and emits any complete lines
// it now contains. Always reports len(p) as the number of bytes
// consumed.
func (lw *LineWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	lw.buf = append(lw.buf, p...)
	for {
		i := bytes.IndexByte(lw.buf, '\n')
		if i < 0 {
			break
		}
		lw.ob.Line(string(lw.buf[:i]))
		lw.buf = lw.buf[i+1:]
	}
	return len(p), nil
}

// Flush emits any unterminated trailing bytes as a final line and
// clears the buffer. Idempotent.
func (lw *LineWriter) Flush() {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	if len(lw.buf) == 0 {
		return
	}
	lw.ob.Line(string(lw.buf))
	lw.buf = lw.buf[:0]
}
