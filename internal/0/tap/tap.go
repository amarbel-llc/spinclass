package tap

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
)

type Writer struct {
	w      io.Writer
	n      int
	failed bool
}

func NewWriter(w io.Writer) *Writer {
	// Writes target the caller's io.Writer; errors are not threaded through the
	// formatter's int/void method signatures, so they are discarded explicitly.
	_, _ = fmt.Fprintln(w, "TAP version 14")
	return &Writer{w: w}
}

func (tw *Writer) Ok(description string) int {
	tw.n++
	_, _ = fmt.Fprintf(tw.w, "ok %d - %s\n", tw.n, description)
	return tw.n
}

func (tw *Writer) NotOk(description string, diagnostics map[string]string) int {
	tw.n++
	tw.failed = true
	_, _ = fmt.Fprintf(tw.w, "not ok %d - %s\n", tw.n, description)
	if len(diagnostics) > 0 {
		_, _ = fmt.Fprintln(tw.w, "  ---")
		keys := make([]string, 0, len(diagnostics))
		for k := range diagnostics {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := diagnostics[k]
			if strings.Contains(v, "\n") {
				_, _ = fmt.Fprintf(tw.w, "  %s: |\n", k)
				lines := strings.Split(v, "\n")
				for len(lines) > 0 && lines[len(lines)-1] == "" {
					lines = lines[:len(lines)-1]
				}
				for _, line := range lines {
					_, _ = fmt.Fprintf(tw.w, "    %s\n", line)
				}
			} else {
				_, _ = fmt.Fprintf(tw.w, "  %s: %s\n", k, v)
			}
		}
		_, _ = fmt.Fprintln(tw.w, "  ...")
	}
	return tw.n
}

func (tw *Writer) Skip(description, reason string) int {
	tw.n++
	_, _ = fmt.Fprintf(tw.w, "ok %d - %s # SKIP %s\n", tw.n, description, reason)
	return tw.n
}

func (tw *Writer) PlanAhead(n int) {
	_, _ = fmt.Fprintf(tw.w, "1..%d\n", n)
}

func (tw *Writer) Plan() {
	_, _ = fmt.Fprintf(tw.w, "1..%d\n", tw.n)
}

func (tw *Writer) HasFailures() bool {
	return tw.failed
}

// Subtest creates a child Writer that buffers its output. The child does NOT
// emit a TAP version header (subtests omit it per TAP-14 spec). Call
// EndSubtest on the parent when done.
func (tw *Writer) Subtest(name string) *Writer {
	return &Writer{w: &bytes.Buffer{}}
}

// EndSubtest writes the buffered subtest output (indented 4 spaces) under a
// "# Subtest:" comment, then emits the parent test point as ok/not ok based
// on whether the subtest had failures.
func (tw *Writer) EndSubtest(name string, sub *Writer) int {
	buf := sub.w.(*bytes.Buffer)

	_, _ = fmt.Fprintf(tw.w, "    # Subtest: %s\n", name)
	for _, line := range strings.Split(buf.String(), "\n") {
		if line == "" {
			continue
		}
		_, _ = fmt.Fprintf(tw.w, "    %s\n", line)
	}

	tw.n++
	if sub.HasFailures() {
		_, _ = fmt.Fprintf(tw.w, "not ok %d - %s\n", tw.n, name)
		tw.failed = true
	} else {
		_, _ = fmt.Fprintf(tw.w, "ok %d - %s\n", tw.n, name)
	}
	return tw.n
}
