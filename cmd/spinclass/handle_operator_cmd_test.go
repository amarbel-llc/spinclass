package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/purse-first/libs/go-mcp/server"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// spinclass#478: `sc handle-grant` and `sc handle-move` are the operator's own
// handle edits. They need a terminal and no handle, judge every session on its
// own, prompt once, default to a pending default-rights grant, and never write
// on a dry run.

const handleRecipient = "coordinator-key"

// stubOperatorHandle drives the command's seams. sessions maps a principal to
// the live session key it runs (the frontend principal mapping).
func stubOperatorHandle(t *testing.T, terminal bool, typed string, sessions map[string]string) (stdout, stderr *bytes.Buffer) {
	t.Helper()
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	oldTTY, oldIn, oldOut, oldErr, oldOf := operatorHandleIsTerminal, operatorHandleConfirmIn, operatorHandleStdout, operatorHandleStderr, operatorHandleSessionOf
	t.Cleanup(func() {
		operatorHandleIsTerminal, operatorHandleConfirmIn, operatorHandleStdout, operatorHandleStderr, operatorHandleSessionOf = oldTTY, oldIn, oldOut, oldErr, oldOf
	})
	operatorHandleIsTerminal = func() bool { return terminal }
	operatorHandleConfirmIn = func() io.Reader { return strings.NewReader(typed) }
	operatorHandleStdout = func() io.Writer { return stdout }
	operatorHandleStderr = func() io.Writer { return stderr }
	operatorHandleSessionOf = func(p string) (string, error) {
		if k, ok := sessions[p]; ok {
			return k, nil
		}
		return "", errors.New("no such principal")
	}
	return stdout, stderr
}

var coordinatorLive = map[string]string{handleRecipient: "circus/coordinator"}

// handleFixture stages worker/kid held by "orig" (accepted) and "pend" (pending).
func handleFixture(t *testing.T) (repoPath string) {
	t.Helper()
	repoPath, _ = restartChildFixture(t, "operator-shell", session.State{
		Holders:        []string{"orig"},
		PendingHandles: []string{"pend"},
		HandleRights:   map[string]string{"orig": "observe,close", "pend": "observe,close"},
	})
	return repoPath
}

func TestOperatorHandleRefusesWithoutATerminal(t *testing.T) {
	stubOperatorHandle(t, false, "", coordinatorLive)
	repoPath := handleFixture(t)
	err := runOperatorHandle(operatorHandleGrant, operatorHandleParams{Target: []string{"worker/kid"}, To: handleRecipient})
	if err == nil || !strings.Contains(err.Error(), "terminals") {
		t.Fatalf("err = %v, want the terminal refusal", err)
	}
	if got := readKid(t, repoPath).PendingHandles; !slices.Equal(got, []string{"pend"}) {
		t.Errorf("pending = %v, nothing may be written", got)
	}
}

// grant adds the recipient, pending, with the default rights and the operator
// marker, keeps every existing holder, and asks once.
func TestOperatorHandleGrantAddsAPendingDefaultHandle(t *testing.T) {
	stdout, stderr := stubOperatorHandle(t, true, "y\n", coordinatorLive)
	repoPath := handleFixture(t)

	if err := runOperatorHandle(operatorHandleGrant, operatorHandleParams{Target: []string{"worker/kid"}, To: handleRecipient}); err != nil {
		t.Fatalf("err = %v (stdout %q)", err, stdout.String())
	}
	st := readKid(t, repoPath)
	if !slices.Equal(st.Holders, []string{"orig"}) || !slices.Equal(st.PendingHandles, []string{"pend", handleRecipient}) {
		t.Errorf("holders %v pending %v; want orig kept, pend kept, the recipient pending", st.Holders, st.PendingHandles)
	}
	if st.HandleRights[handleRecipient] != "observe,close" {
		t.Errorf("rights = %q, want the default observe,close", st.HandleRights[handleRecipient])
	}
	p := st.HandleProvenance[handleRecipient]
	if p.GrantedBy != session.HandleOperator || p.GrantedAt == nil || p.AcceptedAt != nil {
		t.Errorf("provenance = %+v, want granted_by operator, not accepted", p)
	}
	if st.SpawnerReleased {
		t.Error("grant must not end the spawner's authority")
	}
	if !strings.Contains(stdout.String(), "ok 1 - worker/kid") || !strings.Contains(stdout.String(), "grant "+handleRecipient) {
		t.Errorf("stdout = %q, want one ok line for worker/kid", stdout.String())
	}
	if !strings.Contains(stderr.String(), "apply to 1 session(s)? [y/N]") || strings.Contains(stderr.String(), "right ") {
		t.Errorf("stderr = %q, want one prompt and no dangerous-right line", stderr.String())
	}
}

func TestOperatorHandleMoveLeavesOnlyTheRecipientAndEndsTheSpawnerPath(t *testing.T) {
	stdout, _ := stubOperatorHandle(t, true, "y\n", coordinatorLive)
	repoPath := handleFixture(t)

	if err := runOperatorHandle(operatorHandleMove, operatorHandleParams{Target: []string{"worker/kid"}, To: handleRecipient}); err != nil {
		t.Fatalf("err = %v (stdout %q)", err, stdout.String())
	}
	st := readKid(t, repoPath)
	if len(st.Holders) != 0 || !slices.Equal(st.PendingHandles, []string{handleRecipient}) {
		t.Errorf("holders %v pending %v; want the recipient alone", st.Holders, st.PendingHandles)
	}
	for _, gone := range []string{"orig", "pend"} {
		if _, has := st.HandleRights[gone]; has {
			t.Errorf("%s still has a rights entry", gone)
		}
	}
	if !st.SpawnerReleased {
		t.Error("move must end the spawner's legacy key path (Q4)")
	}
	if !strings.Contains(stdout.String(), "remove orig, pend") || !strings.Contains(stdout.String(), "legacy key path") {
		t.Errorf("stdout = %q, want the removals and the spawner path named", stdout.String())
	}
}

func TestOperatorHandleDryRunWritesNothingAndAsksNothing(t *testing.T) {
	stdout, stderr := stubOperatorHandle(t, true, "", coordinatorLive)
	repoPath := handleFixture(t)
	before := readKid(t, repoPath)

	if err := runOperatorHandle(operatorHandleMove, operatorHandleParams{Target: []string{"worker/kid"}, To: handleRecipient, DryRun: true}); err != nil {
		t.Fatalf("err = %v", err)
	}
	after := readKid(t, repoPath)
	if !slices.Equal(after.Holders, before.Holders) || !slices.Equal(after.PendingHandles, before.PendingHandles) || after.SpawnerReleased {
		t.Errorf("a dry run changed the record: %+v -> %+v", before, after)
	}
	if !strings.Contains(stdout.String(), "(dry run)") || strings.Contains(stderr.String(), "[y/N]") {
		t.Errorf("stdout %q stderr %q; want a dry-run line and no prompt", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "worker/kid: now [orig, pend (pending)]") {
		t.Errorf("stderr = %q, want the plan listing the current holders", stderr.String())
	}
}

func TestOperatorHandleDecliningTheBatchChangesNothing(t *testing.T) {
	for _, typed := range []string{"n\n", "\n", ""} {
		stdout, _ := stubOperatorHandle(t, true, typed, coordinatorLive)
		repoPath := handleFixture(t)
		err := runOperatorHandle(operatorHandleGrant, operatorHandleParams{Target: []string{"worker/kid"}, To: handleRecipient})
		if err == nil || !strings.Contains(stdout.String(), declinedAtThePrompt) {
			t.Errorf("typed %q: err = %v, stdout %q; want a nonzero exit and the declined line", typed, err, stdout.String())
		}
		if slices.Contains(readKid(t, repoPath).PendingHandles, handleRecipient) {
			t.Errorf("typed %q: the handle was written despite the decline", typed)
		}
	}
}

// Dangerous rights are named and listed in the prompt; a name outside the
// vocabulary is refused before anything is judged.
func TestOperatorHandleListsDangerousRightsAndRefusesUnknownOnes(t *testing.T) {
	_, stderr := stubOperatorHandle(t, true, "n\n", coordinatorLive)
	handleFixture(t)
	_ = runOperatorHandle(operatorHandleGrant, operatorHandleParams{Target: []string{"worker/kid"}, To: handleRecipient, Rights: "observe,close,merge,restart"})
	for _, want := range []string{"right merge:", "arbitrary code", "right restart:"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, missing %q", stderr.String(), want)
		}
	}

	stubOperatorHandle(t, true, "y\n", coordinatorLive)
	repoPath := handleFixture(t)
	err := runOperatorHandle(operatorHandleGrant, operatorHandleParams{Target: []string{"worker/kid"}, To: handleRecipient, Rights: "observe,teleport"})
	if err == nil || !strings.Contains(err.Error(), "teleport") {
		t.Fatalf("err = %v, want the unknown right named", err)
	}
	if slices.Contains(readKid(t, repoPath).PendingHandles, handleRecipient) {
		t.Error("nothing may be written for an unknown right")
	}
}

// An unknown or remote recipient needs --force-unknown (Q6); a qualified key
// that resolves to a live local session is stored as the bare key.
func TestOperatorHandleChecksTheRecipient(t *testing.T) {
	stubOperatorHandle(t, true, "y\n", coordinatorLive)
	repoPath := handleFixture(t)
	err := runOperatorHandle(operatorHandleGrant, operatorHandleParams{Target: []string{"worker/kid"}, To: "typo-key"})
	if err == nil || !strings.Contains(err.Error(), "--force-unknown") {
		t.Fatalf("err = %v, want the unknown-recipient refusal naming --force-unknown", err)
	}
	if slices.Contains(readKid(t, repoPath).PendingHandles, "typo-key") {
		t.Error("an unknown recipient must not be written without --force-unknown")
	}

	_, stderr := stubOperatorHandle(t, true, "y\n", coordinatorLive)
	if err := runOperatorHandle(operatorHandleGrant, operatorHandleParams{Target: []string{"worker/kid"}, To: "typo-key", ForceUnknown: true}); err != nil {
		t.Fatalf("--force-unknown: %v", err)
	}
	if !slices.Contains(readKid(t, repoPath).PendingHandles, "typo-key") || !strings.Contains(stderr.String(), "--force-unknown") {
		t.Errorf("pending = %v stderr %q; want the unknown key recorded and flagged", readKid(t, repoPath).PendingHandles, stderr.String())
	}

	// A qualified form that resolves locally is normalized to the bare key.
	stubOperatorHandle(t, true, "y\n", coordinatorLive)
	repoPath = handleFixture(t)
	if err := runOperatorHandle(operatorHandleGrant, operatorHandleParams{Target: []string{"worker/kid"}, To: handleRecipient + "@flic.example"}); err != nil {
		t.Fatalf("qualified local: %v", err)
	}
	if !slices.Contains(readKid(t, repoPath).PendingHandles, handleRecipient) {
		t.Errorf("pending = %v, want the bare key", readKid(t, repoPath).PendingHandles)
	}

	// A qualified key that resolves to nothing here is another host's: stored as given, unverified.
	stubOperatorHandle(t, true, "y\n", coordinatorLive)
	repoPath = handleFixture(t)
	if err := runOperatorHandle(operatorHandleGrant, operatorHandleParams{Target: []string{"worker/kid"}, To: "remote-key@other.example"}); err == nil {
		t.Fatal("a remote principal without --force-unknown must be refused")
	}
	if err := runOperatorHandle(operatorHandleGrant, operatorHandleParams{Target: []string{"worker/kid"}, To: "remote-key@other.example", ForceUnknown: true}); err != nil {
		t.Fatalf("remote with --force-unknown: %v", err)
	}
	if !slices.Contains(readKid(t, repoPath).PendingHandles, "remote-key@other.example") {
		t.Errorf("pending = %v, want the qualified key as given", readKid(t, repoPath).PendingHandles)
	}
}

// One failing target does not stop the rest, and the exit is nonzero.
func TestOperatorHandleOneFailureDoesNotStopTheRest(t *testing.T) {
	stdout, _ := stubOperatorHandle(t, true, "y\n", coordinatorLive)
	repoPath := handleFixture(t)
	err := runOperatorHandle(operatorHandleGrant, operatorHandleParams{Target: []string{"nope/none", "worker/kid"}, To: handleRecipient})
	if err == nil || !strings.Contains(err.Error(), "1 of 2") {
		t.Fatalf("err = %v, want 1 of 2 not changed", err)
	}
	if !slices.Contains(readKid(t, repoPath).PendingHandles, handleRecipient) {
		t.Error("the valid target must still be changed")
	}
	if !strings.Contains(stdout.String(), "not ok 1 - nope/none") || !strings.Contains(stdout.String(), "ok 2 - worker/kid") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

// --all-on-host covers every live worktree session except the recipient's own,
// and is exclusive with named sessions.
func TestOperatorHandleAllOnHostSkipsTheRecipientsOwnSession(t *testing.T) {
	stdout, _ := stubOperatorHandle(t, true, "y\n", map[string]string{handleRecipient: "worker/kid2"})
	repoPath := handleFixture(t)
	addKid2(t, repoPath, nil)

	if err := runOperatorHandle(operatorHandleGrant, operatorHandleParams{AllOnHost: true, To: handleRecipient}); err != nil {
		t.Fatalf("err = %v (stdout %q)", err, stdout.String())
	}
	if !slices.Contains(readKid(t, repoPath).PendingHandles, handleRecipient) {
		t.Error("worker/kid must receive the handle")
	}
	if slices.Contains(readKid2(t, repoPath).PendingHandles, handleRecipient) {
		t.Error("the recipient's own session must not hold a handle on itself")
	}
	if !strings.Contains(stdout.String(), "worker/kid2: skipped: this is the recipient's own session") {
		t.Errorf("stdout = %q, want the skip named", stdout.String())
	}

	if err := runOperatorHandle(operatorHandleGrant, operatorHandleParams{AllOnHost: true, Target: []string{"worker/kid"}, To: handleRecipient}); err == nil {
		t.Error("--all-on-host with named sessions must be refused")
	}
	if err := runOperatorHandle(operatorHandleGrant, operatorHandleParams{To: handleRecipient}); err == nil {
		t.Error("no sessions and no --all-on-host must be refused")
	}
}

// A recipient that already holds, and a move with nobody else to remove, is a
// reported no-op, never an upgrade.
func TestOperatorHandleAnExistingHolderIsANoOp(t *testing.T) {
	stdout, stderr := stubOperatorHandle(t, true, "", map[string]string{"orig": "x/y"})
	repoPath := handleFixture(t)
	if err := runOperatorHandle(operatorHandleGrant, operatorHandleParams{Target: []string{"worker/kid"}, To: "orig", Rights: "observe,close,merge"}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := readKid(t, repoPath).HandleRights["orig"]; got != "observe,close" {
		t.Errorf("rights = %q; a re-grant must not upgrade them", got)
	}
	if !strings.Contains(stdout.String(), "nothing to change") || strings.Contains(stderr.String(), "[y/N]") {
		t.Errorf("stdout %q stderr %q; want a no-op line and no prompt", stdout.String(), stderr.String())
	}
}

func TestOperatorHandleCommandsAreCLIOnly(t *testing.T) {
	app := buildApp()
	for _, name := range []string{"handle-grant", "handle-move"} {
		cmd, ok := app.GetCommand(name)
		if !ok || cmd.RunCLI == nil || cmd.Run != nil {
			t.Fatalf("%s: registered=%v; want a CLI-only command (RunCLI, no Run)", name, ok)
		}
	}
	registry := server.NewToolRegistryV1()
	app.RegisterMCPToolsV1(registry)
	tools, err := registry.ListTools(context.Background())
	if err != nil || len(tools) == 0 {
		t.Fatalf("tools: %d, %v: the assertion would be vacuous", len(tools), err)
	}
	for _, tool := range tools {
		if strings.HasPrefix(tool.Name, "handle-") {
			t.Errorf("%s must not be an MCP tool", tool.Name)
		}
	}
	var p operatorHandleParams
	if err := json.Unmarshal([]byte(`{"target":["a/b"],"to":"k","all-on-host":false,"dry-run":true,"force-unknown":true}`), &p); err != nil || !p.DryRun || !p.ForceUnknown {
		t.Errorf("param decoding: %+v, %v", p, err)
	}
}

func TestOperatorHandleAnEmptyInvocationFails(t *testing.T) {
	stubOperatorHandle(t, true, "", coordinatorLive)
	if err := runOperatorHandle(operatorHandleGrant, operatorHandleParams{}); err == nil {
		t.Error("an empty invocation must fail")
	}
}
