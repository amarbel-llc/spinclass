package sysprompt

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/git"
)

// envFunc builds a getenv stub from a map.
func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func wdFunc(dir string) func() (string, error) {
	return func() (string, error) { return dir, nil }
}

func TestResolveWorktreeMode(t *testing.T) {
	wt := "/repos/myrepo/.worktrees/feat-x"
	env := map[string]string{
		"SPINCLASS_SESSION_ID":  "myrepo/feat-x",
		"SPINCLASS_REPO":        "myrepo",
		"SPINCLASS_BRANCH":      "feat-x",
		"SPINCLASS_WORKTREE":    wt,
		"SPINCLASS_DESCRIPTION": "do a thing",
		"CLOWN_GROUP_ID":        "myrepo/feat-x",
	}
	// cwd inside the worktree (a subdirectory) must still resolve to worktree.
	c := resolve(envFunc(env), wdFunc(filepath.Join(wt, "internal", "pkg")))

	if c.Mode != ModeWorktree {
		t.Fatalf("Mode: got %q, want %q", c.Mode, ModeWorktree)
	}
	if c.SessionKey != "myrepo/feat-x" || c.Branch != "feat-x" || c.Worktree != wt {
		t.Errorf("coordinates: got %+v", c)
	}
	if c.Description != "do a thing" || c.GroupID != "myrepo/feat-x" {
		t.Errorf("description/group: got %+v", c)
	}
}

// A nested clown launched from within a worktree session inherits
// SPINCLASS_WORKTREE; when the serve cwd is NOT inside it, the env var alone
// must not mislabel the session as a worktree.
func TestResolveWorktreeEnvButCwdOutside(t *testing.T) {
	env := map[string]string{"SPINCLASS_WORKTREE": "/repos/myrepo/.worktrees/feat-x"}
	c := resolve(envFunc(env), wdFunc("/somewhere/else/not-a-repo"))

	if c.Mode != ModeMainCheckout {
		t.Fatalf("Mode: got %q, want %q (cwd outside the inherited worktree)", c.Mode, ModeMainCheckout)
	}
}

func TestResolveMainCheckout(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir, "trunk")

	env := map[string]string{"CLOWN_SESSION_ID": "clown-key-123"}
	c := resolve(envFunc(env), wdFunc(dir))

	if c.Mode != ModeMainCheckout {
		t.Fatalf("Mode: got %q, want %q", c.Mode, ModeMainCheckout)
	}
	if c.Branch != "trunk" {
		t.Errorf("Branch: got %q, want %q", c.Branch, "trunk")
	}
	if c.Worktree == "" || c.Repo != filepath.Base(c.Worktree) {
		t.Errorf("Worktree/Repo: got worktree=%q repo=%q", c.Worktree, c.Repo)
	}
	if c.SessionKey != "clown-key-123" {
		t.Errorf("SessionKey: got %q, want CLOWN_SESSION_ID", c.SessionKey)
	}
}

func TestRenderWorktreeVariant(t *testing.T) {
	got, err := Render(Coordinates{
		Mode:       ModeWorktree,
		SessionKey: "myrepo/feat-x",
		Branch:     "feat-x",
		Worktree:   "/repos/myrepo/.worktrees/feat-x",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	mustContain(t, got, "myrepo/feat-x") // live session key injected
	mustContain(t, got, "Worktree management")
	mustContain(t, got, "EnterWorktree") // worktree-only guidance present
	mustContain(t, got, "Background jobs (merge/check)")
	if strings.Contains(got, "main checkout") {
		t.Error("worktree variant must not mention 'main checkout'")
	}
}

func TestRenderMainCheckoutVariant(t *testing.T) {
	got, err := Render(Coordinates{
		Mode:       ModeMainCheckout,
		SessionKey: "clown-key-123",
		Repo:       "myrepo",
		Branch:     "trunk",
		Worktree:   "/repos/myrepo",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	mustContain(t, got, "main checkout")
	mustContain(t, got, "clown-key-123")
	mustContain(t, got, "Background jobs (merge/check)") // async guidance still applies
	// The worktree-only guidance must NOT leak into the main-checkout variant.
	if strings.Contains(got, "Worktree management") || strings.Contains(got, "EnterWorktree") {
		t.Error("main-checkout variant must omit worktree-management guidance")
	}
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("fragment missing %q\n---\n%s", needle, haystack)
	}
}

func gitInit(t *testing.T, dir, branch string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"checkout", "-b", branch},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		if _, err := git.Run(dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
}
