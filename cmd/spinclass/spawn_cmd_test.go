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

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/spawnhandshake"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// TestHandleSpawnSessionValidation exercises the cheap parameter rejections:
// they must fire as error results BEFORE any worktree/state creation, so no
// fixture repo is needed (HOME is a bare sandbox).
func TestHandleSpawnSessionValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPINCLASS_SESSION_ID", "driver/test-session")

	cases := []struct {
		name string
		args string
		want string
	}{
		{"missing brief", `{"repo":"somewhere"}`, "brief is required"},
		// repo is OPTIONAL now (spinclass#262: omitted => the current repo), so a
		// missing repo is no longer a validation error — it is covered by
		// spawn.ResolveRepo's TestResolveRepoAllowsSameRepo and runSpawn's
		// current-repo resolution instead.
		{"bad hello-timeout", `{"repo":"somewhere","brief":"do","hello-timeout":"bogus"}`, "invalid hello-timeout"},
		{"negative hello-timeout", `{"repo":"somewhere","brief":"do","hello-timeout":"-5s"}`, "must be positive"},
		{"zero hello-timeout", `{"repo":"somewhere","brief":"do","hello-timeout":"0s"}`, "must be positive"},
		{"unknown repo", `{"repo":"no-such-repo","brief":"do the thing"}`, "no repo named"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := handleSpawnSession(context.Background(), json.RawMessage(tc.args), nil)
			if err != nil {
				t.Fatalf("handleSpawnSession: %v", err)
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

// The #148 recursive-spawn gate and its test are gone: a spawned worker may
// now spawn its own workers. What used to be tested here has moved to the two
// mechanisms that actually still constrain this —
// perms.AlwaysAsk/TestSpawnSessionAsksEvenForSubagent (every spawn at every
// depth prompts a human, so fan-out cannot run away silently) and
// TestAuthorizeChildReap (spawned_by still governs who may reap whom, so the
// field remains load-bearing even though it no longer gates spawning).

// spawnCmdHappySweatfile mirrors internal/spawn's happy-path fixture: the
// stub spawn-entry (exec'd directly — FDR-0017 Piece 1) just drops a marker in
// the worktree; the test plays the worker's SessionStart hook by sending the
// chat hello itself.
const spawnCmdHappySweatfile = `[session-entry]
spawn-entry = ["sh", "-c", 'touch "$PWD/launched"', "sh", "{prompt}"]
`

// spawnCmdModelSweatfile mirrors spawnCmdHappySweatfile but includes a
// literal "--" provider-args separator, so a `model` param has somewhere to
// splice into (spawnCmdHappySweatfile has none and would only ever exercise
// the "no separator" error, not alias validation).
const spawnCmdModelSweatfile = `[session-entry]
spawn-entry = ["sh", "-c", 'touch "$PWD/launched"', "sh", "--", "{prompt}"]
`

// spawnCmdJugglerModelSweatfile selects a non-claude provider and maps it in
// [session-entry.model-flags], so a model name that would never pass the
// fixed Claude alias set (a GGUF-style name here) must still succeed.
const spawnCmdJugglerModelSweatfile = `[session-entry]
spawn-entry = ["sh", "-c", 'touch "$PWD/launched"', "sh", "--provider=juggler", "--", "{prompt}"]

[session-entry.model-flags]
juggler = "--model"
`

// newSpawnCmdFixture sandboxes HOME (worktree creation trusts the workspace
// in ~/.claude.json) and XDG_STATE_HOME (session index + chatroom), builds a
// worker repo at $HOME/eng/repos/worker with the given sweatfile, pins the
// driver identity via $SPINCLASS_SESSION_ID, and chdirs to HOME so the
// driver-repo same-repo rejection resolves to "no driver repo".
func newSpawnCmdFixture(t *testing.T, sweatfileTOML, driverKey string) (home, repoPath string) {
	t.Helper()
	testgit.RequireGit(t)
	home = t.TempDir()
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
	t.Chdir(home)
	return home, repoPath
}

// TestHandleSpawnSessionHappyPath drives the MCP handler end to end over the
// stub-template fixture: a goroutine plays the worker's SessionStart hook
// (marker file appears → send the hello), and the result text must carry the
// worker's session key, worktree path, multiplexer id, and the chat hint
// naming the driver key.
func TestHandleSpawnSessionHappyPath(t *testing.T) {
	const driverKey = "driver/test-session"
	_, repoPath := newSpawnCmdFixture(t, spawnCmdHappySweatfile, driverKey)

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
					helloErr <- spawnhandshake.SendHello("worker/"+branch, driverKey, "")
					return
				}
			}
		}
	}()

	res, err := handleSpawnSession(
		context.Background(),
		json.RawMessage(`{"repo":"worker","brief":"do the thing","description":"spawned worker","hello-timeout":"15s"}`),
		nil,
	)
	close(stop)
	if err != nil {
		t.Fatalf("handleSpawnSession: %v", err)
	}
	if herr := <-helloErr; herr != nil {
		t.Fatalf("hello goroutine: %v", herr)
	}
	if res.IsErr {
		t.Fatalf("expected success, got error result: %s", res.Text)
	}

	worktrees, _ := filepath.Glob(filepath.Join(repoPath, ".worktrees", "*"))
	if len(worktrees) != 1 {
		t.Fatalf("expected exactly one worker worktree, got %v", worktrees)
	}
	branch := filepath.Base(worktrees[0])

	for _, want := range []string{
		"session_key: worker/" + branch,
		"worktree_path: " + worktrees[0],
		"multiplexer_id: worker/" + branch, // the session key, not the branch (#146)
		"worker will message " + driverKey + " via chat",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("result text missing %q:\n%s", want, res.Text)
		}
	}

	st, err := session.Read(repoPath, branch)
	if err != nil {
		t.Fatalf("session.Read: %v", err)
	}
	if st.SpawnedBy != driverKey {
		t.Errorf("SpawnedBy = %q, want %q", st.SpawnedBy, driverKey)
	}
	if st.Description != "spawned worker" {
		t.Errorf("Description = %q, want %q", st.Description, "spawned worker")
	}
}

// TestHandleSpawnSessionBadModelForClaudeProvider proves the provider-aware
// model validation actually rejects a bad Claude alias end to end (not just
// at the internal/spawn unit level), and that the failure happens BEFORE any
// worktree is created — renderSpawn (which now does this validation) still
// runs before shop.Create on the spawn path.
func TestHandleSpawnSessionBadModelForClaudeProvider(t *testing.T) {
	const driverKey = "driver/test-session"
	_, repoPath := newSpawnCmdFixture(t, spawnCmdModelSweatfile, driverKey)

	res, err := handleSpawnSession(
		context.Background(),
		json.RawMessage(`{"repo":"worker","brief":"do the thing","model":"gpt5"}`),
		nil,
	)
	if err != nil {
		t.Fatalf("handleSpawnSession: %v", err)
	}
	if !res.IsErr {
		t.Fatalf("expected error result, got success: %s", res.Text)
	}
	if !strings.Contains(res.Text, "unrecognized model") {
		t.Errorf("error text = %q, want it to mention the unrecognized model", res.Text)
	}

	worktrees, _ := filepath.Glob(filepath.Join(repoPath, ".worktrees", "*"))
	if len(worktrees) != 0 {
		t.Errorf("expected no worktree to be created, found %v", worktrees)
	}
}

// TestHandleSpawnSessionModelForNonClaudeProviderSucceeds is the actual
// juggler-composition proof: a model name that would never pass the fixed
// Claude alias set succeeds end to end once the resolved spawn-entry selects
// a non-claude provider that's mapped in [session-entry.model-flags].
func TestHandleSpawnSessionModelForNonClaudeProviderSucceeds(t *testing.T) {
	const driverKey = "driver/test-session"
	_, repoPath := newSpawnCmdFixture(t, spawnCmdJugglerModelSweatfile, driverKey)

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
					helloErr <- spawnhandshake.SendHello("worker/"+branch, driverKey, "")
					return
				}
			}
		}
	}()

	res, err := handleSpawnSession(
		context.Background(),
		json.RawMessage(`{"repo":"worker","brief":"do the thing","model":"llama-3-70b-instruct.Q4_K_M","hello-timeout":"15s"}`),
		nil,
	)
	close(stop)
	if err != nil {
		t.Fatalf("handleSpawnSession: %v", err)
	}
	if herr := <-helloErr; herr != nil {
		t.Fatalf("hello goroutine: %v", herr)
	}
	if res.IsErr {
		t.Fatalf("expected success (non-claude provider, unvalidated model alias), got error result: %s", res.Text)
	}
}

// TestHandleSpawnSessionNoDriverKey verifies spawn refuses to launch without
// a resolvable driver identity: the hello gate and the worker's message-back
// contract both need a driver session key to target.
func TestHandleSpawnSessionNoDriverKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPINCLASS_SESSION_ID", "")
	dir := t.TempDir() // not a worktree, no implicit session
	t.Chdir(dir)

	res, err := handleSpawnSession(context.Background(), json.RawMessage(`{"repo":"worker","brief":"do"}`), nil)
	if err != nil {
		t.Fatalf("handleSpawnSession: %v", err)
	}
	if !res.IsErr {
		t.Fatalf("expected error result without a driver identity, got success: %s", res.Text)
	}
	if !strings.Contains(res.Text, "driver session key") {
		t.Errorf("error text = %q, want it to mention the driver session key", res.Text)
	}
}
