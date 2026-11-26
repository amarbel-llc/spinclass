package spawn

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// madeBySpawns lists the worktrees and the branches the worker repo has beyond
// its main checkout.
func madeBySpawns(t *testing.T, repoPath string) (worktrees, branches []string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repoPath, ".worktrees"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	for _, e := range entries {
		worktrees = append(worktrees, e.Name())
	}
	out, err := exec.Command("git", "-C", repoPath, "for-each-ref", "--format=%(refname:short)", "refs/heads").Output()
	if err != nil {
		t.Fatal(err)
	}
	current, err := exec.Command("git", "-C", repoPath, "branch", "--show-current").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range strings.Fields(string(out)) {
		if b != strings.TrimSpace(string(current)) {
			branches = append(branches, b)
		}
	}
	return worktrees, branches
}

const rollbackEntry = "[session-entry]\nspawn-entry = [\"true\", \"--\", \"{prompt}\"]\n"

func TestASpawnWhoseKeyHolderCannotStartCreatesNothing(t *testing.T) {
	home, repoPath := newWorkerFixture(t, rollbackEntry)
	prev := KeyholderPreflight
	t.Cleanup(func() { KeyholderPreflight = prev })
	KeyholderPreflight = func(_, _, _ string) error { return errors.New("no user manager; next: wake it") }
	started := 0
	withKeyholderHook(t, func(_, _, _ string) (map[string]string, func(), error) {
		started++
		return nil, nil, nil
	})
	_, err := LaunchDetachedWith(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "", ConfineDefault)
	if err == nil || !strings.Contains(err.Error(), "next: wake it") {
		t.Fatalf("err = %v", err)
	}
	if wt, br := madeBySpawns(t, repoPath); len(wt) != 0 || len(br) != 0 || started != 0 {
		t.Errorf("worktrees %v, branches %v, pair starts %d: want nothing", wt, br, started)
	}
}

func TestASpawnWhosePairFailsToStartLeavesNothing(t *testing.T) {
	home, repoPath := newWorkerFixture(t, rollbackEntry)
	withKeyholderHook(t, func(_, _, _ string) (map[string]string, func(), error) {
		return nil, nil, errors.New("starting fibby: boom")
	})
	_, err := LaunchDetachedWith(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "", ConfineDefault)
	if err == nil || !strings.Contains(err.Error(), "starting fibby: boom") || !strings.Contains(err.Error(), "nothing is left") {
		t.Fatalf("err = %v", err)
	}
	if wt, br := madeBySpawns(t, repoPath); len(wt) != 0 || len(br) != 0 {
		t.Errorf("worktrees %v, branches %v: want nothing", wt, br)
	}
}

// The spawn says whether the worker got its own holder: what the caller prints.
func TestASpawnReportsWhetherTheWorkerHasItsOwnKeyHolder(t *testing.T) {
	for name, kh := range map[string]map[string]string{"own": {"SSH_AUTH_SOCK": "/holder/agent.sock"}, "none": nil} {
		t.Run(name, func(t *testing.T) {
			home, repoPath := newWorkerFixture(t, rollbackEntry)
			withKeyholderHook(t, func(_, _, _ string) (map[string]string, func(), error) { return kh, nil, nil })
			p, err := LaunchDetachedWith(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "", ConfineDefault)
			if err != nil {
				t.Fatal(err)
			}
			if p.OwnKeyHolder != (kh != nil) {
				t.Errorf("OwnKeyHolder = %v", p.OwnKeyHolder)
			}
		})
	}
}

// A worktree that holds work is never removed: the refusal names the command.
func TestARefusedSpawnNeverRemovesAWorktreeThatHoldsWork(t *testing.T) {
	cases := map[string]func(t *testing.T, wt string){
		"an uncommitted file": func(t *testing.T, wt string) {
			if err := os.WriteFile(filepath.Join(wt, "precious"), []byte("work\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"a commit": func(t *testing.T, wt string) {
			cmd := exec.Command("git", "-C", wt, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "--no-verify", "-q", "-m", "work")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("commit: %v: %s", err, out)
			}
		},
	}
	for name, leaveWork := range cases {
		t.Run(name, func(t *testing.T) {
			home, repoPath := newWorkerFixture(t, rollbackEntry)
			var worktree, key string
			withKeyholderHook(t, func(_, _, sessionKey string) (map[string]string, func(), error) {
				key = sessionKey
				worktree = filepath.Join(repoPath, ".worktrees", sessionKey[strings.Index(sessionKey, "/")+1:])
				leaveWork(t, worktree)
				return nil, nil, errors.New("boom")
			})
			_, err := LaunchDetachedWith(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "", ConfineDefault)
			if err == nil || !strings.Contains(err.Error(), "sc close "+key+" --force") {
				t.Fatalf("err = %v, want the command that removes %s", err, key)
			}
			if _, serr := os.Stat(worktree); serr != nil {
				t.Errorf("the worktree that holds work is gone: %v", serr)
			}
		})
	}
}
