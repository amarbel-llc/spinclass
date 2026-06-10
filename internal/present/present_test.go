package present

import (
	"strings"
	"testing"

	"github.com/amarbel-llc/crap/go-crap/v2/crap"
)

func TestResolveFormat(t *testing.T) {
	cases := []struct {
		in    string
		isTTY bool
		want  string
		err   bool
	}{
		{"", true, FormatViewport, false},
		{"", false, FormatNdjson, false},
		{"auto", true, FormatViewport, false},
		{"auto", false, FormatNdjson, false},
		{"viewport", false, FormatViewport, false},
		{"plain", true, FormatPlain, false},
		{"ndjson", true, FormatNdjson, false},
		{"tap", true, "", true},   // retired for merge/check
		{"table", true, "", true}, // never valid here
	}
	for _, c := range cases {
		got, err := ResolveFormat(c.in, c.isTTY)
		if c.err && err == nil {
			t.Errorf("ResolveFormat(%q): want error", c.in)
		}
		if !c.err && (err != nil || got != c.want) {
			t.Errorf("ResolveFormat(%q,%v) = %q,%v want %q", c.in, c.isTTY, got, err, c.want)
		}
	}
}

// WithReporter(FormatNdjson) writes records to out verbatim.
func TestWithReporterNdjson(t *testing.T) {
	var out strings.Builder
	err := WithReporter(FormatNdjson, "title", &out, &out, func(rep *crap.Reporter) error {
		ts := rep.TestStream(1)
		ts.Ok("rebase feature")
		ts.Finish()
		return nil
	})
	if err != nil {
		t.Fatalf("WithReporter: %v", err)
	}
	for _, want := range []string{`"type":"test"`, "rebase feature", `"type":"summary"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("ndjson output missing %q:\n%s", want, out.String())
		}
	}
}

// WithReporter(FormatPlain) renders verdict lines, echoing failure diagnostics.
func TestWithReporterPlain(t *testing.T) {
	var out strings.Builder
	_ = WithReporter(FormatPlain, "title", &out, &out, func(rep *crap.Reporter) error {
		ts := rep.TestStream(2)
		ts.Ok("rebase feature")
		ts.NotOk("merge feature", map[string]any{"message": "boom"})
		ts.Finish()
		return nil
	})
	if !strings.Contains(out.String(), "✓ rebase feature") {
		t.Errorf("plain output missing ok verdict:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "✗ merge feature") || !strings.Contains(out.String(), "boom") {
		t.Errorf("plain output missing failure verdict/diagnostic:\n%s", out.String())
	}
}

// RenderPlain converts a buffered record stream to verdict lines.
func TestRenderPlain(t *testing.T) {
	var rec strings.Builder
	rep := crap.NewReporter(&rec, crap.ReporterOptions{})
	ts := rep.TestStream(1)
	ts.NotOk("pre-merge hook", map[string]any{"message": "exit 1"})
	ts.Finish()

	text := RenderPlain(strings.NewReader(rec.String()))
	if !strings.Contains(text, "✗ pre-merge hook") || !strings.Contains(text, "exit 1") {
		t.Errorf("RenderPlain missing failure: %q", text)
	}
}

// LineWriter splits arbitrary chunks into per-line Phase.Output records.
func TestLineWriter(t *testing.T) {
	var rec strings.Builder
	rep := crap.NewReporter(&rec, crap.ReporterOptions{})
	ph := rep.Phase("hook")
	lw := NewLineWriter(ph)
	_, _ = lw.Write([]byte("line one\npartial"))
	_, _ = lw.Write([]byte(" line\n"))
	lw.Flush()
	ph.Done()

	if !strings.Contains(rec.String(), "line one") || !strings.Contains(rec.String(), "partial line") {
		t.Errorf("LineWriter output records missing lines:\n%s", rec.String())
	}
}
