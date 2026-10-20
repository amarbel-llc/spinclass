package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/store"
)

// confinedFixture is newFixture with an isolated git environment and a repo
// identity, so commits made through Do have an identity to fall back on.
func confinedFixture(t *testing.T) fixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))
	f := newFixture(t)
	for _, kv := range [][2]string{{"user.name", "Repo User"}, {"user.email", "repo@example.com"}, {"commit.gpgsign", "false"}} {
		mustRun(t, f.repo, "config", kv[0], kv[1])
	}
	return f
}

func (f fixture) session(confinement store.Confinement, identity store.Identity) store.Session {
	s := f.own()
	s.Confinement = confinement
	s.Identity = identity
	return s
}

const ownershipRefusal = "a confined caller may only touch"

func isOwnershipRefusal(err error) bool {
	return err != nil && strings.Contains(err.Error(), ownershipRefusal)
}

func lastCommitIdentity(t *testing.T, dir string) string {
	t.Helper()
	return gitOutput(t, dir, "log", "-1", "--format=author=%an <%ae> committer=%cn <%ce>")
}

var confinementNames = map[store.Confinement]string{
	store.ConfinementUnresolved: "unresolved",
	store.ConfinementConfined:   "confined",
	store.ConfinementUnconfined: "unconfined",
}

// commitVerbs are the verbs that create commits. setup prepares state with
// plain git (no Do); act runs the verb through Do in the session worktree.
var commitVerbs = []struct {
	name  string
	setup func(t *testing.T, f fixture)
	act   func(t *testing.T, f fixture, s store.Session) error
	// originalAuthor is the author a verb that replays an existing commit keeps
	// whatever is stamped; empty for verbs that author a new commit.
	originalAuthor string
}{
	{
		name: "commit",
		setup: func(t *testing.T, f fixture) {
			write(t, f.worktree, "x.txt", "x\n")
			mustRun(t, f.worktree, "add", "x.txt")
		},
		act: func(t *testing.T, f fixture, s store.Session) error {
			_, err := doAs(t, s, "commit", "", map[string]any{"message": "stamped"})
			return err
		},
	},
	{
		name: "revert",
		setup: func(t *testing.T, f fixture) {
			write(t, f.worktree, "y.txt", "y\n")
			mustRun(t, f.worktree, "add", "y.txt")
			mustRun(t, f.worktree, "commit", "-m", "to be reverted")
		},
		act: func(t *testing.T, f fixture, s store.Session) error {
			_, err := doAs(t, s, "branch", "revert", map[string]any{"commits": []string{"HEAD"}})
			return err
		},
	},
	{
		name: "cherry-pick",
		setup: func(t *testing.T, f fixture) {
			mustRun(t, f.worktree, "checkout", "-b", sessionBranch+"--pick")
			write(t, f.worktree, "p.txt", "p\n")
			mustRun(t, f.worktree, "add", "p.txt")
			mustRun(t, f.worktree, "commit", "-m", "to be picked")
			mustRun(t, f.worktree, "checkout", sessionBranch)
		},
		act: func(t *testing.T, f fixture, s store.Session) error {
			_, err := doAs(t, s, "branch", "cherry-pick", map[string]any{"commits": []string{sessionBranch + "--pick"}})
			return err
		},
		originalAuthor: "Repo User <repo@example.com>",
	},
	{
		name: "rebase",
		setup: func(t *testing.T, f fixture) {
			mustRun(t, f.repo, "commit", "--allow-empty", "-m", "main moves on")
		},
		act: func(t *testing.T, f fixture, s store.Session) error {
			_, err := doAs(t, s, "branch", "rebase", map[string]any{"upstream": "main"})
			return err
		},
		originalAuthor: "t <t@example.com>",
	},
}

func TestIdentityIsStampedOnCommitCreatingVerbs(t *testing.T) {
	full := store.Identity{AuthorName: "Op", AuthorEmail: "op@example.com", CommitterName: "Bot", CommitterEmail: "bot@example.com"}
	authorOnly := store.Identity{AuthorName: "Op", AuthorEmail: "op@example.com"}

	for _, verb := range commitVerbs {
		for _, confinement := range []store.Confinement{store.ConfinementUnconfined, store.ConfinementConfined} {
			for _, tc := range []struct {
				name     string
				identity store.Identity
				author   string
				commit   string
			}{
				{"unset leaves git's own resolution", store.Identity{}, "Repo User <repo@example.com>", "Repo User <repo@example.com>"},
				{"author and committer", full, "Op <op@example.com>", "Bot <bot@example.com>"},
				{"author set, committer left to git", authorOnly, "Op <op@example.com>", "Repo User <repo@example.com>"},
			} {
				t.Run(verb.name+"/"+confinementNames[confinement]+"/"+tc.name, func(t *testing.T) {
					f := confinedFixture(t)
					verb.setup(t, f)
					if err := verb.act(t, f, f.session(confinement, tc.identity)); err != nil {
						t.Fatal(err)
					}
					wantAuthor := tc.author
					if verb.originalAuthor != "" {
						wantAuthor = verb.originalAuthor
					}
					want := "author=" + wantAuthor + " committer=" + tc.commit
					if got := lastCommitIdentity(t, f.worktree); got != want {
						t.Errorf("got %q, want %q", got, want)
					}
				})
			}
		}
	}
}

// installMarkerHooks makes every hook that fires on the verbs under test touch
// one marker file, wired the way FDR 0019's installer does.
func installMarkerHooks(t *testing.T, f fixture) (marker string) {
	t.Helper()
	hooksDir := filepath.Join(filepath.Dir(f.repo), "hooks")
	marker = filepath.Join(filepath.Dir(f.repo), "hook-ran")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pre-commit", "commit-msg", "post-commit", "reference-transaction", "post-checkout", "post-rewrite"} {
		script := "#!/bin/sh\ntouch \"" + marker + "\"\n"
		if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustRun(t, f.repo, "config", "extensions.worktreeConfig", "true")
	mustRun(t, f.worktree, "config", "--worktree", "core.hooksPath", hooksDir)
	return marker
}

func TestHooksRunForUnconfinedCallersAndNeverForConfinedOnes(t *testing.T) {
	for _, verb := range commitVerbs {
		for _, tc := range []struct {
			confinement  store.Confinement
			wantHookRuns bool
		}{
			{store.ConfinementUnconfined, true},
			{store.ConfinementConfined, false},
			{store.ConfinementUnresolved, false},
		} {
			t.Run(verb.name+"/"+confinementNames[tc.confinement], func(t *testing.T) {
				f := confinedFixture(t)
				verb.setup(t, f)
				marker := installMarkerHooks(t, f)
				headBefore := gitOutput(t, f.worktree, "rev-parse", "HEAD")
				if err := verb.act(t, f, f.session(tc.confinement, store.Identity{})); err != nil {
					t.Fatal(err)
				}
				if gitOutput(t, f.worktree, "rev-parse", "HEAD") == headBefore {
					t.Fatal("the verb must have created a commit")
				}
				_, statErr := os.Stat(marker)
				if ran := statErr == nil; ran != tc.wantHookRuns {
					t.Errorf("hook ran = %v, want %v", ran, tc.wantHookRuns)
				}
			})
		}
	}
}

func TestConfinedFailingPreCommitHookDoesNotBlockTheCommit(t *testing.T) {
	f := confinedFixture(t)
	installHook(t, f.repo, f.worktree, `echo "hook says no" >&2; exit 1`)
	write(t, f.worktree, "x.txt", "x\n")
	mustRun(t, f.worktree, "add", "x.txt")

	if _, err := doAs(t, f.session(store.ConfinementUnconfined, store.Identity{}), "commit", "", map[string]any{"message": "m"}); err == nil {
		t.Fatal("an unconfined caller keeps the hook, so the failing hook must abort the commit")
	}
	if _, err := doAs(t, f.session(store.ConfinementConfined, store.Identity{}), "commit", "", map[string]any{"message": "m"}); err != nil {
		t.Fatalf("a confined caller's commit skips the hook: %v", err)
	}
}

func TestWithoutHooksForConfinedArgv(t *testing.T) {
	confined := store.Session{Confinement: store.ConfinementConfined}
	unconfined := store.Session{Confinement: store.ConfinementUnconfined}
	hooksOff := []string{"-c", "core.hooksPath=/dev/null"}

	for _, tc := range []struct {
		name    string
		session store.Session
		op      store.Op
		argv    []string
		want    []string
	}{
		{"confined commit gets --no-verify and no hooks", confined, store.Op{Tool: "commit"}, []string{"commit", "-m", "x"}, append(append([]string{}, hooksOff...), "commit", "--no-verify", "-m", "x")},
		{"confined amend", confined, store.Op{Tool: "commit"}, []string{"commit", "--amend", "--no-edit"}, append(append([]string{}, hooksOff...), "commit", "--no-verify", "--amend", "--no-edit")},
		{"confined cherry-pick", confined, store.Op{Tool: "branch", Subcommand: "cherry-pick"}, []string{"cherry-pick", "abc"}, append(append([]string{}, hooksOff...), "cherry-pick", "abc")},
		{"confined revert", confined, store.Op{Tool: "branch", Subcommand: "revert"}, []string{"revert", "--no-edit", "abc"}, append(append([]string{}, hooksOff...), "revert", "--no-edit", "abc")},
		{"confined rebase", confined, store.Op{Tool: "branch", Subcommand: "rebase"}, []string{"rebase", "main"}, append(append([]string{}, hooksOff...), "rebase", "main")},
		{"confined restack", confined, store.Op{Tool: "branch", Subcommand: "restack"}, []string{"rebase", "--update-refs", "--onto", "a", "b^"}, append(append([]string{}, hooksOff...), "rebase", "--update-refs", "--onto", "a", "b^")},
		{"confined checkout (post-checkout hook)", confined, store.Op{Tool: "branch", Subcommand: "checkout"}, []string{"checkout", "x", "--"}, append(append([]string{}, hooksOff...), "checkout", "x", "--")},
		{"unresolved is confined", store.Session{}, store.Op{Tool: "commit"}, []string{"commit", "-m", "x"}, append(append([]string{}, hooksOff...), "commit", "--no-verify", "-m", "x")},
		{"confined read is untouched", confined, store.Op{Tool: "branch", Subcommand: "log"}, []string{"log"}, []string{"log"}},
		{"unconfined commit is untouched", unconfined, store.Op{Tool: "commit"}, []string{"commit", "-m", "x"}, []string{"commit", "-m", "x"}},
		{"unconfined rebase is untouched", unconfined, store.Op{Tool: "branch", Subcommand: "rebase"}, []string{"rebase", "main"}, []string{"rebase", "main"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := withoutHooksForConfined(tc.session, tc.op, tc.argv)
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

type ownershipCase struct {
	name        string
	tool, sub   string
	args        func(f fixture) map[string]any
	wantRefused bool
}

func withArgs(args map[string]any) func(fixture) map[string]any {
	return func(fixture) map[string]any { return args }
}

func foreignWorktreeArgs(extra map[string]any) func(fixture) map[string]any {
	return func(f fixture) map[string]any {
		args := map[string]any{"repo_path": f.foreignWorktree}
		for k, v := range extra {
			args[k] = v
		}
		return args
	}
}

func mainCheckoutArgs(extra map[string]any) func(fixture) map[string]any {
	return func(f fixture) map[string]any {
		args := map[string]any{"repo_path": f.repo}
		for k, v := range extra {
			args[k] = v
		}
		return args
	}
}

func refState(t *testing.T, f fixture) string {
	t.Helper()
	return gitOutput(t, f.repo, "for-each-ref") + "\n" +
		gitOutput(t, f.worktree, "rev-parse", "HEAD") + "\n" +
		gitOutput(t, f.foreignWorktree, "rev-parse", "HEAD") + "\n" +
		gitOutput(t, f.repo, "rev-parse", "HEAD")
}

var ownershipCases = []ownershipCase{
	{"create own topic", "branch", "create", withArgs(map[string]any{"name": sessionBranch + "--new"}), false},
	{"create foreign name", "branch", "create", withArgs(map[string]any{"name": "foo"}), true},
	{"create default branch name", "branch", "create", withArgs(map[string]any{"name": "main"}), true},
	{"create with a refs/heads prefix is a different branch", "branch", "create", withArgs(map[string]any{"name": "refs/heads/foo"}), true},
	{"create lookalike prefix", "branch", "create", withArgs(map[string]any{"name": sessionBranch + "-topic2"}), true},

	{"delete own topic", "branch", "delete", withArgs(map[string]any{"name": sessionBranch + "--topic"}), false},
	{"delete HEAD resolves to the owned branch", "branch", "delete", withArgs(map[string]any{"name": "HEAD"}), false},
	{"delete foreign", "branch", "delete", withArgs(map[string]any{"name": "B2"}), true},
	{"delete foreign by full ref", "branch", "delete", withArgs(map[string]any{"name": "refs/heads/B2"}), true},
	{"delete foreign by heads/ abbreviation", "branch", "delete", withArgs(map[string]any{"name": "heads/B2"}), true},
	{"delete default branch", "branch", "delete", withArgs(map[string]any{"name": "main"}), true},
	{"delete lookalike", "branch", "delete", withArgs(map[string]any{"name": sessionBranch + "-topic"}), true},
	{"delete via reflog syntax", "branch", "delete", withArgs(map[string]any{"name": "@{-1}"}), true},
	{"delete forced foreign", "branch", "delete", withArgs(map[string]any{"name": "B2", "force": true}), true},

	{"checkout own topic", "branch", "checkout", withArgs(map[string]any{"ref": sessionBranch + "--topic"}), false},
	{"checkout foreign", "branch", "checkout", withArgs(map[string]any{"ref": "B2"}), true},
	{"checkout foreign by full ref", "branch", "checkout", withArgs(map[string]any{"ref": "refs/heads/B2"}), true},
	{"checkout default branch", "branch", "checkout", withArgs(map[string]any{"ref": "main"}), true},
	{"checkout new foreign branch", "branch", "checkout", withArgs(map[string]any{"ref": "foo", "new_branch": true}), true},
	{"checkout new own branch", "branch", "checkout", withArgs(map[string]any{"ref": sessionBranch + "--n", "new_branch": true}), false},

	{"reset-hard on the own branch", "branch", "reset-hard", withArgs(map[string]any{"ref": "HEAD~1"}), false},
	{"reset-hard in a foreign worktree", "branch", "reset-hard", foreignWorktreeArgs(map[string]any{"ref": "HEAD~1"}), true},
	{"reset-soft in the main checkout", "branch", "reset-soft", mainCheckoutArgs(nil), true},

	{"commit in the own worktree", "commit", "", withArgs(map[string]any{"message": "m", "allow_empty": true}), false},
	{"commit in a foreign worktree", "commit", "", foreignWorktreeArgs(map[string]any{"message": "m", "allow_empty": true}), true},
	{"commit in the main checkout", "commit", "", mainCheckoutArgs(map[string]any{"message": "m", "allow_empty": true}), true},
	{"commit via a relative path leaving the worktree", "commit", "", withArgs(map[string]any{"message": "m", "allow_empty": true, "repo_path": ".."}), true},

	{"rebase the own branch", "branch", "rebase", withArgs(map[string]any{"upstream": "main"}), false},
	{"rebase HEAD", "branch", "rebase", withArgs(map[string]any{"upstream": "main", "rebase_branch": "HEAD"}), false},
	{"rebase a foreign branch", "branch", "rebase", withArgs(map[string]any{"upstream": "main", "rebase_branch": "B2"}), true},
	{"rebase the default branch", "branch", "rebase", withArgs(map[string]any{"upstream": "B2", "rebase_branch": "main"}), true},
	{"rebase with update_refs across a foreign branch", "branch", "rebase", withArgs(map[string]any{"upstream": "main", "update_refs": true}), true},

	{"cherry-pick in a foreign worktree", "branch", "cherry-pick", foreignWorktreeArgs(map[string]any{"commits": []string{"HEAD"}}), true},
	{"revert in the main checkout", "branch", "revert", mainCheckoutArgs(map[string]any{"commits": []string{"HEAD"}}), true},
	{"stage add in a foreign worktree", "stage", "add", foreignWorktreeArgs(map[string]any{"paths": []string{"a.txt"}}), true},
	{"stage add in the own worktree", "stage", "add", withArgs(map[string]any{"paths": []string{"a.txt"}}), false},
	{"stash list of a foreign worktree", "stash", "list", foreignWorktreeArgs(nil), true},
	{"stash list of the own worktree", "stash", "list", withArgs(nil), false},

	{"read of a foreign branch is not an ownership matter", "branch", "log", withArgs(map[string]any{"ref": "B2", "oneline": true}), false},
}

func TestConfinedCallersAreRefusedUnownedTargetsInTheBody(t *testing.T) {
	for _, tc := range ownershipCases {
		for _, confinement := range []store.Confinement{store.ConfinementConfined, store.ConfinementUnresolved} {
			if confinement == store.ConfinementUnresolved && !tc.wantRefused {
				continue
			}
			t.Run(tc.name+"/"+confinementNames[confinement], func(t *testing.T) {
				f := confinedFixture(t)
				before := refState(t, f)
				_, err := doAs(t, f.session(confinement, store.Identity{}), tc.tool, tc.sub, tc.args(f))
				if got := isOwnershipRefusal(err); got != tc.wantRefused {
					t.Fatalf("ownership refusal = %v, want %v (err: %v)", got, tc.wantRefused, err)
				}
				if tc.wantRefused && refState(t, f) != before {
					t.Error("a refused operation must change nothing")
				}
			})
		}
	}
}

// A caller whose ownership cannot be computed (no session branch, or no
// session worktree) is refused every write: the hook's deferral for an
// unresolved session is only safe because of this.
func TestWritesWithoutComputableOwnershipAreRefused(t *testing.T) {
	for name, mutate := range map[string]func(*store.Session){
		"no branch":   func(s *store.Session) { s.Branch = "" },
		"no worktree": func(s *store.Session) { s.Dir = "" },
	} {
		t.Run(name, func(t *testing.T) {
			f := confinedFixture(t)
			s := f.session(store.ConfinementUnresolved, store.Identity{})
			mutate(&s)
			before := refState(t, f)
			_, err := doAs(t, s, "commit", "", map[string]any{"message": "m", "allow_empty": true, "repo_path": f.worktree})
			if !isOwnershipRefusal(err) {
				t.Fatalf("want an ownership refusal, got %v", err)
			}
			if refState(t, f) != before {
				t.Error("a refused operation must change nothing")
			}
		})
	}
}

func TestConfinedReadsAreScopedToTheSessionWorktree(t *testing.T) {
	const readRefusal = "may only read its session worktree"
	for _, tc := range []struct {
		name        string
		args        func(f fixture) map[string]any
		wantRefused bool
	}{
		{"no repo_path", withArgs(map[string]any{"oneline": true}), false},
		{"the own worktree", func(f fixture) map[string]any { return map[string]any{"repo_path": f.worktree} }, false},
		{"a subdirectory of the own worktree", func(f fixture) map[string]any { return map[string]any{"repo_path": f.worktree + "/."} }, false},
		{"a foreign worktree", foreignWorktreeArgs(nil), true},
		{"the main checkout", mainCheckoutArgs(nil), true},
		{"a relative path leaving the worktree", withArgs(map[string]any{"repo_path": ".."}), true},
		{"a path that is no repository", withArgs(map[string]any{"repo_path": "/nonexistent-dir"}), true},
	} {
		for _, confinement := range []store.Confinement{store.ConfinementConfined, store.ConfinementUnresolved} {
			t.Run(tc.name+"/"+confinementNames[confinement], func(t *testing.T) {
				f := confinedFixture(t)
				_, err := doAs(t, f.session(confinement, store.Identity{}), "branch", "log", tc.args(f))
				if got := err != nil && strings.Contains(err.Error(), readRefusal); got != tc.wantRefused {
					t.Fatalf("read refusal = %v, want %v (err: %v)", got, tc.wantRefused, err)
				}
			})
		}
		t.Run(tc.name+"/unconfined", func(t *testing.T) {
			f := confinedFixture(t)
			_, err := doAs(t, f.session(store.ConfinementUnconfined, store.Identity{}), "branch", "log", tc.args(f))
			if err != nil && strings.Contains(err.Error(), readRefusal) {
				t.Fatalf("an unconfined read is never scoped by the body, got %v", err)
			}
		})
	}
}

func TestUnconfinedCallersAreNeverRefusedByTheBody(t *testing.T) {
	for _, tc := range ownershipCases {
		t.Run(tc.name, func(t *testing.T) {
			f := confinedFixture(t)
			_, err := doAs(t, f.session(store.ConfinementUnconfined, store.Identity{}), tc.tool, tc.sub, tc.args(f))
			if isOwnershipRefusal(err) {
				t.Fatalf("the hook is the only gate for an unconfined caller, got %v", err)
			}
		})
	}
}
