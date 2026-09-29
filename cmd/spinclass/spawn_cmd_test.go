package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/spawnhandshake"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// testDriverPrincipal is the fixed CLOWN_SESSION_ID newSpawnCmdFixture pins as
// the driver's principal (FDR 0032 D1) — the hello target and the "worker will
// message ..." chat address — distinct from driverKey, which stays the
// display-only SPINCLASS_SESSION_ID / SpawnedBy session key.
const testDriverPrincipal = "1d3a5c7e-9b0f-4d2a-8e6c-0a1b2c3d4e5f"

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
// TestAuthorizeChildReap (a handle still governs who may reap whom, so the
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
// driver's display session key via $SPINCLASS_SESSION_ID and its principal
// (FDR 0032 D1: the hello target and chat address) via $CLOWN_SESSION_ID =
// testDriverPrincipal, and chdirs to HOME so the driver-repo same-repo
// rejection resolves to "no driver repo".
func newSpawnCmdFixture(t *testing.T, sweatfileTOML, driverKey string) (home, repoPath string) {
	t.Helper()
	testgit.RequireGit(t)
	// Force clown OFF so handleSpawnSession takes the synchronous path and
	// returns the full result inline (spinclass#266 made the MCP tool async
	// under clown; these end-to-end tests exercise the shared spawn machinery
	// deterministically via the sync fallback — the async dispatch, response
	// contract, and reap-if-dead policy are covered by focused unit tests).
	// clown.Enabled() gates on CLOWN_BIN, not CLOWN_SESSION_ID (below), so
	// pinning the latter for currentPrincipal does not flip this back on.
	t.Setenv("CLOWN_BIN", "")
	_ = os.Unsetenv("CLOWN_BIN")
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("SPINCLASS_SESSION_ID", driverKey)
	t.Setenv("CLOWN_SESSION_ID", testDriverPrincipal)
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

// playWorkerHello plays the worker's SessionStart hook for a handleSpawnSession
// end-to-end test: it polls repoPath's worktrees for the spawn template's
// "launched" marker, derives the worker's session key from the worktree dirname,
// and sends the hello to target — the driver's PRINCIPAL (FDR 0032 D1), which is
// what the real hook now targets via SpawnedByPrincipal. Returns a stop func
// (call once handleSpawnSession returns) and the hello goroutine's error channel.
func playWorkerHello(t *testing.T, repoPath, target string) (stop func(), helloErr chan error) {
	t.Helper()
	helloErr = make(chan error, 1)
	stopCh := make(chan struct{})
	go func() {
		deadline := time.After(15 * time.Second)
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stopCh:
				helloErr <- nil
				return
			case <-deadline:
				helloErr <- fmt.Errorf("spawn template marker never appeared")
				return
			case <-tick.C:
				matches, _ := filepath.Glob(filepath.Join(repoPath, ".worktrees", "*", "launched"))
				if len(matches) == 1 {
					branch := filepath.Base(filepath.Dir(matches[0]))
					helloErr <- spawnhandshake.SendHello("worker/"+branch, target, "")
					return
				}
			}
		}
	}()
	var once sync.Once
	stop = func() { once.Do(func() { close(stopCh) }) }
	return stop, helloErr
}

// TestHandleSpawnSessionHappyPath drives the MCP handler end to end over the
// stub-template fixture: a goroutine plays the worker's SessionStart hook
// (marker file appears → send the hello to the driver's principal), and the
// result text must carry the worker's session key, worktree path, multiplexer
// id, and the chat hint naming the driver's principal. The written session
// state must carry both the legacy display key (SpawnedBy) and the FDR 0032
// authority fields (SpawnedByPrincipal, Holders).
func TestHandleSpawnSessionHappyPath(t *testing.T) {
	const driverKey = "driver/test-session"
	_, repoPath := newSpawnCmdFixture(t, spawnCmdHappySweatfile, driverKey)

	stop, helloErr := playWorkerHello(t, repoPath, testDriverPrincipal)

	res, err := handleSpawnSession(
		context.Background(),
		json.RawMessage(`{"repo":"worker","brief":"do the thing","description":"spawned worker","hello-timeout":"15s"}`),
		nil,
	)
	stop()
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
		"worker will message " + testDriverPrincipal + " via chat",
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
	if st.SpawnedByPrincipal != testDriverPrincipal {
		t.Errorf("SpawnedByPrincipal = %q, want %q", st.SpawnedByPrincipal, testDriverPrincipal)
	}
	if len(st.Holders) != 1 || st.Holders[0] != testDriverPrincipal {
		t.Errorf("Holders = %v, want [%q]", st.Holders, testDriverPrincipal)
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

	stop, helloErr := playWorkerHello(t, repoPath, testDriverPrincipal)

	res, err := handleSpawnSession(
		context.Background(),
		json.RawMessage(`{"repo":"worker","brief":"do the thing","model":"llama-3-70b-instruct.Q4_K_M","hello-timeout":"15s"}`),
		nil,
	)
	stop()
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

// TestHandleSpawnSessionFromNonRepoCwd is the FDR 0032 slice 0 repro for
// spinclass#332: the driver's cwd carries no spinclass session key at all
// (SPINCLASS_SESSION_ID unset, cwd not a worktree or main checkout — so
// currentSessionKey would error), yet the spawn still succeeds because
// currentDriver()'s principal (CLOWN_SESSION_ID) never depends on cwd or git.
// The written child state records that: SpawnedBy empty, SpawnedByPrincipal
// and Holders carrying the driver's principal.
func TestHandleSpawnSessionFromNonRepoCwd(t *testing.T) {
	_, repoPath := newSpawnCmdFixture(t, spawnCmdHappySweatfile, "driver/test-session")
	// Override the fixture's display session key to "" — the #332 shape: no
	// worktree, no main checkout, nothing for currentSessionKey to resolve.
	t.Setenv("SPINCLASS_SESSION_ID", "")

	// Confirm the premise: HOME is not a git repository, so bestEffortSessionKey
	// (currentSessionKey via git.CommonDir) has nothing to resolve.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if _, cerr := git.CommonDir(home); cerr == nil {
		t.Fatalf("expected HOME (%s) to NOT be a git repo — the #332 premise", home)
	}

	stop, helloErr := playWorkerHello(t, repoPath, testDriverPrincipal)

	res, err := handleSpawnSession(
		context.Background(),
		json.RawMessage(`{"repo":"worker","brief":"do","hello-timeout":"15s"}`),
		nil,
	)
	stop()
	if err != nil {
		t.Fatalf("handleSpawnSession: %v", err)
	}
	if herr := <-helloErr; herr != nil {
		t.Fatalf("hello goroutine: %v", herr)
	}
	if res.IsErr {
		t.Fatalf("expected success spawning from a non-repo cwd, got error result: %s", res.Text)
	}
	if !strings.Contains(res.Text, "worker will message "+testDriverPrincipal+" via chat") {
		t.Errorf("result text missing chat hint naming the principal:\n%s", res.Text)
	}

	worktrees, _ := filepath.Glob(filepath.Join(repoPath, ".worktrees", "*"))
	if len(worktrees) != 1 {
		t.Fatalf("expected exactly one worker worktree, got %v", worktrees)
	}
	branch := filepath.Base(worktrees[0])
	st, err := session.Read(repoPath, branch)
	if err != nil {
		t.Fatalf("session.Read: %v", err)
	}
	if st.SpawnedBy != "" {
		t.Errorf("SpawnedBy = %q, want empty (driver had no session key)", st.SpawnedBy)
	}
	if st.SpawnedByPrincipal != testDriverPrincipal {
		t.Errorf("SpawnedByPrincipal = %q, want %q", st.SpawnedByPrincipal, testDriverPrincipal)
	}
	if len(st.Holders) != 1 || st.Holders[0] != testDriverPrincipal {
		t.Errorf("Holders = %v, want [%q]", st.Holders, testDriverPrincipal)
	}
}
