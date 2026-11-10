package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/server"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// spinclass#473: `sc restart SESSION... --posture P` is the operator's own
// restart. It needs a terminal, no handle, judges every target on its own, and
// prompts once before any widening.

// secondPrincipal is the principal of worker/kid2 in the multi-target tests.
const secondPrincipal = "3b7e2f10-4c5d-4e6f-9a8b-1c2d3e4f5a6b"

// stubOperatorRestart drives the command's seams: terminal says whether stdin
// and stdout are terminals, typed is what the operator answers at the prompt.
func stubOperatorRestart(t *testing.T, terminal bool, typed string) (stdout, stderr *bytes.Buffer) {
	t.Helper()
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	oldTTY, oldIn, oldOut, oldErr := operatorRestartIsTerminal, operatorRestartConfirmIn, operatorRestartStdout, operatorRestartStderr
	t.Cleanup(func() {
		operatorRestartIsTerminal, operatorRestartConfirmIn, operatorRestartStdout, operatorRestartStderr = oldTTY, oldIn, oldOut, oldErr
	})
	operatorRestartIsTerminal = func() bool { return terminal }
	operatorRestartConfirmIn = func() io.Reader { return strings.NewReader(typed) }
	operatorRestartStdout = func() io.Writer { return stdout }
	operatorRestartStderr = func() io.Writer { return stderr }
	return stdout, stderr
}

// addKid2 stages a second session, worker/kid2, in repoPath with entry pinned.
func addKid2(t *testing.T, repoPath string, entry *session.PinnedEntry) {
	t.Helper()
	wtPath := filepath.Join(repoPath, ".worktrees", "kid2")
	testgit.MustWorktreeAdd(t, repoPath, wtPath, "kid2")
	if err := session.Write(session.State{
		SessionState: session.StateInactive,
		RepoPath:     repoPath,
		WorktreePath: wtPath,
		Branch:       "kid2",
		SessionKey:   "worker/kid2",
		Principal:    secondPrincipal,
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC(),
		Entry:        entry,
	}); err != nil {
		t.Fatal(err)
	}
}

func readKid2(t *testing.T, repoPath string) *session.State {
	t.Helper()
	st, err := session.Read(repoPath, "kid2")
	if err != nil {
		t.Fatalf("session.Read: %v", err)
	}
	return st
}

// operatorFixture stages worker/kid with entry pinned and the posture hand-off.
// The caller is "operator-shell", a principal holding NO handle: the operator
// needs none.
func operatorFixture(t *testing.T, entry *session.PinnedEntry, widen bool) (repoPath, wtPath string) {
	t.Helper()
	repoPath, wtPath = restartChildFixture(t, "operator-shell", session.State{
		Holders:      []string{"holder"},
		HandleRights: map[string]string{"holder": "observe,restart"},
		Entry:        entry,
	})
	body := restartPostureHandoffTOML
	if widen {
		body += allowWideningTOML
	}
	writeGlobalSweatfile(t, os.Getenv("HOME"), body)
	return repoPath, wtPath
}

func operatorArgv(principal, posture, reason string) []string {
	return []string{"clown", "restart", "--session", principal, "--when", "idle", "--posture", posture, "--reason", reason}
}

func verdictLines(out string) []string {
	return strings.Split(strings.TrimRight(out, "\n"), "\n")
}

func TestOperatorRestartNarrowsWithoutAPromptOrAHandle(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stdout, stderr := stubOperatorRestart(t, true, "")
	repoPath, _ := operatorFixture(t, harnessEntry(nil), false)

	if err := runOperatorRestart(operatorRestartParams{Target: []string{"worker/kid"}, Posture: "unattended"}); err != nil {
		t.Fatalf("narrowing must succeed: %v (stdout %q)", err, stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("narrowing must not prompt; stderr = %q", stderr.String())
	}
	req := readKid(t, repoPath).RestartRequest
	if req == nil || req.Posture != "unattended" || req.Requester != operatorRestartRequester {
		t.Fatalf("restart request = %+v, want posture unattended by %q", req, operatorRestartRequester)
	}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].argv, operatorArgv(childPrincipal, "unattended", defaultOperatorRestartReason)) {
		t.Fatalf("hand-off calls = %+v", *calls)
	}
	if got, want := stdout.String(), "worker/kid: attended -> unattended: restart requested\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestOperatorRestartWideningWithTheKeyAsksOnce(t *testing.T) {
	for _, tc := range []struct {
		typed     string
		requested bool
	}{{"y\n", true}, {"n\n", false}, {"\n", false}} {
		t.Run(strings.TrimSpace(tc.typed), func(t *testing.T) {
			calls := stubRestartHandoff(t, "", nil)
			stdout, stderr := stubOperatorRestart(t, true, tc.typed)
			repoPath, _ := operatorFixture(t, harnessEntry(map[string]string{"CLOWN_UNATTENDED": "1"}), true)

			err := runOperatorRestart(operatorRestartParams{Target: []string{"worker/kid"}, Posture: "attended", Reason: "pair with me"})
			if !strings.Contains(stderr.String(), "worker/kid") || !strings.Contains(stderr.String(), "[y/N]") {
				t.Errorf("the prompt must list the widening target with a y/N default of no; stderr = %q", stderr.String())
			}
			req := readKid(t, repoPath).RestartRequest
			if tc.requested {
				if err != nil {
					t.Fatalf("an approved widening must succeed: %v", err)
				}
				if req == nil || req.Posture != "attended" || req.Requester != operatorRestartRequester {
					t.Fatalf("request = %+v", req)
				}
				if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].argv, operatorArgv(childPrincipal, "attended", "pair with me")) {
					t.Fatalf("hand-off calls = %+v", *calls)
				}
				if got, want := stdout.String(), "worker/kid: unattended -> attended: restart requested\n"; got != want {
					t.Errorf("stdout = %q, want %q", got, want)
				}
				return
			}
			if err == nil {
				t.Fatal("a declined widening must exit nonzero")
			}
			if req != nil || len(*calls) != 0 {
				t.Errorf("a declined widening must record and run nothing: %+v %+v", req, *calls)
			}
			if got := stdout.String(); !strings.Contains(got, "worker/kid: unattended -> attended: refused: declined at the prompt") {
				t.Errorf("stdout = %q", got)
			}
		})
	}
}

func TestOperatorRestartWideningWithoutTheKeyIsRefusedWithoutAPrompt(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stdout, stderr := stubOperatorRestart(t, true, "y\n")
	repoPath, _ := operatorFixture(t, harnessEntry(map[string]string{"CLOWN_UNATTENDED": "1"}), false)

	if err := runOperatorRestart(operatorRestartParams{Target: []string{"worker/kid"}, Posture: "attended"}); err == nil {
		t.Fatal("want a nonzero exit")
	}
	if stderr.Len() != 0 {
		t.Errorf("a widening that cannot happen must not prompt; stderr = %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "worker/kid: unattended -> attended: refused: ") || !strings.Contains(stdout.String(), "allow-posture-widening") {
		t.Errorf("stdout = %q, want a refusal naming the key", stdout.String())
	}
	if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
		t.Error("nothing may be recorded or run")
	}
}

// One failing target does not stop the rest; each prints one line; the exit is
// nonzero. A declined widening does not stop a narrowing in the same batch.
func TestOperatorRestartJudgesEveryTargetOnItsOwn(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stdout, _ := stubOperatorRestart(t, true, "n\n")
	repoPath, _ := operatorFixture(t, harnessEntry(nil), true)
	addKid2(t, repoPath, nil) // unpinned: refused

	err := runOperatorRestart(operatorRestartParams{Target: []string{"nope/none", "worker/kid2", "worker/kid"}, Posture: "unattended"})
	if err == nil {
		t.Fatal("want a nonzero exit when any target failed")
	}
	lines := verdictLines(stdout.String())
	if len(lines) != 3 {
		t.Fatalf("want one verdict line per target, got %q", stdout.String())
	}
	if !strings.HasPrefix(lines[0], "nope/none: unknown -> unattended: refused: ") {
		t.Errorf("line 0 = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "worker/kid2: unknown -> unattended: refused: ") || !strings.Contains(lines[1], "no pinned session entry") {
		t.Errorf("line 1 = %q", lines[1])
	}
	if lines[2] != "worker/kid: attended -> unattended: restart requested" {
		t.Errorf("line 2 = %q", lines[2])
	}
	if readKid(t, repoPath).RestartRequest == nil || readKid2(t, repoPath).RestartRequest != nil {
		t.Error("only worker/kid may carry a request")
	}
	if len(*calls) != 1 {
		t.Errorf("hand-off calls = %+v, want exactly one", *calls)
	}
}

// One --posture covers the batch, so a widening batch is all widenings: one
// prompt lists them all, and declining refuses each.
func TestOperatorRestartPromptsOnceForTheWholeBatch(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stdout, stderr := stubOperatorRestart(t, true, "n\n")
	repoPath, _ := operatorFixture(t, harnessEntry(map[string]string{"CLOWN_UNATTENDED": "1"}), true)
	addKid2(t, repoPath, harnessEntry(nil))

	err := runOperatorRestart(operatorRestartParams{Target: []string{"worker/kid", "worker/kid2"}, Posture: "attended"})
	if err == nil {
		t.Fatal("declined widenings must exit nonzero")
	}
	if strings.Count(stderr.String(), "[y/N]") != 1 || !strings.Contains(stderr.String(), "worker/kid2") {
		t.Errorf("want one prompt listing both targets; stderr = %q", stderr.String())
	}
	lines := verdictLines(stdout.String())
	if len(lines) != 2 || !strings.Contains(lines[0], "declined at the prompt") || !strings.Contains(lines[1], "declined at the prompt") {
		t.Errorf("stdout = %q", stdout.String())
	}
	if len(*calls) != 0 || readKid(t, repoPath).RestartRequest != nil || readKid2(t, repoPath).RestartRequest != nil {
		t.Error("nothing may be recorded or run")
	}
}

func TestOperatorRestartRefusesWithoutATerminal(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stdout, stderr := stubOperatorRestart(t, false, "y\n")
	repoPath, _ := operatorFixture(t, harnessEntry(nil), true)

	err := runOperatorRestart(operatorRestartParams{Target: []string{"worker/kid"}, Posture: "unattended"})
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("want a terminal refusal, got %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 || len(*calls) != 0 || readKid(t, repoPath).RestartRequest != nil {
		t.Errorf("nothing may be touched: stdout %q stderr %q calls %+v", stdout.String(), stderr.String(), *calls)
	}
}

func TestOperatorRestartRefusesConfinedAndUnpinnedTargets(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stdout, _ := stubOperatorRestart(t, true, "")
	repoPath, wtPath := operatorFixture(t, harnessEntry(nil), true)
	addKid2(t, repoPath, nil)
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		t.Fatal(err)
	}
	rec := confinement.UnitRecordPath(stateDir, wtPath)
	if err := os.MkdirAll(filepath.Dir(rec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runOperatorRestart(operatorRestartParams{Target: []string{"worker/kid", "worker/kid2"}, Posture: "unattended"}); err == nil {
		t.Fatal("want a nonzero exit")
	}
	lines := verdictLines(stdout.String())
	if len(lines) != 2 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(lines[0], "worker/kid: ") || !strings.Contains(lines[0], "refused: ") || !strings.Contains(lines[0], "confined") {
		t.Errorf("line 0 = %q", lines[0])
	}
	if !strings.Contains(lines[1], "worker/kid2: unknown -> unattended: refused: ") || !strings.Contains(lines[1], "no pinned session entry") {
		t.Errorf("line 1 = %q", lines[1])
	}
	if len(*calls) != 0 {
		t.Errorf("hand-off calls = %+v", *calls)
	}
}

func TestOperatorRestartKeepIsAPlainRestart(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stdout, stderr := stubOperatorRestart(t, true, "")
	repoPath, _ := operatorFixture(t, harnessEntry(map[string]string{"CLOWN_UNATTENDED": "1"}), false)

	if err := runOperatorRestart(operatorRestartParams{Target: []string{"worker/kid"}, Posture: "keep"}); err != nil {
		t.Fatalf("keep must succeed: %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("keep must not prompt: %q", stderr.String())
	}
	req := readKid(t, repoPath).RestartRequest
	if req == nil || req.Posture != "" || req.Requester != operatorRestartRequester {
		t.Fatalf("request = %+v, want a plain request", req)
	}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].argv, operatorArgv(childPrincipal, "keep", defaultOperatorRestartReason)) {
		t.Fatalf("hand-off calls = %+v", *calls)
	}
	if got, want := stdout.String(), "worker/kid: unattended -> unattended: restart requested\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// Through the real CLI parser: several positional targets and a flag after them.
func TestOperatorRestartParsesSeveralTargetsAndThePostureFlag(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stdout, _ := stubOperatorRestart(t, true, "")
	repoPath, _ := operatorFixture(t, harnessEntry(nil), false)
	addKid2(t, repoPath, harnessEntry(nil))

	err := buildApp().RunCLI(context.Background(), []string{"restart", "worker/kid", "worker/kid2", "--posture", "unattended", "--reason", "lane quiet"}, nil)
	if err != nil {
		t.Fatalf("RunCLI: %v (stdout %q)", err, stdout.String())
	}
	if got := verdictLines(stdout.String()); len(got) != 2 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if len(*calls) != 2 || !reflect.DeepEqual((*calls)[1].argv, operatorArgv(secondPrincipal, "unattended", "lane quiet")) {
		t.Fatalf("hand-off calls = %+v", *calls)
	}
}

func TestOperatorRestartIsNotAnMCPTool(t *testing.T) {
	app := buildApp()
	cmd, ok := app.GetCommand("restart")
	if !ok {
		t.Fatal("sc restart is not registered")
	}
	if cmd.Run != nil || cmd.RunCLI == nil {
		t.Fatalf("must be RunCLI-only (Run=%v RunCLI=%v)", cmd.Run != nil, cmd.RunCLI != nil)
	}
	registry := server.NewToolRegistryV1()
	app.RegisterMCPToolsV1(registry)
	tools, err := registry.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) == 0 {
		t.Fatal("empty tool list: the assertion would be vacuous")
	}
	for _, tool := range tools {
		if tool.Name == "restart" {
			t.Fatal("sc restart is exposed as an MCP tool")
		}
	}
}

func TestOperatorRestartRejectsAnUnknownPostureBeforeAnything(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	stdout, _ := stubOperatorRestart(t, true, "")
	operatorFixture(t, harnessEntry(nil), false)
	err := runOperatorRestart(operatorRestartParams{Target: []string{"worker/kid"}, Posture: "wide-open"})
	if err == nil || !strings.Contains(err.Error(), "unattended, attended or keep") {
		t.Fatalf("err = %v", err)
	}
	if stdout.Len() != 0 || len(*calls) != 0 {
		t.Error("nothing may run")
	}
}

// The batch prompt ends its line even when the answer had no newline.
func TestOperatorRestartPromptEndsTheLineWithoutANewlineInTheAnswer(t *testing.T) {
	stubRestartHandoff(t, "", nil)
	_, stderr := stubOperatorRestart(t, true, "y")
	operatorFixture(t, harnessEntry(map[string]string{"CLOWN_UNATTENDED": "1"}), true)
	if err := runOperatorRestart(operatorRestartParams{Target: []string{"worker/kid"}, Posture: "attended"}); err != nil {
		t.Fatalf("an approved widening must succeed: %v", err)
	}
	if !strings.HasSuffix(stderr.String(), "[y/N] \n") {
		t.Errorf("the prompt line is not ended when the answer has no newline:\n%q", stderr.String())
	}
}

// notifiedTargets is who the stubbed wakes went to.
func notifiedTargets(calls []notifyCall) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.target)
	}
	return out
}

// Operator ruling (spinclass#473, option a): the preflight accepts a widening
// whose recorded requester is the `sc restart` marker. The holders are woken,
// the marker (not a principal) is not.
func TestOperatorWideningPassesThePreflight(t *testing.T) {
	notes := stubNotifyPrincipal(t)
	stubRestartHandoff(t, "", nil)
	stubOperatorRestart(t, true, "y\n")
	repoPath, _ := operatorFixture(t, harnessEntry(map[string]string{"CLOWN_UNATTENDED": "1"}), true)
	if err := runOperatorRestart(operatorRestartParams{Target: []string{"worker/kid"}, Posture: "attended"}); err != nil {
		t.Fatalf("request: %v", err)
	}
	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "restart" || v.Posture != "attended" {
		t.Fatalf("verdict = %+v, %v; want restart with posture attended", v, err)
	}
	if _, has := readKid(t, repoPath).Entry.Env["CLOWN_UNATTENDED"]; has {
		t.Error("the pinned entry must lose the marker")
	}
	if got := notifiedTargets(*notes); !reflect.DeepEqual(got, []string{"holder"}) {
		t.Errorf("woken = %q, want only the holder (never the operator marker)", got)
	}
}

// A refused operator request wakes the holders, not the marker.
func TestOperatorRequestRefusedAtThePreflightWakesNoMarker(t *testing.T) {
	notes := stubNotifyPrincipal(t)
	stubRestartHandoff(t, "", nil)
	stubOperatorRestart(t, true, "y\n")
	operatorFixture(t, harnessEntry(map[string]string{"CLOWN_UNATTENDED": "1"}), true)
	if err := runOperatorRestart(operatorRestartParams{Target: []string{"worker/kid"}, Posture: "attended"}); err != nil {
		t.Fatalf("request: %v", err)
	}
	// The key is withdrawn before the preflight.
	writeGlobalSweatfile(t, os.Getenv("HOME"), restartPostureHandoffTOML)
	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "refused" {
		t.Fatalf("verdict = %+v, %v; want refused", v, err)
	}
	if got := notifiedTargets(*notes); !reflect.DeepEqual(got, []string{"holder"}) {
		t.Errorf("woken = %q, want only the holder", got)
	}
}

// --rearm of an operator request is allowed and re-emits its posture.
func TestOperatorRequestMayBeRearmed(t *testing.T) {
	stubNotifyPrincipal(t)
	stubRestartHandoff(t, "", nil)
	stubOperatorRestart(t, true, "")
	operatorFixture(t, harnessEntry(nil), false)
	if err := runOperatorRestart(operatorRestartParams{Target: []string{"worker/kid"}, Posture: "unattended"}); err != nil {
		t.Fatalf("request: %v", err)
	}
	if v, err := runRestartPreflight("worker/kid"); err != nil || v.Verdict != "restart" {
		t.Fatalf("verdict = %+v, %v", v, err)
	}
	rv, err := runRestartRearm("worker/kid")
	if err != nil || rv.Verdict != "restart" || rv.Posture != "unattended" {
		t.Fatalf("rearm verdict = %+v, %v; want restart re-emitting unattended", rv, err)
	}
}

// The acceptance is exact: a requester that only resembles the marker is a
// non-holder and is refused, at the preflight and at --rearm.
func TestPreflightAcceptsOnlyTheExactOperatorMarker(t *testing.T) {
	for _, requester := range []string{"operator", "operator (sc restart) ", "Operator (sc restart)", "someone-else"} {
		t.Run(requester, func(t *testing.T) {
			stubNotifyPrincipal(t)
			stubRestartHandoff(t, "", nil)
			repoPath, _ := operatorFixture(t, harnessEntry(map[string]string{"CLOWN_UNATTENDED": "1"}), true)
			st := readKid(t, repoPath)
			st.RestartRequest = &session.RestartRequest{
				RequestedAt: time.Now().UTC(), Requester: requester, Reason: "forged", When: "idle", Posture: "attended",
			}
			if err := session.Write(*st); err != nil {
				t.Fatal(err)
			}
			v, err := runRestartPreflight("worker/kid")
			if err != nil || v.Verdict != "refused" {
				t.Fatalf("verdict = %+v, %v; want refused", v, err)
			}

			// --rearm: an in-flight restart recorded as by this requester.
			now := time.Now().UTC()
			st = readKid(t, repoPath)
			st.RestartRequest, st.RestartingAt, st.RestartingBy, st.RestartingReason = nil, &now, requester, "forged"
			if err := session.Write(*st); err != nil {
				t.Fatal(err)
			}
			rv, err := runRestartRearm("worker/kid")
			if err != nil || rv.Verdict != "refused" {
				t.Fatalf("rearm verdict = %+v, %v; want refused", rv, err)
			}
		})
	}
}
