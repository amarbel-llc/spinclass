package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/spawnhandshake"
	"github.com/amarbel-llc/spinclass/internal/testgit"
)

// TestHandleForkSessionValidation exercises the cheap parameter rejections:
// they must fire as error results BEFORE any worktree/state creation, so no
// fixture repo is needed (HOME is a bare sandbox; cwd is irrelevant because
// brief/timeout are checked first).
func TestHandleForkSessionValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPINCLASS_SESSION_ID", "driver/test-session")
	t.Chdir(t.TempDir())

	cases := []struct {
		name string
		args string
		want string
	}{
		{"missing brief", `{}`, "brief is required"},
		{"bad hello-timeout", `{"brief":"do","hello-timeout":"bogus"}`, "invalid hello-timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := handleForkSession(context.Background(), json.RawMessage(tc.args), nil)
			if err != nil {
				t.Fatalf("handleForkSession: %v", err)
			}
			if !res.IsErr {
				t.Fatalf("expected error result, got success: %s", res.Text)
			}
			if !strings.Contains(res.Text, tc.want) {
				t.Errorf("error text = %q, want it to contain %q", res.Text, tc.want)
			}
		})
	}
}

// TestHandleForkSessionNotInWorktree verifies the v1 layout constraint: the
// caller must sit inside an sc worktree (.worktrees/<branch>). A plain dir —
// and, by the same IsWorktree check, an implicit main-checkout session — gets
// a clear rejection instead of a confusing git error.
func TestHandleForkSessionNotInWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPINCLASS_SESSION_ID", "driver/test-session")
	t.Chdir(t.TempDir())

	res, err := handleForkSession(context.Background(), json.RawMessage(`{"brief":"do the thing"}`), nil)
	if err != nil {
		t.Fatalf("handleForkSession: %v", err)
	}
	if !res.IsErr {
		t.Fatalf("expected error result outside a worktree, got success: %s", res.Text)
	}
	if !strings.Contains(res.Text, "worktree") {
		t.Errorf("error text = %q, want it to mention the worktree requirement", res.Text)
	}
}

// TestHandleForkSessionMainCheckout pins the v1 main-checkout decision: an
// implicit (main-checkout) caller is rejected with the same clear error —
// CLI `sc fork` has the identical .worktrees-layout constraint today.
func TestHandleForkSessionMainCheckout(t *testing.T) {
	testgit.RequireGit(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("SPINCLASS_SESSION_ID", "driver/test-session")
	repoPath := filepath.Join(home, "eng", "repos", "worker")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.MustInit(t, repoPath)
	t.Chdir(repoPath)

	res, err := handleForkSession(context.Background(), json.RawMessage(`{"brief":"do the thing"}`), nil)
	if err != nil {
		t.Fatalf("handleForkSession: %v", err)
	}
	if !res.IsErr {
		t.Fatalf("expected error result from a main checkout, got success: %s", res.Text)
	}
	if !strings.Contains(res.Text, "worktree") {
		t.Errorf("error text = %q, want it to mention the worktree requirement", res.Text)
	}
}

// newForkCmdFixture builds the detached-fork caller fixture: a repo at
// $HOME/eng/repos/worker carrying the stub spawn sweatfile (the hierarchy
// loads templates from the repo level) plus an sc-style worktree at
// .worktrees/<branch> that becomes the cwd, with the driver identity pinned
// via $SPINCLASS_SESSION_ID.
func newForkCmdFixture(t *testing.T, sweatfileTOML, driverKey, branch string) (repoPath, wtPath string) {
	t.Helper()
	testgit.RequireGit(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("SPINCLASS_SESSION_ID", driverKey)
	repoPath = filepath.Join(home, "eng", "repos", "worker")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.MustInit(t, repoPath)
	if err := os.WriteFile(filepath.Join(repoPath, "sweatfile"), []byte(sweatfileTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	wtPath = filepath.Join(repoPath, ".worktrees", branch)
	testgit.MustWorktreeAdd(t, repoPath, wtPath, branch)
	t.Chdir(wtPath)
	return repoPath, wtPath
}

// awaitForkHello plays the forked worker's SessionStart hook: wait for the
// stub spawn template's marker file in the new worktree, then send the chat
// hello from the worker's session key to the driver. Returns a channel that
// yields the goroutine's outcome and a stop channel to abort it.
func awaitForkHello(newPath, workerKey, driverKey string) (<-chan error, chan<- struct{}) {
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
				helloErr <- fmt.Errorf("spawn template marker never appeared in %s", newPath)
				return
			case <-tick.C:
				if _, err := os.Stat(filepath.Join(newPath, "launched")); err == nil {
					helloErr <- spawnhandshake.SendHello(workerKey, driverKey, "")
					return
				}
			}
		}
	}()
	return helloErr, stop
}

// TestHandleForkSessionHappyPath drives the MCP handler end to end over the
// stub-template fixture (mirroring TestHandleSpawnSessionHappyPath, but the
// caller cwd is an sc worktree and the fork stays same-repo): a goroutine
// plays the worker's SessionStart hook (marker file appears → send the
// hello), and the result must carry the forked worker's session key,
// worktree path, multiplexer id, and the chat hint naming the driver key.
func TestHandleForkSessionHappyPath(t *testing.T) {
	const driverKey = "worker/feat"
	repoPath, _ := newForkCmdFixture(t, spawnCmdHappySweatfile, driverKey, "feat")

	newPath := filepath.Join(repoPath, ".worktrees", "feat-fork")
	helloErr, stop := awaitForkHello(newPath, "worker/feat-fork", driverKey)

	res, err := handleForkSession(
		context.Background(),
		json.RawMessage(`{"new-branch":"feat-fork","brief":"do the thing","description":"forked worker","hello-timeout":"15s"}`),
		nil,
	)
	close(stop)
	if err != nil {
		t.Fatalf("handleForkSession: %v", err)
	}
	if herr := <-helloErr; herr != nil {
		t.Fatalf("hello goroutine: %v", herr)
	}
	if res.IsErr {
		t.Fatalf("expected success, got error result: %s", res.Text)
	}

	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("forked worktree missing at %s: %v", newPath, err)
	}

	for _, want := range []string{
		"session_key: worker/feat-fork",
		"worktree_path: " + newPath,
		"multiplexer_id: worker/feat-fork", // the session key, not the branch (#146)
		"worker will message " + driverKey + " via chat",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("result text missing %q:\n%s", want, res.Text)
		}
	}

	st, err := session.Read(repoPath, "feat-fork")
	if err != nil {
		t.Fatalf("session.Read: %v", err)
	}
	if st.SpawnedBy != driverKey {
		t.Errorf("SpawnedBy = %q, want %q", st.SpawnedBy, driverKey)
	}
	if st.Description != "forked worker" {
		t.Errorf("Description = %q, want %q", st.Description, "forked worker")
	}
}

// TestHandleForkSessionBadModelForClaudeProvider proves the provider-aware
// model validation actually rejects a bad Claude alias end to end on the
// fork path too. Unlike spawn, worktree.CreateFrom runs BEFORE
// spawn.LaunchExisting's render (see runForkDetached's doc comment), so —
// exactly like any other bad spawn-entry config on this path — the forked
// worktree IS left behind on this failure; this test pins that existing,
// already-accepted contract rather than assuming spawn's no-litter guarantee
// carries over.
func TestHandleForkSessionBadModelForClaudeProvider(t *testing.T) {
	const driverKey = "worker/feat"
	repoPath, _ := newForkCmdFixture(t, spawnCmdModelSweatfile, driverKey, "feat")

	res, err := handleForkSession(
		context.Background(),
		json.RawMessage(`{"brief":"do the thing","model":"gpt5"}`),
		nil,
	)
	if err != nil {
		t.Fatalf("handleForkSession: %v", err)
	}
	if !res.IsErr {
		t.Fatalf("expected error result, got success: %s", res.Text)
	}
	if !strings.Contains(res.Text, "unrecognized model") {
		t.Errorf("error text = %q, want it to mention the unrecognized model", res.Text)
	}

	newPath := filepath.Join(repoPath, ".worktrees", "feat-1")
	if _, statErr := os.Stat(newPath); statErr != nil {
		t.Errorf("expected the forked worktree to exist at %s despite the model error (existing fork contract), stat: %v", newPath, statErr)
	}
}

// TestHandleForkSessionAutoBranch verifies the ForkName default: without
// new-branch the fork lands on <branch>-1.
func TestHandleForkSessionAutoBranch(t *testing.T) {
	const driverKey = "worker/feat"
	repoPath, _ := newForkCmdFixture(t, spawnCmdHappySweatfile, driverKey, "feat")

	newPath := filepath.Join(repoPath, ".worktrees", "feat-1")
	helloErr, stop := awaitForkHello(newPath, "worker/feat-1", driverKey)

	res, err := handleForkSession(
		context.Background(),
		json.RawMessage(`{"brief":"do the thing","hello-timeout":"15s"}`),
		nil,
	)
	close(stop)
	if err != nil {
		t.Fatalf("handleForkSession: %v", err)
	}
	if herr := <-helloErr; herr != nil {
		t.Fatalf("hello goroutine: %v", herr)
	}
	if res.IsErr {
		t.Fatalf("expected success, got error result: %s", res.Text)
	}
	if !strings.Contains(res.Text, "session_key: worker/feat-1") {
		t.Errorf("result text missing auto-named session key:\n%s", res.Text)
	}
}
