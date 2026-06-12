package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// windowSweatfile adds a spawn-window stub recording its substituted args
// into the worktree (cmd.Dir), proving the fire-and-forget exec ran there
// with {id}/{dir} rendered.
const windowSweatfile = happySweatfile + `spawn-window = ["sh", "-c", 'printf "%s %s" "$1" "$2" > "$PWD/window.txt"', "sh", "{id}", "{dir}"]
`

// failingWindowSweatfile's window command exits nonzero — the spawn must
// not care.
const failingWindowSweatfile = happySweatfile + `spawn-window = ["false", "{id}"]
`

// helloAfterLaunch plays the worker's SessionStart hook for Launch tests:
// it polls for the spawn template's marker file, derives the worker key
// from the worktree dir name, and sends the hello to the driver. Returns
// a stop func (call after Launch returns) and the error channel.
func helloAfterLaunch(t *testing.T, repoPath, driverKey string) (func(), chan error) {
	t.Helper()
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
	var once sync.Once
	return func() { once.Do(func() { close(stop) }) }, helloErr
}

func TestLaunchSpawnWindowFires(t *testing.T) {
	home, repoPath := newWorkerFixture(t, windowSweatfile)
	const driverKey = "driver/window-test"
	stop, helloErr := helloAfterLaunch(t, repoPath, driverKey)

	res, err := Launch(home, repoPath, driverKey, "brief", "", 15*time.Second)
	stop()
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if herr := <-helloErr; herr != nil {
		t.Fatalf("hello goroutine: %v", herr)
	}

	// The window command is fire-and-forget (async): poll briefly.
	deadline := time.Now().Add(5 * time.Second)
	var data []byte
	for time.Now().Before(deadline) {
		if b, rerr := os.ReadFile(filepath.Join(res.WorktreePath, "window.txt")); rerr == nil {
			data = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	want := res.SessionKey + " " + res.WorktreePath
	if string(data) != want {
		t.Errorf("window.txt = %q, want %q", data, want)
	}
}

func TestLaunchSpawnWindowFailureDoesNotFailSpawn(t *testing.T) {
	home, repoPath := newWorkerFixture(t, failingWindowSweatfile)
	const driverKey = "driver/window-fail-test"
	stop, helloErr := helloAfterLaunch(t, repoPath, driverKey)

	_, err := Launch(home, repoPath, driverKey, "brief", "", 15*time.Second)
	stop()
	if err != nil {
		t.Fatalf("Launch failed because of the window command: %v", err)
	}
	if herr := <-helloErr; herr != nil {
		t.Fatalf("hello goroutine: %v", herr)
	}
}

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

	branch := filepath.Base(res.WorktreePath)
	if branch == "" || branch == "." {
		t.Fatal("empty worktree basename")
	}
	if want := "worker/" + branch; res.SessionKey != want {
		t.Errorf("SessionKey: got %q, want %q", res.SessionKey, want)
	}
	// {id} is the full session key — the name start/resume/liveness-probe
	// entries address multiplexer sessions by (#146).
	if res.MultiplexerID != res.SessionKey {
		t.Errorf("MultiplexerID: got %q, want session key %q", res.MultiplexerID, res.SessionKey)
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
	wantArgs := []string{res.SessionKey, "true", brief}
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

// envSweatfile's spawn template dumps its environment so the test can
// assert the worker (not driver) identity vars were delivered, plus a
// [session-entry].env var from the sweatfile cascade.
const envSweatfile = `[session-entry]
spawn       = ["sh", "-c", 'env > "$1/env.txt"', "sh", "{dir}", "{entry}"]
spawn-entry = ["true", "{prompt}"]

[session-entry.env]
SPINCLASS_GROUP = "workers"
`

// envValue scans `env` output for key and returns its value. Scanning line
// prefixes (instead of building a map) sidesteps host env vars whose values
// contain newlines.
func envValue(t *testing.T, envTxt, key string) string {
	t.Helper()
	for _, line := range strings.Split(envTxt, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	t.Errorf("env.txt has no %s", key)
	return ""
}

// TestLaunchExecEnvCarriesWorkerIdentity guards the spawn exec's cmd.Env:
// the worker process must see the WORKER's spinclass identity (mirroring
// executor.SessionExecutor.Attach's env construction), not the driver's
// inherited SPINCLASS_* vars — otherwise a multiplexer that propagates
// client env hands the worker the driver's chat identity.
func TestLaunchExecEnvCarriesWorkerIdentity(t *testing.T) {
	home, repoPath := newWorkerFixture(t, envSweatfile)
	// The driver's identity, baked into this process env the way
	// executor.SessionExecutor does for a real driver session.
	t.Setenv("SPINCLASS_SESSION_ID", "driver/test-session")
	t.Setenv("SPINCLASS_WORKTREE", "/driver/worktree")

	const desc = "env worker"
	// No hello sender: Launch errors on the hello deadline, but the spawn
	// template has already run by then and written env.txt.
	_, err := Launch(home, repoPath, "driver/test-session", "do work", desc, 300*time.Millisecond)
	if err == nil {
		t.Fatal("expected hello-deadline error, got nil")
	}

	envFiles, _ := filepath.Glob(filepath.Join(repoPath, ".worktrees", "*", "env.txt"))
	if len(envFiles) != 1 {
		t.Fatalf("expected exactly one env.txt, got %v", envFiles)
	}
	wt := filepath.Dir(envFiles[0])
	branch := filepath.Base(wt)
	data, err := os.ReadFile(envFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	envTxt := string(data)

	tmpDir := filepath.Join(wt, ".tmp")
	want := map[string]string{
		"SPINCLASS_SESSION_ID":  "worker/" + branch,
		"SPINCLASS_REPO":        "worker",
		"SPINCLASS_BRANCH":      branch,
		"SPINCLASS_WORKTREE":    wt,
		"SPINCLASS_DESCRIPTION": desc,
		"TMPDIR":                tmpDir,
		"CLAUDE_CODE_TMPDIR":    tmpDir,
		// From the sweatfile [session-entry].env layer.
		"SPINCLASS_GROUP": "workers",
	}
	for k, v := range want {
		if got := envValue(t, envTxt, k); got != v {
			t.Errorf("%s: got %q, want %q", k, got, v)
		}
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

	// Template validation runs BEFORE any worktree or session-state
	// creation: bad config must not litter. No worktree at all…
	worktrees, _ := filepath.Glob(filepath.Join(repoPath, ".worktrees", "*"))
	if len(worktrees) != 0 {
		t.Errorf("worktree created despite missing spawn-entry: %v", worktrees)
	}
	// …and no session state file in the central index.
	states, _ := filepath.Glob(filepath.Join(home, ".local", "state", "spinclass", "sessions", "*"))
	if len(states) != 0 {
		t.Errorf("session state written despite missing spawn-entry: %v", states)
	}
}
