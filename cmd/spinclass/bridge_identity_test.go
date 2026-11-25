package main

// The identity of a bridged call: the session acted on is the listener's
// CallerOrigin.SessionKey and nothing else. These tests stage real frontend
// records, then make the environment (CLOWN_SESSION_ID) and a principal mapping
// file lie, and assert the verbs still act on the origin's session.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// stagedSession is one real session: a git worktree, its frontend record and
// the frontend principal mapping of its principal.
type stagedSession struct {
	repo, wt, key, principal string
}

// stageSession writes a real session record (and the mapping the SessionStart
// hook writes for an unconfined session) under the current XDG_STATE_HOME.
// holders are the record's accepted holders.
func stageSession(t *testing.T, repoName, branch, principal string, holders []string) stagedSession {
	t.Helper()
	repo, wt := stageNamedWorktree(t, repoName, branch)
	s := stagedSession{repo: repo, wt: wt, key: repoName + "/" + branch, principal: principal}
	if err := session.Write(session.State{
		SessionState:       session.StateInactive,
		RepoPath:           repo,
		WorktreePath:       wt,
		Branch:             branch,
		SessionKey:         s.key,
		Principal:          principal,
		SpawnedByPrincipal: "spawner",
		Holders:            holders,
		Entrypoint:         []string{"/bin/sh"},
		StartedAt:          time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := confinement.RecordUnconfinedPrincipal(os.Getenv("XDG_STATE_HOME"), principal, s.key, wt); err != nil {
		t.Fatal(err)
	}
	return s
}

// stageSessionKey is stageSession for a key written <repo-dirname>/<branch>.
func stageSessionKey(t *testing.T, key, principal string) stagedSession {
	t.Helper()
	repo, branch, ok := strings.Cut(key, "/")
	if !ok {
		t.Fatalf("session key %q has no slash", key)
	}
	return stageSession(t, repo, branch, principal, []string{"spawner"})
}

func fileSHA(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("hashing %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fingerprint is the sha256 of the session's authoritative record and of its
// principal mapping, so a test can assert neither was written.
func (s stagedSession) fingerprint(t *testing.T) string {
	t.Helper()
	return fileSHA(t, session.RecordPath(s.wt)) + "/" +
		fileSHA(t, confinement.PrincipalMappingPath(os.Getenv("XDG_STATE_HOME"), s.principal))
}

func (s stagedSession) record(t *testing.T) *session.State {
	t.Helper()
	st, err := session.FindByTarget(s.key)
	if err != nil {
		t.Fatalf("reading the record of %s: %v", s.key, err)
	}
	return st
}

func bridgedCtxFor(key string) context.Context {
	return confinement.WithOrigin(context.Background(), confinement.CallerOrigin{
		Transport:  confinement.TransportBridge,
		SessionKey: key,
		Unit:       "clown-agent-test.service",
	})
}

// liarFixture is session A (the listener's) and session B (the victim) with
// every ambient source naming B: CLOWN_SESSION_ID and SPINCLASS_SESSION_ID are
// B's, and A's principal mapping file names B's session. A holds nothing; B's
// holders include A's principal, so list-handles answers differently for the
// two principals. The trusted settings allow restart and self-close.
type liarFixture struct {
	a, b    stagedSession
	handoff *[]handoffCall
}

func newLiarFixture(t *testing.T) liarFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return stageLiars(t)
}

// stageLiars is newLiarFixture over the HOME and XDG_STATE_HOME already set.
func stageLiars(t *testing.T) liarFixture {
	t.Helper()
	state := os.Getenv("XDG_STATE_HOME")
	writeGlobalSweatfile(t, os.Getenv("HOME"), "[permissions]\nallow-self-close = true\n"+restartHandoffTOML)
	f := liarFixture{handoff: stubRestartHandoff(t, "", nil)}
	f.a = stageSession(t, "alpha-repo", "a-branch", selfPrincipal, []string{"spawner"})
	f.b = stageSession(t, "beta-repo", "b-branch", childPrincipal, []string{"spawner", selfPrincipal})
	t.Setenv("CLOWN_SESSION_ID", f.b.principal)
	t.Setenv("SPINCLASS_SESSION_ID", f.b.key)
	if err := confinement.RecordUnconfinedPrincipal(state, f.a.principal, f.b.key, f.b.wt); err != nil {
		t.Fatal(err)
	}
	return f
}

// The args name B in every field a verb might read.
func (f liarFixture) forgedArgs() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"session": f.b.key, "principal": f.b.principal, "child": f.b.key,
		"target": f.b.key, "session_key": f.b.key,
	})
	return b
}

func (f liarFixture) wantBUntouched(t *testing.T, before string) {
	t.Helper()
	if got := f.b.fingerprint(t); got != before {
		t.Errorf("the victim's record or mapping changed:\nbefore %s\nafter  %s", before, got)
	}
	if rr := f.b.record(t).RestartRequest; rr != nil {
		t.Errorf("a restart was queued on the victim: %+v", rr)
	}
	if cr := f.b.record(t).CloseRequest; cr != nil {
		t.Errorf("a close was queued on the victim: %+v", cr)
	}
}

func TestBridgedRestartThisSessionActsOnTheOriginsSession(t *testing.T) {
	f := newLiarFixture(t)
	before := f.b.fingerprint(t)

	res, err := handleRestartThisSession(bridgedCtxFor(f.a.key), f.forgedArgs(), nil)
	if err != nil || res.IsErr {
		t.Fatalf("restart-this-session: %v %+v", err, res)
	}
	if len(*f.handoff) != 1 {
		t.Fatalf("hand-off calls = %+v, want exactly one", *f.handoff)
	}
	call := (*f.handoff)[0]
	if call.dir != f.a.wt || !strings.Contains(strings.Join(call.argv, " "), "--session "+f.a.principal+" ") {
		t.Errorf("hand-off %+v does not name session A (%s, %s)", call, f.a.wt, f.a.principal)
	}
	if strings.Contains(strings.Join(call.argv, " "), f.b.principal) {
		t.Errorf("the hand-off names the victim's principal: %v", call.argv)
	}
	rr := f.a.record(t).RestartRequest
	if rr == nil || rr.Requester != f.a.principal {
		t.Errorf("A's record does not carry its own restart request: %+v", rr)
	}
	f.wantBUntouched(t, before)
}

func TestBridgedCloseThisSessionActsOnTheOriginsSession(t *testing.T) {
	f := newLiarFixture(t)
	before := f.b.fingerprint(t)

	res, err := handleCloseThisSession(bridgedCtxFor(f.a.key), f.forgedArgs(), nil)
	if err != nil || res.IsErr {
		t.Fatalf("close-this-session: %v %+v", err, res)
	}
	cr := f.a.record(t).CloseRequest
	if cr == nil || cr.Principal != f.a.principal {
		t.Errorf("A's record does not carry its own close request: %+v", cr)
	}
	f.wantBUntouched(t, before)
}

func TestBridgedCancelRestartOwnFormActsOnTheOriginsSession(t *testing.T) {
	f := newLiarFixture(t)
	before := f.b.fingerprint(t)
	ctx := bridgedCtxFor(f.a.key)

	if res, err := handleRestartThisSession(ctx, json.RawMessage(`{}`), nil); err != nil || res.IsErr {
		t.Fatalf("restart-this-session: %v %+v", err, res)
	}
	if f.a.record(t).RestartRequest == nil {
		t.Fatal("no restart was queued on A")
	}
	// Only unknown fields besides child: the own form.
	res, err := handleCancelRestart(ctx, json.RawMessage(`{"principal":"`+f.b.principal+`","session":"`+f.b.key+`"}`), nil)
	if err != nil || res.IsErr || !strings.Contains(res.Text, "cancelled the queued restart of "+f.a.key) {
		t.Fatalf("cancel-restart: %v %+v", err, res)
	}
	if f.a.record(t).RestartRequest != nil {
		t.Error("A's restart request survived the cancel")
	}
	f.wantBUntouched(t, before)
}

func TestBridgedWhoamiAndListHandlesAnswerForTheOriginsSession(t *testing.T) {
	f := newLiarFixture(t)
	ctx := bridgedCtxFor(f.a.key)

	res, err := handleWhoami(ctx, f.forgedArgs(), nil)
	if err != nil || res.IsErr {
		t.Fatalf("whoami: %v %+v", err, res)
	}
	for _, want := range []string{"principal: " + f.a.principal + "\n", "session_key: " + f.a.key + "\n"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("whoami lacks %q:\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "principal: "+f.b.principal) {
		t.Errorf("whoami answers for the victim:\n%s", res.Text)
	}
	// A holds a handle on B (and B's principal holds none): the listing is A's.
	res, err = handleListHandles(ctx, f.forgedArgs(), nil)
	if err != nil || res.IsErr || !strings.Contains(res.Text, "held    "+f.b.key) {
		t.Fatalf("list-handles is not session A's: %v %+v", err, res)
	}
}

// A bridged call whose origin names no frontend record is refused; it never
// falls back to the environment's principal.
func TestBridgedCallWithAnUnresolvableOriginIsRefusedNotFallenBack(t *testing.T) {
	f := newLiarFixture(t)
	before := f.b.fingerprint(t)
	for name, key := range map[string]string{
		"no such session":            "ghost/session",
		"empty key":                  "",
		"a worktree name, not a key": "a-branch",
	} {
		ctx := bridgedCtxFor(key)
		for tool, run := range map[string]func() (string, bool){
			"restart-this-session": func() (string, bool) { return runHandler(handleRestartThisSession)(ctx, json.RawMessage(`{}`)) },
			"close-this-session":   func() (string, bool) { return runHandler(handleCloseThisSession)(ctx, json.RawMessage(`{}`)) },
			"cancel-restart":       func() (string, bool) { return runHandler(handleCancelRestart)(ctx, json.RawMessage(`{}`)) },
			"whoami":               func() (string, bool) { return runHandler(handleWhoami)(ctx, json.RawMessage(`{}`)) },
			"list-handles":         func() (string, bool) { return runHandler(handleListHandles)(ctx, json.RawMessage(`{}`)) },
		} {
			t.Run(name+"/"+tool, func(t *testing.T) {
				text, isErr := run()
				if !isErr || !strings.HasPrefix(text, "refused: ") {
					t.Errorf("got isErr=%v %q, want a refused: text", isErr, text)
				}
				if strings.Contains(text, f.b.principal) || strings.Contains(text, f.b.key) {
					t.Errorf("the refusal names the victim: %q", text)
				}
			})
		}
	}
	if len(*f.handoff) != 0 {
		t.Errorf("a hand-off ran: %+v", *f.handoff)
	}
	f.wantBUntouched(t, before)
}

// A record that carries no principal (the SessionStart stamp is not there yet)
// has no principal to act as: refused, never the environment's.
func TestBridgedCallOnARecordWithNoPrincipalIsRefused(t *testing.T) {
	f := newLiarFixture(t)
	if err := updateLoadedSession(f.a.record(t), func(cur *session.State) bool {
		cur.Principal = ""
		return true
	}); err != nil {
		t.Fatal(err)
	}
	text, isErr := runHandler(handleRestartThisSession)(bridgedCtxFor(f.a.key), json.RawMessage(`{}`))
	if !isErr || !strings.HasPrefix(text, "refused: ") || !strings.Contains(text, "no principal") {
		t.Errorf("got isErr=%v %q, want a refusal naming the missing principal", isErr, text)
	}
	if len(*f.handoff) != 0 {
		t.Errorf("a hand-off ran: %+v", *f.handoff)
	}
}
