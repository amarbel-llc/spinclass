package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/spinclass/internal/chat"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/testgit"
)

// newWorkerFixture sandboxes HOME (worktree creation trusts the workspace in
// ~/.claude.json) and XDG_STATE_HOME (session index + chatroom), then builds
// a worker repo at $HOME/eng/repos/worker with the given sweatfile.
func newWorkerFixture(t *testing.T, sweatfileTOML string) (home, repoPath string) {
	t.Helper()
	testgit.RequireGit(t)
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	repoPath = filepath.Join(home, "eng", "repos", "worker")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.MustInit(t, repoPath)
	if sweatfileTOML != "" {
		if err := os.WriteFile(filepath.Join(repoPath, "sweatfile"), []byte(sweatfileTOML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home, repoPath
}

// happySweatfile's spawn template records its working directory (the marker
// proves cmd.Dir = the worktree) and its positional argv (one element per
// line) so the test can assert {id} substitution and that the brief stayed
// a single element.
const happySweatfile = `[session-entry]
spawn       = ["sh", "-c", 'touch "$PWD/launched"; printf "%s\n" "$@" > "$PWD/argv.txt"', "sh", "{id}", "{entry}"]
spawn-entry = ["true", "{prompt}"]
`

func TestLaunchHappyPath(t *testing.T) {
	home, repoPath := newWorkerFixture(t, happySweatfile)
	const driverKey = "driver/test-session"
	const desc = "test worker"
	brief := `fix the "thing" with spaces`

	// The stub template cannot send the hello itself (SendHello is a Go
	// API), so a goroutine plays the worker's SessionStart hook: poll for
	// the template's marker, derive the worker key from the worktree dir
	// name, send the hello to the driver.
	helloErr := make(chan error, 1)
	stop := make(chan struct{})
	go func() {
		deadline := time.After(15 * time.Second)
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				helloErr <- nil
				return
			case <-deadline:
				helloErr <- fmt.Errorf("spawn template marker never appeared")
				return
			case <-tick.C:
				matches, _ := filepath.Glob(filepath.Join(repoPath, ".worktrees", "*", "launched"))
				if len(matches) == 1 {
					branch := filepath.Base(filepath.Dir(matches[0]))
					helloErr <- chat.SendHello("worker/"+branch, driverKey)
					return
				}
			}
		}
	}()

	res, err := Launch(home, repoPath, driverKey, brief, desc, 15*time.Second)
	close(stop)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if herr := <-helloErr; herr != nil {
		t.Fatalf("hello goroutine: %v", herr)
	}

	branch := res.MultiplexerID
	if branch == "" {
		t.Fatal("empty MultiplexerID")
	}
	if want := "worker/" + branch; res.SessionKey != want {
		t.Errorf("SessionKey: got %q, want %q", res.SessionKey, want)
	}
	if want := filepath.Join(repoPath, ".worktrees", branch); res.WorktreePath != want {
		t.Errorf("WorktreePath: got %q, want %q", res.WorktreePath, want)
	}

	// Substitution threading: the template ran with cmd.Dir = the worktree.
	if _, err := os.Stat(filepath.Join(res.WorktreePath, "launched")); err != nil {
		t.Errorf("marker not in worktree: %v", err)
	}
	argv, err := os.ReadFile(filepath.Join(res.WorktreePath, "argv.txt"))
	if err != nil {
		t.Fatalf("argv.txt: %v", err)
	}
	gotArgs := strings.Split(strings.TrimRight(string(argv), "\n"), "\n")
	wantArgs := []string{branch, "true", brief}
	if len(gotArgs) != len(wantArgs) {
		t.Fatalf("argv: got %d elements %q, want %q (brief must stay one element)", len(gotArgs), gotArgs, wantArgs)
	}
	for i := range wantArgs {
		if gotArgs[i] != wantArgs[i] {
			t.Errorf("argv[%d]: got %q, want %q", i, gotArgs[i], wantArgs[i])
		}
	}

	st, err := session.Read(repoPath, branch)
	if err != nil {
		t.Fatalf("session.Read: %v", err)
	}
	if st.SpawnedBy != driverKey {
		t.Errorf("SpawnedBy: got %q, want %q", st.SpawnedBy, driverKey)
	}
	if st.Description != desc {
		t.Errorf("Description: got %q, want %q", st.Description, desc)
	}
	if st.SessionKey != res.SessionKey {
		t.Errorf("SessionKey in state: got %q, want %q", st.SessionKey, res.SessionKey)
	}
	if st.PID != 0 {
		t.Errorf("PID: got %d, want 0 (adopted later by the worker's SessionStart hook)", st.PID)
	}
	if got := st.Env["SPINCLASS_SESSION_ID"]; got != res.SessionKey {
		t.Errorf("Env[SPINCLASS_SESSION_ID]: got %q, want %q", got, res.SessionKey)
	}
}

// timeoutSweatfile launches successfully but nothing ever sends the hello.
const timeoutSweatfile = `[session-entry]
spawn       = ["sh", "-c", 'touch "$PWD/launched"', "sh", "{entry}"]
spawn-entry = ["true", "{prompt}"]
`

func TestLaunchHelloTimeout(t *testing.T) {
	home, repoPath := newWorkerFixture(t, timeoutSweatfile)
	const driverKey = "driver/test-session"
	deadline := 500 * time.Millisecond

	_, err := Launch(home, repoPath, driverKey, "do work", "desc", deadline)
	if err == nil {
		t.Fatal("expected hello-deadline error, got nil")
	}
	if !strings.Contains(err.Error(), deadline.String()) {
		t.Errorf("error %q does not mention the deadline %s", err, deadline)
	}

	// A timed-out spawn intentionally leaves the worktree and its state
	// behind for inspection — `sc close` cleans it up.
	markers, _ := filepath.Glob(filepath.Join(repoPath, ".worktrees", "*", "launched"))
	if len(markers) != 1 {
		t.Fatalf("expected exactly one launched worktree, got %v", markers)
	}
	branch := filepath.Base(filepath.Dir(markers[0]))
	st, err := session.Read(repoPath, branch)
	if err != nil {
		t.Fatalf("session.Read after timeout: %v", err)
	}
	if st.SpawnedBy != driverKey {
		t.Errorf("SpawnedBy after timeout: got %q, want %q", st.SpawnedBy, driverKey)
	}
}

// noEntrySweatfile configures a spawn template but no spawn-entry harness.
const noEntrySweatfile = `[session-entry]
spawn = ["sh", "-c", 'touch "$PWD/launched"', "sh", "{entry}"]
`

func TestLaunchMissingSpawnEntryErrorsBeforeExec(t *testing.T) {
	home, repoPath := newWorkerFixture(t, noEntrySweatfile)

	_, err := Launch(home, repoPath, "driver/test-session", "do work", "desc", time.Second)
	if err == nil {
		t.Fatal("expected missing spawn-entry error, got nil")
	}
	if !strings.Contains(err.Error(), "[session-entry].spawn-entry") {
		t.Errorf("error %q does not name [session-entry].spawn-entry", err)
	}

	// The error fired before any template exec: no marker anywhere.
	markers, _ := filepath.Glob(filepath.Join(repoPath, ".worktrees", "*", "launched"))
	if len(markers) != 0 {
		t.Errorf("spawn template ran despite missing spawn-entry: %v", markers)
	}
}
