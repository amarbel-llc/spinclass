package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/clown"
	"code.linenisgreat.com/spinclass/internal/alfa/job"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

const selfPrincipal = "5e1f0c2a-7b3d-4a96-8c14-2d9e6f0a1b37"

// selfCloseFixture stands up a repo with one worktree session whose own
// principal is selfPrincipal (the CLOWN_SESSION_ID of the caller), held by
// "spawner". allow writes the global sweatfile that grants allow-self-close.
func selfCloseFixture(t *testing.T, allow bool) (repoPath, wtPath string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", selfPrincipal)
	t.Setenv("SPINCLASS_SESSION_ID", "")
	if allow {
		writeGlobalSweatfile(t, home, "[permissions]\nallow-self-close = true\n")
	}

	repoPath, wtPath = stageChildWorktree(t)
	if err := session.Write(session.State{
		SessionState:       session.StateInactive,
		RepoPath:           repoPath,
		WorktreePath:       wtPath,
		Branch:             "kid",
		SessionKey:         "worker/kid",
		Principal:          selfPrincipal,
		SpawnedByPrincipal: "spawner",
		Holders:            []string{"spawner"},
		Entrypoint:         []string{"/bin/sh"},
		StartedAt:          time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	// A principal resolves to its session only through a frontend mapping, never
	// through the record's own principal field. This is the mapping the SessionStart
	// hook writes for an unconfined session; tests of a confined one replace it.
	if err := confinement.RecordUnconfinedPrincipal(os.Getenv("XDG_STATE_HOME"), selfPrincipal, "worker/kid", wtPath); err != nil {
		t.Fatal(err)
	}
	return repoPath, wtPath
}

func writeGlobalSweatfile(t *testing.T, home, body string) {
	t.Helper()
	path := filepath.Join(home, ".config", "spinclass", "sweatfile")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func callCloseThis(t *testing.T, args string) (text string, isErr bool) {
	t.Helper()
	res, err := handleCloseThisSession(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("handleCloseThisSession: %v", err)
	}
	return res.Text, res.IsErr
}

func readKid(t *testing.T, repoPath string) *session.State {
	t.Helper()
	st, err := session.Read(repoPath, "kid")
	if err != nil {
		t.Fatalf("session.Read: %v", err)
	}
	return st
}

// stubExitWakes records the exit wake a close emits.
type exitWake struct {
	holders []string
	key     string
	reason  clown.ExitReason
}

func stubExitWakes(t *testing.T) *[]exitWake {
	t.Helper()
	var got []exitWake
	orig := emitExitWakesFn
	emitExitWakesFn = func(holders []string, key string, reason clown.ExitReason) error {
		got = append(got, exitWake{holders, key, reason})
		return nil
	}
	t.Cleanup(func() { emitExitWakesFn = orig })
	return &got
}

func TestCloseThisSessionRefusesWithoutScope(t *testing.T) {
	repoPath, wtPath := selfCloseFixture(t, false)

	text, isErr := callCloseThis(t, `{}`)
	if !isErr || !strings.Contains(text, "allow-self-close") {
		t.Fatalf("want a refusal naming allow-self-close, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).CloseRequest != nil {
		t.Error("a refused call must not record a close request")
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("worktree must be untouched: %v", err)
	}
}

// A repo-layer sweatfile is writable by the agent, so it must not grant the
// right.
func TestCloseThisSessionIgnoresRepoLayerScope(t *testing.T) {
	repoPath, wtPath := selfCloseFixture(t, false)
	body := "[hooks]\ndisable-nix-gc = true\n[permissions]\nallow-self-close = true\n"
	if err := os.WriteFile(filepath.Join(repoPath, "sweatfile"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, isErr := callCloseThis(t, `{}`); !isErr {
		t.Fatal("a repo-layer allow-self-close must not enable the tool")
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("worktree must be untouched: %v", err)
	}
}

func TestCloseThisSessionQueuesWhenAllowedAndClean(t *testing.T) {
	repoPath, wtPath := selfCloseFixture(t, true)

	text, isErr := callCloseThis(t, `{}`)
	if isErr {
		t.Fatalf("expected the request to be accepted, got %q", text)
	}
	req := readKid(t, repoPath).CloseRequest
	if req == nil || req.Principal != selfPrincipal {
		t.Fatalf("close request = %+v, want one by %s", req, selfPrincipal)
	}
	// The teardown is deferred: nothing is removed inside the call.
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("the worktree must survive the call (teardown runs after the last turn): %v", err)
	}
}

func TestCloseThisSessionNeverForces(t *testing.T) {
	for _, args := range []string{`{"force":true}`, `{"force":"true"}`, `{"force":false}`} {
		repoPath, _ := selfCloseFixture(t, true)
		text, isErr := callCloseThis(t, args)
		if !isErr || !strings.Contains(text, "force") {
			t.Errorf("%s: want a refusal naming force, got isErr=%v %q", args, isErr, text)
		}
		if readKid(t, repoPath).CloseRequest != nil {
			t.Errorf("%s: must not record a request", args)
		}
	}
}

func TestCloseThisSessionRefusesUnmergedWork(t *testing.T) {
	repoPath, wtPath := selfCloseFixture(t, true)
	mustCommitInWorktree(t, wtPath)

	text, isErr := callCloseThis(t, `{}`)
	if !isErr || !strings.Contains(text, "kid") {
		t.Fatalf("want a refusal about the unmerged branch, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).CloseRequest != nil {
		t.Error("must not record a request for unmerged work")
	}
}

func TestCloseThisSessionRefusesWhileAJobRuns(t *testing.T) {
	repoPath, wtPath := selfCloseFixture(t, true)
	if err := job.Write(wtPath, &job.Job{
		ID: "j1", Kind: "merge", Status: job.StatusRunning, ServePID: os.Getpid(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	text, isErr := callCloseThis(t, `{}`)
	if !isErr || !strings.Contains(text, "job") {
		t.Fatalf("want a refusal naming the running job, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).CloseRequest != nil {
		t.Error("must not record a request while a job runs")
	}
}

// addHeldChild adds a second live session in the same repo that holders hold.
func addHeldChild(t *testing.T, repoPath string, holders []string) {
	t.Helper()
	wt := filepath.Join(repoPath, ".worktrees", "grand")
	testgit.MustWorktreeAdd(t, repoPath, wt, "grand")
	if err := session.Write(session.State{
		SessionState: session.StateInactive,
		RepoPath:     repoPath,
		WorktreePath: wt,
		Branch:       "grand",
		SessionKey:   "worker/grand",
		Holders:      holders,
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}

// Orphan guard: closing would leave a live session with no accepted holder.
func TestCloseThisSessionRefusesWhenSoleHolderOfALiveSession(t *testing.T) {
	repoPath, _ := selfCloseFixture(t, true)
	addHeldChild(t, repoPath, []string{selfPrincipal})

	text, isErr := callCloseThis(t, `{}`)
	if !isErr || !strings.Contains(text, "worker/grand") {
		t.Fatalf("want a refusal naming worker/grand, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).CloseRequest != nil {
		t.Error("must not record a request that would orphan a child")
	}
}

func TestCloseThisSessionAllowsWhenAnotherHolderRemains(t *testing.T) {
	repoPath, _ := selfCloseFixture(t, true)
	addHeldChild(t, repoPath, []string{selfPrincipal, "someone-else"})

	if text, isErr := callCloseThis(t, `{}`); isErr {
		t.Fatalf("another holder remains, so the request should be accepted: %q", text)
	}
	if readKid(t, repoPath).CloseRequest == nil {
		t.Error("expected a recorded close request")
	}
}

func TestCloseThisSessionRefusesAnUnidentifiedCaller(t *testing.T) {
	selfCloseFixture(t, true)
	t.Setenv("CLOWN_SESSION_ID", "")
	if _, isErr := callCloseThis(t, `{}`); !isErr {
		t.Fatal("a caller whose principal maps to no session must be refused")
	}
}

// requestClose runs the tool once and returns the fixture, ready for the runner.
func requestClose(t *testing.T) (repoPath, wtPath string) {
	t.Helper()
	repoPath, wtPath = selfCloseFixture(t, true)
	if text, isErr := callCloseThis(t, `{}`); isErr {
		t.Fatalf("setup: request refused: %q", text)
	}
	return repoPath, wtPath
}

func TestCloseSelfRunTearsDownAndWakesOtherHolders(t *testing.T) {
	wakes := stubExitWakes(t)
	repoPath, wtPath := requestClose(t)

	if err := runCloseSelfRun("worker/kid"); err != nil {
		t.Fatalf("runCloseSelfRun: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree still present after the runner (stat err = %v)", err)
	}
	out, _ := exec.Command("git", "-C", repoPath, "branch", "--list", "kid").Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("branch kid still exists: %q", out)
	}
	if got := readKid(t, repoPath).ResolveState(); got != session.StateAbandoned {
		t.Errorf("state = %q, want a tombstone", got)
	}
	if len(*wakes) != 1 || (*wakes)[0].reason != clown.ExitShutdown ||
		strings.Join((*wakes)[0].holders, ",") != "spawner" || (*wakes)[0].key != "worker/kid" {
		t.Errorf("wakes = %+v, want one shutdown wake to [spawner] for worker/kid", *wakes)
	}
}

func TestCloseSelfRunWithoutARequestDoesNothing(t *testing.T) {
	wakes := stubExitWakes(t)
	_, wtPath := selfCloseFixture(t, true)

	if err := runCloseSelfRun("worker/kid"); err != nil {
		t.Fatalf("runCloseSelfRun: %v", err)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("no request, so the worktree must be untouched: %v", err)
	}
	if len(*wakes) != 0 {
		t.Errorf("no request, so no wake: %+v", *wakes)
	}
}

// The runner is the authority: work made after the request refuses the
// teardown, clears the request, and tells the holders.
func TestCloseSelfRunRefusesWorkAddedAfterTheRequest(t *testing.T) {
	wakes := stubExitWakes(t)
	repoPath, wtPath := requestClose(t)
	mustCommitInWorktree(t, wtPath)

	if err := runCloseSelfRun("worker/kid"); err == nil {
		t.Fatal("expected the runner to refuse unmerged work")
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("a refused teardown must leave the worktree: %v", err)
	}
	if readKid(t, repoPath).CloseRequest != nil {
		t.Error("a refused request must be cleared so it is not retried forever")
	}
	if len(*wakes) != 1 || (*wakes)[0].reason != clown.ExitFailed {
		t.Errorf("wakes = %+v, want one failed wake", *wakes)
	}
}

// The scope is checked again at run time: revoking it between request and run
// stops the teardown.
func TestCloseSelfRunRefusesWhenScopeWasRevoked(t *testing.T) {
	stubExitWakes(t)
	repoPath, wtPath := requestClose(t)
	writeGlobalSweatfile(t, os.Getenv("HOME"), "[permissions]\nallow-self-close = false\n")

	if err := runCloseSelfRun("worker/kid"); err == nil {
		t.Fatal("expected the runner to refuse once the scope is gone")
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("worktree must survive: %v", err)
	}
	if readKid(t, repoPath).CloseRequest != nil {
		t.Error("the request must be cleared")
	}
}

// forgeCloseRequestPrincipal rewrites the queued request the way an agent that
// can write its own worktree state could.
func forgeCloseRequestPrincipal(t *testing.T, repoPath, forged string, alsoSessionPrincipal bool) {
	t.Helper()
	st := readKid(t, repoPath)
	st.CloseRequest.Principal = forged
	if alsoSessionPrincipal {
		st.Principal = forged
	}
	if err := session.Write(*st); err != nil {
		t.Fatal(err)
	}
}

// writeFrontendMapping plants the frontend principal mapping the launching
// frontend writes for a confined session.
func writeFrontendMapping(t *testing.T, principal, sessionKey, worktree string) {
	t.Helper()
	path := confinement.PrincipalMappingPath(os.Getenv("XDG_STATE_HOME"), principal)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"version":1,"principal":%q,"session_key":%q,"worktree":%q,"unit":"agent-test.service"}`, principal, sessionKey, worktree)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertCloseRefusedAndSessionWhole(t *testing.T, repoPath, wtPath string) {
	t.Helper()
	if err := runCloseSelfRun("worker/kid"); err == nil {
		t.Fatal("expected the runner to refuse")
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("a refused teardown must leave the worktree: %v", err)
	}
	if readKid(t, repoPath).CloseRequest != nil {
		t.Error("a refused request must be cleared")
	}
}

// A forged CloseRequest.Principal must not let the runner skip the orphan
// check: the runner takes the principal from the session's own record (an
// unconfined session), not from the request (found by the FDR divergence
// audit, FDR 0035).
func TestCloseSelfRunIgnoresAForgedRequestPrincipal(t *testing.T) {
	stubExitWakes(t)
	repoPath, wtPath := requestClose(t)
	addHeldChild(t, repoPath, []string{selfPrincipal})
	forgeCloseRequestPrincipal(t, repoPath, "a-forged-principal", false)

	assertCloseRefusedAndSessionWhole(t, repoPath, wtPath)
}

// For a confined session the frontend mapping decides, even when the agent
// rewrote both the request and its session's recorded principal.
func TestCloseSelfRunUsesTheFrontendMappingForAConfinedSession(t *testing.T) {
	stubExitWakes(t)
	repoPath, wtPath := requestClose(t)
	addHeldChild(t, repoPath, []string{selfPrincipal})
	writeFrontendMapping(t, selfPrincipal, "worker/kid", wtPath)
	forgeCloseRequestPrincipal(t, repoPath, "a-forged-principal", true)

	assertCloseRefusedAndSessionWhole(t, repoPath, wtPath)
}

func TestCloseSelfRunRefusesAConfinedSessionWithoutAMapping(t *testing.T) {
	stubExitWakes(t)
	repoPath, wtPath := requestClose(t)
	// requestClose's fixture bound an unconfined mapping; this session has none.
	if err := os.Remove(confinement.PrincipalMappingPath(os.Getenv("XDG_STATE_HOME"), selfPrincipal)); err != nil {
		t.Fatal(err)
	}
	spec := confinement.UnitSpecPath(os.Getenv("XDG_STATE_HOME"), wtPath)
	if err := os.MkdirAll(filepath.Dir(spec), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	assertCloseRefusedAndSessionWhole(t, repoPath, wtPath)
}

// The mapping is honoured in the other direction too: a confined session the
// mapping names closes normally.
func TestCloseSelfRunClosesAConfinedSessionItsMappingNames(t *testing.T) {
	stubExitWakes(t)
	repoPath, wtPath := requestClose(t)
	writeFrontendMapping(t, selfPrincipal, "worker/kid", wtPath)

	if err := runCloseSelfRun("worker/kid"); err != nil {
		t.Fatalf("runCloseSelfRun: %v", err)
	}
	if got := readKid(t, repoPath).ResolveState(); got != session.StateAbandoned {
		t.Errorf("state = %q, want a tombstone", got)
	}
}

// The tool resolves its caller through the frontend mapping, not the worktree
// state's recorded principal, which the agent can rewrite (audit row 0035-159).
func TestCloseThisSessionResolvesTheCallerThroughTheFrontendMapping(t *testing.T) {
	repoPath, wtPath := selfCloseFixture(t, true)
	st := readKid(t, repoPath)
	st.Principal = "a-principal-the-agent-wrote"
	if err := session.Write(*st); err != nil {
		t.Fatal(err)
	}
	writeFrontendMapping(t, selfPrincipal, "worker/kid", wtPath)

	if text, isErr := callCloseThis(t, `{}`); isErr {
		t.Fatalf("the mapping names this session, so the request must be accepted: %q", text)
	}
	if readKid(t, repoPath).CloseRequest == nil {
		t.Error("expected a recorded close request")
	}
}

// A mapping that exists wins outright: if it names no live session, or does
// not parse, the caller resolves to nothing, even though the worktree state
// still claims the principal.
func TestCloseThisSessionNeverFallsBackPastAMapping(t *testing.T) {
	for name, write := range map[string]func(t *testing.T, wtPath string){
		"mapping names a session that is not live": func(t *testing.T, wtPath string) {
			writeFrontendMapping(t, selfPrincipal, "worker/gone", wtPath)
		},
		"mapping does not parse": func(t *testing.T, _ string) {
			path := confinement.PrincipalMappingPath(os.Getenv("XDG_STATE_HOME"), selfPrincipal)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			repoPath, wtPath := selfCloseFixture(t, true)
			write(t, wtPath)
			if _, isErr := callCloseThis(t, `{}`); !isErr {
				t.Fatal("a mapping that resolves nothing must refuse, not fall back to the worktree state")
			}
			if readKid(t, repoPath).CloseRequest != nil {
				t.Error("must not record a request")
			}
		})
	}
}

func TestCloseSelfRunWithAnUnknownTargetIsANoOp(t *testing.T) {
	selfCloseFixture(t, true)
	if err := runCloseSelfRun("nope/nothing"); err != nil {
		t.Fatalf("an unknown session has nothing to close: %v", err)
	}
}
