package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, repo, name, content, message string) {
	t.Helper()
	write(t, repo, name, content)
	mustRun(t, repo, "add", name)
	mustRun(t, repo, "commit", "-m", message)
}

func currentBranch(t *testing.T, repo string) string {
	t.Helper()
	return gitOutput(t, repo, "branch", "--show-current")
}

func subject(t *testing.T, repo, rev string) string {
	t.Helper()
	return gitOutput(t, repo, "log", "-1", "--format=%s", rev)
}

func wantErrContaining(t *testing.T, err error, substring string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substring) {
		t.Fatalf("want error containing %q, got %v", substring, err)
	}
}

func asConflict(t *testing.T, err error) *ConflictError {
	t.Helper()
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want *ConflictError, got %v", err)
	}
	return conflict
}

func TestStageAdd(t *testing.T) {
	repo := newRepo(t)
	write(t, repo, "new.txt", "x\n")
	write(t, repo, "intent.txt", "y\n")

	if _, err := do(t, repo, "stage", "add", nil); err == nil {
		t.Fatal("add without paths must error")
	}
	if _, err := do(t, repo, "stage", "add", map[string]any{"paths": []string{"new.txt"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := do(t, repo, "stage", "add", map[string]any{"paths": []string{"intent.txt"}, "intent_to_add": true}); err != nil {
		t.Fatal(err)
	}
	status := gitOutput(t, repo, "status", "--short")
	if !strings.Contains(status, "A  new.txt") || !strings.Contains(status, "A intent.txt") || strings.Contains(status, "A  intent.txt") {
		t.Fatalf("status: %q", status)
	}
}

func TestStagePathsAreNeverOptions(t *testing.T) {
	repo := newRepo(t)
	write(t, repo, "new.txt", "x\n")
	if _, err := do(t, repo, "stage", "add", map[string]any{"paths": []string{"-A"}}); err == nil {
		t.Fatal("a path named -A must be a pathspec, not an option")
	}
	if status := gitOutput(t, repo, "status", "--short"); !strings.Contains(status, "?? new.txt") {
		t.Fatalf("nothing may be staged, got %q", status)
	}
}

func TestStageUnstage(t *testing.T) {
	repo := newRepo(t)
	write(t, repo, "one.txt", "1\n")
	write(t, repo, "two.txt", "2\n")
	mustRun(t, repo, "add", "one.txt", "two.txt")

	if _, err := do(t, repo, "stage", "unstage", map[string]any{"paths": []string{"one.txt"}}); err != nil {
		t.Fatal(err)
	}
	status := gitOutput(t, repo, "status", "--short")
	if !strings.Contains(status, "?? one.txt") || !strings.Contains(status, "A  two.txt") {
		t.Fatalf("after unstaging one path: %q", status)
	}
	if _, err := do(t, repo, "stage", "unstage", nil); err != nil {
		t.Fatal(err)
	}
	if status = gitOutput(t, repo, "status", "--short"); strings.Contains(status, "A  ") {
		t.Fatalf("after unstaging all: %q", status)
	}
}

func TestStageRm(t *testing.T) {
	repo := newRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	commitFile(t, repo, "d/f.txt", "f\n", "add d")

	if _, err := do(t, repo, "stage", "rm", nil); err == nil {
		t.Fatal("rm without paths must error")
	}
	if _, err := do(t, repo, "stage", "rm", map[string]any{"paths": []string{"d"}}); err == nil {
		t.Fatal("rm of a directory needs recursive")
	}
	if _, err := do(t, repo, "stage", "rm", map[string]any{"paths": []string{"d"}, "recursive": true}); err != nil {
		t.Fatal(err)
	}
	if status := gitOutput(t, repo, "status", "--short"); !strings.Contains(status, "D  d/f.txt") {
		t.Fatalf("status: %q", status)
	}

	write(t, repo, "a.txt", "modified\n")
	if _, err := do(t, repo, "stage", "rm", map[string]any{"paths": []string{"a.txt"}}); err == nil {
		t.Fatal("rm of a locally modified file needs force")
	}
	if _, err := do(t, repo, "stage", "rm", map[string]any{"paths": []string{"a.txt"}, "force": true}); err != nil {
		t.Fatal(err)
	}
}

func TestStageMv(t *testing.T) {
	repo := newRepo(t)
	if _, err := do(t, repo, "stage", "mv", map[string]any{"source": "a.txt"}); err == nil {
		t.Fatal("mv without destination must error")
	}
	if _, err := do(t, repo, "stage", "mv", map[string]any{"source": "a.txt", "destination": "b.txt"}); err != nil {
		t.Fatal(err)
	}
	if status := gitOutput(t, repo, "status", "--short"); !strings.Contains(status, "R  a.txt -> b.txt") {
		t.Fatalf("status: %q", status)
	}
}

func TestCommit(t *testing.T) {
	cases := []struct {
		name        string
		stageChange bool
		args        map[string]any
		wantSubject string
		wantErr     string
	}{
		{"message", true, map[string]any{"message": "second"}, "second", ""},
		{"allow empty", false, map[string]any{"message": "empty", "allow_empty": true}, "empty", ""},
		{"nothing staged", false, map[string]any{"message": "x"}, "", "nothing"},
		{"missing message", true, map[string]any{}, "", "message is required"},
		{"amend keeps the message", false, map[string]any{"amend": true}, "first", ""},
		{"amend rewrites the message", false, map[string]any{"amend": true, "message": "renamed"}, "renamed", ""},
		{"fixup ignores message", true, map[string]any{"fixup": "HEAD", "message": "ignored"}, "fixup! first", ""},
		{"squash ignores message", true, map[string]any{"squash": "HEAD", "message": "ignored"}, "squash! first", ""},
		{"fixup and squash", true, map[string]any{"fixup": "HEAD", "squash": "HEAD"}, "", "mutually exclusive"},
		{"amend and fixup", true, map[string]any{"amend": true, "fixup": "HEAD"}, "", "amend cannot be combined"},
		{"option-like fixup", true, map[string]any{"fixup": "--amend"}, "", "option-like"},
		{"option-like squash", true, map[string]any{"squash": "-x"}, "", "option-like"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := newRepo(t)
			if c.stageChange {
				write(t, repo, "change.txt", "c\n")
				mustRun(t, repo, "add", "change.txt")
			}
			_, err := do(t, repo, "commit", "", c.args)
			if c.wantErr != "" {
				wantErrContaining(t, err, c.wantErr)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := subject(t, repo, "HEAD"); got != c.wantSubject {
				t.Fatalf("subject %q, want %q", got, c.wantSubject)
			}
		})
	}
}

func TestBranchCreateDeleteCheckout(t *testing.T) {
	repo := newRepo(t)
	v1 := gitOutput(t, repo, "rev-parse", "v1")

	if _, err := do(t, repo, "branch", "create", nil); err == nil {
		t.Fatal("create without name must error")
	}
	if _, err := do(t, repo, "branch", "create", map[string]any{"name": "feat", "start_point": "v1"}); err != nil {
		t.Fatal(err)
	}
	if got := gitOutput(t, repo, "rev-parse", "feat"); got != v1 {
		t.Fatalf("feat at %s, want %s", got, v1)
	}
	if _, err := do(t, repo, "branch", "create", map[string]any{"name": "feat"}); err == nil {
		t.Fatal("creating an existing branch must error")
	}

	mustRun(t, repo, "checkout", "-b", "wip")
	commitFile(t, repo, "w.txt", "w\n", "wip work")
	mustRun(t, repo, "checkout", "main")
	if _, err := do(t, repo, "branch", "delete", map[string]any{"name": "wip"}); err == nil {
		t.Fatal("deleting an unmerged branch needs force")
	}
	if _, err := do(t, repo, "branch", "delete", map[string]any{"name": "wip", "force": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := do(t, repo, "branch", "delete", map[string]any{"name": "feat"}); err != nil {
		t.Fatal(err)
	}

	if _, err := do(t, repo, "branch", "checkout", map[string]any{"ref": "topic"}); err != nil {
		t.Fatal(err)
	}
	if got := currentBranch(t, repo); got != "topic" {
		t.Fatalf("current branch %q", got)
	}
	if _, err := do(t, repo, "branch", "checkout", map[string]any{"ref": "fresh", "new_branch": true, "start_point": "v1"}); err != nil {
		t.Fatal(err)
	}
	if got := currentBranch(t, repo); got != "fresh" {
		t.Fatalf("current branch %q", got)
	}

	write(t, repo, "a.txt", "scribble\n")
	if _, err := do(t, repo, "branch", "checkout", map[string]any{"paths": []string{"a.txt"}}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(data) != "one\n" {
		t.Fatalf("a.txt not restored: %q", data)
	}

	for name, args := range map[string]map[string]any{
		"unknown ref":                {"ref": "nope"},
		"missing ref":                {},
		"ours without paths":         {"ref": "topic", "ours": true},
		"ours and theirs":            {"paths": []string{"a.txt"}, "ours": true, "theirs": true},
		"theirs without any pathset": {"ref": "topic", "theirs": true},
	} {
		if _, err := do(t, repo, "branch", "checkout", args); err == nil {
			t.Errorf("%s: must error", name)
		}
	}
}

func TestBranchResets(t *testing.T) {
	repo := newRepo(t)
	mustRun(t, repo, "checkout", "-b", "work")
	commitFile(t, repo, "x.txt", "x\n", "second")

	if _, err := do(t, repo, "branch", "reset-soft", nil); err != nil {
		t.Fatal(err)
	}
	if got := subject(t, repo, "HEAD"); got != "first" {
		t.Fatalf("HEAD subject %q", got)
	}
	if status := gitOutput(t, repo, "status", "--short"); !strings.Contains(status, "A  x.txt") {
		t.Fatalf("soft reset must keep the index: %q", status)
	}
	mustRun(t, repo, "commit", "-m", "second again")

	if _, err := do(t, repo, "branch", "reset-hard", nil); err == nil {
		t.Fatal("reset-hard needs a ref")
	}
	if _, err := do(t, repo, "branch", "reset-hard", map[string]any{"ref": "HEAD~1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "x.txt")); !os.IsNotExist(err) {
		t.Fatalf("hard reset must drop x.txt, stat err %v", err)
	}
}

func TestRefusesDestructiveOpsOnDefaultBranch(t *testing.T) {
	cases := []struct {
		name     string
		checkout string
		tool     string
		sub      string
		args     map[string]any
	}{
		{"delete main", "topic", "branch", "delete", map[string]any{"name": "main"}},
		{"delete full main ref, forced", "topic", "branch", "delete", map[string]any{"name": "refs/heads/main", "force": true}},
		{"delete master, forced", "topic", "branch", "delete", map[string]any{"name": "master", "force": true}},
		{"reset-hard on main", "main", "branch", "reset-hard", map[string]any{"ref": "topic"}},
		{"rebase main", "main", "branch", "rebase", map[string]any{"upstream": "topic"}},
		{"rebase_branch main", "topic", "branch", "rebase", map[string]any{"upstream": "topic", "rebase_branch": "main"}},
		{"rebase_branch full master ref", "topic", "branch", "rebase", map[string]any{"upstream": "topic", "rebase_branch": "refs/heads/master"}},
		{"restack on main", "main", "branch", "restack", map[string]any{"root": "topic", "onto": "topic"}},
		{"restack with main as root", "topic", "branch", "restack", map[string]any{"root": "main", "onto": "topic"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := newRepo(t)
			mustRun(t, repo, "branch", "master")
			mustRun(t, repo, "checkout", c.checkout)
			refsBefore := gitOutput(t, repo, "for-each-ref")
			headBefore := gitOutput(t, repo, "rev-parse", "HEAD")

			_, err := do(t, repo, c.tool, c.sub, c.args)
			wantErrContaining(t, err, "default branch")
			if got := gitOutput(t, repo, "for-each-ref"); got != refsBefore {
				t.Fatalf("refs moved:\n%s\nwas:\n%s", got, refsBefore)
			}
			if got := gitOutput(t, repo, "rev-parse", "HEAD"); got != headBefore {
				t.Fatalf("HEAD moved to %s from %s", got, headBefore)
			}
		})
	}
}

func TestRefusesOriginHeadTargetAsDefaultBranch(t *testing.T) {
	repo := newRepo(t)
	mustRun(t, repo, "branch", "trunk")
	mustRun(t, repo, "update-ref", "refs/remotes/origin/trunk", "trunk")
	mustRun(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	mustRun(t, repo, "checkout", "topic")

	_, err := do(t, repo, "branch", "delete", map[string]any{"name": "trunk", "force": true})
	wantErrContaining(t, err, "default branch")
	if gitOutput(t, repo, "branch", "--list", "trunk") == "" {
		t.Fatal("trunk was deleted")
	}
}

func TestOptionLikeValuesAreRefused(t *testing.T) {
	cases := []struct {
		tool, sub string
		args      map[string]any
	}{
		{"branch", "create", map[string]any{"name": "--x"}},
		{"branch", "create", map[string]any{"name": "ok", "start_point": "--x"}},
		{"branch", "delete", map[string]any{"name": "-D"}},
		{"branch", "reset-soft", map[string]any{"ref": "--hard"}},
		{"branch", "reset-hard", map[string]any{"ref": "--x"}},
		{"branch", "checkout", map[string]any{"ref": "--orphan=x"}},
		{"branch", "rebase", map[string]any{"upstream": "--x"}},
		{"branch", "rebase", map[string]any{"upstream": "main", "onto": "--x"}},
		{"branch", "rebase", map[string]any{"upstream": "main", "rebase_branch": "--x"}},
		{"branch", "restack", map[string]any{"root": "--x", "onto": "main"}},
		{"branch", "restack", map[string]any{"root": "topic", "onto": "--x"}},
		{"branch", "cherry-pick", map[string]any{"commits": []string{"--x"}}},
		{"branch", "revert", map[string]any{"commits": []string{"HEAD", "--x"}}},
		{"commit", "", map[string]any{"message": "m", "fixup": "--x"}},
		{"commit", "", map[string]any{"message": "m", "squash": "--x"}},
	}
	for _, c := range cases {
		repo := newRepo(t)
		mustRun(t, repo, "checkout", "topic")
		_, err := do(t, repo, c.tool, c.sub, c.args)
		if err == nil || !strings.Contains(err.Error(), "option-like") {
			t.Errorf("%s %s %v: want option-like refusal, got %v", c.tool, c.sub, c.args, err)
		}
	}
}

// divergedRepo has main and work (checked out) both changing a.txt, so a
// rebase of work onto main conflicts.
func divergedRepo(t *testing.T) string {
	t.Helper()
	repo := newRepo(t)
	mustRun(t, repo, "checkout", "-b", "work")
	commitFile(t, repo, "a.txt", "work\n", "work change")
	mustRun(t, repo, "checkout", "main")
	commitFile(t, repo, "a.txt", "main\n", "main change")
	mustRun(t, repo, "checkout", "work")
	return repo
}

func TestRebaseConflictLeavesInProgressStateAndAbortRestores(t *testing.T) {
	repo := divergedRepo(t)
	workBefore := gitOutput(t, repo, "rev-parse", "work")

	_, err := do(t, repo, "branch", "rebase", map[string]any{"upstream": "main"})
	conflict := asConflict(t, err)
	if conflict.ExitCode != 1 || len(conflict.Paths) != 1 || conflict.Paths[0] != "a.txt" {
		t.Fatalf("conflict details: %+v", conflict)
	}
	if !strings.Contains(conflict.Error(), "continue=true") || !strings.Contains(conflict.Error(), "a.txt") {
		t.Fatalf("message: %s", conflict.Error())
	}
	if !gitPathExists(repo, "rebase-merge") {
		t.Fatal("rebase must be left in progress")
	}

	if _, err = do(t, repo, "branch", "rebase", map[string]any{"abort": true}); err != nil {
		t.Fatal(err)
	}
	if gitPathExists(repo, "rebase-merge") || gitOutput(t, repo, "rev-parse", "work") != workBefore {
		t.Fatal("abort must restore the branch")
	}
}

func TestRebaseConflictContinueAndSkip(t *testing.T) {
	t.Run("continue", func(t *testing.T) {
		repo := divergedRepo(t)
		_, err := do(t, repo, "branch", "rebase", map[string]any{"upstream": "main"})
		asConflict(t, err)
		write(t, repo, "a.txt", "resolved\n")
		mustRun(t, repo, "add", "a.txt")
		if _, err = do(t, repo, "branch", "rebase", map[string]any{"continue": true}); err != nil {
			t.Fatal(err)
		}
		if subject(t, repo, "HEAD") != "work change" || subject(t, repo, "HEAD~1") != "main change" {
			t.Fatalf("history: %s", gitOutput(t, repo, "log", "--format=%s"))
		}
	})
	t.Run("skip", func(t *testing.T) {
		repo := divergedRepo(t)
		_, err := do(t, repo, "branch", "rebase", map[string]any{"upstream": "main"})
		asConflict(t, err)
		if _, err = do(t, repo, "branch", "rebase", map[string]any{"skip": true}); err != nil {
			t.Fatal(err)
		}
		if got := subject(t, repo, "HEAD"); got != "main change" {
			t.Fatalf("HEAD subject %q", got)
		}
	})
}

func TestRebaseControlFlagErrors(t *testing.T) {
	repo := newRepo(t)
	mustRun(t, repo, "checkout", "topic")
	if _, err := do(t, repo, "branch", "rebase", map[string]any{"continue": true}); err == nil {
		t.Fatal("continue without a rebase in progress must error")
	} else if errors.As(err, new(*ConflictError)) {
		t.Fatalf("not a conflict: %v", err)
	}
	wantErrContaining(t, func() error {
		_, err := do(t, repo, "branch", "rebase", map[string]any{"continue": true, "abort": true})
		return err
	}(), "mutually exclusive")
	wantErrContaining(t, func() error {
		_, err := do(t, repo, "branch", "rebase", nil)
		return err
	}(), "upstream is required")
}

func TestRebaseAutosquash(t *testing.T) {
	repo := newRepo(t)
	mustRun(t, repo, "checkout", "-b", "work")
	commitFile(t, repo, "b.txt", "b\n", "add b")
	commitFile(t, repo, "c.txt", "c\n", "add c")
	write(t, repo, "b.txt", "b2\n")
	mustRun(t, repo, "add", "b.txt")
	if _, err := do(t, repo, "commit", "", map[string]any{"fixup": "HEAD~1"}); err != nil {
		t.Fatal(err)
	}

	if _, err := do(t, repo, "branch", "rebase", map[string]any{"upstream": "main", "autosquash": true}); err != nil {
		t.Fatal(err)
	}
	if got := gitOutput(t, repo, "log", "--format=%s", "main..HEAD"); got != "add c\nadd b" {
		t.Fatalf("history after autosquash: %q", got)
	}
	if got := gitOutput(t, repo, "show", "HEAD~1:b.txt"); got != "b2" {
		t.Fatalf("fixup content not folded in: %q", got)
	}
}

func TestRebaseAutostash(t *testing.T) {
	repo := newRepo(t)
	mustRun(t, repo, "checkout", "-b", "work")
	commitFile(t, repo, "w.txt", "w\n", "work change")
	mustRun(t, repo, "checkout", "main")
	commitFile(t, repo, "m.txt", "m\n", "main change")
	mustRun(t, repo, "checkout", "work")
	write(t, repo, "a.txt", "dirty\n")

	if _, err := do(t, repo, "branch", "rebase", map[string]any{"upstream": "main"}); err == nil {
		t.Fatal("rebase with a dirty tree must fail without autostash")
	}
	if _, err := do(t, repo, "branch", "rebase", map[string]any{"upstream": "main", "autostash": true}); err != nil {
		t.Fatal(err)
	}
	if got := gitOutput(t, repo, "status", "--short"); !strings.Contains(got, "M a.txt") {
		t.Fatalf("dirty change not restored: %q", got)
	}
}

func TestRebaseOntoAndUpdateRefs(t *testing.T) {
	repo := newRepo(t)
	mustRun(t, repo, "checkout", "-b", "b1")
	commitFile(t, repo, "b1.txt", "1\n", "b1")
	mustRun(t, repo, "checkout", "-b", "b2")
	commitFile(t, repo, "b2.txt", "2\n", "b2")
	mustRun(t, repo, "checkout", "main")
	commitFile(t, repo, "m.txt", "m\n", "main change")
	mustRun(t, repo, "checkout", "b2")

	if _, err := do(t, repo, "branch", "rebase", map[string]any{"upstream": "main", "update_refs": true}); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "merge-base", "--is-ancestor", "main", "b1")
	mustRun(t, repo, "merge-base", "--is-ancestor", "b1", "b2")

	mustRun(t, repo, "checkout", "-b", "grafted", "b2")
	if _, err := do(t, repo, "branch", "rebase", map[string]any{"upstream": "b1", "onto": "topic"}); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "merge-base", "--is-ancestor", "topic", "grafted")
	if got := gitOutput(t, repo, "log", "--format=%s", "topic..grafted"); got != "b2" {
		t.Fatalf("grafted history: %q", got)
	}
}

func TestRestack(t *testing.T) {
	repo := newRepo(t)
	for _, branch := range []string{"b1", "b2", "b3"} {
		mustRun(t, repo, "checkout", "-b", branch)
		commitFile(t, repo, branch+".txt", branch+"\n", branch)
	}
	mustRun(t, repo, "checkout", "main")
	commitFile(t, repo, "m.txt", "m\n", "main change")
	mustRun(t, repo, "checkout", "b3")

	if _, err := do(t, repo, "branch", "restack", map[string]any{"root": "b1"}); err == nil {
		t.Fatal("restack needs onto")
	}
	if _, err := do(t, repo, "branch", "restack", map[string]any{"root": "b1", "onto": "main"}); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "merge-base", "--is-ancestor", "main", "b1")
	mustRun(t, repo, "merge-base", "--is-ancestor", "b1", "b2")
	mustRun(t, repo, "merge-base", "--is-ancestor", "b2", "b3")
	if got := currentBranch(t, repo); got != "b3" {
		t.Fatalf("restack left %q checked out", got)
	}
	if got := gitOutput(t, repo, "log", "--format=%s", "main..b3"); got != "b3\nb2\nb1" {
		t.Fatalf("restacked history: %q", got)
	}
}

func TestCherryPickAndRevert(t *testing.T) {
	repo := newRepo(t)
	mustRun(t, repo, "checkout", "-b", "src")
	commitFile(t, repo, "s.txt", "s\n", "src change")
	srcSha := gitOutput(t, repo, "rev-parse", "HEAD")
	mustRun(t, repo, "checkout", "-b", "work", "main")

	if _, err := do(t, repo, "branch", "cherry-pick", nil); err == nil {
		t.Fatal("cherry-pick needs commits")
	}
	headBefore := gitOutput(t, repo, "rev-parse", "HEAD")
	if _, err := do(t, repo, "branch", "cherry-pick", map[string]any{"commits": []string{srcSha}, "no_commit": true}); err != nil {
		t.Fatal(err)
	}
	if gitOutput(t, repo, "rev-parse", "HEAD") != headBefore || !strings.Contains(gitOutput(t, repo, "status", "--short"), "A  s.txt") {
		t.Fatal("no_commit must stage without committing")
	}
	mustRun(t, repo, "reset", "--hard", "HEAD")

	if _, err := do(t, repo, "branch", "cherry-pick", map[string]any{"commits": []string{srcSha}}); err != nil {
		t.Fatal(err)
	}
	if got := subject(t, repo, "HEAD"); got != "src change" {
		t.Fatalf("cherry-picked subject %q", got)
	}

	if _, err := do(t, repo, "branch", "revert", nil); err == nil {
		t.Fatal("revert needs commits")
	}
	if _, err := do(t, repo, "branch", "revert", map[string]any{"commits": []string{"HEAD"}}); err != nil {
		t.Fatal(err)
	}
	if got := subject(t, repo, "HEAD"); got != `Revert "src change"` {
		t.Fatalf("revert subject %q", got)
	}
}

func TestCherryPickAndRevertConflictsLeaveInProgressState(t *testing.T) {
	repo := newRepo(t)
	mustRun(t, repo, "checkout", "-b", "src")
	commitFile(t, repo, "a.txt", "src\n", "src change")
	srcSha := gitOutput(t, repo, "rev-parse", "HEAD")
	mustRun(t, repo, "checkout", "-b", "work", "main")
	commitFile(t, repo, "a.txt", "work\n", "work change")

	_, err := do(t, repo, "branch", "cherry-pick", map[string]any{"commits": []string{srcSha}})
	conflict := asConflict(t, err)
	if len(conflict.Paths) != 1 || conflict.Paths[0] != "a.txt" || !gitPathExists(repo, "CHERRY_PICK_HEAD") {
		t.Fatalf("cherry-pick conflict: %+v", conflict)
	}
	mustRun(t, repo, "cherry-pick", "--abort")

	commitFile(t, repo, "a.txt", "later\n", "later change")
	_, err = do(t, repo, "branch", "revert", map[string]any{"commits": []string{"HEAD~1"}})
	asConflict(t, err)
	if !gitPathExists(repo, "REVERT_HEAD") {
		t.Fatal("revert must be left in progress")
	}
}

func linkedWorktree(t *testing.T, repo string) string {
	t.Helper()
	wt := filepath.Join(filepath.Dir(repo), "sess")
	mustRun(t, repo, "worktree", "add", "-b", "sess", wt)
	return wt
}

func TestSpinclassStateIsNeverCapturedInAWorktree(t *testing.T) {
	repo := newRepo(t)
	wt := linkedWorktree(t, repo)

	// spinclass applies its excludes to the common .git/info/exclude.
	excludes := sweatfile.GetDefault().GitExcludes()
	if !containsString(excludes, ".spinclass/") {
		t.Fatalf("default excludes must cover .spinclass/, got %v", excludes)
	}
	infoDir := filepath.Join(repo, ".git", "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, infoDir, "exclude", strings.Join(excludes, "\n")+"\n")

	if err := os.MkdirAll(filepath.Join(wt, ".spinclass"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(wt, ".spinclass"), "job.log", "log\n")
	write(t, wt, "real.txt", "r\n")

	if _, err := do(t, wt, "stage", "add", map[string]any{"paths": []string{".spinclass/job.log"}}); err == nil {
		t.Fatal("explicitly adding an excluded path must be refused, never forced")
	}
	if _, err := do(t, wt, "stage", "add", map[string]any{"paths": []string{"."}}); err != nil {
		t.Fatal(err)
	}
	if _, err := do(t, wt, "commit", "", map[string]any{"message": "real work"}); err != nil {
		t.Fatal(err)
	}
	tracked := gitOutput(t, wt, "ls-files")
	if !strings.Contains(tracked, "real.txt") || strings.Contains(tracked, ".spinclass") {
		t.Fatalf("tracked files: %q", tracked)
	}
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// installHook points the worktree's own core.hooksPath at a directory holding
// one pre-commit script, the same wiring FDR 0019's installer uses
// (extensions.worktreeConfig + `config --worktree core.hooksPath`).
func installHook(t *testing.T, repo, wt, script string) {
	t.Helper()
	hooksDir := filepath.Join(filepath.Dir(repo), "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "config", "extensions.worktreeConfig", "true")
	mustRun(t, wt, "config", "--worktree", "core.hooksPath", hooksDir)
}

func TestCommitRunsThePerWorktreeHook(t *testing.T) {
	repo := newRepo(t)
	wt := linkedWorktree(t, repo)
	marker := filepath.Join(filepath.Dir(repo), "hook-ran")
	installHook(t, repo, wt, `touch "`+marker+`"`)

	write(t, wt, "x.txt", "x\n")
	mustRun(t, wt, "add", "x.txt")
	if _, err := do(t, wt, "commit", "", map[string]any{"message": "hooked"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("pre-commit hook did not run through grit_commit: %v", err)
	}
}

func TestFailingHookAbortsTheCommit(t *testing.T) {
	repo := newRepo(t)
	wt := linkedWorktree(t, repo)
	installHook(t, repo, wt, `echo "hook says no" >&2; exit 1`)

	write(t, wt, "x.txt", "x\n")
	mustRun(t, wt, "add", "x.txt")
	headBefore := gitOutput(t, wt, "rev-parse", "HEAD")
	_, err := do(t, wt, "commit", "", map[string]any{"message": "blocked"})
	wantErrContaining(t, err, "hook says no")
	if gitOutput(t, wt, "rev-parse", "HEAD") != headBefore {
		t.Fatal("a failing hook must abort the commit")
	}
}
