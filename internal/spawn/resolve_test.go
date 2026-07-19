package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

// fakeWorkspace builds a fake $HOME with two workspace roots
// (eng/repos, eng-alt/repos) holding git repos foo and bar.
func fakeWorkspace(t *testing.T) (home, fooPath, barPath string) {
	t.Helper()
	testgit.RequireGit(t)
	home = t.TempDir()
	fooPath = filepath.Join(home, "eng", "repos", "foo")
	barPath = filepath.Join(home, "eng-alt", "repos", "bar")
	for _, p := range []string{fooPath, barPath} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		testgit.MustInit(t, p)
	}
	return home, fooPath, barPath
}

func TestResolveRepoLeafHit(t *testing.T) {
	home, fooPath, barPath := fakeWorkspace(t)

	got, err := ResolveRepo(home, "bar", fooPath)
	if err != nil {
		t.Fatalf("ResolveRepo: %v", err)
	}
	if got != barPath {
		t.Errorf("got %q, want %q", got, barPath)
	}
}

func TestResolveRepoAmbiguousLeafListsCandidates(t *testing.T) {
	home, fooPath, _ := fakeWorkspace(t)
	// Same leaf under the second root: foo now exists under eng AND eng-alt.
	dup := filepath.Join(home, "eng-alt", "repos", "foo")
	if err := os.MkdirAll(dup, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.MustInit(t, dup)

	driver := filepath.Join(home, "eng", "repos", "driver")
	if err := os.MkdirAll(driver, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.MustInit(t, driver)

	_, err := ResolveRepo(home, "foo", driver)
	if err == nil {
		t.Fatal("expected ambiguity error, got nil")
	}
	for _, candidate := range []string{fooPath, dup} {
		if !strings.Contains(err.Error(), candidate) {
			t.Errorf("error %q does not list candidate %q", err, candidate)
		}
	}
}

func TestResolveRepoMiss(t *testing.T) {
	home, fooPath, _ := fakeWorkspace(t)

	_, err := ResolveRepo(home, "nosuch", fooPath)
	if err == nil {
		t.Fatal("expected no-match error, got nil")
	}
	if !strings.Contains(err.Error(), "nosuch") {
		t.Errorf("error %q does not name the target", err)
	}
}

func TestResolveRepoRejectsSameRepo(t *testing.T) {
	home, fooPath, _ := fakeWorkspace(t)

	_, err := ResolveRepo(home, "foo", fooPath)
	if err == nil {
		t.Fatal("expected same-repo error, got nil")
	}
}

func TestResolveRepoExplicitPathEscape(t *testing.T) {
	home, fooPath, _ := fakeWorkspace(t)

	// A repo outside any workspace root, addressed by absolute path.
	outside := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.MustInit(t, outside)

	got, err := ResolveRepo(home, outside, fooPath)
	if err != nil {
		t.Fatalf("ResolveRepo: %v", err)
	}
	if got != outside {
		t.Errorf("got %q, want %q", got, outside)
	}
}

// TestResolveRepoBareNameMatchingCwdSubdirStillLeafSearches guards against
// the explicit-path escape hijacking a bare repo dirname: a cwd subdir that
// happens to share the target's name must NOT short-circuit leaf search —
// only a target containing a path separator is treated as a path.
func TestResolveRepoBareNameMatchingCwdSubdirStillLeafSearches(t *testing.T) {
	home, fooPath, barPath := fakeWorkspace(t)

	// A cwd whose subdir is coincidentally named like the target repo.
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "bar"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)

	got, err := ResolveRepo(home, "bar", fooPath)
	if err != nil {
		t.Fatalf("ResolveRepo: %v", err)
	}
	if got != barPath {
		t.Errorf("got %q, want leaf-searched %q (cwd subdir must not hijack)", got, barPath)
	}
}

func TestResolveRepoExplicitPathNonRepoErrors(t *testing.T) {
	home, fooPath, _ := fakeWorkspace(t)

	notRepo := t.TempDir()
	_, err := ResolveRepo(home, notRepo, fooPath)
	if err == nil {
		t.Fatal("expected non-repo error, got nil")
	}
}

func TestResolveRepoExplicitPathWorktreeRejected(t *testing.T) {
	// A worktree has a .git FILE, not a directory — it is not a main
	// checkout and must be rejected even via the explicit-path escape.
	home, fooPath, barPath := fakeWorkspace(t)
	wt := filepath.Join(barPath, ".worktrees", "wt-branch")
	testgit.MustWorktreeAdd(t, barPath, wt, "wt-branch")

	_, err := ResolveRepo(home, wt, fooPath)
	if err == nil {
		t.Fatal("expected worktree rejection, got nil")
	}
}

func TestResolveRepoExplicitSameRepoRejected(t *testing.T) {
	home, fooPath, _ := fakeWorkspace(t)

	_, err := ResolveRepo(home, fooPath, fooPath)
	if err == nil {
		t.Fatal("expected same-repo error for explicit path, got nil")
	}
}
