package merge

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
)

func prepareExpecting(t *testing.T, repoDir, wtPath, branch, expect string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	_, err := PrepareMergeWith(context.Background(), rep, ts, repoDir, wtPath, branch, "main", false, PostMergeOptions{ExpectSha: expect})
	ts.Finish()
	return buf.String(), err
}

// #353: a merge given the commit the caller reviewed proceeds when the
// branch tip is that commit, by full or abbreviated id.
func TestPrepareMergeAcceptsTheExpectedTip(t *testing.T) {
	for _, abbrev := range []bool{false, true} {
		repoDir, wtPath := autostashRepo(t)
		tip := runGit(t, wtPath, "rev-parse", "HEAD")
		if abbrev {
			tip = tip[:10]
		}
		if stream, err := prepareExpecting(t, repoDir, wtPath, "feat", tip); err != nil {
			t.Fatalf("expect %s: %v\n%s", tip, err, stream)
		}
	}
}

// #353: a branch that moved after the caller's review is refused before the
// rebase, with a refusal a caller can tell from a gate failure.
func TestPrepareMergeRefusesABranchThatMovedFromTheExpectedTip(t *testing.T) {
	repoDir, wtPath := autostashRepo(t)
	reviewed := runGit(t, wtPath, "rev-parse", "HEAD")
	writeFile(t, wtPath, "late.txt", "unreviewed")
	runGit(t, wtPath, "add", "late.txt")
	runGit(t, wtPath, "commit", "-m", "late")
	moved := runGit(t, wtPath, "rev-parse", "HEAD")

	stream, err := prepareExpecting(t, repoDir, wtPath, "feat", reviewed)
	if !errors.Is(err, ErrBranchMoved) {
		t.Fatalf("err = %v, want ErrBranchMoved\n%s", err, stream)
	}
	if !strings.Contains(stream, "branch moved") || !strings.Contains(stream, shortSha(moved)) {
		t.Errorf("stream lacks a refusal naming the moved tip:\n%s", stream)
	}
	if got := runGit(t, wtPath, "rev-parse", "HEAD"); got != moved {
		t.Errorf("HEAD = %s, want %s untouched (no rebase)", got, moved)
	}
}

// An expected id that names no commit is an error, not a silent pass.
func TestPrepareMergeRefusesAnUnknownExpectedCommit(t *testing.T) {
	repoDir, wtPath := autostashRepo(t)
	if _, err := prepareExpecting(t, repoDir, wtPath, "feat", "0123456789abcdef0123456789abcdef01234567"); err == nil {
		t.Fatal("an unknown expected commit was accepted")
	}
}
