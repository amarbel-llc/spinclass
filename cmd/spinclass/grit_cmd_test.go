package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/auth"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/store"
)

func stubGritCallerUnconfined(t *testing.T) {
	t.Helper()
	original := gritCaller
	t.Cleanup(func() { gritCaller = original })
	gritCaller = func(cwd string) store.Session {
		s := store.Session{Dir: cwd, Confinement: store.ConfinementUnconfined}
		if branch, err := git.BranchCurrent(cwd); err == nil {
			s.Branch = branch
		}
		return s
	}
}

func TestResolveGritCallerWithoutAPrincipalIsConfined(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if got := resolveGritCaller(t.TempDir()); got.Confinement != store.ConfinementConfined {
		t.Fatalf("an unresolved caller must be confined, got %v", got.Confinement)
	}
}

func TestResolveGritCallerWithAnUnrecordedPrincipalIsUnconfined(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "a-principal-with-no-records")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	removeRecord, err := registerServeRecord()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(removeRecord)
	if got := resolveGritCaller(t.TempDir()); got.Confinement != store.ConfinementUnconfined {
		t.Fatalf("a principal no record names must be unconfined, got %v", got.Confinement)
	}
}

func TestResolveGritCallerWithoutAServeRecordIsConfined(t *testing.T) {
	t.Setenv("CLOWN_SESSION_ID", "a-principal-with-no-records")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if got := resolveGritCaller(t.TempDir()); got.Confinement != store.ConfinementConfined {
		t.Fatalf("a serve with no record must be confined, got %v", got.Confinement)
	}
}

func mustGitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func isolatedGitRepo(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gitconfig"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("CLOWN_SESSION_ID", "a-principal-with-no-records")
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGitIn(t, repo, "init", "-b", "main")
	mustGitIn(t, repo, "commit", "--allow-empty", "-m", "first")
	return repo
}

func TestResolveGritCallerOutsideASessionWorktreeOwnsNothing(t *testing.T) {
	repo := isolatedGitRepo(t)
	got := resolveGritCaller(repo)
	if !got.NoSession || got.Branch != "" {
		t.Fatalf("a plain checkout is no session worktree: NoSession=%v Branch=%q (the cwd's branch must not be borrowed)", got.NoSession, got.Branch)
	}
	if got := resolveGritCaller(t.TempDir()); !got.NoSession {
		t.Error("a directory that is no repository is no session worktree")
	}
}

func TestResolveGritCallerInASessionWorktreeOwnsItsBranch(t *testing.T) {
	repo := isolatedGitRepo(t)
	worktree := filepath.Join(repo, ".worktrees", "glad-buckeye")
	mustGitIn(t, repo, "worktree", "add", "-b", "glad-buckeye", worktree)
	if err := session.Write(session.State{RepoPath: repo, WorktreePath: worktree, Branch: "glad-buckeye", SessionKey: "repo/glad-buckeye", SessionState: session.StateInactive}); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(worktree, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGitIn(t, worktree, "checkout", "-b", "glad-buckeye--topic")

	got := resolveGritCaller(sub)
	if got.NoSession || got.Dir != worktree || got.Branch != "glad-buckeye" {
		t.Fatalf("got Dir=%q Branch=%q NoSession=%v, want the session worktree and the session branch (not the checked-out topic)", got.Dir, got.Branch, got.NoSession)
	}
}

// A confined caller's remote verbs use only the frontend-state credential. A
// file in the worktree could have been planted by the unit, so with the
// frontend copy gone it gets no credential at all (and git fails on its own
// authentication error); an unconfined caller may still use a legacy copy.
func TestTrustedRepoCredentialForConfinedCallersIsFrontendOnly(t *testing.T) {
	repo := isolatedGitRepo(t)
	worktree := filepath.Join(repo, ".worktrees", "glad-buckeye")
	mustGitIn(t, repo, "worktree", "add", "-b", "glad-buckeye", worktree)
	if err := session.Write(session.State{RepoPath: repo, WorktreePath: worktree, Branch: "glad-buckeye", SessionKey: "repo/glad-buckeye", SessionState: session.StateInactive}); err != nil {
		t.Fatal(err)
	}
	legacy := auth.LegacyCredentialPath(worktree)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("https://spinclass:planted@evil.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := trustedRepoFor(worktree, true).CredentialFile; got != "" {
		t.Errorf("confined, frontend copy absent: credential %q, want none", got)
	}
	if got := trustedRepoFor(worktree, false).CredentialFile; got != legacy {
		t.Errorf("unconfined: credential %q, want the legacy copy %q", got, legacy)
	}

	state, err := auth.StateCredentialPath(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(state), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("https://spinclass:tok@forge.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, confined := range []bool{true, false} {
		if got := trustedRepoFor(worktree, confined).CredentialFile; got != state {
			t.Errorf("confined=%v with a frontend copy: credential %q, want %q", confined, got, state)
		}
	}
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	if got := trustedRepoFor(worktree, true).CredentialFile; got != "" {
		t.Errorf("confined after deleting the frontend copy: credential %q, want none", got)
	}
}

// A silent git success (stage add) and an empty error must still carry text:
// the V1 content block drops an empty `text` and clients reject the result as
// "content[0] missing text" (seen from the nikulin canary).
func TestGritHandlerNeverReturnsEmptyText(t *testing.T) {
	stubGritCallerUnconfined(t)
	repo := isolatedGitRepo(t)
	t.Chdir(repo)
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, tool, args string
	}{
		{"stage add (silent success)", "stage", `{"subcommand":"add","paths":["new.txt"]}`},
		{"unsupported verb", "branch", `{"subcommand":"no-such-verb"}`},
		{"missing subcommand", "tag", `{}`},
		{"read with output", "stage", `{"subcommand":"status"}`},
	} {
		res, err := gritHandler(tc.tool)(context.Background(), json.RawMessage(tc.args), nil)
		if err != nil || res == nil {
			t.Fatalf("%s: handler returned (%v, %v)", tc.name, res, err)
		}
		if strings.TrimSpace(res.Text) == "" {
			t.Errorf("%s: empty text, the client would see a malformed result", tc.name)
		}
	}
	if got := nonEmptyText("  \n", "fallback"); got != "fallback" {
		t.Errorf("blank text must fall back, got %q", got)
	}
}

func TestRunGritRefusesWritesOutsideASessionWorktree(t *testing.T) {
	repo := isolatedGitRepo(t)
	t.Chdir(repo)
	before, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	_, err := runGrit(context.Background(), "commit", json.RawMessage(`{"message":"m","allow_empty":true}`))
	if err == nil || !strings.Contains(err.Error(), "sc start") {
		t.Fatalf("want a refusal pointing at sc start, got %v", err)
	}
	after, _ := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if string(before) != string(after) {
		t.Error("the refused commit moved HEAD")
	}
	if _, err := runGrit(context.Background(), "branch", json.RawMessage(`{"subcommand":"log","oneline":true}`)); err != nil {
		t.Errorf("reads still work: %v", err)
	}
}

func TestStoreIdentityForHonoursOnlyLayersAboveTheRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(repo, "sweatfile"), "[store]\nauthor-name = \"Repo\"\nauthor-email = \"repo@x\"\n")
	if got := storeIdentityFor(repo); got != (store.Identity{}) {
		t.Errorf("a repo-layer identity must be ignored, got %+v", got)
	}
	write(filepath.Join(home, ".config", "spinclass", "sweatfile"), "[store]\nauthor-name = \"Global\"\nauthor-email = \"global@x\"\n")
	if got := storeIdentityFor(repo); got.AuthorName != "Global" || got.CommitterEmail != "global@x" {
		t.Errorf("the global identity must apply, got %+v", got)
	}
}

func TestGritStashToolRoundtrip(t *testing.T) {
	stubGritCallerUnconfined(t)
	repo := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(repo, "gitconfig"))
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(repo))
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"config", "commit.gpgsign", "false"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	file := filepath.Join(repo, "a.txt")
	if err := os.WriteFile(file, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "a.txt"}, {"commit", "-m", "first"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(file, []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	run := func(args string) string {
		t.Helper()
		out, err := runGrit(context.Background(), "stash", json.RawMessage(args))
		if err != nil {
			t.Fatalf("stash %s: %v", args, err)
		}
		return out
	}
	run(`{"subcommand":"save","message":"wip"}`)
	if got, _ := os.ReadFile(file); string(got) != "one\n" {
		t.Fatalf("save must restore the tree, got %q", got)
	}
	if out := run(`{"subcommand":"list"}`); !strings.Contains(out, "On main: wip") {
		t.Fatalf("list: %q", out)
	}
	run(`{"subcommand":"apply","stash_ref":"stash@{0}"}`)
	if got, _ := os.ReadFile(file); string(got) != "two\n" {
		t.Fatalf("apply must restore the change, got %q", got)
	}
	run(`{"subcommand":"drop"}`)
	if out := run(`{"subcommand":"list"}`); out != "no stash entries" {
		t.Fatalf("list after drop: %q", out)
	}
}

func TestGritToolsRegistered(t *testing.T) {
	app := buildApp()
	for _, n := range []string{"grit_stage", "grit_branch", "grit_commit", "grit_tag", "grit_worktree", "grit_stash", "grit_remote", "grit_merge", "grit_clone"} {
		if _, ok := app.GetCommand(n); !ok {
			t.Errorf("%s not registered", n)
		}
	}
}

func TestRunGritUnsupportedAndMissingSubcommand(t *testing.T) {
	_, err := runGrit(context.Background(), "tag", json.RawMessage(`{"subcommand":"rename"}`))
	if err == nil || !strings.Contains(err.Error(), "not yet supported") {
		t.Fatalf("want not-yet-supported error, got %v", err)
	}
	if _, err = runGrit(context.Background(), "tag", json.RawMessage(`{}`)); err == nil {
		t.Fatal("missing subcommand must error")
	}
}

func TestRunGritCommitNeedsNoSubcommand(t *testing.T) {
	stubGritCallerUnconfined(t)
	_, err := runGrit(context.Background(), "commit", json.RawMessage(`{}`))
	if err == nil || strings.Contains(err.Error(), "subcommand is required") || strings.Contains(err.Error(), "not yet supported") {
		t.Fatalf("commit must reach the backend (and fail on the missing message), got %v", err)
	}
}
