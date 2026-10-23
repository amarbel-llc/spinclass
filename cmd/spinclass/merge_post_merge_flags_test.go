package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

// forcePostMergeTerminal swaps the terminal seam and captures what the
// operator would see on stdout.
func forcePostMergeTerminal(t *testing.T, isTerminal bool) *bytes.Buffer {
	t.Helper()
	out := &bytes.Buffer{}
	oldTerm, oldOut := postMergeAcceptIsTerminal, postMergeOperatorStdout
	t.Cleanup(func() { postMergeAcceptIsTerminal, postMergeOperatorStdout = oldTerm, oldOut })
	postMergeAcceptIsTerminal = func() bool { return isTerminal }
	postMergeOperatorStdout = func() io.Writer { return out }
	return out
}

func TestAcceptPostMergeRefusedWithoutATerminal(t *testing.T) {
	forcePostMergeTerminal(t, false)
	_, err := postMergeOperatorOptions("sha256:abc", false)
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("want a refusal naming the terminal requirement, got %v", err)
	}
}

func TestAcceptPostMergeWithTerminalAnnouncesAndCarriesDigest(t *testing.T) {
	out := forcePostMergeTerminal(t, true)
	pm, err := postMergeOperatorOptions("sha256:abc", false)
	if err != nil {
		t.Fatal(err)
	}
	if pm.AcceptPostMergeDigest != "sha256:abc" {
		t.Errorf("digest = %q, want it carried to the merge", pm.AcceptPostMergeDigest)
	}
	if pm.AnnounceAccept == nil {
		t.Fatal("the accepted section must be printed before the merge proceeds")
	}
	_, _ = pm.AnnounceAccept.Write([]byte("x"))
	if out.String() != "x" {
		t.Errorf("announce writes to %q, want the operator's stdout", out.String())
	}
}

func TestShowPostMergeNeedsNoTerminalAndCarriesNoDigest(t *testing.T) {
	forcePostMergeTerminal(t, false)
	pm, err := postMergeOperatorOptions("", true)
	if err != nil {
		t.Fatal(err)
	}
	if pm.ShowPostMerge == nil || pm.AcceptPostMergeDigest != "" {
		t.Errorf("show must be a pure dry run: %+v", pm)
	}
}

func TestShowAndAcceptPostMergeAreExclusive(t *testing.T) {
	forcePostMergeTerminal(t, true)
	if _, err := postMergeOperatorOptions("sha256:abc", true); err == nil {
		t.Fatal("--show-post-merge with --accept-post-merge must be refused")
	}
}

func TestNoPostMergeFlagsLeaveOptionsEmpty(t *testing.T) {
	forcePostMergeTerminal(t, false)
	pm, err := postMergeOperatorOptions("", false)
	if err != nil {
		t.Fatal(err)
	}
	if pm.AcceptPostMergeDigest != "" || pm.ShowPostMerge != nil || pm.AnnounceAccept != nil {
		t.Errorf("unexpected options: %+v", pm)
	}
}

// The MCP merge tools have no way to accept a changed post-merge section: an
// agent merge of a branch that changes it is refused before landing.
func changedPostMergeSessionFixture(t *testing.T) (repoPath, mainBefore string) {
	t.Helper()
	cwd, repoPath, _ := gatedWorktreeFixture(t, gateSweat)
	testgit.MustWriteFile(t, cwd, "sweatfile", gateSweat+"\n[[post-merge]]\nname = \"krone\"\ncommand = \"touch "+t.TempDir()+"/RAN\"\n")
	testgit.MustGit(t, cwd, "add", "sweatfile")
	testgit.MustGit(t, cwd, "commit", "-q", "-m", "add post-merge target")
	commitFile(t, cwd, "x.txt")
	return repoPath, testgit.MustGit(t, repoPath, "rev-parse", "HEAD")
}

func TestMergeThisSessionHandlerRefusesChangedPostMergeSection(t *testing.T) {
	repoPath, mainBefore := changedPostMergeSessionFixture(t)

	res, err := handleMergeThisSession(context.Background(), json.RawMessage(`{"local_only":true}`), nil)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !res.IsErr || !strings.Contains(res.Text, "post-merge section") {
		t.Fatalf("want a post-merge section refusal, got IsErr=%v: %s", res.IsErr, res.Text)
	}
	if after := testgit.MustGit(t, repoPath, "rev-parse", "HEAD"); after != mainBefore {
		t.Errorf("the default branch moved %s -> %s", mainBefore, after)
	}
}

func TestMergeThisSessionAsyncHandlerRefusesChangedPostMergeSection(t *testing.T) {
	repoPath, mainBefore := changedPostMergeSessionFixture(t)

	res, err := handleMergeThisSessionAsync(context.Background(), json.RawMessage(`{"local_only":true}`), nil)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !res.IsErr || !strings.Contains(res.Text, "post-merge section") {
		t.Fatalf("want a synchronous post-merge section refusal, got IsErr=%v: %s", res.IsErr, res.Text)
	}
	if after := testgit.MustGit(t, repoPath, "rev-parse", "HEAD"); after != mainBefore {
		t.Errorf("the default branch moved %s -> %s", mainBefore, after)
	}
}
