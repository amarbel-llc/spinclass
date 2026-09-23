package job

import (
	"bytes"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/present"
)

// localAdvanceSkipLine matches a hard-coded rendering prefix, so lock it to
// what the real renderer emits for a skip point (merge.reportLocalAdvance's
// ts.Skip rendered by present.RenderPlain) — a renderer change must fail here,
// not silently drop the "merge LANDED" line from the async wake (#295).
func TestLocalAdvanceSkipLineMatchesRenderedSkip(t *testing.T) {
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{Title: "merge feature", Source: "spinclass"})
	ts := rep.TestStream(0)
	ts.Ok("merge feature")
	ts.Skip("advance local main", "merge LANDED on origin/main at abc123def456; only local main was not advanced: dirty")
	ts.Ok("remove worktree feature")
	ts.Finish()

	text := present.RenderPlain(&buf)
	line := localAdvanceSkipLine(text)
	if line == "" {
		t.Fatalf("no advance-local skip line found in rendered ladder:\n%s", text)
	}
	if !strings.Contains(line, "merge LANDED on origin/main at abc123def456") {
		t.Errorf("lifted line %q lost the landing statement", line)
	}
}
