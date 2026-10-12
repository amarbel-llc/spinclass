package hookrun_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "code.linenisgreat.com/spinclass/internal/hookrun"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

// Target: command succeeds, no verify => ok (opaque-command / graceful degradation).
func TestTargetCommandOnlyOK(t *testing.T) {
	dir := t.TempDir()
	tgt := sweatfile.PostMergeTarget{Name: "k", Command: "echo deployed"}
	var buf bytes.Buffer
	run, err := Target(context.Background(), tgt, dir, nil, &buf)
	if err != nil || run.Verdict != sweatfile.PostMergeOK {
		t.Fatalf("got verdict=%q err=%v out=%q", run.Verdict, err, buf.String())
	}
	if run.VerifyRan || run.CommandDuration <= 0 {
		t.Errorf("run = %+v; want command timed, verify not run", run)
	}
	if !strings.Contains(buf.String(), "deployed") {
		t.Errorf("command output not streamed: %q", buf.String())
	}
}

// Target: command fails => command-failed, and verify is NOT run.
func TestTargetCommandFailedSkipsVerify(t *testing.T) {
	dir := t.TempDir()
	verifyMarker := filepath.Join(dir, "verify-ran")
	tgt := sweatfile.PostMergeTarget{
		Name:    "k",
		Command: "echo boom >&2; exit 3",
		Verify:  sptr("touch " + verifyMarker),
	}
	var buf bytes.Buffer
	run, err := Target(context.Background(), tgt, dir, nil, &buf)
	if err == nil || run.Verdict != sweatfile.PostMergeCommandFailed {
		t.Fatalf("got verdict=%q err=%v", run.Verdict, err)
	}
	if run.VerifyRan {
		t.Error("VerifyRan must be false when the command failed")
	}
	if _, statErr := os.Stat(verifyMarker); statErr == nil {
		t.Error("verify must NOT run when the command failed")
	}
}

// Target: command succeeds, verify succeeds => ok, verify observed the env.
func TestTargetVerifyOK(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "verify-out")
	tgt := sweatfile.PostMergeTarget{
		Name:    "k",
		Command: "echo triggered",
		Verify:  sptr("echo sha=$SPINCLASS_MERGED_SHA > " + out),
	}
	var buf bytes.Buffer
	run, err := Target(context.Background(), tgt, dir, []string{"SPINCLASS_MERGED_SHA=abc123"}, &buf)
	if err != nil || run.Verdict != sweatfile.PostMergeOK {
		t.Fatalf("got verdict=%q err=%v out=%q", run.Verdict, err, buf.String())
	}
	if !run.VerifyRan || run.VerifyDuration <= 0 {
		t.Errorf("run = %+v; want verify timed", run)
	}
	raw, readErr := os.ReadFile(out)
	if readErr != nil {
		t.Fatalf("verify did not run: %v", readErr)
	}
	if got := strings.TrimSpace(string(raw)); got != "sha=abc123" {
		t.Errorf("verify env: got %q, want sha=abc123", got)
	}
}

// Target: command succeeds but verify fails => verify-failed (the split that
// tells a human "investigate the probe/ack path" rather than "fix the change").
func TestTargetVerifyFailed(t *testing.T) {
	dir := t.TempDir()
	tgt := sweatfile.PostMergeTarget{
		Name:    "k",
		Command: "echo triggered",
		Verify:  sptr("echo unhealthy >&2; exit 1"),
	}
	var buf bytes.Buffer
	run, err := Target(context.Background(), tgt, dir, nil, &buf)
	if err == nil || run.Verdict != sweatfile.PostMergeVerifyFailed {
		t.Fatalf("got verdict=%q err=%v", run.Verdict, err)
	}
	if !run.VerifyRan {
		t.Error("VerifyRan must be true when verify ran and failed")
	}
	if !strings.Contains(buf.String(), "unhealthy") {
		t.Errorf("verify output not streamed: %q", buf.String())
	}
}
