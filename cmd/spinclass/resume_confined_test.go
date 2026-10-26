package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// spinclass#400: a session launched confined resumes confined, and nothing of a
// once-confined worktree runs unconfined.

var harnessResume = []string{"clown", "--", "--resume", "0b8fd1b2-8d38-4c6e-9f0c-3f2f5b1c9a11"}

// markConfined plants the frontend unit record clown wrote for the fixture's
// worktree, and, when withSpec, the launch spec spinclass wrote for it.
func (f entryFixture) markConfined(t *testing.T, withSpec bool) (specPath string) {
	t.Helper()
	stateDir := os.Getenv("XDG_STATE_HOME")
	rec := confinement.UnitRecordPath(stateDir, f.wt)
	if err := os.MkdirAll(filepath.Dir(rec), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, []byte(`{"principal":"p"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	specPath = confinement.UnitSpecPath(stateDir, f.wt)
	if withSpec {
		if err := os.WriteFile(specPath, []byte(`{"spec":"v1"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Hour)
		if err := os.Chtimes(specPath, old, old); err != nil {
			t.Fatal(err)
		}
	}
	return specPath
}

// wantRefusal asserts err names the session key and every way-out phrase.
func wantRefusal(t *testing.T, err error, wayOut ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("no refusal")
	}
	for _, w := range append([]string{"repo/feat", "#400"}, wayOut...) {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("refusal %q does not contain %q", err, w)
		}
	}
}

const (
	wayResume = "sc resume repo/feat"
	wayClose  = "sc close repo/feat"
)

func TestResumeOfOnceConfinedSessionNamesItsSpecLast(t *testing.T) {
	f := newEntryFixture(t, "")
	f.writeState(t, &session.PinnedEntry{
		Start:  []string{"/bin/pinned-start"},
		Resume: harnessResume,
		Env:    map[string]string{"MARK": "pinned", "CLOWN_AGENT_SCOPE": "/evil/spec.json"},
	})
	spec := f.markConfined(t, true)
	before, _ := os.ReadFile(spec)
	beforeInfo, _ := os.Stat(spec)
	c := stubAttach(t)

	if err := resumeFeat(t); err != nil {
		t.Fatalf("resume: %v", err)
	}
	se := capturedSessionExecutor(t, c)
	if got := se.Env["CLOWN_AGENT_SCOPE"]; got != spec {
		t.Errorf("CLOWN_AGENT_SCOPE = %q, want the recorded spec %q", got, spec)
	}
	if se.Env["MARK"] != "pinned" {
		t.Errorf("the pinned env was lost: %v", se.Env)
	}
	after, _ := os.ReadFile(spec)
	afterInfo, _ := os.Stat(spec)
	if !bytes.Equal(before, after) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Error("resume rewrote the unit spec")
	}
}

func TestResumeOfOnceConfinedSessionWithMissingSpecRefuses(t *testing.T) {
	f := newEntryFixture(t, "")
	f.writeState(t, &session.PinnedEntry{Start: []string{"/bin/s"}, Resume: harnessResume})
	f.markConfined(t, false)
	c := stubAttach(t)

	err := resumeFeat(t)
	wantRefusal(t, err, "missing", wayClose, "resurrect")
	if c.called {
		t.Error("a once-confined session with no spec was attached")
	}
}

func TestResumeOfOnceConfinedSessionRefusesShellFallback(t *testing.T) {
	f := newEntryFixture(t, "")
	f.writeState(t, &session.PinnedEntry{Start: []string{"fish"}})
	f.markConfined(t, true)
	c := stubAttach(t)

	err := resumeFeat(t)
	wantRefusal(t, err, "unconfined shell", wayResume, wayClose)
	if c.called {
		t.Error("a shell was attached in a once-confined worktree")
	}
}

func TestResumeOfOnceConfinedSessionRefusesNonResumeHarnessFlags(t *testing.T) {
	for _, flag := range []string{"-c", "--continue", "--fork-session", "--from-pr", "--from-pr=12"} {
		t.Run(flag, func(t *testing.T) {
			f := newEntryFixture(t, "")
			f.writeState(t, &session.PinnedEntry{Start: []string{"/bin/s"}, Resume: []string{"clown", "--", flag}})
			f.markConfined(t, true)
			c := stubAttach(t)

			wantRefusal(t, resumeFeat(t), flag, wayResume, wayClose)
			if c.called {
				t.Errorf("%s was attached", flag)
			}
		})
	}
}

// Not over-matching: a session id or title that merely contains the letters.
func TestResumeOfOnceConfinedSessionAcceptsOrdinaryResumeArgv(t *testing.T) {
	f := newEntryFixture(t, "")
	f.writeState(t, &session.PinnedEntry{Start: []string{"/bin/s"}, Resume: []string{"clown", "--", "--resume", "c-continue", "--model", "opus"}})
	f.markConfined(t, true)
	c := stubAttach(t)

	if err := resumeFeat(t); err != nil {
		t.Fatalf("resume: %v", err)
	}
	capturedSessionExecutor(t, c)
}

func TestResumeOfNeverConfinedSessionIsUnchanged(t *testing.T) {
	f := newEntryFixture(t, "")
	f.writeState(t, &session.PinnedEntry{Start: []string{"fish"}})
	c := stubAttach(t)

	if err := resumeFeat(t); err != nil {
		t.Fatalf("resume of a never-confined shell session: %v", err)
	}
	se := capturedSessionExecutor(t, c)
	if v, ok := se.Env["CLOWN_AGENT_SCOPE"]; ok {
		t.Errorf("CLOWN_AGENT_SCOPE = %q on a never-confined session", v)
	}
}

func TestConfinedResumeScopeFailsClosedWhenStateDirIsUnknowable(t *testing.T) {
	st := &session.State{SessionKey: "repo/feat", WorktreePath: t.TempDir()}
	st.SessionKey = "repo/feat"
	_, err := confinedResumeScope("", errors.New("no home"), st, harnessResume, false)
	wantRefusal(t, err, wayClose)
}
