package merge

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/session"
)

// writePresence writes a clown presence record for decoration (the spinclass
// session key) under $XDG_STATE_HOME/clown/presence, lastSeen at seen.
func writePresence(t *testing.T, stateHome, decoration string, seen time.Time) {
	t.Helper()
	dir := filepath.Join(stateHome, "clown", "presence")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"sessionKey":"p1","decoration":"` + decoration + `","lastSeen":"` + seen.UTC().Format(time.RFC3339Nano) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "p1.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func liveSessionRepo(t *testing.T, branch string) (repoDir, wtPath, stateHome string) {
	t.Helper()
	repoDir = setupRepo(t)
	stateHome = t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	wtPath = setupWorktree(t, repoDir, branch)
	if err := os.WriteFile(filepath.Join(wtPath, "new.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, wtPath, "add", "new.txt")
	runGit(t, wtPath, "commit", "-m", "work")
	return repoDir, wtPath, stateHome
}

func assertWorktreeKept(t *testing.T, repoDir, wtPath, branch string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(wtPath, "new.txt")); err != nil {
		t.Errorf("the live session's worktree was removed: %v", err)
	}
	if err := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", "refs/heads/"+branch).Run(); err != nil {
		t.Errorf("the live session's branch was deleted: %v", err)
	}
}

// #409: a terminal `sc merge <target>` of a session whose agent is still
// running lands the work but must not tear down the worktree under it. A
// fresh clown presence record for the session key is the liveness signal.
func TestOutOfSessionMergeKeepsALiveSessionsWorktree(t *testing.T) {
	repoDir, wtPath, stateHome := liveSessionRepo(t, "feature-live")
	writePresence(t, stateHome, filepath.Base(repoDir)+"/feature-live", time.Now())

	recs, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-live", "main", false, false)
	if err != nil {
		t.Fatalf("Resolved: %v", err)
	}
	if log := runGit(t, repoDir, "log", "--oneline", "main"); !strings.Contains(log, "work") {
		t.Errorf("the merge did not land: %s", log)
	}
	assertWorktreeKept(t, repoDir, wtPath, "feature-live")
	tr, ok := findTest(testRecords(recs), "keep worktree feature-live")
	if !ok || !tr.OK || !strings.Contains(tr.Description, "live") {
		t.Errorf("want an ok keep-worktree point naming the live session, got %+v (all: %v)", tr, testDescs(testRecords(recs)))
	}
	if _, ok := findTest(testRecords(recs), "remove worktree"); ok {
		t.Error("a remove-worktree point was emitted for a live session")
	}
}

// A session whose recorded state is active with a live PID also counts, for a
// session not running under clown. The PID is worker-writable, but trusting
// it here only ever keeps a worktree, never removes one.
func TestOutOfSessionMergeKeepsAnActiveSessionWithALivePID(t *testing.T) {
	repoDir, wtPath, _ := liveSessionRepo(t, "feature-pid")
	if err := session.Write(session.State{
		PID: os.Getpid(), SessionState: session.StateActive,
		RepoPath: repoDir, WorktreePath: wtPath, Branch: "feature-pid",
		SessionKey: filepath.Base(repoDir) + "/feature-pid", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-pid", "main", false, false); err != nil {
		t.Fatalf("Resolved: %v", err)
	}
	assertWorktreeKept(t, repoDir, wtPath, "feature-pid")
}

// A stale presence record is dead (clown RFC-0014 §4.2): the merge tears
// down as before.
func TestOutOfSessionMergeTearsDownWhenPresenceIsStale(t *testing.T) {
	repoDir, wtPath, stateHome := liveSessionRepo(t, "feature-stale")
	writePresence(t, stateHome, filepath.Base(repoDir)+"/feature-stale", time.Now().Add(-time.Hour))
	if _, err := runResolved(t, &mockExecutor{}, repoDir, wtPath, "feature-stale", "main", false, false); err != nil {
		t.Fatalf("Resolved: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("a dead session's worktree was kept: %v", err)
	}
}
