package merge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// spinclass#330: the post-merge phase can outlive the session whose TMPDIR the
// merging process inherited (sc run tears the session worktree down before
// post-merge). A target's `mktemp -d` must still succeed, so the phase gets
// its own TMPDIR that does not depend on any session.
func TestPostMergeTargetGetsLiveTmpdirWhenInheritedOneIsGone(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "torn-down-session", ".tmp"))
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	out := filepath.Join(t.TempDir(), "mktemp.out")
	writeRepoSweatfile(t, repoDir, `
[[post-merge]]
name = "deploy"
command = 'd=$(mktemp -d) && test -d "$d" && printf %s "$d" > `+out+`'
`)

	recs, err := runFinish(t, repoDir, wtPath, "feature", false)
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	if n, ok := findNode(recs, "post-merge deploy"); !ok || !n.exitOK {
		t.Fatalf("post-merge deploy failed: %+v (all: %v)", n, nodeNames(recs))
	}
	got, readErr := os.ReadFile(out)
	if readErr != nil {
		t.Fatalf("target never wrote its mktemp dir: %v", readErr)
	}
	if !strings.HasPrefix(string(got), os.Getenv("XDG_RUNTIME_DIR")) {
		t.Errorf("mktemp dir %q is not under the phase tmpdir root %q", got, os.Getenv("XDG_RUNTIME_DIR"))
	}
}

// The phase tmpdir never derives from the inherited TMPDIR, even a live one: a
// live session's .tmp can still vanish under a detached post-merge child.
func TestPostMergeEnvTmpdirIgnoresInheritedTmpdir(t *testing.T) {
	inherited := t.TempDir()
	t.Setenv("TMPDIR", inherited)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	env := PostMergeEnv(PostMergeFacts{})
	var tmp string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "TMPDIR="); ok {
			tmp = v
		}
	}
	if tmp == "" || strings.HasPrefix(tmp, inherited) {
		t.Fatalf("TMPDIR = %q, want a session-independent dir (not under %q)", tmp, inherited)
	}
	if info, err := os.Stat(tmp); err != nil || !info.IsDir() {
		t.Fatalf("phase TMPDIR %q does not exist: %v", tmp, err)
	}
}
