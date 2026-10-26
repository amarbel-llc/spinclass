package merge

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
)

// prepareWithReporter runs PrepareMergeWith and returns the raw record stream.
func prepareWithReporter(t *testing.T, repoDir, wtPath, branch string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	_, err := PrepareMergeWith(context.Background(), rep, ts, repoDir, wtPath, branch, "main", false, PostMergeOptions{})
	ts.Finish()
	return buf.String(), err
}

// #240: when the rebase rewrites the session's commits, the result names the
// new HEAD and the old one, because the old sha a caller remembers is gone
// even if a later stage fails.
func TestPrepareMergeNamesTheHeadTheRebaseRewrote(t *testing.T) {
	repoDir, wtPath := autostashRepo(t)
	before := runGit(t, wtPath, "rev-parse", "HEAD")

	stream, err := prepareWithReporter(t, repoDir, wtPath, "feat")
	if err != nil {
		t.Fatalf("prepare: %v\n%s", err, stream)
	}
	after := runGit(t, wtPath, "rev-parse", "HEAD")
	if before == after {
		t.Fatal("the fixture's rebase did not rewrite HEAD, so this test proves nothing")
	}
	want := "rebase rewrote feat: HEAD is now " + shortSha(after) + " (was " + shortSha(before) + ")"
	if !strings.Contains(stream, want) {
		t.Errorf("stream lacks %q:\n%s", want, stream)
	}
}

// A rebase that moved nothing reports no rewrite.
func TestPrepareMergeReportsNoRewriteForANoOpRebase(t *testing.T) {
	repoDir := setupRepo(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	wtPath := setupWorktree(t, repoDir, "feat")
	writeFile(t, wtPath, "work.txt", "w")
	runGit(t, wtPath, "add", "work.txt")
	runGit(t, wtPath, "commit", "-m", "work")

	stream, err := prepareWithReporter(t, repoDir, wtPath, "feat")
	if err != nil {
		t.Fatalf("prepare: %v\n%s", err, stream)
	}
	if strings.Contains(stream, "rebase rewrote") {
		t.Errorf("a no-op rebase reported a rewrite:\n%s", stream)
	}
}
