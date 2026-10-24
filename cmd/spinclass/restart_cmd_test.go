package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/confinement"
	"code.linenisgreat.com/spinclass/internal/job"
	"code.linenisgreat.com/spinclass/internal/session"
)

// restartHandoffTOML is the trusted-layer hand-off every test that expects a
// restart to go through writes into the global sweatfile.
const restartHandoffTOML = "[session-entry]\n" +
	`restart = ["clown", "restart", "--session", "{principal}", "--key", "{session-key}", "--when", "{when}", "--reason", "{reason}"]` + "\n"

// childPrincipal is the principal of the restarted child in the child tests.
const childPrincipal = "9c1d7e44-2b60-4f0a-8e35-6a1b0c2d3e4f"

type handoffCall struct {
	dir  string
	argv []string
}

// stubRestartHandoff replaces the exec of the hand-off: it records each call
// and returns out and err.
func stubRestartHandoff(t *testing.T, out string, err error) *[]handoffCall {
	t.Helper()
	var calls []handoffCall
	orig := restartHandoffFn
	restartHandoffFn = func(dir string, argv []string) (string, error) {
		calls = append(calls, handoffCall{dir, argv})
		return out, err
	}
	t.Cleanup(func() { restartHandoffFn = orig })
	return &calls
}

type notifyCall struct{ target, from, message string }

func stubNotifyPrincipal(t *testing.T) *[]notifyCall {
	t.Helper()
	var calls []notifyCall
	orig := notifyPrincipalFn
	notifyPrincipalFn = func(_ context.Context, target, from, message string) error {
		calls = append(calls, notifyCall{target, from, message})
		return nil
	}
	t.Cleanup(func() { notifyPrincipalFn = orig })
	return &calls
}

// restartSelfFixture is selfCloseFixture with the hand-off configured in the
// global sweatfile (when handoff is set).
func restartSelfFixture(t *testing.T, handoff bool) (repoPath, wtPath string) {
	t.Helper()
	repoPath, wtPath = selfCloseFixture(t, false)
	if handoff {
		writeGlobalSweatfile(t, os.Getenv("HOME"), restartHandoffTOML)
	}
	return repoPath, wtPath
}

func callRestartThis(t *testing.T, args string) (text string, isErr bool) {
	t.Helper()
	res, err := handleRestartThisSession(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("handleRestartThisSession: %v", err)
	}
	return res.Text, res.IsErr
}

func callRestartChild(t *testing.T, args string) (text string, isErr bool) {
	t.Helper()
	res, err := handleRestartChildSession(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("handleRestartChildSession: %v", err)
	}
	return res.Text, res.IsErr
}

func wantSelfArgv(reason string) []string {
	return []string{"clown", "restart", "--session", selfPrincipal, "--key", "worker/kid", "--when", "idle", "--reason", reason}
}

func TestExpandRestartHandoff(t *testing.T) {
	got := expandRestartHandoff(
		[]string{"clown", "--s={principal}", "{session-key}", "{when}", "{reason}", "{unknown}"},
		"p", "r/b", "idle", "because {principal}")
	want := []string{"clown", "--s=p", "r/b", "idle", "because {principal}", "{unknown}"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("expandRestartHandoff = %q, want %q (substitution is one pass: a reason cannot smuggle a placeholder)", got, want)
	}
}

func TestRestartThisSessionRefusesWithoutHandoff(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, _ := restartSelfFixture(t, false)

	text, isErr := callRestartThis(t, `{}`)
	if !isErr || !strings.Contains(text, "[session-entry]") || !strings.Contains(text, "restart") {
		t.Fatalf("want a refusal naming [session-entry] restart, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).RestartRequest != nil {
		t.Error("a refused call must not record a restart request")
	}
	if len(*calls) != 0 {
		t.Errorf("no hand-off may run: %+v", *calls)
	}
}

// A repo-layer sweatfile is writable by the agent, so it must not choose the
// command spinclass runs.
func TestRestartThisSessionIgnoresRepoLayerHandoff(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, _ := restartSelfFixture(t, false)
	body := "[hooks]\ndisable-nix-gc = true\n[session-entry]\nrestart = [\"evil\"]\n"
	if err := os.WriteFile(filepath.Join(repoPath, "sweatfile"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, isErr := callRestartThis(t, `{}`); !isErr {
		t.Fatal("a repo-layer restart hand-off must not enable the tool")
	}
	if len(*calls) != 0 {
		t.Errorf("the repo-layer command must never run: %+v", *calls)
	}
}

func TestRestartThisSessionRefusesWhileAJobRuns(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, wtPath := restartSelfFixture(t, true)
	if err := job.Write(wtPath, &job.Job{
		ID: "j1", Kind: "merge", Status: job.StatusRunning, ServePID: os.Getpid(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	text, isErr := callRestartThis(t, `{}`)
	if !isErr || !strings.Contains(text, "job") {
		t.Fatalf("want a refusal naming the running job, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
		t.Error("must neither record a request nor run the hand-off while a job runs")
	}
}

func TestRestartThisSessionRefusesANonUUIDPrincipal(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, wtPath := restartSelfFixture(t, true)
	const odd = "not-a-uuid"
	t.Setenv("CLOWN_SESSION_ID", odd)
	if err := confinement.RecordUnconfinedPrincipal(os.Getenv("XDG_STATE_HOME"), odd, "worker/kid", wtPath); err != nil {
		t.Fatal(err)
	}

	text, isErr := callRestartThis(t, `{}`)
	if !isErr || !strings.Contains(text, "UUID") {
		t.Fatalf("want a refusal naming UUID, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
		t.Error("a non-UUID principal must neither record a request nor run the hand-off")
	}
}

func TestRestartObstacleRefusesAnImplicitSession(t *testing.T) {
	st := &session.State{Kind: session.KindImplicit, RepoPath: t.TempDir(), SessionKey: "r/main"}
	_, obstacle, err := restartPlan(st, selfPrincipal)
	if err != nil || !strings.Contains(obstacle, "implicit") {
		t.Fatalf("restartPlan = %q, %v; want an obstacle naming implicit", obstacle, err)
	}
}

func TestRestartThisSessionQueuesAndRunsTheHandoff(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, wtPath := restartSelfFixture(t, true)

	text, isErr := callRestartThis(t, `{"reason":"need new tools"}`)
	if isErr {
		t.Fatalf("expected the restart to be accepted, got %q", text)
	}
	if !strings.Contains(text, "end your turn") {
		t.Errorf("the result must tell the agent to end its turn: %q", text)
	}
	req := readKid(t, repoPath).RestartRequest
	if req == nil || req.Requester != selfPrincipal || req.When != "idle" || req.Reason != "need new tools" || req.RequestedAt.IsZero() {
		t.Fatalf("restart request = %+v, want one by %s, idle, reason given", req, selfPrincipal)
	}
	if len(*calls) != 1 || (*calls)[0].dir != wtPath || !reflect.DeepEqual((*calls)[0].argv, wantSelfArgv("need new tools")) {
		t.Fatalf("hand-off calls = %+v, want one in %s with argv %q", *calls, wtPath, wantSelfArgv("need new tools"))
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("a restart removes nothing: %v", err)
	}
}

func TestRestartThisSessionDefaultReason(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	restartSelfFixture(t, true)
	if text, isErr := callRestartThis(t, `{}`); isErr {
		t.Fatalf("refused: %q", text)
	}
	want := wantSelfArgv("requested by the session itself")
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].argv, want) {
		t.Errorf("hand-off calls = %+v, want argv %q", *calls, want)
	}
}

func TestRestartThisSessionHandoffFailureClearsTheRequest(t *testing.T) {
	stubRestartHandoff(t, "clown: no such session", errors.New("exit status 3"))
	repoPath, _ := restartSelfFixture(t, true)

	text, isErr := callRestartThis(t, `{}`)
	if !isErr || !strings.Contains(text, "clown: no such session") {
		t.Fatalf("want an error carrying the hand-off output, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).RestartRequest != nil {
		t.Error("a failed hand-off must clear the request so the frontend never restarts on it")
	}
}

func TestRestartThisSessionHasNoForce(t *testing.T) {
	for _, args := range []string{`{"force":true}`, `{"force":false}`} {
		calls := stubRestartHandoff(t, "", nil)
		repoPath, _ := restartSelfFixture(t, true)
		text, isErr := callRestartThis(t, args)
		if !isErr || !strings.Contains(text, "force") {
			t.Errorf("%s: want a refusal naming force, got isErr=%v %q", args, isErr, text)
		}
		if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
			t.Errorf("%s: must not record a request or run the hand-off", args)
		}
	}
}

func TestRestartThisSessionRefusesAnUnidentifiedCaller(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	restartSelfFixture(t, true)
	t.Setenv("CLOWN_SESSION_ID", "")
	if _, isErr := callRestartThis(t, `{}`); !isErr || len(*calls) != 0 {
		t.Fatal("a caller whose principal maps to no session must be refused without a hand-off")
	}
}

// restartChildFixture stages worker/kid, whose own principal is childPrincipal,
// with the hand-off configured and caller as the calling principal.
func restartChildFixture(t *testing.T, caller string, st session.State) (repoPath, wtPath string) {
	t.Helper()
	st.Principal = childPrincipal
	repoPath, wtPath = childHandleFixture(t, caller, st)
	writeGlobalSweatfile(t, os.Getenv("HOME"), restartHandoffTOML)
	return repoPath, wtPath
}

func wantChildArgv(reason string) []string {
	return []string{"clown", "restart", "--session", childPrincipal, "--key", "worker/kid", "--when", "idle", "--reason", reason}
}

func TestRestartChildSessionRefusedWithoutAHandle(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, _ := restartChildFixture(t, "stranger", session.State{Holders: []string{"someone-else"}})

	text, isErr := callRestartChild(t, `{"child":"worker/kid"}`)
	if !isErr || !strings.Contains(text, "no handle") {
		t.Fatalf("want authorizeHandleUse's no-handle refusal, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
		t.Error("a caller with no handle must neither record a request nor run the hand-off")
	}
}

// A default grant (observe,close) is not enough: restart is its own right.
func TestRestartChildSessionRefusedWithoutTheRestartRight(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, _ := restartChildFixture(t, "holder", session.State{
		Holders:      []string{"holder"},
		HandleRights: map[string]string{"holder": "observe,close"},
	})

	text, isErr := callRestartChild(t, `{"child":"worker/kid"}`)
	if !isErr || !strings.Contains(text, "restart") || !strings.Contains(text, "observe,close") {
		t.Fatalf("want a refusal naming the missing restart right and what is held, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
		t.Error("a holder without the right must neither record a request nor run the hand-off")
	}
}

func TestRestartChildSessionAllowedWithTheRestartRight(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, wtPath := restartChildFixture(t, "holder", session.State{
		Holders:      []string{"holder"},
		HandleRights: map[string]string{"holder": "observe,restart"},
	})

	text, isErr := callRestartChild(t, `{"child":"worker/kid","reason":"pick up new config"}`)
	if isErr {
		t.Fatalf("a holder with restart must be allowed: %q", text)
	}
	req := readKid(t, repoPath).RestartRequest
	if req == nil || req.Requester != "holder" || req.When != "idle" || req.Reason != "pick up new config" {
		t.Fatalf("restart request = %+v, want one by holder, idle", req)
	}
	if len(*calls) != 1 || (*calls)[0].dir != wtPath || !reflect.DeepEqual((*calls)[0].argv, wantChildArgv("pick up new config")) {
		t.Fatalf("hand-off calls = %+v, want one in %s with argv %q", *calls, wtPath, wantChildArgv("pick up new config"))
	}
}

func TestRestartChildSessionDefaultReason(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	restartChildFixture(t, "holder", session.State{
		Holders: []string{"holder"}, HandleRights: map[string]string{"holder": "observe,restart"},
	})
	if text, isErr := callRestartChild(t, `{"child":"worker/kid"}`); isErr {
		t.Fatalf("refused: %q", text)
	}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].argv, wantChildArgv("requested by a holder")) {
		t.Errorf("hand-off calls = %+v, want default reason", *calls)
	}
}

// The spawner holds every right, restart included, through the session-key path.
func TestRestartChildSessionAllowedForTheSpawnerKeyPath(t *testing.T) {
	const driverKey = "driver/main-oak"
	calls := stubRestartHandoff(t, "", nil)
	repoPath, _ := restartChildFixture(t, "unrelated-principal", session.State{SpawnedBy: driverKey})
	t.Setenv("SPINCLASS_SESSION_ID", driverKey)

	if text, isErr := callRestartChild(t, `{"child":"worker/kid"}`); isErr {
		t.Fatalf("the spawner must be allowed: %q", text)
	}
	if readKid(t, repoPath).RestartRequest == nil || len(*calls) != 1 {
		t.Error("expected a recorded request and one hand-off")
	}
}

func TestRestartChildSessionAcceptsAPendingHandleOnFirstUse(t *testing.T) {
	stubRestartHandoff(t, "", nil)
	repoPath, _ := restartChildFixture(t, "pending", session.State{
		PendingHandles: []string{"pending"},
		HandleRights:   map[string]string{"pending": "restart"},
	})
	if text, isErr := callRestartChild(t, `{"child":"worker/kid"}`); isErr {
		t.Fatalf("a pending holder with restart must be allowed: %q", text)
	}
	if !readKid(t, repoPath).IsHolder("pending") {
		t.Error("exercising the handle must accept it")
	}
}

func TestRestartChildSessionRefusesAConfinedCaller(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	asConfinedServeCaller(t, "a-confined-principal")
	text, isErr := callRestartChild(t, `{"child":"worker/kid"}`)
	if !isErr || !strings.Contains(text, "confined") || len(*calls) != 0 {
		t.Fatalf("want a confined refusal and no hand-off, got isErr=%v %q calls=%v", isErr, text, *calls)
	}
}

func TestRestartChildSessionHasNoForce(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, _ := restartChildFixture(t, "holder", session.State{
		Holders: []string{"holder"}, HandleRights: map[string]string{"holder": "observe,restart"},
	})
	text, isErr := callRestartChild(t, `{"child":"worker/kid","force":true}`)
	if !isErr || !strings.Contains(text, "force") {
		t.Fatalf("want a refusal naming force, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
		t.Error("must not record a request or run the hand-off")
	}
}

// The child's repo hierarchy supplies the hand-off, and the same obstacles apply
// to the child as to a self-restart.
func TestRestartChildSessionObstacles(t *testing.T) {
	holder := session.State{Holders: []string{"holder"}, HandleRights: map[string]string{"holder": "observe,restart"}}

	t.Run("no hand-off", func(t *testing.T) {
		calls := stubRestartHandoff(t, "", nil)
		repoPath, _ := restartChildFixture(t, "holder", holder)
		writeGlobalSweatfile(t, os.Getenv("HOME"), "")
		if text, isErr := callRestartChild(t, `{"child":"worker/kid"}`); !isErr || !strings.Contains(text, "[session-entry]") {
			t.Fatalf("want a refusal naming [session-entry], got isErr=%v %q", isErr, text)
		}
		if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
			t.Error("nothing may be recorded or run")
		}
	})
	t.Run("job running", func(t *testing.T) {
		calls := stubRestartHandoff(t, "", nil)
		repoPath, wtPath := restartChildFixture(t, "holder", holder)
		if err := job.Write(wtPath, &job.Job{
			ID: "j1", Kind: "check", Status: job.StatusRunning, ServePID: os.Getpid(), StartedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		if text, isErr := callRestartChild(t, `{"child":"worker/kid"}`); !isErr || !strings.Contains(text, "job") {
			t.Fatalf("want a refusal naming the job, got isErr=%v %q", isErr, text)
		}
		if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
			t.Error("nothing may be recorded or run")
		}
	})
	t.Run("non-UUID child principal", func(t *testing.T) {
		calls := stubRestartHandoff(t, "", nil)
		repoPath, _ := restartChildFixture(t, "holder", holder)
		st := readKid(t, repoPath)
		st.Principal = "not-a-uuid"
		if err := session.Write(*st); err != nil {
			t.Fatal(err)
		}
		if text, isErr := callRestartChild(t, `{"child":"worker/kid"}`); !isErr || !strings.Contains(text, "UUID") {
			t.Fatalf("want a refusal naming UUID, got isErr=%v %q", isErr, text)
		}
		if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
			t.Error("nothing may be recorded or run")
		}
	})
	t.Run("hand-off failure", func(t *testing.T) {
		stubRestartHandoff(t, "boom output", errors.New("exit status 1"))
		repoPath, _ := restartChildFixture(t, "holder", holder)
		if text, isErr := callRestartChild(t, `{"child":"worker/kid"}`); !isErr || !strings.Contains(text, "boom output") {
			t.Fatalf("want the hand-off output, got isErr=%v %q", isErr, text)
		}
		if readKid(t, repoPath).RestartRequest != nil {
			t.Error("a failed hand-off must clear the request")
		}
	})
}

// requestRestart queues a restart through the tool, ready for the preflight.
func requestRestart(t *testing.T) (repoPath, wtPath string) {
	t.Helper()
	stubRestartHandoff(t, "", nil)
	repoPath, wtPath = restartSelfFixture(t, true)
	if text, isErr := callRestartThis(t, `{"reason":"why"}`); isErr {
		t.Fatalf("setup: request refused: %q", text)
	}
	return repoPath, wtPath
}

func TestRestartPreflightWithoutARequestIsNone(t *testing.T) {
	restartSelfFixture(t, true)
	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "none" {
		t.Fatalf("got %+v, %v; want none", v, err)
	}
	if v, err := runRestartPreflight("nope/nothing"); err != nil || v.Verdict != "none" {
		t.Fatalf("unknown session: got %+v, %v; want none", v, err)
	}
}

func TestRestartPreflightConsumesTheRequest(t *testing.T) {
	repoPath, _ := requestRestart(t)

	v, err := runRestartPreflight("worker/kid")
	if err != nil {
		t.Fatalf("runRestartPreflight: %v", err)
	}
	if v.Verdict != "restart" || v.When != "idle" {
		t.Fatalf("verdict = %+v, want restart/idle", v)
	}
	if readKid(t, repoPath).RestartRequest != nil {
		t.Error("the request is consumed, so the relaunched session does not restart again")
	}
	if v2, _ := runRestartPreflight("worker/kid"); v2.Verdict != "none" {
		t.Errorf("a second preflight = %+v, want none", v2)
	}
}

func TestRestartPreflightDefaultsToTheSessionOfClownSessionID(t *testing.T) {
	requestRestart(t)
	if v, err := runRestartPreflight(""); err != nil || v.Verdict != "restart" {
		t.Fatalf("got %+v, %v; want restart for the CLOWN_SESSION_ID session", v, err)
	}
}

func TestRestartPreflightRefusesAJobStartedSinceTheRequest(t *testing.T) {
	notified := stubNotifyPrincipal(t)
	repoPath, wtPath := requestRestart(t)
	if err := job.Write(wtPath, &job.Job{
		ID: "j9", Kind: "merge", Status: job.StatusRunning, ServePID: os.Getpid(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	v, err := runRestartPreflight("worker/kid")
	if err != nil {
		t.Fatalf("runRestartPreflight: %v", err)
	}
	if v.Verdict != "refused" || !strings.Contains(v.Reason, "job") {
		t.Fatalf("verdict = %+v, want refused naming the job", v)
	}
	if readKid(t, repoPath).RestartRequest != nil {
		t.Error("a refused request must be cleared so it is not retried forever")
	}
	if len(*notified) != 1 || (*notified)[0].target != "spawner" || !strings.Contains((*notified)[0].message, "worker/kid") {
		t.Errorf("notifications = %+v, want one to the holder [spawner] naming the session", *notified)
	}
}

func TestRestartPreflightRefusesWhenTheHandoffWasRevoked(t *testing.T) {
	stubNotifyPrincipal(t)
	repoPath, _ := requestRestart(t)
	writeGlobalSweatfile(t, os.Getenv("HOME"), "")

	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "refused" {
		t.Fatalf("got %+v, %v; want refused", v, err)
	}
	if readKid(t, repoPath).RestartRequest != nil {
		t.Error("the request must be cleared")
	}
}

// A confined session with no frontend mapping cannot be tied to a principal,
// and the preflight must not fall back to the agent-writable state.
func TestRestartPreflightRefusesAConfinedSessionWithoutAMapping(t *testing.T) {
	stubNotifyPrincipal(t)
	repoPath, wtPath := requestRestart(t)
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

	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "refused" {
		t.Fatalf("got %+v, %v; want refused", v, err)
	}
	if readKid(t, repoPath).RestartRequest != nil {
		t.Error("the request must be cleared")
	}
}

func TestPrintRestartVerdictIsOneJSONLine(t *testing.T) {
	var buf bytes.Buffer
	if err := printRestartVerdict(&buf, restartVerdict{Verdict: "restart", Reason: "queued by p", When: "idle"}); err != nil {
		t.Fatal(err)
	}
	want := `{"verdict":"restart","reason":"queued by p","when":"idle"}` + "\n"
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}
