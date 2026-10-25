package job

import (
	"bytes"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/bravo/present"
)

// pushOutcomeLine matches hard-coded rendering prefixes too (#376): lock it to
// what the real renderer emits for merge.reportPush's two forms, and make sure
// a post-merge target whose name merely contains "push" cannot be mistaken
// for the landing.
func TestPushOutcomeLineMatchesRenderedPoints(t *testing.T) {
	render := func(emit func(ts *crap.TestStream)) string {
		var buf bytes.Buffer
		rep := crap.NewReporter(&buf, crap.ReporterOptions{Title: "merge feature", Source: "spinclass"})
		ts := rep.TestStream(0)
		emit(ts)
		ts.Finish()
		return present.RenderPlain(&buf)
	}
	pushed := render(func(ts *crap.TestStream) {
		ts.Ok("merge feature")
		ts.Ok("push origin/main abc1234")
	})
	if got := pushOutcomeLine(pushed); got != "✓ push origin/main abc1234" {
		t.Errorf("pushed ladder: got %q\n%s", got, pushed)
	}
	local := render(func(ts *crap.TestStream) {
		ts.Ok("merge feature")
		ts.Skip("push", "merge landed on local main at abc1234 only; NOT pushed to any remote")
	})
	if got := pushOutcomeLine(local); !strings.HasPrefix(got, "↷ push") || !strings.Contains(got, "NOT pushed") {
		t.Errorf("local-only ladder: got %q\n%s", got, local)
	}
	none := render(func(ts *crap.TestStream) {
		ts.Ok("merge feature")
		ts.Ok("post-merge push-notify")
	})
	if got := pushOutcomeLine(none); got != "" {
		t.Errorf("a post-merge target named push-notify matched: %q\n%s", got, none)
	}
}

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
