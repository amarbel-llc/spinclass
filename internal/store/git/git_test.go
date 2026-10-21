package git

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/store"
)

func mustRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newRepo makes a repo with one commit on main, a tag, and a second branch.
func newRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gitconfig"))
	t.Setenv("GIT_CEILING_DIRECTORIES", root)
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "init", "-b", "main")
	mustRun(t, repo, "config", "user.email", "t@t")
	mustRun(t, repo, "config", "user.name", "t")
	mustRun(t, repo, "config", "commit.gpgsign", "false")
	mustRun(t, repo, "config", "tag.gpgsign", "false")
	write(t, repo, "a.txt", "one\n")
	mustRun(t, repo, "add", "a.txt")
	mustRun(t, repo, "commit", "-m", "first")
	mustRun(t, repo, "tag", "v1")
	mustRun(t, repo, "branch", "topic")
	return repo
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func do(t *testing.T, repo, tool, sub string, a map[string]any) (string, error) {
	t.Helper()
	return doAs(t, store.Session{Dir: repo, Confinement: store.ConfinementUnconfined}, tool, sub, a)
}

func doAs(t *testing.T, s store.Session, tool, sub string, a map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	res, err := New().Do(context.Background(), s, store.Op{Tool: tool, Subcommand: sub, Args: raw})
	return res.Text, err
}

func TestStageStatusAndDiff(t *testing.T) {
	repo := newRepo(t)
	write(t, repo, "a.txt", "one\ntwo\n")
	write(t, repo, "new.txt", "x\n")

	out, err := do(t, repo, "stage", "status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "M a.txt") || !strings.Contains(out, "?? new.txt") {
		t.Fatalf("status output: %q", out)
	}
	out, err = do(t, repo, "stage", "status", map[string]any{"untracked": "no"})
	if err != nil || strings.Contains(out, "new.txt") {
		t.Fatalf("untracked=no: %q, %v", out, err)
	}

	out, err = do(t, repo, "stage", "diff", nil)
	if err != nil || !strings.Contains(out, "+two") {
		t.Fatalf("unstaged diff: %q, %v", out, err)
	}
	if out, _ = do(t, repo, "stage", "diff", map[string]any{"staged": true}); out != "" {
		t.Fatalf("staged diff should be empty, got %q", out)
	}
	mustRun(t, repo, "add", "a.txt")
	if out, _ = do(t, repo, "stage", "diff", map[string]any{"staged": true, "stat_only": true}); !strings.Contains(out, "a.txt") {
		t.Fatalf("staged stat: %q", out)
	}
	if _, err = do(t, repo, "stage", "status", map[string]any{"untracked": "bogus"}); err == nil {
		t.Fatal("bad untracked mode must error")
	}
}

func TestBranchLogRevParseDiff(t *testing.T) {
	repo := newRepo(t)
	write(t, repo, "a.txt", "one\ntwo\n")
	mustRun(t, repo, "commit", "-am", "second")

	out, err := do(t, repo, "branch", "log", map[string]any{"oneline": true, "max_count": 1})
	if err != nil || strings.Count(out, "\n") != 0 || !strings.Contains(out, "second") {
		t.Fatalf("log: %q, %v", out, err)
	}
	sha, err := do(t, repo, "branch", "rev-parse", map[string]any{"ref": "v1"})
	if err != nil || len(sha) != 40 {
		t.Fatalf("rev-parse: %q, %v", sha, err)
	}
	if _, err = do(t, repo, "branch", "rev-parse", map[string]any{"ref": "nope"}); err == nil {
		t.Fatal("unknown ref must error")
	}
	if _, err = do(t, repo, "branch", "rev-parse", nil); err == nil {
		t.Fatal("missing ref must error")
	}
	out, err = do(t, repo, "branch", "diff", map[string]any{"ref": "v1", "context_lines": 0})
	if err != nil || !strings.Contains(out, "+two") {
		t.Fatalf("diff: %q, %v", out, err)
	}
	if _, err = do(t, repo, "branch", "log", map[string]any{"ref": "--output=x"}); err == nil {
		t.Fatal("option-like ref must be refused")
	}
}

func TestTagListAndVerify(t *testing.T) {
	repo := newRepo(t)
	mustRun(t, repo, "tag", "v2")
	mustRun(t, repo, "tag", "other")

	out, err := do(t, repo, "tag", "list", map[string]any{"pattern": "v*", "sort": "refname"})
	if err != nil || out != "v1\nv2" {
		t.Fatalf("list: %q, %v", out, err)
	}
	out, _ = do(t, repo, "tag", "list", map[string]any{"sort": "refname", "max_count": 1})
	if out != "other" {
		t.Fatalf("max_count: %q", out)
	}
	if _, err = do(t, repo, "tag", "verify", map[string]any{"name": "v1"}); err == nil {
		t.Fatal("verifying an unsigned tag must error")
	}
	if _, err = do(t, repo, "tag", "verify", nil); err == nil {
		t.Fatal("missing name must error")
	}
}

func TestWorktreeList(t *testing.T) {
	repo := newRepo(t)
	wt := filepath.Join(filepath.Dir(repo), "wt")
	mustRun(t, repo, "worktree", "add", wt, "topic")

	out, err := do(t, repo, "worktree", "list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "branch refs/heads/topic") || !strings.Contains(out, "branch refs/heads/main") {
		t.Fatalf("worktree list: %q", out)
	}
}

func TestRepoPathOverridesSession(t *testing.T) {
	repo := newRepo(t)
	raw, _ := json.Marshal(map[string]any{"repo_path": repo, "ref": "v1"})
	res, err := New().Do(context.Background(), store.Session{Dir: t.TempDir(), Confinement: store.ConfinementUnconfined}, store.Op{Tool: "branch", Subcommand: "rev-parse", Args: raw})
	if err != nil || len(res.Text) != 40 {
		t.Fatalf("repo_path: %q, %v", res.Text, err)
	}
	if _, err = New().Do(context.Background(), store.Session{}, store.Op{Tool: "tag", Subcommand: "list"}); err == nil {
		t.Fatal("no dir must error")
	}
}

func TestUnknownVerbsUnsupported(t *testing.T) {
	repo := newRepo(t)
	for _, c := range [][2]string{{"worktree", "add"}, {"remote", "set-url"}, {"tag", "rename"}, {"bogus", "x"}} {
		_, err := do(t, repo, c[0], c[1], nil)
		if !errors.Is(err, store.ErrUnsupported) {
			t.Errorf("%v: want ErrUnsupported, got %v", c, err)
		}
	}
}

func TestClassify(t *testing.T) {
	b := New()
	cases := []struct {
		tool, sub string
		want      store.Verdict
	}{
		{"stage", "status", store.Allow},
		{"stage", "diff", store.Allow},
		{"branch", "log", store.Allow},
		{"branch", "rev-parse", store.Allow},
		{"branch", "diff", store.Allow},
		{"tag", "list", store.Allow},
		{"tag", "verify", store.Allow},
		{"worktree", "list", store.Allow},
		{"remote", "fetch", store.Allow},
		{"stage", "add", store.Ask},
		{"branch", "delete", store.Ask},
		{"worktree", "remove", store.Ask},
		{"remote", "push", store.Ask},
		{"stash", "save", store.Ask},
		{"commit", "", store.Ask},
		{"unknown", "verb", store.Ask},
	}
	for _, c := range cases {
		if got := b.Classify(store.Session{}, store.Op{Tool: c.tool, Subcommand: c.sub}); got != c.want {
			t.Errorf("%s %s: got %v, want %v", c.tool, c.sub, got, c.want)
		}
	}
}

// Every declared capability needs a class decision: an unclassified verb would
// silently be floor (always ask).
func TestCapabilitiesAreClassified(t *testing.T) {
	for tool, subs := range New().Capabilities() {
		for _, sub := range subs {
			if store.ClassOf(tool, sub) == store.ClassFloor {
				t.Errorf("%s %s declared but has no class", tool, sub)
			}
		}
	}
}
