package run

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func commitFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", name)
	gitIn(t, dir, "commit", "-q", "-m", "commit "+name)
}

// regenerateStream runs the phase's seam on a repo whose branch `work` was cut
// from `main` (holding lock L1) and then committed lockOnWork, and returns the
// crap stream it wrote.
func regenerateStream(t *testing.T, lockOnWork string, off bool) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	commitFile(t, dir, "flake.lock", "L1")
	gitIn(t, dir, "checkout", "-q", "-b", "work")
	if lockOnWork != "L1" {
		commitFile(t, dir, "flake.lock", lockOnWork)
	}
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	reportRegenerate(ts, dir, dir, off)
	ts.Finish()
	return buf.String()
}

func TestRegenerateSeamIsSilentWhenTheLockIsUnchanged(t *testing.T) {
	if got := regenerateStream(t, "L1", false); strings.Contains(got, "regenerate") {
		t.Errorf("an unchanged lock must add nothing to the stream:\n%s", got)
	}
}

func TestRegenerateSeamReportsAChangedLock(t *testing.T) {
	got := regenerateStream(t, "L2", false)
	if !strings.Contains(got, "regenerate") || !strings.Contains(got, "flake.lock changed") {
		t.Errorf("a changed lock is not reported:\n%s", got)
	}
	if strings.Contains(got, "--no-regenerate") {
		t.Errorf("the phase is on, so the report must not blame the flag:\n%s", got)
	}
}

func TestRegenerateSeamNamesTheFlagWhenOff(t *testing.T) {
	got := regenerateStream(t, "L2", true)
	if !strings.Contains(got, "--no-regenerate") {
		t.Errorf("a changed lock under --no-regenerate is not reported as skipped by the flag:\n%s", got)
	}
}

func TestParseArgsNoRegenerate(t *testing.T) {
	spec, err := ParseArgs([]string{"--no-regenerate", "--", "true"}, nil)
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if !spec.NoRegenerate {
		t.Error("--no-regenerate did not set NoRegenerate")
	}
	def, err := ParseArgs([]string{"--", "true"}, nil)
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	if def.NoRegenerate {
		t.Error("the phase must be on by default")
	}
}
