package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/spawnhandshake"
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

// happySweatfile's spawn-entry (exec'd directly — FDR-0017 Piece 1) records its
// working directory (the marker proves cmd.Dir = the worktree) and its
// positional argv (one element per line) so the test can assert {prompt}
// substitution and that the brief stayed a single element. The `launched`
// marker — which the hello sender waits on before firing — is written LAST, so
// that once the hello arrives every other side effect (argv.txt) is already on
// disk: the entry now runs concurrently with Launch (startDetached), not before
// it returns.
const happySweatfile = `[session-entry]
spawn-entry = ["sh", "-c", 'printf "%s\n" "$@" > "$PWD/argv.txt"; touch "$PWD/launched"', "sh", "{prompt}"]
`

// windowSweatfile adds a spawn-window stub recording its substituted args
// into the worktree (cmd.Dir), proving the fire-and-forget exec ran there
// with {id}/{dir} rendered AND {attach-id} resolved from the hello. The stub
// writes via temp file + mv so the test's existence poll can never observe
// the file empty between the shell's O_TRUNC open and printf's write.
const windowSweatfile = happySweatfile + `spawn-window = ["sh", "-c", 'printf "%s %s %s" "$1" "$2" "$3" > "$PWD/window.txt.tmp" && mv "$PWD/window.txt.tmp" "$PWD/window.txt"', "sh", "{id}", "{dir}", "{attach-id}"]
`

// failingWindowSweatfile's window command exits nonzero — the spawn must
// not care.
const failingWindowSweatfile = happySweatfile + `spawn-window = ["false", "{id}"]
`

// testPoshID is the posh session id the fake worker reports in its hello, so
// tests can assert it round-trips into Result and the {attach-id} window
// substitution.
const testPoshID = "b3bfa155-8881-4177-92f7-33862a309d2c"

// helloAfterLaunch plays the worker's SessionStart hook for Launch tests:
// it polls for the spawn template's marker file, derives the worker key
// from the worktree dir name, and sends the hello (carrying testPoshID) to
// the driver. Returns a stop func (call after Launch returns) and the error
// channel.
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
					helloErr <- spawnhandshake.SendHello("worker/"+branch, driverKey, testPoshID)
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
	want := res.SessionKey + " " + res.WorktreePath + " " + testPoshID
	if string(data) != want {
		t.Errorf("window.txt = %q, want %q", data, want)
	}
	// The hello's posh id must also surface on Result for the driver/CLI.
	if res.PoshSessionID != testPoshID {
		t.Errorf("Result.PoshSessionID = %q, want %q", res.PoshSessionID, testPoshID)
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

	stop, helloErr := helloAfterLaunch(t, repoPath, driverKey)

	res, err := Launch(home, repoPath, driverKey, brief, desc, 15*time.Second)
	stop()
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
	// MultiplexerID is the worker's session key (its chat target).
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
	// spawn-entry is exec'd directly with {prompt}→brief; the brief must stay a
	// single argv element (no shell joining).
	wantArgs := []string{brief}
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

// blockingSweatfile's spawn-entry records its pid, signals readiness via the
// launched marker, then blocks forever (exec sleep). spinclass must still
// return — it detaches the entry instead of waiting for it to exit. Under the
// old cmd.Run() launch this test hangs, because Launch blocks on the sleep and
// never reaches the hello wait.
const blockingSweatfile = `[session-entry]
spawn-entry = ["sh", "-c", 'echo $$ > "$PWD/entry.pid"; touch "$PWD/launched"; exec sleep 30', "sh", "{prompt}"]
`

// killEntryPidFile SIGKILLs the pid recorded by blockingSweatfile's entry, so a
// detached (setsid) sleep does not outlive the test.
func killEntryPidFile(t *testing.T, repoPath string) {
	t.Helper()
	pids, _ := filepath.Glob(filepath.Join(repoPath, ".worktrees", "*", "entry.pid"))
	for _, p := range pids {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// TestLaunchDetachesNonExitingEntry is the always-detach guarantee: a
// spawn-entry that never exits must not wedge Launch. Launch must return
// promptly once the worker's hello arrives, with the entry still running
// detached.
func TestLaunchDetachesNonExitingEntry(t *testing.T) {
	home, repoPath := newWorkerFixture(t, blockingSweatfile)
	const driverKey = "driver/detach-test"
	t.Cleanup(func() { killEntryPidFile(t, repoPath) })

	stop, helloErr := helloAfterLaunch(t, repoPath, driverKey)
	t.Cleanup(stop)

	done := make(chan error, 1)
	go func() {
		_, err := Launch(home, repoPath, driverKey, "brief", "", 15*time.Second)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Launch returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Launch did not return while the spawn-entry was still running — it blocked on the entry instead of detaching")
	}
	if herr := <-helloErr; herr != nil {
		t.Fatalf("hello goroutine: %v", herr)
	}
}

// envSweatfile's spawn-entry dumps its environment so the test can assert the
// worker (not driver) identity vars were delivered, plus a [session-entry].env
// var from the sweatfile cascade.
const envSweatfile = `[session-entry]
spawn-entry = ["sh", "-c", 'env > "$PWD/env.txt"', "sh", "{prompt}"]

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

// envLookup is the non-fatal variant of envValue: it reports whether key is
// present (used to assert a var was stripped/absent).
func envLookup(envTxt, key string) (string, bool) {
	for _, line := range strings.Split(envTxt, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v, true
		}
	}
	return "", false
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
	// The driver is a clown session, so clown exported CLOWN_SESSION_ID into
	// this process env. It must NOT leak to the worker, or the worker's clown
	// arms the driver's channel and directed chat wakes are dropped (#169).
	t.Setenv("CLOWN_SESSION_ID", "driver/test-session")

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
	// The driver's CLOWN_SESSION_ID must be stripped, not propagated, so the
	// worker's clown re-derives its channel from the worker's SPINCLASS key.
	if got, found := envLookup(envTxt, "CLOWN_SESSION_ID"); found {
		t.Errorf("CLOWN_SESSION_ID leaked to worker: got %q, want stripped", got)
	}
}

// timeoutSweatfile launches successfully but nothing ever sends the hello.
const timeoutSweatfile = `[session-entry]
spawn-entry = ["sh", "-c", 'touch "$PWD/launched"', "sh", "{prompt}"]
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
