package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/hungstatus"
	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// khMintingRunner is the fake runner whose every mint gives a DIFFERENT key, as
// the real holder's does: a replaced pair has a new key.
type khMintingRunner struct {
	*khFakeRunner
	n int
}

func (r *khMintingRunner) Output(ctx context.Context, argv, env []string) ([]byte, error) {
	if len(argv) >= 3 && argv[1] == "holder" && argv[2] == "mint" {
		if r.mintErr != nil {
			return nil, r.mintErr
		}
		r.n++
		return []byte("ssh-ed25519 " + base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("blob-%d", r.n))) + " holder:reader\n"), nil
	}
	return r.khFakeRunner.Output(ctx, argv, env)
}

// khLoseThePair makes every unit the fake started read as not running, the way
// a host reboot or a dead agent unit does.
func khLoseThePair(r *khMintingRunner) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for u := range r.active {
		r.active[u] = false
	}
}

// khRemintEnv points the key holder at the fake runner and a short state base
// (sockets live under it) for every seam the re-mint paths read.
func khRemintEnv(t *testing.T) *khMintingRunner {
	t.Helper()
	dir, err := os.MkdirTemp("", "khs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	base := filepath.Join(dir, "kh")
	r := &khMintingRunner{khFakeRunner: newKhFake(t)}
	mk := func() *keyholder.Launcher {
		return &keyholder.Launcher{Base: base, Tools: keyholder.Tools{Fibby: "/pin/fibby", Piggy: "/pin/piggy"}, Run: r, Wait: time.Second}
	}
	t.Cleanup(keyholder.OverrideSSHPins("/pin/ssh-add", "/pin/ssh-keygen"))
	prevBase, prevL, prevS := keyholderStateBaseFn, keyholderLauncherFn, keyholderStatusLauncherFn
	keyholderStateBaseFn = func() string { return base }
	keyholderLauncherFn = func(keyholder.Tools) (*keyholder.Launcher, error) { return mk(), nil }
	keyholderStatusLauncherFn = mk
	t.Cleanup(func() { keyholderStateBaseFn, keyholderLauncherFn, keyholderStatusLauncherFn = prevBase, prevL, prevS })
	return r
}

// khSessionOn stages the worker/kid session (held and spawned by "spawner") with
// the key holder switched on and the restart hand-off configured.
func khSessionOn(t *testing.T) (repoPath string) {
	t.Helper()
	stubRestartHandoff(t, "", nil) // no real hand-off is exec'd
	repoPath, _ = restartSelfFixture(t, true)
	writeGlobalSweatfile(t, os.Getenv("HOME"), restartHandoffTOML+"[keyholder]\nmode = \"on\"\n")
	return repoPath
}

func khStart(t *testing.T, repoPath string, stderr *bytes.Buffer) {
	t.Helper()
	if _, _, err := keyholderSessionEnvUndo(os.Getenv("HOME"), repoPath, "worker/kid", stderr); err != nil {
		t.Fatalf("starting the pair: %v", err)
	}
}

func khFingerprints(t *testing.T, base string) string {
	t.Helper()
	b, err := os.ReadFile(keyholder.LastPath(base, "worker/kid"))
	if err != nil {
		t.Fatal(err)
	}
	var h struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal(b, &h); err != nil {
		t.Fatal(err)
	}
	return h.Fingerprint
}

func TestAReMintIsToldToTheOperatorAndWakesHoldersAndTheSpawnerWithBothFingerprints(t *testing.T) {
	r := khRemintEnv(t)
	notified := stubNotifyPrincipal(t)
	repoPath := khSessionOn(t)
	var first bytes.Buffer
	khStart(t, repoPath, &first)
	if first.Len() != 0 || len(*notified) != 0 {
		t.Fatalf("a first mint announced something: stderr %q, wakes %+v", first.String(), *notified)
	}
	oldFP := khFingerprints(t, keyholderStateBaseFn())

	khLoseThePair(r)
	var again bytes.Buffer
	khStart(t, repoPath, &again)
	newFP := khFingerprints(t, keyholderStateBaseFn())
	if oldFP == newFP {
		t.Fatal("the fake did not mint a different key")
	}
	for _, want := range []string{"worker/kid", oldFP, newFP, "NEW key", "sc session-key worker/kid"} {
		if !strings.Contains(again.String(), want) {
			t.Errorf("stderr %q lacks %q", again.String(), want)
		}
	}
	if len(*notified) != 1 || (*notified)[0].target != "spawner" {
		t.Fatalf("wakes = %+v, want one to the session's holder and spawner", *notified)
	}
	msg := (*notified)[0].message
	for _, want := range []string{"worker/kid", oldFP, newFP, "CHANGED"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the wake %q lacks %q", msg, want)
		}
	}
}

func TestReMintWakesAreRateLimitedAndTheNextOneCountsWhatItHeldBack(t *testing.T) {
	r := khRemintEnv(t)
	notified := stubNotifyPrincipal(t)
	repoPath := khSessionOn(t)
	t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	prev := keyholderNow
	t.Cleanup(func() { keyholderNow = prev })
	khStart(t, repoPath, &bytes.Buffer{})
	for i, at := range []time.Duration{0, time.Minute, keyholder.NoticeInterval + time.Minute} {
		keyholderNow = func() time.Time { return t0.Add(at) }
		khLoseThePair(r)
		khStart(t, repoPath, &bytes.Buffer{})
		wantWakes := map[int]int{0: 1, 1: 1, 2: 2}[i]
		if len(*notified) != wantWakes {
			t.Fatalf("after re-mint %d: %d wakes, want %d", i+1, len(*notified), wantWakes)
		}
	}
	if last := (*notified)[len(*notified)-1].message; !strings.Contains(last, "1 further re-mints") {
		t.Errorf("the wake after the interval %q does not count the one it held back", last)
	}
}

func TestAReMintWakeFailureNeverFailsTheLaunch(t *testing.T) {
	r := khRemintEnv(t)
	orig := notifyPrincipalFn
	notifyPrincipalFn = func(context.Context, string, string, string) error { return errors.New("ringmaster down") }
	t.Cleanup(func() { notifyPrincipalFn = orig })
	repoPath := khSessionOn(t)
	khStart(t, repoPath, &bytes.Buffer{})
	khLoseThePair(r)
	khStart(t, repoPath, &bytes.Buffer{}) // khStart fails the test on an error
}

func TestKeyholderNoteSaysDeadThenReMintedUntilACertificateIsInstalled(t *testing.T) {
	r := khRemintEnv(t)
	stubNotifyPrincipal(t)
	repoPath := khSessionOn(t)
	if n := keyholderNote("worker/kid", true); n != "" {
		t.Fatalf("no pair yet: note %q", n)
	}
	khStart(t, repoPath, &bytes.Buffer{})
	if n := keyholderNote("worker/kid", false); n != "" {
		t.Errorf("a healthy pair is quiet in `sc list`: %q", n)
	}
	if n := keyholderNote("worker/kid", true); !strings.Contains(n, "ok") {
		t.Errorf("session-health says a healthy pair is ok: %q", n)
	}
	khLoseThePair(r)
	if n := keyholderNote("worker/kid", false); !strings.Contains(n, "DEAD") {
		t.Errorf("a lost pair note = %q, want DEAD", n)
	}
	khStart(t, repoPath, &bytes.Buffer{})
	n := keyholderNote("worker/kid", false)
	if !strings.Contains(n, "RE-MINTED") || !strings.Contains(n, "sc session-key") {
		t.Fatalf("a re-minted pair note = %q, want RE-MINTED and the way out", n)
	}
	cert := keyholder.CertPath(keyholderStateBaseFn(), "worker/kid")
	if err := os.WriteFile(cert, []byte("ssh-ed25519-cert-v01@openssh.com AAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := keyholderNote("worker/kid", false); n != "" {
		t.Errorf("with a certificate installed the note is %q, want none", n)
	}
}

func TestScListShowsTheKeyholderNoteInTextAndJSON(t *testing.T) {
	r := khRemintEnv(t)
	stubNotifyPrincipal(t)
	repoPath := khSessionOn(t)
	khStart(t, repoPath, &bytes.Buffer{})
	khLoseThePair(r)
	// keyholderNoteFn is what `sc list` calls; with the pair lost it is DEAD.
	res, err := runListResult(context.Background(), false, "json", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var rows []session.ListRow
	if err := json.Unmarshal([]byte(res.Text), &rows); err != nil {
		t.Fatalf("list json: %v\n%s", err, res.Text)
	}
	var got string
	for _, row := range rows {
		if row.SessionKey == "worker/kid" {
			got = row.KeyHolder
		}
	}
	if !strings.Contains(got, "DEAD") {
		t.Fatalf("the json row's keyholder = %q, want the DEAD note", got)
	}
	txt, err := runListResult(context.Background(), false, "text", nil, nil)
	if err != nil || !strings.Contains(txt.Text, "keyholder: DEAD") {
		t.Errorf("the text listing lacks the note: %v\n%s", err, txt.Text)
	}
}

func TestSessionHealthShowsTheKeyholderNote(t *testing.T) {
	r := khRemintEnv(t)
	stubNotifyPrincipal(t)
	repoPath := khSessionOn(t)
	khStart(t, repoPath, &bytes.Buffer{})
	d := fakeHealthDeps(&hungstatus.Status{SessionKey: "worker/kid"}, hungstatus.FrontendLive, false)
	d.KeyholderNote = productionHealthDeps().KeyholderNote
	out, err := runSessionHealthWith(sessionHealthParams{}, "spawner", d)
	if err != nil || !strings.Contains(out, "keyholder: ok") {
		t.Fatalf("session-health = %q, %v; want the pair reported ok", out, err)
	}
	khLoseThePair(r)
	out, err = runSessionHealthWith(sessionHealthParams{}, "spawner", d)
	if err != nil || !strings.Contains(out, "keyholder: DEAD") {
		t.Fatalf("session-health after the loss = %q, %v; want DEAD", out, err)
	}
}

// The restart paths: clown relaunches without `sc resume`, so the restart
// preflight (which every queue path ends in: restart-this-session,
// restart-child-session, `sc restart`, restart-hung-child) and its --rearm are
// where a dead pair has to be noticed.
func TestRestartPreflightRestartsADeadPairBeforeTheRelaunchAndAnnouncesTheNewKey(t *testing.T) {
	stubScheduleRestartWatch(t, nil)
	notified := stubNotifyPrincipal(t)
	r := khRemintEnv(t)
	repoPath := khSessionOn(t)
	khStart(t, repoPath, &bytes.Buffer{})
	oldFP := khFingerprints(t, keyholderStateBaseFn())
	if text, isErr := callRestartThis(t, `{"reason":"why"}`); isErr {
		t.Fatalf("queueing the restart: %s", text)
	}
	khLoseThePair(r)
	startsBefore := len(r.started)

	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "restart" {
		t.Fatalf("preflight = %+v, %v; want restart", v, err)
	}
	if len(r.started) != startsBefore+2 {
		t.Errorf("units started during the preflight: %d, want the pair (2) restarted", len(r.started)-startsBefore)
	}
	newFP := khFingerprints(t, keyholderStateBaseFn())
	if newFP == oldFP {
		t.Fatal("the restarted pair kept the old key")
	}
	var told bool
	for _, c := range *notified {
		if strings.Contains(c.message, oldFP) && strings.Contains(c.message, newFP) {
			told = true
		}
	}
	if !told {
		t.Errorf("no wake carried the old and the new fingerprint: %+v", *notified)
	}
}

func TestRestartPreflightRefusesARestartWhoseDeadPairCannotBeStarted(t *testing.T) {
	stubScheduleRestartWatch(t, nil)
	stubNotifyPrincipal(t)
	r := khRemintEnv(t)
	repoPath := khSessionOn(t)
	khStart(t, repoPath, &bytes.Buffer{})
	if text, isErr := callRestartThis(t, `{"reason":"why"}`); isErr {
		t.Fatalf("queueing the restart: %s", text)
	}
	khLoseThePair(r)
	r.mintErr = errors.New("holder refused")

	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "refused" || !strings.Contains(v.Reason, "key holder") || !strings.Contains(v.Reason, "holder refused") {
		t.Fatalf("preflight = %+v, %v; want a refusal naming the key holder and the cause", v, err)
	}
	if readKid(t, repoPath).RestartRequest != nil {
		t.Error("a refused restart must clear the request")
	}
	if readKid(t, repoPath).RestartingAt != nil {
		t.Error("a refused restart must not mark the session restarting")
	}
}

func TestRestartPreflightLeavesASessionThatNeverHadAPairAlone(t *testing.T) {
	stubScheduleRestartWatch(t, nil)
	stubNotifyPrincipal(t)
	r := khRemintEnv(t)
	khSessionOn(t)
	if text, isErr := callRestartThis(t, `{"reason":"why"}`); isErr {
		t.Fatalf("queueing the restart: %s", text)
	}
	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "restart" {
		t.Fatalf("preflight = %+v, %v; want restart", v, err)
	}
	if len(r.started) != 0 {
		t.Errorf("a session that never had a pair got units started: %v", r.started)
	}
}

func TestRestartRearmChecksThePairToo(t *testing.T) {
	r := khRemintEnv(t)
	stubScheduleRestartWatch(t, nil)
	stubNotifyPrincipal(t)
	repoPath := khSessionOn(t)
	khStart(t, repoPath, &bytes.Buffer{})
	if text, isErr := callRestartThis(t, `{"reason":"why"}`); isErr {
		t.Fatalf("queueing the restart: %s", text)
	}
	if v, err := runRestartPreflight("worker/kid"); err != nil || v.Verdict != "restart" {
		t.Fatalf("setup preflight = %+v, %v", v, err)
	}
	khLoseThePair(r)
	startsBefore := len(r.started)
	v, err := runRestartRearm("worker/kid")
	if err != nil || v.Verdict != "restart" {
		t.Fatalf("rearm = %+v, %v; want restart", v, err)
	}
	if len(r.started) != startsBefore+2 {
		t.Errorf("units started during the re-arm: %d, want the pair (2) restarted", len(r.started)-startsBefore)
	}

	khLoseThePair(r)
	r.mintErr = errors.New("holder refused")
	if v, err := runRestartRearm("worker/kid"); err != nil || v.Verdict != "refused" || !strings.Contains(v.Reason, "key holder") {
		t.Fatalf("rearm with an unstartable pair = %+v, %v; want a refusal naming the key holder", v, err)
	}
}
