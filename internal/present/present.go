// Package present selects and drives the rendering of merge/check
// ndjson-crap streams: a live bubbletea viewport on a TTY, plain
// verdict-per-line text, or the raw records (the wire). The producer side
// is crap.Reporter; this package owns only consumer wiring.
package present

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/amarbel-llc/crap/go-crap/v2/crap"
	"github.com/amarbel-llc/crap/go-crap/v2/ndjsoncrap"
	"github.com/amarbel-llc/crap/go-crap/v2/viewport"
)

// Resolved format names. "auto" and "" resolve to viewport on a TTY and
// ndjson otherwise (madder sync's convention).
const (
	FormatViewport = "viewport"
	FormatPlain    = "plain"
	FormatNdjson   = "ndjson"
)

// ResolveFormat maps the merge/check --format value to a concrete renderer.
// TAP is retired for these commands; naming it gets a pointed error.
func ResolveFormat(format string, stdoutIsTTY bool) (string, error) {
	switch format {
	case "", "auto":
		if stdoutIsTTY {
			return FormatViewport, nil
		}
		return FormatNdjson, nil
	case FormatViewport, FormatPlain, FormatNdjson:
		return format, nil
	case "tap", "table":
		return "", fmt.Errorf("format %q is not supported for merge/check (TAP was retired here); use auto, viewport, plain, or ndjson", format)
	default:
		return "", fmt.Errorf("unknown format %q (valid: auto, viewport, plain, ndjson)", format)
	}
}

// WithReporter builds a crap.Reporter wired to the resolved renderer, runs
// fn with it, and tears the renderer down. stdout receives ndjson/plain
// output; tty receives the live viewport (callers pass os.Stderr so
// `sc merge > records.ndjson` keeps a live viewport). fn's error is
// returned; a renderer error is joined only if fn succeeded.
func WithReporter(format, title string, stdout, tty io.Writer, fn func(rep *crap.Reporter) error) error {
	opts := crap.ReporterOptions{Title: title, Source: "spinclass"}

	if format == FormatNdjson {
		rep := crap.NewReporter(stdout, opts)
		return fn(rep)
	}

	pr, pw := io.Pipe()
	done := make(chan error, 1)
	out := stdout
	isTTY := false
	if format == FormatViewport {
		out = tty
		isTTY = true
	}
	// Start the consumer before NewReporter: the Reporter writes its Meta
	// header synchronously, and an unbuffered io.Pipe write blocks until
	// the renderer reads it.
	go func() {
		done <- viewport.Present(pr, viewport.Options{Title: title, Out: out, IsTTY: isTTY})
	}()
	rep := crap.NewReporter(pw, opts)
	err := fn(rep)
	_ = pw.Close()
	renderErr := <-done
	if err != nil {
		return err
	}
	return renderErr
}

// RenderPlain renders a buffered ndjson-crap stream as plain verdict lines
// — the agent-facing text for MCP results and async job ResultText.
func RenderPlain(records io.Reader) string {
	var b strings.Builder
	_ = viewport.Present(records, viewport.Options{Out: &b, IsTTY: false})
	return strings.TrimRight(b.String(), "\n")
}

// phaseOutput is the subset of crap.Phase LineWriter needs (test seam).
type phaseOutput interface {
	Output(stream, data string)
}

// LineWriter adapts an io.Writer (the hook's live output sink) into
// per-line Phase.Output records. Partial lines are buffered until their
// newline arrives; Flush emits any trailing partial line.
type LineWriter struct {
	ph  phaseOutput
	buf bytes.Buffer
}

func NewLineWriter(ph phaseOutput) *LineWriter { return &LineWriter{ph: ph} }

func (l *LineWriter) Write(p []byte) (int, error) {
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadString('\n')
		if err != nil {
			// no full line yet; keep the partial
			l.buf.Reset()
			l.buf.WriteString(line)
			break
		}
		l.ph.Output(ndjsoncrap.StreamStdout, line)
	}
	return len(p), nil
}

// Flush emits any buffered partial line as a final output record.
func (l *LineWriter) Flush() {
	if l.buf.Len() > 0 {
		l.ph.Output(ndjsoncrap.StreamStdout, l.buf.String()+"\n")
		l.buf.Reset()
	}
}
