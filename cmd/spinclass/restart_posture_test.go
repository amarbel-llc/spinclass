package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// spinclass#342: a restart can ask for a permission posture. Narrowing to
// unattended is free; widening to attended needs the restart right on a child,
// a trusted-layer allow-posture-widening, and is refused for the session
// itself and for a confined session; the preflight re-checks it and records the
// posture in the pinned entry in the same locked update that consumes the
// request.

const restartPostureHandoffTOML = "[session-entry]\n" +
	`restart = ["clown", "restart", "--session", "{principal}", "--when", "{when}", "--posture", "{posture}", "--reason", "{reason}"]` + "\n"

const allowWideningTOML = "allow-posture-widening = true\n"

// harnessEntry is a pinned resume entry a dontAsk marker means something for.
func harnessEntry(env map[string]string) *session.PinnedEntry {
	return &session.PinnedEntry{
		Start:  []string{"/bin/sh"},
		Resume: []string{"clown", "--", "--resume", "{principal}"},
		Env:    env,
	}
}

// restartPostureChild stages worker/kid with a pinned harness entry, held by
// "holder" with the restart right, and the hand-off that carries {posture}.
// widen adds the trusted allow-posture-widening key to the global sweatfile.
func restartPostureChild(t *testing.T, entry *session.PinnedEntry, widen bool) (repoPath, wtPath string) {
	t.Helper()
	repoPath, wtPath = restartChildFixture(t, "holder", session.State{
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

func postureArgv(posture, reason string) []string {
	return []string{"clown", "restart", "--session", childPrincipal, "--when", "idle", "--posture", posture, "--reason", reason}
}

// Narrowing is free: no key, no ask beyond the restart right. The request
// records the posture and the hand-off carries it.
func TestRestartChildNarrowsToUnattendedWithNoKey(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, _ := restartPostureChild(t, harnessEntry(nil), false)

	text, isErr := callRestartChild(t, `{"child":"worker/kid","posture":"unattended","reason":"go quiet"}`)
	if isErr {
		t.Fatalf("narrowing must be allowed: %q", text)
	}
	if req := readKid(t, repoPath).RestartRequest; req == nil || req.Posture != "unattended" {
		t.Fatalf("restart request = %+v, want posture unattended", req)
	}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].argv, postureArgv("unattended", "go quiet")) {
		t.Fatalf("hand-off calls = %+v, want one with argv %q", *calls, postureArgv("unattended", "go quiet"))
	}
}

// No posture, "keep" and "" expand {posture} to keep and record nothing.
func TestRestartWithoutAPostureHandsOffKeepAndRecordsNone(t *testing.T) {
	for _, args := range []string{`{"child":"worker/kid"}`, `{"child":"worker/kid","posture":"keep"}`, `{"child":"worker/kid","posture":""}`} {
		calls := stubRestartHandoff(t, "", nil)
		repoPath, _ := restartPostureChild(t, harnessEntry(nil), false)
		if text, isErr := callRestartChild(t, args); isErr {
			t.Fatalf("%s refused: %q", args, text)
		}
		if req := readKid(t, repoPath).RestartRequest; req == nil || req.Posture != "" {
			t.Errorf("%s: request = %+v, want no posture", args, req)
		}
		if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].argv, postureArgv("keep", "requested by a holder")) {
			t.Errorf("%s: hand-off argv = %+v, want keep", args, *calls)
		}
	}
}

func TestRestartRefusesAnUnknownPosture(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, _ := restartPostureChild(t, harnessEntry(nil), true)
	text, isErr := callRestartChild(t, `{"child":"worker/kid","posture":"wide-open"}`)
	if !isErr || !strings.Contains(text, "unattended, attended or keep") {
		t.Fatalf("want a refusal naming the postures, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
		t.Error("nothing may be recorded or run")
	}
}

// Widening without the trusted key is refused with the reason, even for a
// holder with the right.
func TestRestartChildWideningNeedsTheTrustedKey(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, _ := restartPostureChild(t, harnessEntry(map[string]string{"CLOWN_UNATTENDED": "1"}), false)
	text, isErr := callRestartChild(t, `{"child":"worker/kid","posture":"attended"}`)
	if !isErr || !strings.Contains(text, "allow-posture-widening") {
		t.Fatalf("want a refusal naming the key, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
		t.Error("nothing may be recorded or run")
	}
}

// The key counts only from a layer above the repo: a repo-level sweatfile that
// sets it is ignored.
func TestRestartChildWideningIgnoresARepoLevelKey(t *testing.T) {
	calls := stubRestartHandoff(t, "", nil)
	repoPath, _ := restartPostureChild(t, harnessEntry(map[string]string{"CLOWN_UNATTENDED": "1"}), false)
	writeSweatfile(t, repoPath, "[session-entry]\n"+allowWideningTOML)
	text, isErr := callRestartChild(t, `{"child":"worker/kid","posture":"attended"}`)
	if !isErr || !strings.Contains(text, "allow-posture-widening") {
		t.Fatalf("a repo-level key must not allow widening: isErr=%v %q", isErr, text)
	}
	if len(*calls) != 0 {
		t.Error("no hand-off may run")
	}
}

// With the key, a holder with the restart right may widen; the preflight
// removes the marker from the pinned entry, prints the posture and records it.
func TestRestartChildWideningWithTheKeyThroughThePreflight(t *testing.T) {
	stubNotifyPrincipal(t)
	calls := stubRestartHandoff(t, "", nil)
	repoPath, _ := restartPostureChild(t, harnessEntry(map[string]string{"CLOWN_UNATTENDED": "1", "KEEP": "me"}), true)

	if text, isErr := callRestartChild(t, `{"child":"worker/kid","posture":"attended"}`); isErr {
		t.Fatalf("a keyed widening by a holder must be allowed: %q", text)
	}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].argv, postureArgv("attended", "requested by a holder")) {
		t.Fatalf("hand-off calls = %+v", *calls)
	}

	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "restart" || v.Posture != "attended" {
		t.Fatalf("verdict = %+v, %v; want restart with posture attended", v, err)
	}
	st := readKid(t, repoPath)
	if _, has := st.Entry.Env["CLOWN_UNATTENDED"]; has || st.Entry.Env["KEEP"] != "me" {
		t.Errorf("pinned env = %v; want the marker gone and the rest kept", st.Entry.Env)
	}
	if st.RestartingPosture != "attended" {
		t.Errorf("RestartingPosture = %q", st.RestartingPosture)
	}
	if v.Mode != "auto" {
		t.Errorf("verdict mode = %q, want auto: the mode an attended start gets", v.Mode)
	}
}

// The attended verdict names the mode an attended START gets (spinclass#476):
// auto, or acceptEdits under disable-auto-mode, whatever the pinned entry is
// (a spawned worker's shell, an sc-started session's shell, or a harness). The
// rearm re-emits it, and no other posture carries a mode.
func TestRestartAttendedVerdictCarriesTheAttendedStartMode(t *testing.T) {
	entries := map[string]*session.PinnedEntry{
		"shell pin (sc start / spawn)": {Start: []string{"/bin/fish"}, Env: map[string]string{"CLOWN_UNATTENDED": "1"}},
		"harness pin":                  harnessEntry(map[string]string{"CLOWN_UNATTENDED": "1"}),
	}
	for name, entry := range entries {
		for _, tc := range []struct{ extra, want string }{
			{"", "auto"},
			{"disable-auto-mode = true\n", "acceptEdits"},
		} {
			t.Run(name+"/"+tc.want, func(t *testing.T) {
				stubNotifyPrincipal(t)
				stubRestartHandoff(t, "", nil)
				restartPostureChild(t, entry, true)
				writeGlobalSweatfile(t, os.Getenv("HOME"), restartPostureHandoffTOML+allowWideningTOML+tc.extra)
				if text, isErr := callRestartChild(t, `{"child":"worker/kid","posture":"attended"}`); isErr {
					t.Fatalf("refused: %q", text)
				}
				v, err := runRestartPreflight("worker/kid")
				if err != nil || v.Posture != "attended" || v.Mode != tc.want {
					t.Fatalf("verdict = %+v, %v; want attended with mode %s", v, err, tc.want)
				}
				var buf bytes.Buffer
				if err := printRestartVerdict(&buf, v); err != nil || !strings.Contains(buf.String(), `"mode":"`+tc.want+`"`) {
					t.Errorf("verdict line = %q, want the mode key (%v)", buf.String(), err)
				}
				rv, err := runRestartRearm("worker/kid")
				if err != nil || rv.Mode != tc.want {
					t.Errorf("rearm verdict = %+v, %v; want the same mode", rv, err)
				}
			})
		}
	}

	stubNotifyPrincipal(t)
	stubRestartHandoff(t, "", nil)
	restartPostureChild(t, harnessEntry(nil), false)
	if text, isErr := callRestartChild(t, `{"child":"worker/kid","posture":"unattended"}`); isErr {
		t.Fatalf("refused: %q", text)
	}
	if v, err := runRestartPreflight("worker/kid"); err != nil || v.Mode != "" {
		t.Errorf("unattended verdict = %+v, %v; want no mode", v, err)
	}
}

// Narrowing through the preflight sets the marker, prints the posture, and
// --rearm re-emits it.
func TestRestartPreflightNarrowingSetsTheMarkerAndRearmReEmitsIt(t *testing.T) {
	stubNotifyPrincipal(t)
	stubRestartHandoff(t, "", nil)
	repoPath, _ := restartPostureChild(t, harnessEntry(map[string]string{"KEEP": "me"}), false)
	if text, isErr := callRestartChild(t, `{"child":"worker/kid","posture":"unattended"}`); isErr {
		t.Fatalf("refused: %q", text)
	}

	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "restart" || v.Posture != "unattended" {
		t.Fatalf("verdict = %+v, %v; want restart with posture unattended", v, err)
	}
	st := readKid(t, repoPath)
	if st.Entry.Env["CLOWN_UNATTENDED"] != "1" || st.Entry.Env["KEEP"] != "me" {
		t.Errorf("pinned env = %v; want the marker set and the rest kept", st.Entry.Env)
	}

	rv, err := runRestartRearm("worker/kid")
	if err != nil || rv.Verdict != "restart" || rv.Posture != "unattended" {
		t.Fatalf("rearm verdict = %+v, %v; want restart re-emitting unattended", rv, err)
	}
}

// A verdict with no posture prints no posture key and leaves the entry alone.
func TestRestartPreflightWithoutAPostureLeavesTheEntryAndTheJSONAlone(t *testing.T) {
	stubNotifyPrincipal(t)
	stubRestartHandoff(t, "", nil)
	env := map[string]string{"CLOWN_UNATTENDED": "1"}
	repoPath, _ := restartPostureChild(t, harnessEntry(env), false)
	if text, isErr := callRestartChild(t, `{"child":"worker/kid"}`); isErr {
		t.Fatalf("refused: %q", text)
	}
	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "restart" || v.Posture != "" {
		t.Fatalf("verdict = %+v, %v", v, err)
	}
	var buf bytes.Buffer
	if err := printRestartVerdict(&buf, v); err != nil || strings.Contains(buf.String(), "posture") {
		t.Errorf("verdict line = %q, want no posture key (%v)", buf.String(), err)
	}
	if got := readKid(t, repoPath).Entry.Env; !reflect.DeepEqual(got, env) {
		t.Errorf("pinned env = %v, want unchanged %v", got, env)
	}
}

// A session may not widen itself, key or no key; it may narrow itself.
func TestRestartThisSessionMayNarrowButNotWiden(t *testing.T) {
	stubNotifyPrincipal(t)
	calls := stubRestartHandoff(t, "", nil)
	repoPath, _ := restartSelfFixture(t, true)
	pinSelfEntry(t, repoPath, harnessEntry(map[string]string{"CLOWN_UNATTENDED": "1"}))
	writeGlobalSweatfile(t, os.Getenv("HOME"), restartPostureHandoffTOML+allowWideningTOML)

	text, isErr := callRestartThis(t, `{"posture":"attended"}`)
	if !isErr || !strings.Contains(text, "may not widen its own") {
		t.Fatalf("want a refusal that a session may not widen itself, got isErr=%v %q", isErr, text)
	}
	if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
		t.Error("nothing may be recorded or run")
	}

	if text, isErr := callRestartThis(t, `{"posture":"unattended"}`); isErr {
		t.Fatalf("a session may narrow itself: %q", text)
	}
	if req := readKid(t, repoPath).RestartRequest; req == nil || req.Posture != "unattended" {
		t.Errorf("request = %+v, want posture unattended", req)
	}
}

func pinSelfEntry(t *testing.T, repoPath string, entry *session.PinnedEntry) {
	t.Helper()
	st := readKid(t, repoPath)
	st.Entry = entry
	if err := session.Write(*st); err != nil {
		t.Fatal(err)
	}
}

// A session launched confined is refused for every posture.
func TestRestartPostureRefusesAConfinedSession(t *testing.T) {
	for _, posture := range []string{"unattended", "attended"} {
		calls := stubRestartHandoff(t, "", nil)
		repoPath, wtPath := restartPostureChild(t, harnessEntry(nil), true)
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
		text, isErr := callRestartChild(t, `{"child":"worker/kid","posture":"`+posture+`"}`)
		if !isErr || !strings.Contains(text, "confine") {
			t.Fatalf("%s: want a confinement refusal, got isErr=%v %q", posture, isErr, text)
		}
		if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
			t.Errorf("%s: nothing may be recorded or run", posture)
		}
	}
}

// A session with no pinned entry, or one whose entry names a harness that is
// not claude, cannot be restarted into a posture.
func TestRestartPostureRefusesAnEntryItCannotRecordIn(t *testing.T) {
	cases := map[string]*session.PinnedEntry{
		"no pinned entry": nil,
		"a non-claude provider": {
			Start:  []string{"/bin/sh"},
			Resume: []string{"clown", "--provider", "codex", "--", "--resume", "{principal}"},
		},
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			calls := stubRestartHandoff(t, "", nil)
			repoPath, _ := restartPostureChild(t, entry, true)
			text, isErr := callRestartChild(t, `{"child":"worker/kid","posture":"unattended"}`)
			if !isErr {
				t.Fatalf("want a refusal, got %q", text)
			}
			if readKid(t, repoPath).RestartRequest != nil || len(*calls) != 0 {
				t.Error("nothing may be recorded or run")
			}
		})
	}
}

// `sc start`, `sc resume` and a spawn all pin the interactive [session-entry]
// start (a shell such as fish), which later runs clown: the entry never names
// the harness (spinclass#476). Narrowing such a session to unattended must be
// accepted by the request and the preflight, since the frontend gates the
// posture against the harness it actually runs; the pinned env carries the
// marker for the next `sc resume`. Both a start-only and a start+resume shell
// pin are the shape.
func TestRestartNarrowsASessionPinnedWithAShellEntry(t *testing.T) {
	for name, entry := range map[string]*session.PinnedEntry{
		"start only":     {Start: []string{"/nix/store/x-fish-4.9.3/bin/fish"}},
		"start + resume": {Start: []string{"/bin/fish"}, Resume: []string{"/bin/fish", "-c", "clown --resume"}},
	} {
		t.Run(name, func(t *testing.T) {
			calls := stubRestartHandoff(t, "", nil)
			repoPath, _ := restartPostureChild(t, entry, false)
			text, isErr := callRestartChild(t, `{"child":"worker/kid","posture":"unattended"}`)
			if isErr {
				t.Fatalf("refused: %s", text)
			}
			if len(*calls) != 1 {
				t.Fatalf("hand-offs = %d, want 1", len(*calls))
			}
			v, err := runRestartPreflight("worker/kid")
			if err != nil || v.Verdict != "restart" || v.Posture != "unattended" {
				t.Fatalf("preflight = %+v, %v", v, err)
			}
			if st := readKid(t, repoPath); st.Entry.Env["CLOWN_UNATTENDED"] != "1" {
				t.Errorf("pinned env = %v, want the marker recorded", st.Entry.Env)
			}
		})
	}
}

// The request is state the session can write, so the preflight re-checks a
// widening: a forged attended request is refused when the key is off, when the
// requester is the session itself, or when the requester no longer holds the
// restart right, and the pinned entry is left alone.
func TestRestartPreflightRefusesAForgedWidening(t *testing.T) {
	forge := func(t *testing.T, widen bool, requester string, rights string) (repoPath string) {
		t.Helper()
		stubNotifyPrincipal(t)
		stubRestartHandoff(t, "", nil)
		repoPath, _ = restartChildFixture(t, "holder", session.State{
			Holders:      []string{"holder"},
			HandleRights: map[string]string{"holder": rights},
			Entry:        harnessEntry(map[string]string{"CLOWN_UNATTENDED": "1"}),
		})
		body := restartPostureHandoffTOML
		if widen {
			body += allowWideningTOML
		}
		writeGlobalSweatfile(t, os.Getenv("HOME"), body)
		st := readKid(t, repoPath)
		st.RestartRequest = &session.RestartRequest{
			RequestedAt: time.Now().UTC(), Requester: requester, Reason: "forged", When: "idle", Posture: "attended",
		}
		if err := session.Write(*st); err != nil {
			t.Fatal(err)
		}
		return repoPath
	}
	for name, tc := range map[string]struct {
		widen     bool
		requester string
		rights    string
	}{
		"the key is off":               {false, "holder", "observe,restart"},
		"the requester is the session": {true, childPrincipal, "observe,restart"},
		"the requester lost the right": {true, "holder", "observe,close"},
	} {
		t.Run(name, func(t *testing.T) {
			repoPath := forge(t, tc.widen, tc.requester, tc.rights)
			v, err := runRestartPreflight("worker/kid")
			if err != nil || v.Verdict != "refused" {
				t.Fatalf("verdict = %+v, %v; want refused", v, err)
			}
			st := readKid(t, repoPath)
			if st.Entry.Env["CLOWN_UNATTENDED"] != "1" || st.RestartRequest != nil || st.RestartingAt != nil {
				t.Errorf("a refused widening must leave the entry as it was, consume the request and not mark restarting: %+v", st)
			}
		})
	}
}

// cancel-restart drops the posture with the request.
func TestCancelRestartDropsThePosture(t *testing.T) {
	stubNotifyPrincipal(t)
	stubRestartHandoff(t, "", nil)
	repoPath, _ := restartPostureChild(t, harnessEntry(nil), false)
	if text, isErr := callRestartChild(t, `{"child":"worker/kid","posture":"unattended"}`); isErr {
		t.Fatalf("refused: %q", text)
	}
	if _, err := runCancelRestart(cancelRestartParams{Child: "worker/kid"}); err != nil {
		t.Fatal(err)
	}
	if readKid(t, repoPath).RestartRequest != nil {
		t.Error("the request, posture included, must be gone")
	}
	v, _ := runRestartPreflight("worker/kid")
	if v.Verdict != "none" || v.Posture != "" {
		t.Errorf("verdict after cancel = %+v, want none", v)
	}
}

func paramNames(params []command.Param) []string {
	var names []string
	for _, p := range params {
		names = append(names, p.Name)
	}
	return names
}

// The restart tools declare the posture parameter.
func TestRestartToolsDeclarePosture(t *testing.T) {
	for name, params := range map[string][]string{
		"restart-this-session":  paramNames(restartThisSessionParamList()),
		"restart-child-session": paramNames(restartChildSessionParamList()),
	} {
		found := false
		for _, p := range params {
			found = found || p == "posture"
		}
		if !found {
			t.Errorf("%s lacks a posture parameter: %v", name, params)
		}
	}
	// And the JSON the tools accept round-trips the field.
	var p restartChildParams
	if err := json.Unmarshal([]byte(`{"child":"a/b","posture":"unattended"}`), &p); err != nil || p.Posture != "unattended" {
		t.Errorf("params = %+v, %v", p, err)
	}
}
