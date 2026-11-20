package main

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/purse-first/libs/go-mcp/server"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// `sc stop` and `sc start-stopped` are the operator's stop and start: the same
// request path as stop-child-session and start-child-session, with the human at
// the terminal as the authority (no handle), a terminal required, every target
// judged first, one prompt for the batch (default no), and one verdict line
// per target.

// operatorStopFixture stages worker/kid with the restart hand-off configured,
// held by a principal that has NO stop right: the operator needs none.
func operatorStopFixture(t *testing.T) (repoPath string) {
	t.Helper()
	repoPath, _ = restartChildFixture(t, "operator-shell", session.State{
		Holders:      []string{"holder"},
		HandleRights: map[string]string{"holder": "observe"},
	})
	return repoPath
}

func TestOperatorStopRefusesWithoutATerminal(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stubOperatorRestart(t, false, "y\n")
	repoPath := operatorStopFixture(t)
	err := runOperatorStop(operatorStopParams{Target: []string{"worker/kid"}})
	if err == nil || !strings.Contains(err.Error(), "terminals") {
		t.Fatalf("err = %v, want the terminal refusal", err)
	}
	if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
		t.Error("nothing may be recorded or run")
	}
}

// A stop is asked once for the batch, then queued through the restart hand-off
// as a stop request by the operator, with no handle consulted.
func TestOperatorStopQueuesAStopAfterOnePrompt(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stdout, stderr := stubOperatorRestart(t, true, "y\n")
	repoPath := operatorStopFixture(t)

	if err := runOperatorStop(operatorStopParams{Target: []string{"worker/kid"}}); err != nil {
		t.Fatalf("err = %v (stdout %q)", err, stdout.String())
	}
	req := readKid(t, repoPath).RestartRequest
	if req == nil || req.Kind != session.RequestKindStop || req.Requester != operatorRestartRequester ||
		req.When != restartWhenIdle || req.Reason != defaultOperatorStopReason {
		t.Fatalf("request = %+v, want an idle stop by %q with the default reason", req, operatorRestartRequester)
	}
	if len(*calls) != 1 {
		t.Fatalf("hand-off calls = %+v, want one", *calls)
	}
	if !strings.Contains(stderr.String(), "worker/kid") || !strings.Contains(stderr.String(), "stop 1 session(s)? [y/N]") {
		t.Errorf("stderr = %q, want one prompt listing the session", stderr.String())
	}
	if got, want := stdout.String(), "worker/kid: stop requested\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestOperatorStopNowIsShownAndRecorded(t *testing.T) {
	stubRestartHandoff(t, "", nil)
	stdout, stderr := stubOperatorRestart(t, true, "y\n")
	repoPath := operatorStopFixture(t)

	if err := runOperatorStop(operatorStopParams{Target: []string{"worker/kid"}, Now: true, Reason: "runaway"}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if req := readKid(t, repoPath).RestartRequest; req == nil || req.When != restartWhenNow || req.Reason != "runaway" {
		t.Fatalf("request = %+v, want a stop now with the reason", req)
	}
	if !strings.Contains(stderr.String(), "NOW") || !strings.Contains(stderr.String(), "killing") {
		t.Errorf("stderr = %q, want the prompt to say the stop is NOW and kills the turn", stderr.String())
	}
	if got, want := stdout.String(), "worker/kid: stop NOW requested\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestOperatorStopDecliningChangesNothing(t *testing.T) {
	for _, typed := range []string{"n\n", "\n", ""} {
		calls := stubRestartHandoff(t, "", nil)
		stdout, _ := stubOperatorRestart(t, true, typed)
		repoPath := operatorStopFixture(t)
		err := runOperatorStop(operatorStopParams{Target: []string{"worker/kid"}})
		if err == nil || !strings.Contains(stdout.String(), "worker/kid: refused: "+declinedAtThePrompt) {
			t.Errorf("typed %q: err = %v stdout %q; want a nonzero exit and the declined line", typed, err, stdout.String())
		}
		if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
			t.Errorf("typed %q: a declined stop must record and run nothing", typed)
		}
	}
}

// A named session that is already stopped is refused before the prompt.
func TestOperatorStopRefusesAStoppedSessionWithoutAPrompt(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stdout, stderr := stubOperatorRestart(t, true, "y\n")
	stoppedChildFixture(t, "holder", "observe", true)

	if err := runOperatorStop(operatorStopParams{Target: []string{"worker/kid"}}); err == nil {
		t.Fatal("want a nonzero exit")
	}
	if strings.Contains(stderr.String(), "[y/N]") || len(*calls) != 0 {
		t.Errorf("stderr %q calls %+v; nothing to stop means no prompt and no hand-off", stderr.String(), *calls)
	}
	if !strings.Contains(stdout.String(), "worker/kid: refused: ") || !strings.Contains(stdout.String(), "already stopped") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

// One failing target does not stop the rest, and the exit is nonzero.
func TestOperatorStopJudgesEveryTargetOnItsOwn(t *testing.T) {
	stubRestartHandoff(t, "", nil)
	stdout, _ := stubOperatorRestart(t, true, "y\n")
	repoPath := operatorStopFixture(t)

	err := runOperatorStop(operatorStopParams{Target: []string{"nope/none", "worker/kid"}})
	if err == nil || !strings.Contains(err.Error(), "1 of 2") {
		t.Fatalf("err = %v, want 1 of 2 not stopped", err)
	}
	lines := verdictLines(stdout.String())
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "nope/none: refused: ") || lines[1] != "worker/kid: stop requested" {
		t.Errorf("lines = %q", lines)
	}
	if readKid(t, repoPath).RestartRequest == nil {
		t.Error("the valid target must still be stopped")
	}
}

// --all-on-host stops every live worktree session, minus --except and the
// caller's own session; the skipped ones are ok lines.
func TestOperatorStopAllOnHostHonoursExceptAndSkipsTheCallersOwnSession(t *testing.T) {
	stubRestartHandoff(t, "", nil)
	stdout, _ := stubOperatorRestart(t, true, "y\n")
	repoPath := operatorStopFixture(t)
	addKid2(t, repoPath, nil)

	if err := runOperatorStop(operatorStopParams{AllOnHost: true, Except: []string{"worker/kid2"}}); err != nil {
		t.Fatalf("err = %v (stdout %q)", err, stdout.String())
	}
	if readKid(t, repoPath).RestartRequest == nil {
		t.Error("worker/kid must be stopped")
	}
	if readKid2(t, repoPath).RestartRequest != nil {
		t.Error("worker/kid2 was excepted")
	}
	if !strings.Contains(stdout.String(), "worker/kid2: skipped: excepted (--except)") {
		t.Errorf("stdout = %q", stdout.String())
	}

	stubRestartHandoff(t, "", nil)
	stdout, _ = stubOperatorRestart(t, true, "y\n")
	repoPath = operatorStopFixture(t)
	t.Setenv("SPINCLASS_SESSION_ID", "worker/kid")
	if err := runOperatorStop(operatorStopParams{AllOnHost: true}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if readKid(t, repoPath).RestartRequest != nil {
		t.Error("the caller's own session must not be stopped by --all-on-host")
	}
	if !strings.Contains(stdout.String(), "worker/kid: skipped: this is the caller's own session") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestOperatorStopRefusesABadInvocation(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    operatorStopParams
		want string
	}{
		{"no sessions", operatorStopParams{}, "--all-on-host"},
		{"both", operatorStopParams{Target: []string{"worker/kid"}, AllOnHost: true}, "--all-on-host"},
		{"except without all-on-host", operatorStopParams{Target: []string{"worker/kid"}, Except: []string{"worker/kid2"}}, "--except"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := stubRestartHandoff(t, "", nil)
			stubOperatorRestart(t, true, "y\n")
			repoPath := operatorStopFixture(t)
			if err := runOperatorStop(tc.p); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a refusal naming %q", err, tc.want)
			}
			if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
				t.Error("nothing may be recorded or run")
			}
		})
	}
}

// A target naming one clown of a session (SESSION@principal) is reserved for
// sessions with more than one clown; today it is refused per target.
func TestOperatorStopReservesThePerClownTargetForm(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stdout, _ := stubOperatorRestart(t, true, "y\n")
	repoPath := operatorStopFixture(t)
	if err := runOperatorStop(operatorStopParams{Target: []string{"worker/kid@" + childPrincipal}}); err == nil {
		t.Fatal("want a nonzero exit")
	}
	if !strings.Contains(stdout.String(), "refused: ") || !strings.Contains(stdout.String(), "one clown of a session") {
		t.Errorf("stdout = %q, want the reserved-form refusal", stdout.String())
	}
	if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
		t.Error("nothing may be recorded or run")
	}
}

// The operator starts a stopped session with no handle: the same start hand-off
// and notice as start-child-session, the operator named as the starter.
func TestOperatorStartStartsAStoppedSessionWithoutAHandle(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stdout, stderr := stubOperatorRestart(t, true, "y\n")
	repoPath, _ := stoppedChildFixture(t, "holder", "observe", true)

	if err := runOperatorStart(operatorStartParams{Target: []string{"worker/kid"}, Message: "carry on"}); err != nil {
		t.Fatalf("err = %v (stdout %q)", err, stdout.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("hand-off calls = %+v, want one", *calls)
	}
	argv := (*calls)[0].argv
	msg := argv[len(argv)-1]
	if !slices.Contains(argv, childPrincipal) || !strings.Contains(msg, "quiet hours") ||
		!strings.Contains(msg, "by "+operatorRestartRequester+".") || !strings.HasSuffix(msg, "carry on") {
		t.Errorf("argv = %q, want the principal and a notice naming the operator, then the message", argv)
	}
	if readKid(t, repoPath).StoppedAt != nil {
		t.Error("the stopped mark must be cleared")
	}
	if !strings.Contains(stderr.String(), "start 1 session(s)? [y/N]") {
		t.Errorf("stderr = %q, want one prompt", stderr.String())
	}
	if got, want := stdout.String(), "worker/kid: started\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestOperatorStartRefusesWhatItCannotStart(t *testing.T) {
	// Not stopped.
	calls := stubRestartHandoff(t, "", nil)
	stdout, stderr := stubOperatorRestart(t, true, "y\n")
	operatorStopFixture(t)
	writeGlobalSweatfile(t, os.Getenv("HOME"), restartHandoffTOML+startHandoffTOML)
	if err := runOperatorStart(operatorStartParams{Target: []string{"worker/kid"}, Message: "go"}); err == nil {
		t.Fatal("not stopped: want a nonzero exit")
	}
	if !strings.Contains(stdout.String(), "worker/kid: refused: ") || !strings.Contains(stdout.String(), "not stopped") ||
		strings.Contains(stderr.String(), "[y/N]") || len(*calls) != 0 {
		t.Errorf("not stopped: stdout %q stderr %q calls %+v", stdout.String(), stderr.String(), *calls)
	}

	// No start hand-off configured.
	calls = stubRestartHandoff(t, "", nil)
	stdout, _ = stubOperatorRestart(t, true, "y\n")
	repoPath, _ := stoppedChildFixture(t, "holder", "observe", false)
	if err := runOperatorStart(operatorStartParams{Target: []string{"worker/kid"}, Message: "go"}); err == nil {
		t.Fatal("no hand-off: want a nonzero exit")
	}
	if !strings.Contains(stdout.String(), "start-handoff") || len(*calls) != 0 || readKid(t, repoPath).StoppedAt == nil {
		t.Errorf("no hand-off: stdout %q calls %+v", stdout.String(), *calls)
	}

	// No message, or no terminal: refused before anything is judged.
	stubOperatorRestart(t, true, "y\n")
	if err := runOperatorStart(operatorStartParams{Target: []string{"worker/kid"}, Message: " "}); err == nil || !strings.Contains(err.Error(), "--message") {
		t.Errorf("no message: err = %v", err)
	}
	stubOperatorRestart(t, false, "y\n")
	if err := runOperatorStart(operatorStartParams{Target: []string{"worker/kid"}, Message: "go"}); err == nil || !strings.Contains(err.Error(), "terminals") {
		t.Errorf("no terminal: err = %v", err)
	}
}

func TestOperatorStopAndStartAreCLIOnly(t *testing.T) {
	app := buildApp()
	for _, name := range []string{"stop", "start-stopped"} {
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
		if tool.Name == "stop" || tool.Name == "start-stopped" {
			t.Errorf("%s must not be an MCP tool", tool.Name)
		}
	}
}
