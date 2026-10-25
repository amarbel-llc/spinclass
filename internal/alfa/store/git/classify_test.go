package git

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/store"
)

const sessionBranch = "glad-buckeye"

type fixture struct {
	repo, worktree, foreignWorktree string
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{
		"-C", dir,
		"-c", "user.name=t", "-c", "user.email=t@example.com",
		"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false",
	}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newFixture builds a main checkout on main, the session worktree on
// glad-buckeye (two commits ahead of main), a foreign worktree on B2 (pointing
// at the session's first commit), and the lookalike branches and tags.
func newFixture(t *testing.T) fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{
		repo:            filepath.Join(root, "repo"),
		worktree:        filepath.Join(root, "repo", ".worktrees", sessionBranch),
		foreignWorktree: filepath.Join(root, "repo", ".worktrees", "other-session"),
	}
	if err := os.MkdirAll(f.repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.repo, "init", "-b", "main")
	runGit(t, f.repo, "commit", "--allow-empty", "-m", "c-1")
	runGit(t, f.repo, "commit", "--allow-empty", "-m", "c0")
	runGit(t, f.repo, "branch", sessionBranch+"--topic")
	runGit(t, f.repo, "branch", sessionBranch+"-topic")
	runGit(t, f.repo, "worktree", "add", "-b", sessionBranch, f.worktree)
	runGit(t, f.worktree, "commit", "--allow-empty", "-m", "c1")
	runGit(t, f.worktree, "branch", "B2")
	runGit(t, f.worktree, "commit", "--allow-empty", "-m", "c2")
	runGit(t, f.repo, "worktree", "add", f.foreignWorktree, "B2")
	runGit(t, f.repo, "tag", sessionBranch+"--t")
	runGit(t, f.repo, "tag", "foo")
	return f
}

func (f fixture) own() store.Session {
	return store.Session{Dir: f.worktree, Branch: sessionBranch}
}

func op(t *testing.T, tool, subcommand string, args map[string]any) store.Op {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return store.Op{Tool: tool, Subcommand: subcommand, Args: raw}
}

type classifyCase struct {
	name       string
	tool, sub  string
	args       map[string]any
	want       store.Verdict
	foreignDir bool
}

func runCases(t *testing.T, f fixture, cases []classifyCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := f.own()
			if tc.foreignDir {
				s.Dir = f.foreignWorktree
			}
			got := New().Classify(s, op(t, tc.tool, tc.sub, tc.args))
			if got != tc.want {
				t.Errorf("Classify(%s %s %v) = %v, want %v", tc.tool, tc.sub, tc.args, got, tc.want)
			}
		})
	}
}

func TestClassifyBranchDeleteLaundering(t *testing.T) {
	f := newFixture(t)
	del := func(name string, want store.Verdict) classifyCase {
		return classifyCase{name: name, tool: "branch", sub: "delete", args: map[string]any{"name": name}, want: want}
	}
	runCases(t, f, []classifyCase{
		del("glad-buckeye", store.Allow),
		del("glad-buckeye--topic", store.Allow),
		del("refs/heads/glad-buckeye--topic", store.Allow),
		// git branch -d heads/x deletes refs/heads/heads/x, which does not exist.
		del("heads/glad-buckeye--topic", store.Ask),
		del("glad-buckeye-topic", store.Ask),
		del("B2", store.Ask),
		del("refs/heads/B2", store.Ask),
		del("main", store.Ask),
		del("refs/heads/main", store.Ask),
		del("HEAD", store.Allow),
		del("@", store.Allow),
		del("glad-buckeye--topic^", store.Ask),
		del("glad-buckeye--topic~1", store.Ask),
		del("glad-buckeye--topic..main", store.Ask),
		del("glad-buckeye--topic..B2", store.Ask),
		del("main@{1}", store.Ask),
		del("glad-buckeye--topic@{upstream}", store.Ask),
		del("glad-buckeye--topic:main", store.Ask),
		del("-D", store.Ask),
		del("--glad-buckeye", store.Ask),
		del("", store.Ask),
		del("does-not-exist", store.Ask),
		{name: "name wrong type", tool: "branch", sub: "delete", args: map[string]any{"name": 7}, want: store.Ask},
		{name: "force flag", tool: "branch", sub: "delete", args: map[string]any{"name": "glad-buckeye--topic", "force": true}, want: store.Ask},
		{name: "force wrong type", tool: "branch", sub: "delete", args: map[string]any{"name": "glad-buckeye--topic", "force": "no"}, want: store.Ask},
		{name: "force explicit false", tool: "branch", sub: "delete", args: map[string]any{"name": "glad-buckeye--topic", "force": false}, want: store.Allow},
	})
}

func TestClassifyHeadIsLaunderedOnlyThroughTheCheckedOutBranch(t *testing.T) {
	f := newFixture(t)
	foreign := func(name, tool, sub string, args map[string]any) classifyCase {
		return classifyCase{name: name, tool: tool, sub: sub, args: args, want: store.Ask, foreignDir: true}
	}
	runCases(t, f, []classifyCase{
		foreign("delete HEAD on a foreign branch", "branch", "delete", map[string]any{"name": "HEAD"}),
		foreign("delete @ on a foreign branch", "branch", "delete", map[string]any{"name": "@"}),
		foreign("commit on a foreign branch", "commit", "", map[string]any{"message": "x"}),
		foreign("stage add on a foreign branch", "stage", "add", map[string]any{"paths": []any{"a"}}),
		foreign("reset-hard on a foreign branch", "branch", "reset-hard", map[string]any{"ref": "main"}),
		foreign("push HEAD on a foreign branch", "remote", "push", map[string]any{"branch": "HEAD"}),
		foreign("rebase HEAD on a foreign branch", "branch", "rebase", map[string]any{"upstream": "main"}),
		{name: "stash needs no branch ownership", tool: "stash", sub: "save", args: map[string]any{}, want: store.Allow, foreignDir: true},
	})
}

func TestClassifyNoTargetOperationsBindToCheckedOutBranch(t *testing.T) {
	f := newFixture(t)
	runCases(t, f, []classifyCase{
		{name: "commit", tool: "commit", args: map[string]any{"message": "x"}, want: store.Allow},
		{name: "merge", tool: "merge", args: map[string]any{"branch": "main"}, want: store.Allow},
		{name: "stage add", tool: "stage", sub: "add", args: map[string]any{"paths": []any{"a"}}, want: store.Allow},
		{name: "stage rm", tool: "stage", sub: "rm", args: map[string]any{"paths": []any{"a"}}, want: store.Allow},
		{name: "stage add in main checkout", tool: "stage", sub: "add", args: map[string]any{"repo_path": f.repo}, want: store.Ask},
		{name: "stage add in foreign worktree", tool: "stage", sub: "add", args: map[string]any{"repo_path": f.foreignWorktree}, want: store.Ask},
		{name: "stage add repo_path relative escape", tool: "stage", sub: "add", args: map[string]any{"repo_path": "../other-session"}, want: store.Ask},
		{name: "stage add repo_path subdir", tool: "stage", sub: "add", args: map[string]any{"repo_path": f.worktree}, want: store.Allow},
		{name: "repo_path wrong type", tool: "stage", sub: "add", args: map[string]any{"repo_path": 3}, want: store.Ask},
		{name: "cherry-pick", tool: "branch", sub: "cherry-pick", args: map[string]any{"commits": []any{"main"}}, want: store.Allow},
		{name: "cherry-pick option-like", tool: "branch", sub: "cherry-pick", args: map[string]any{"commits": []any{"--abort"}}, want: store.Ask},
		{name: "clone into the session worktree allows", tool: "clone", args: map[string]any{"source": "x", "destination": "y"}, want: store.Allow},
		{name: "clone outside the session worktree asks", tool: "clone", args: map[string]any{"source": "x", "destination": "/elsewhere/y"}, want: store.Ask},
		{name: "worktree remove of the own worktree asks", tool: "worktree", sub: "remove", args: map[string]any{"path": f.worktree}, want: store.Ask},
		{name: "unknown verb is floor", tool: "branch", sub: "obliterate", args: map[string]any{}, want: store.Ask},
	})
}

// A shelf is private to its worktree, so every stash verb (list included) needs
// only the session-worktree binding, and any other repo_path asks.
func TestClassifyStashNeedsOnlyTheSessionWorktree(t *testing.T) {
	f := newFixture(t)
	var cases []classifyCase
	for _, sub := range []string{"list", "save", "apply", "drop"} {
		cases = append(cases,
			classifyCase{name: sub + " in the session worktree", tool: "stash", sub: sub, args: map[string]any{}, want: store.Allow},
			classifyCase{name: sub + " with the worktree as repo_path", tool: "stash", sub: sub, args: map[string]any{"repo_path": f.worktree}, want: store.Allow},
			classifyCase{name: sub + " aimed at a foreign worktree", tool: "stash", sub: sub, args: map[string]any{"repo_path": f.foreignWorktree}, want: store.Ask},
			classifyCase{name: sub + " aimed at the main checkout", tool: "stash", sub: sub, args: map[string]any{"repo_path": f.repo}, want: store.Ask},
			classifyCase{name: sub + " repo_path wrong type", tool: "stash", sub: sub, args: map[string]any{"repo_path": 3}, want: store.Ask},
		)
	}
	cases = append(cases,
		classifyCase{name: "apply by position", tool: "stash", sub: "apply", args: map[string]any{"stash_ref": "stash@{1}"}, want: store.Allow},
		classifyCase{name: "drop option-like ref", tool: "stash", sub: "drop", args: map[string]any{"stash_ref": "--all"}, want: store.Ask},
		classifyCase{name: "stash_ref wrong type", tool: "stash", sub: "drop", args: map[string]any{"stash_ref": 1}, want: store.Ask},
		classifyCase{name: "save with include_untracked and paths", tool: "stash", sub: "save", args: map[string]any{"include_untracked": true, "paths": []any{"a"}, "message": "m"}, want: store.Allow},
		classifyCase{name: "unknown stash verb is floor", tool: "stash", sub: "pop", args: map[string]any{}, want: store.Ask},
	)
	runCases(t, f, cases)
}

func TestClassifyBranchCreateCheckoutAndRebase(t *testing.T) {
	f := newFixture(t)
	runCases(t, f, []classifyCase{
		{name: "create owned topic", tool: "branch", sub: "create", args: map[string]any{"name": "glad-buckeye--new"}, want: store.Allow},
		{name: "create lookalike", tool: "branch", sub: "create", args: map[string]any{"name": "glad-buckeye-new"}, want: store.Ask},
		{name: "create other session branch", tool: "branch", sub: "create", args: map[string]any{"name": "B2"}, want: store.Ask},
		{name: "create with range syntax", tool: "branch", sub: "create", args: map[string]any{"name": "glad-buckeye--x..main"}, want: store.Ask},
		{name: "create with caret", tool: "branch", sub: "create", args: map[string]any{"name": "glad-buckeye--x^"}, want: store.Ask},
		{name: "create with refs/heads form", tool: "branch", sub: "create", args: map[string]any{"name": "refs/heads/glad-buckeye--x"}, want: store.Ask},
		{name: "create with option-like start point", tool: "branch", sub: "create", args: map[string]any{"name": "glad-buckeye--x", "start_point": "--orphan"}, want: store.Ask},
		{name: "checkout owned", tool: "branch", sub: "checkout", args: map[string]any{"ref": "glad-buckeye--topic"}, want: store.Allow},
		{name: "checkout foreign", tool: "branch", sub: "checkout", args: map[string]any{"ref": "B2"}, want: store.Ask},
		{name: "checkout default", tool: "branch", sub: "checkout", args: map[string]any{"ref": "main"}, want: store.Ask},
		{name: "checkout new owned", tool: "branch", sub: "checkout", args: map[string]any{"ref": "glad-buckeye--n", "new_branch": true}, want: store.Allow},
		{name: "checkout new foreign", tool: "branch", sub: "checkout", args: map[string]any{"ref": "B2--n", "new_branch": true}, want: store.Ask},
		{name: "checkout files from default", tool: "branch", sub: "checkout", args: map[string]any{"ref": "main", "paths": []any{"a"}}, want: store.Allow},
		{name: "rebase current", tool: "branch", sub: "rebase", args: map[string]any{"upstream": "main"}, want: store.Allow},
		{name: "rebase owned topic", tool: "branch", sub: "rebase", args: map[string]any{"rebase_branch": "glad-buckeye--topic", "upstream": "main"}, want: store.Allow},
		{name: "rebase foreign", tool: "branch", sub: "rebase", args: map[string]any{"rebase_branch": "B2", "upstream": "main"}, want: store.Ask},
		{name: "rebase default", tool: "branch", sub: "rebase", args: map[string]any{"rebase_branch": "main", "upstream": "main"}, want: store.Ask},
		{name: "rebase continue", tool: "branch", sub: "rebase", args: map[string]any{"continue": true}, want: store.Allow},
		{name: "rebase update_refs past a foreign branch", tool: "branch", sub: "rebase", args: map[string]any{"upstream": "main", "update_refs": true}, want: store.Ask},
		{name: "restack past a foreign branch", tool: "branch", sub: "restack", args: map[string]any{"root": "glad-buckeye--topic", "onto": "main"}, want: store.Ask},
		{name: "restack over owned commits only", tool: "branch", sub: "restack", args: map[string]any{"root": "glad-buckeye", "onto": "main"}, want: store.Allow},
		{name: "restack foreign root", tool: "branch", sub: "restack", args: map[string]any{"root": "B2", "onto": "main"}, want: store.Ask},
		{name: "reset-hard current", tool: "branch", sub: "reset-hard", args: map[string]any{"ref": "main"}, want: store.Allow},
	})
}

// Operator ruling: rebases and resets of owned branches never prompt; anything
// aimed at the default branch, an unowned branch or another checkout does.
func TestClassifyRebaseAndResetOfOwnedBranchesNeverPrompt(t *testing.T) {
	f := newFixture(t)
	runGit(t, f.repo, "update-ref", "refs/remotes/origin/main", "main")
	runCases(t, f, []classifyCase{
		{name: "reset-soft owned current", tool: "branch", sub: "reset-soft", args: map[string]any{}, want: store.Allow},
		{name: "reset-soft owned current with ref", tool: "branch", sub: "reset-soft", args: map[string]any{"ref": "HEAD~1"}, want: store.Allow},
		{name: "reset-soft aimed at the main checkout", tool: "branch", sub: "reset-soft", args: map[string]any{"repo_path": f.repo}, want: store.Ask},
		{name: "reset-soft aimed at a foreign worktree", tool: "branch", sub: "reset-soft", args: map[string]any{"repo_path": f.foreignWorktree}, want: store.Ask},
		{name: "reset-soft on an unowned current branch", tool: "branch", sub: "reset-soft", args: map[string]any{}, want: store.Ask, foreignDir: true},
		{name: "reset-hard aimed at the main checkout", tool: "branch", sub: "reset-hard", args: map[string]any{"ref": "main", "repo_path": f.repo}, want: store.Ask},
		{name: "reset-hard on an unowned current branch", tool: "branch", sub: "reset-hard", args: map[string]any{"ref": "main"}, want: store.Ask, foreignDir: true},
		{name: "rebase onto the default remote-tracking ref", tool: "branch", sub: "rebase", args: map[string]any{"upstream": "origin/main"}, want: store.Allow},
		{name: "rebase with onto and an owned rebase_branch", tool: "branch", sub: "rebase", args: map[string]any{"upstream": "main", "onto": "origin/main", "rebase_branch": "glad-buckeye--topic"}, want: store.Allow},
		{name: "rebase abort", tool: "branch", sub: "rebase", args: map[string]any{"abort": true}, want: store.Allow},
		{name: "rebase skip", tool: "branch", sub: "rebase", args: map[string]any{"skip": true}, want: store.Allow},
		{name: "rebase continue on an unowned current branch", tool: "branch", sub: "rebase", args: map[string]any{"continue": true}, want: store.Ask, foreignDir: true},
		{name: "rebase abort on an unowned current branch", tool: "branch", sub: "rebase", args: map[string]any{"abort": true}, want: store.Ask, foreignDir: true},
		{name: "rebase skip on an unowned current branch", tool: "branch", sub: "rebase", args: map[string]any{"skip": true}, want: store.Ask, foreignDir: true},
		{name: "restack with the default branch as root", tool: "branch", sub: "restack", args: map[string]any{"root": "main", "onto": "main"}, want: store.Ask},
		{name: "restack on an unowned current branch", tool: "branch", sub: "restack", args: map[string]any{"root": "glad-buckeye", "onto": "main"}, want: store.Ask, foreignDir: true},
	})

	// The session's current branch is the default branch: its checkout is the
	// main one, so a session bound there owns nothing it is standing on.
	onDefault := store.Session{Dir: f.repo, Branch: sessionBranch}
	for _, tc := range []struct {
		name, sub string
		args      map[string]any
	}{
		{"reset-hard", "reset-hard", map[string]any{"ref": "HEAD~1"}},
		{"reset-soft", "reset-soft", map[string]any{}},
		{"rebase continue", "rebase", map[string]any{"continue": true}},
		{"restack", "restack", map[string]any{"root": "glad-buckeye", "onto": "main"}},
	} {
		t.Run("current branch is the default: "+tc.name, func(t *testing.T) {
			if got := New().Classify(onDefault, op(t, "branch", tc.sub, tc.args)); got != store.Ask {
				t.Errorf("Classify = %v, want ask", got)
			}
		})
	}
}

func TestClassifyTagsAndRemotes(t *testing.T) {
	f := newFixture(t)
	runCases(t, f, []classifyCase{
		{name: "tag create owned", tool: "tag", sub: "create", args: map[string]any{"name": "glad-buckeye--v1"}, want: store.Allow},
		{name: "tag create unowned", tool: "tag", sub: "create", args: map[string]any{"name": "v1"}, want: store.Ask},
		{name: "tag create lookalike", tool: "tag", sub: "create", args: map[string]any{"name": "glad-buckeye-v1"}, want: store.Ask},
		{name: "tag delete owned", tool: "tag", sub: "delete", args: map[string]any{"name": "glad-buckeye--t"}, want: store.Allow},
		{name: "tag delete unowned", tool: "tag", sub: "delete", args: map[string]any{"name": "foo"}, want: store.Ask},
		{name: "tag push owned", tool: "tag", sub: "push", args: map[string]any{"name": "glad-buckeye--t"}, want: store.Allow},
		{name: "tag push delete", tool: "tag", sub: "push", args: map[string]any{"name": "glad-buckeye--t", "delete": true}, want: store.Ask},
		{name: "tag list", tool: "tag", sub: "list", args: map[string]any{"pattern": "*"}, want: store.Allow},
		{name: "tag list option-like pattern", tool: "tag", sub: "list", args: map[string]any{"pattern": "--delete"}, want: store.Ask},
		{name: "push current", tool: "remote", sub: "push", args: map[string]any{}, want: store.Allow},
		{name: "push owned with lease", tool: "remote", sub: "push", args: map[string]any{"branch": "glad-buckeye--topic", "force_with_lease": true}, want: store.Allow},
		{name: "push default", tool: "remote", sub: "push", args: map[string]any{"branch": "main"}, want: store.Ask},
		{name: "push owned to default", tool: "remote", sub: "push", args: map[string]any{"remote_branch": "main"}, want: store.Ask},
		{name: "push owned to refs/heads default", tool: "remote", sub: "push", args: map[string]any{"remote_branch": "refs/heads/main"}, want: store.Ask},
		{name: "push owned to foreign", tool: "remote", sub: "push", args: map[string]any{"remote_branch": "B2"}, want: store.Ask},
		{name: "push refspec colon", tool: "remote", sub: "push", args: map[string]any{"branch": "glad-buckeye:main"}, want: store.Ask},
		{name: "push force", tool: "remote", sub: "push", args: map[string]any{"force": true}, want: store.Ask},
		{name: "push delete", tool: "remote", sub: "push", args: map[string]any{"delete": true}, want: store.Ask},
		{name: "push-stack owned", tool: "remote", sub: "push-stack", args: map[string]any{"branches": []any{"glad-buckeye", "glad-buckeye--topic"}}, want: store.Allow},
		{name: "push-stack with foreign", tool: "remote", sub: "push-stack", args: map[string]any{"branches": []any{"glad-buckeye", "B2"}}, want: store.Ask},
		{name: "push-stack empty", tool: "remote", sub: "push-stack", args: map[string]any{"branches": []any{}}, want: store.Ask},
		{name: "fetch", tool: "remote", sub: "fetch", args: map[string]any{}, want: store.Allow},
		{name: "pull current", tool: "remote", sub: "pull", args: map[string]any{}, want: store.Allow},
	})
}

func TestClassifyReadVerbsAllowWithoutSession(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		name string
		op   store.Op
		want store.Verdict
	}{
		{"status", op(t, "stage", "status", nil), store.Allow},
		{"log of any ref", op(t, "branch", "log", map[string]any{"ref": "B2"}), store.Allow},
		{"log option-like ref", op(t, "branch", "log", map[string]any{"ref": "--output=/x"}), store.Ask},
		{"worktree list", op(t, "worktree", "list", nil), store.Allow},
		{"write needs a session", op(t, "commit", "", nil), store.Ask},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := New().Classify(store.Session{}, tc.op); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
	t.Run("write without branch", func(t *testing.T) {
		if got := New().Classify(store.Session{Dir: f.worktree}, op(t, "commit", "", nil)); got != store.Ask {
			t.Errorf("got %v, want ask", got)
		}
	})
	t.Run("malformed arguments", func(t *testing.T) {
		if got := New().Classify(f.own(), store.Op{Tool: "stage", Subcommand: "status", Args: json.RawMessage("{")}); got != store.Ask {
			t.Errorf("got %v, want ask", got)
		}
	})
}

func TestOwned(t *testing.T) {
	f := newFixture(t)
	s := f.own()
	for _, tc := range []struct {
		ref  string
		want bool
	}{
		{"refs/heads/glad-buckeye", true},
		{"refs/heads/glad-buckeye--topic", true},
		{"refs/heads/glad-buckeye-topic", false},
		{"refs/heads/glad-buckeye--x..main", false},
		{"refs/heads/B2", false},
		{"refs/heads/main", false},
		{"refs/remotes/origin/glad-buckeye--topic", true},
		{"refs/remotes/origin/main", false},
		{"refs/tags/glad-buckeye--t", true},
		{"refs/tags/glad-buckeye-t", false},
		{"refs/spinclass/glad-buckeye/stash/1", true},
		{"refs/spinclass/B2/stash/1", false},
		{"glad-buckeye", false},
		{"HEAD", false},
		{"@", false},
		{f.worktree, true},
		{f.foreignWorktree, false},
		{f.repo, false},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			if got := New().Owned(s, store.Ref(tc.ref)); got != tc.want {
				t.Errorf("Owned(%q) = %v, want %v", tc.ref, got, tc.want)
			}
		})
	}
	t.Run("empty session owns nothing", func(t *testing.T) {
		if New().Owned(store.Session{}, "refs/heads/glad-buckeye") {
			t.Error("empty session must own nothing")
		}
	})
	t.Run("default branch is never owned even as the session branch", func(t *testing.T) {
		onMain := store.Session{Dir: f.repo, Branch: "main"}
		if New().Owned(onMain, "refs/heads/main") {
			t.Error("main must never be owned")
		}
	})
	t.Run("origin HEAD target is the default branch", func(t *testing.T) {
		runGit(t, f.repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
		onTrunk := store.Session{Dir: f.repo, Branch: "trunk"}
		if New().Owned(onTrunk, "refs/heads/trunk") {
			t.Error("origin/HEAD target must never be owned")
		}
	})
}
