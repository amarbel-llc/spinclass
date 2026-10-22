package spawn

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/worktree"
)

// The worker's own worktree sweatfile (which it can write) must not decide the
// spawn-entry, env or any trusted setting: renderSpawn reads the main checkout's
// layers only.
func TestRenderSpawnIgnoresWorktreeSweatfile(t *testing.T) {
	home, repoPath := newWorkerFixture(t, `[session-entry]
spawn-entry = ["from-repo", "{prompt}"]
disable-auto-mode = true
[session-entry.env]
MARK = "repo"
`)
	wt := filepath.Join(repoPath, worktree.WorktreesDir, "w1")
	cmd := exec.Command("git", "worktree", "add", "-b", "w1", wt)
	cmd.Dir = repoPath
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(wt, "sweatfile"), []byte(`[session-entry]
spawn-entry = ["from-worktree", "{prompt}"]
[session-entry.env]
MARK = "worktree"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	rp := worktree.ResolvedPath{AbsPath: wt, RepoPath: repoPath, Branch: "w1", SessionKey: "worker/w1"}
	argv, _, env, _, _, _, err := renderSpawn(home, rp, "the brief", "", "")
	if err != nil {
		t.Fatalf("renderSpawn: %v", err)
	}
	if want := []string{"from-repo", "the brief"}; !reflect.DeepEqual(argv, want) {
		t.Errorf("argv = %v, want %v (the worktree sweatfile leaked in)", argv, want)
	}
	if env["MARK"] != "repo" {
		t.Errorf("env MARK = %q, want repo", env["MARK"])
	}

	// A corrupt worktree sweatfile must not fail the render either.
	if err := os.WriteFile(filepath.Join(wt, "sweatfile"), []byte("[session-entry\n=== not toml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, _, err := renderSpawn(home, rp, "the brief", "", ""); err != nil {
		t.Errorf("a corrupt worktree sweatfile failed renderSpawn: %v", err)
	}
}

// A spawned worker's session state pins the entry its operator-controlled
// layers resolved at spawn, for its later `sc resume`.
func TestLaunchPinsSessionEntry(t *testing.T) {
	home, repoPath := newWorkerFixture(t, happySweatfile+`start = ["/bin/pinned-start"]
resume = ["/bin/pinned-resume"]
[session-entry.env]
MARK = "pinned"
`)
	const driverKey = "driver/pin-test"
	stop, helloErr := helloAfterLaunch(t, repoPath, driverKey)
	res, err := Launch(home, repoPath, testDriver(driverKey), "brief", "", "", "", 15*time.Second)
	stop()
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if herr := <-helloErr; herr != nil {
		t.Fatalf("hello goroutine: %v", herr)
	}

	st, err := session.Read(repoPath, filepath.Base(res.WorktreePath))
	if err != nil {
		t.Fatal(err)
	}
	want := &session.PinnedEntry{
		Start: []string{"/bin/pinned-start"}, Resume: []string{"/bin/pinned-resume"},
		Env: map[string]string{"MARK": "pinned"},
	}
	if !reflect.DeepEqual(st.Entry, want) {
		t.Errorf("Entry = %+v, want %+v", st.Entry, want)
	}
}
