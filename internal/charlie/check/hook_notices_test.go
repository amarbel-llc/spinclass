package check

import (
	"bytes"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
	"code.linenisgreat.com/spinclass/internal/bravo/present"
)

// #352: a hook notice is surfaced as a warning line in the plain verdicts
// agents read, never as a failure.
func TestHookNoticesRenderAsWarningsNotFailures(t *testing.T) {
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(1)
	ts.Ok("pre-merge hook")
	emitHookNotices(rep, []string{"hook scope unavailable (exit status 1); running unscoped"})
	ts.Finish()

	text := present.RenderPlain(bytes.NewReader(buf.Bytes()))
	if !strings.Contains(text, "warning: hook scope unavailable (exit status 1); running unscoped") {
		t.Errorf("plain verdicts lack the warning:\n%s", text)
	}
	if strings.Contains(text, "✗") {
		t.Errorf("a notice must not render as a failure:\n%s", text)
	}
}
