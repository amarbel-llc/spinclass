package main

// The evidence rule for the verbs that hold an escalation floor
// (restart-this-session, cancel-restart, stop-this-session): a caller with no
// bridge origin is refused only on EVIDENCE it is confined (a confined record,
// or a record that exists and cannot be read), never for want of evidence. These
// tests put the REAL classifier back and put real files in a throwaway state
// directory: nothing here stubs the classification to "confined".

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// withRealEvidenceClassifier puts the production classifier back for the test.
func withRealEvidenceClassifier(t *testing.T) {
	t.Helper()
	original := serveCallerEvidence
	t.Cleanup(func() { serveCallerEvidence = original })
	serveCallerEvidence = classifyServeCallerEvidence
}

// refusedByTheClassification says a handler's answer is the refusal the
// classification produces (the bridge's contract text, the older route's text
// naming the classifier's reason, or the displacement / cancel refusals), as
// opposed to an answer that came from the verb itself.
func refusedByTheClassification(text string, isErr bool) bool {
	if !isErr {
		return false
	}
	return text == refusalRestartEscalation ||
		strings.Contains(text, "classifies as confined") ||
		strings.Contains(text, "cannot replace this session's queued") ||
		strings.HasPrefix(text, "refused: ")
}

// evidenceCall is one handler call the classification can refuse.
type evidenceCall struct {
	name string
	// needsState: the call stages a queued request first, so it needs the
	// frontend state directory to exist.
	needsState bool
	run        func(t *testing.T, f liarFixture, ctx context.Context) (string, bool)
}

func evidenceCalls() []evidenceCall {
	restart := func(args string) func(*testing.T, liarFixture, context.Context) (string, bool) {
		return func(t *testing.T, f liarFixture, ctx context.Context) (string, bool) {
			return runHandler(handleRestartThisSession)(ctx, json.RawMessage(args))
		}
	}
	return []evidenceCall{
		{"restart force", false, restart(`{"force":true,"reason":"wedged"}`)},
		{"restart fresh_env", false, restart(`{"fresh_env":true}`)},
		{"restart posture", false, restart(`{"posture":"attended"}`)},
		{"restart displacing another party's request", true, func(t *testing.T, f liarFixture, ctx context.Context) (string, bool) {
			queueRestartFor(t, f.b, session.OperatorRestartRequester)
			return runHandler(handleRestartThisSession)(ctx, json.RawMessage(`{"reason":"mine"}`))
		}},
		{"cancel-restart of another party's request", true, func(t *testing.T, f liarFixture, ctx context.Context) (string, bool) {
			queueRestartFor(t, f.b, session.OperatorRestartRequester)
			return runHandler(handleCancelRestart)(ctx, json.RawMessage(`{}`))
		}},
		{"stop displacing another party's request", true, func(t *testing.T, f liarFixture, ctx context.Context) (string, bool) {
			queueRestartFor(t, f.b, session.OperatorRestartRequester)
			return runHandler(handleStopThisSession)(ctx, json.RawMessage(`{"reason":"mine"}`))
		}},
	}
}

func writeStateFile(t *testing.T, rel, content string) string {
	t.Helper()
	path := filepath.Join(os.Getenv("XDG_STATE_HOME"), "spinclass", rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// evidenceCases are the states of the frontend's records, from the stated rule.
// refused is what the rule says for every call; skip names calls a case cannot
// stage.
type evidenceCase struct {
	name    string
	refused bool
	arrange func(t *testing.T, f liarFixture)
	// bridged: the call carries a bridge origin.
	bridged bool
	// noState: the state directory itself is unresolvable.
	noState bool
}

func evidenceCases() []evidenceCase {
	return []evidenceCase{
		{name: "(i) a bridge origin", refused: true, bridged: true, arrange: func(*testing.T, liarFixture) {}},
		{name: "(ii) a unit record names the principal", refused: true, arrange: func(t *testing.T, f liarFixture) {
			writeStateFile(t, "units/u.json", fmt.Sprintf(`{"principal":%q}`, f.b.principal))
		}},
		{name: "(ii) the principal mapping names a confined unit", refused: true, arrange: func(t *testing.T, f liarFixture) {
			if err := os.Remove(confinement.PrincipalMappingPath(os.Getenv("XDG_STATE_HOME"), f.b.principal)); err != nil {
				t.Fatal(err)
			}
			writeStateFile(t, "principals/"+f.b.principal+".json",
				fmt.Sprintf(`{"version":1,"principal":%q,"session_key":%q,"worktree":%q,"unit":"clown-agent-x.service"}`, f.b.principal, f.b.key, f.b.wt))
		}},
		// THE WINDOW: spinclass has written the confined launch spec, clown has
		// written no unit record and no principal mapping names a confined unit.
		// (noState: with no mapping the queueing verbs cannot find the session at all
		// and refuse for that reason, which is not the classification's.)
		{name: "(window) launch spec written, no unit record, no mapping", refused: true, noState: true, arrange: func(t *testing.T, f liarFixture) {
			if err := os.Remove(confinement.PrincipalMappingPath(os.Getenv("XDG_STATE_HOME"), f.b.principal)); err != nil {
				t.Fatal(err)
			}
			writeAt(t, os.Getenv("XDG_STATE_HOME"), "units/"+filepath.Base(confinement.UnitSpecPath(os.Getenv("XDG_STATE_HOME"), f.b.wt)), `{"version":1}`)
		}},
		{name: "(window) launch spec written while the mapping still says unconfined", refused: true, arrange: func(t *testing.T, f liarFixture) {
			writeAt(t, os.Getenv("XDG_STATE_HOME"), "units/"+filepath.Base(confinement.UnitSpecPath(os.Getenv("XDG_STATE_HOME"), f.b.wt)), `{"version":1}`)
		}},
		{name: "(iii) a unit record that does not parse", refused: true, arrange: func(t *testing.T, f liarFixture) {
			writeStateFile(t, "units/bad.json", "{not json")
		}},
		{name: "(iii) a directory where a unit record should be", refused: true, arrange: func(t *testing.T, f liarFixture) {
			writeStateFile(t, "units/keep.json", `{"principal":"someone-else"}`)
			if err := os.Mkdir(filepath.Join(os.Getenv("XDG_STATE_HOME"), "spinclass", "units", "dir.json"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "(iii) a serve record of the principal that does not parse", refused: true, arrange: func(t *testing.T, f liarFixture) {
			writeStateFile(t, fmt.Sprintf("serves/%s-1.json", f.b.principal), "{not json")
		}},
		{name: "(iv) no record at all", refused: false, arrange: func(t *testing.T, f liarFixture) {
			if err := os.Remove(confinement.PrincipalMappingPath(os.Getenv("XDG_STATE_HOME"), f.b.principal)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "(iv) the principal is unresolved", refused: false, arrange: func(t *testing.T, f liarFixture) {
			t.Setenv("CLOWN_SESSION_ID", "")
		}},
		{name: "(iv) the state directory is unresolved", refused: false, noState: true, arrange: func(t *testing.T, f liarFixture) {
			t.Setenv("XDG_STATE_HOME", "")
			t.Setenv("HOME", "")
		}},
		{name: "(iv) no serve record for the principal", refused: false, arrange: func(t *testing.T, f liarFixture) {
			writeStateFile(t, "serves/some-other-principal-1.json", `{"principal":"some-other-principal","pid":1}`)
		}},
		{name: "(v) two live serves of the principal, none confined", refused: false, arrange: func(t *testing.T, f liarFixture) {
			for _, pid := range []int{os.Getpid(), os.Getppid()} {
				writeStateFile(t, fmt.Sprintf("serves/%s-%d.json", f.b.principal, pid),
					fmt.Sprintf(`{"principal":%q,"pid":%d,"ppid":1,"started":"2026-01-01T00:00:00Z"}`, f.b.principal, pid))
			}
		}},
		{name: "(v) two live serves of the principal, a unit record confined", refused: true, arrange: func(t *testing.T, f liarFixture) {
			for _, pid := range []int{os.Getpid(), os.Getppid()} {
				writeStateFile(t, fmt.Sprintf("serves/%s-%d.json", f.b.principal, pid),
					fmt.Sprintf(`{"principal":%q,"pid":%d,"ppid":1,"started":"2026-01-01T00:00:00Z"}`, f.b.principal, pid))
			}
			writeStateFile(t, "units/u.json", fmt.Sprintf(`{"principal":%q}`, f.b.principal))
		}},
	}
}

func TestConfinedCallEvidenceRuleForTheSessionVerbs(t *testing.T) {
	for _, c := range evidenceCases() {
		for _, call := range evidenceCalls() {
			if c.noState && call.needsState {
				continue // a queued request needs the state directory this case removes
			}
			t.Run(c.name+"/"+call.name, func(t *testing.T) {
				stubNotifyPrincipal(t)
				f := newLiarFixture(t)
				withRealEvidenceClassifier(t)
				c.arrange(t, f)
				ctx := context.Background()
				if c.bridged {
					ctx = bridgedCtxFor(f.b.key)
				}
				text, isErr := call.run(t, f, ctx)
				got := refusedByTheClassification(text, isErr)
				if got != c.refused {
					t.Errorf("%s: refused by the classification = %v, want %v (isErr=%v) %q", c.name, got, c.refused, isErr, text)
				}
			})
		}
	}
}

// close-this-session does not classify a call without an origin: the
// classification there is the bridge origin alone, and an absent or unreadable
// record changes nothing for a stdio caller. Over the bridge it is refused as
// before.
func TestCloseThisSessionIsRefusedOnABridgeOriginAndOtherwiseUnchanged(t *testing.T) {
	stubNotifyPrincipal(t)
	f := newLiarFixture(t)
	withRealEvidenceClassifier(t)
	text, isErr := runHandler(handleCloseThisSession)(bridgedCtxFor(f.a.key), json.RawMessage(`{"force":true}`))
	if !isErr || !strings.HasPrefix(text, "refused: ") {
		t.Fatalf("bridged close with force: got isErr=%v %q, want a contract refusal", isErr, text)
	}
	// A corrupt record does not make a stdio close refuse it either.
	writeStateFile(t, "units/bad.json", "{not json")
	if text, isErr := runHandler(handleCloseThisSession)(context.Background(), json.RawMessage(`{}`)); isErr && strings.Contains(text, "classifies as confined") {
		t.Errorf("a stdio close was refused by the classification: %q", text)
	}
}

// The classifier's own kinds, the enum the verbs use.
func TestClassifyServeCallerEvidenceKinds(t *testing.T) {
	cases := []struct {
		name  string
		write func(t *testing.T, state, principal string)
		want  confinement.Evidence
	}{
		{"nothing written", func(*testing.T, string, string) {}, confinement.EvidenceAbsent},
		{"a confined unit record", func(t *testing.T, s, p string) {
			writeAt(t, s, "units/u.json", fmt.Sprintf(`{"principal":%q}`, p))
		}, confinement.EvidenceConfined},
		{"an unparseable unit record", func(t *testing.T, s, _ string) { writeAt(t, s, "units/bad.json", "x") }, confinement.EvidenceUnreadable},
		// (the verbs cannot even resolve their session through such a mapping, so
		// the classifier's answer is pinned here, not through a handler)
		{"an unparseable principal mapping", func(t *testing.T, s, p string) { writeAt(t, s, "principals/"+p+".json", "{not json") }, confinement.EvidenceUnreadable},
		{"a directory in place of the principal mapping", func(t *testing.T, s, p string) {
			if err := os.MkdirAll(filepath.Join(s, "spinclass", "principals", p+".json"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, confinement.EvidenceUnreadable},
		{"a serve record of the principal that does not parse", func(t *testing.T, s, p string) { writeAt(t, s, "serves/"+p+"-1.json", "x") }, confinement.EvidenceUnreadable},
		{"an unconfined mapping", func(t *testing.T, s, p string) {
			if err := confinement.RecordUnconfinedPrincipal(s, p, "r/b", s); err != nil {
				t.Fatal(err)
			}
		}, confinement.EvidenceUnconfined},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			tc.write(t, state, "kind-principal")
			got, why := confinement.ClassifyServeCallerEvidence(confinement.Host{}, state, "kind-principal", "/wt/kinds")
			if got != tc.want {
				t.Errorf("got %v (%s), want %v", got, why, tc.want)
			}
		})
	}
}

func writeAt(t *testing.T, stateDir, rel, content string) {
	t.Helper()
	path := filepath.Join(stateDir, "spinclass", rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
