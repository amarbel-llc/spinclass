package hookrun

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// #352: the scope fallback notice also reaches a notifier carried on ctx, so
// check can surface it beside the verdicts; the hook output keeps it too.
func TestScopeFallbackNoticeReachesTheNotifier(t *testing.T) {
	var notices []string
	ctx := WithNotices(fakeScope(t, refusingScopePrefix), func(n string) { notices = append(notices, n) })
	dir := t.TempDir()
	runLog := filepath.Join(dir, "runs")

	var buf bytes.Buffer
	err := PreMergeContext(ctx, preMergeSweatfile("echo ran >> "+runLog, false), dir, &buf)
	assertFellBackOnce(t, err, runLog, buf.String())

	if len(notices) != 1 || !strings.Contains(notices[0], "hook scope unavailable") {
		t.Fatalf("notices = %q, want one naming the unavailable scope", notices)
	}
	if strings.HasPrefix(notices[0], "[spinclass]") || strings.HasSuffix(notices[0], "\n") {
		t.Errorf("notice %q should be a bare line", notices[0])
	}
}

// A working scope sends no notice.
func TestWorkingScopeSendsNoNotice(t *testing.T) {
	var notices []string
	ctx := WithNotices(fakeScope(t, workingScopePrefix), func(n string) { notices = append(notices, n) })
	dir := t.TempDir()

	var buf bytes.Buffer
	if err := PreMergeContext(ctx, preMergeSweatfile("true", false), dir, &buf); err != nil {
		t.Fatalf("hook: %v\n%s", err, buf.String())
	}
	if len(notices) != 0 {
		t.Errorf("notices = %q, want none", notices)
	}
}
