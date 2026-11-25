package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

const khPub = "ssh-ed25519 AAAAC3NzaFAKE holder:reader"

type sessionKeyEnv struct {
	out  *bytes.Buffer
	base string
	sock string
	pair keyholder.Pair
	live bool
}

func useSessionKeySeams(t *testing.T) *sessionKeyEnv {
	t.Helper()
	e := &sessionKeyEnv{out: &bytes.Buffer{}, base: t.TempDir(), sock: "/run/holder/agent.sock", live: true}
	e.pair = keyholder.Pair{AgentSock: e.sock}
	saved := []any{sessionKeyFind, sessionKeyOut, sessionKeyBase, sessionKeyRunning, sessionKeyAgentList, sessionKeyFingerprint, sessionKeyCertExpiry}
	t.Cleanup(func() {
		sessionKeyFind, sessionKeyOut, sessionKeyBase = saved[0].(func(string) (*session.State, error)), saved[1].(func() io.Writer), saved[2].(func() string)
		sessionKeyRunning = saved[3].(func(context.Context, string, string) (keyholder.Pair, bool, error))
		sessionKeyAgentList = saved[4].(func(context.Context, string) ([]byte, error))
		sessionKeyFingerprint = saved[5].(func(context.Context, string) (string, error))
		sessionKeyCertExpiry = saved[6].(func(context.Context, string) (time.Time, error))
	})
	sessionKeyFind = func(target string) (*session.State, error) {
		return &session.State{SessionKey: "repo/" + target, WorktreePath: "/wt/" + target}, nil
	}
	sessionKeyOut = func() io.Writer { return e.out }
	sessionKeyBase = func() string { return e.base }
	sessionKeyRunning = func(context.Context, string, string) (keyholder.Pair, bool, error) { return e.pair, e.live, nil }
	sessionKeyAgentList = func(_ context.Context, sock string) ([]byte, error) {
		if sock != e.sock {
			t.Errorf("ssh-add was pointed at %q, want the session's holder socket %q", sock, e.sock)
		}
		return []byte(khPub + "\n"), nil
	}
	sessionKeyFingerprint = func(context.Context, string) (string, error) { return "SHA256:FAKEFP", nil }
	sessionKeyCertExpiry = func(context.Context, string) (time.Time, error) {
		return time.Date(2026, 11, 26, 20, 0, 0, 0, time.UTC), nil
	}
	return e
}

func (e *sessionKeyEnv) makeSessionDir(t *testing.T, name string) {
	t.Helper()
	if err := os.MkdirAll(keyholder.SessionDir(e.base, "repo/"+name), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestSessionKeyPrintsExactlyTheKeyAndItsFingerprint(t *testing.T) {
	e := useSessionKeySeams(t)
	e.makeSessionDir(t, "a")
	if err := runSessionKey(context.Background(), json.RawMessage(`{"session":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if got, want := e.out.String(), khPub+"\nSHA256:FAKEFP\n"; got != want {
		t.Errorf("output %q, want exactly %q", got, want)
	}
}

func TestSessionKeyJSONCarriesTheCertificateFieldsReadFromTheInstalledFile(t *testing.T) {
	e := useSessionKeySeams(t)
	e.makeSessionDir(t, "a")
	run := func() map[string]any {
		e.out.Reset()
		if err := runSessionKey(context.Background(), json.RawMessage(`{"session":"a","json":true}`)); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(e.out.Bytes(), &m); err != nil {
			t.Fatalf("not JSON: %q: %v", e.out.String(), err)
		}
		return m
	}
	m := run()
	wantPath := keyholder.CertPath(e.base, "repo/a")
	if m["session"] != "repo/a" || m["public_key"] != khPub || m["fingerprint"] != "SHA256:FAKEFP" || m["cert_path"] != wantPath {
		t.Errorf("json = %v", m)
	}
	if v, present := m["cert_expires"]; !present || v != nil {
		t.Errorf("cert_expires = %v (present %v), want null until a certificate is installed", v, present)
	}
	if err := os.WriteFile(wantPath, []byte("ssh-ed25519-cert-v01@openssh.com AAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if m := run(); m["cert_expires"] != "2026-11-26T20:00:00Z" {
		t.Errorf("cert_expires = %v, want the installed certificate's expiry", m["cert_expires"])
	}
}

func TestSessionKeyRefusesWithTheTwoNamedErrors(t *testing.T) {
	e := useSessionKeySeams(t)
	err := runSessionKey(context.Background(), json.RawMessage(`{"session":"none"}`))
	if err == nil || !strings.Contains(err.Error(), "no holder for session") {
		t.Errorf("no pair: %v, want 'no holder for session'", err)
	}
	e.makeSessionDir(t, "down")
	e.live = false
	err = runSessionKey(context.Background(), json.RawMessage(`{"session":"down"}`))
	if err == nil || !strings.Contains(err.Error(), "holder not running") {
		t.Errorf("dead pair: %v, want 'holder not running'", err)
	}
	if e.out.Len() != 0 {
		t.Errorf("a failed session-key printed %q", e.out.String())
	}
}

func TestSessionKeyRefusesASocketThatIsNotASingleKeyHolder(t *testing.T) {
	e := useSessionKeySeams(t)
	e.makeSessionDir(t, "a")
	for name, listed := range map[string]string{"two keys": khPub + "\nssh-ed25519 BBBB other\n", "none": "The agent has no identities.\n"} {
		sessionKeyAgentList = func(context.Context, string) ([]byte, error) { return []byte(listed), nil }
		if name == "none" {
			sessionKeyAgentList = func(context.Context, string) ([]byte, error) { return nil, errors.New("exit status 1") }
		}
		if err := runSessionKey(context.Background(), json.RawMessage(`{"session":"a"}`)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestParseCertExpiryReadsTheEndOfTheValidityInLocalTime(t *testing.T) {
	out := "x.pub:\n        Type: ssh-ed25519-cert-v01@openssh.com user certificate\n        Valid: from 2026-11-25T20:00:00 to 2026-11-26T20:00:00\n"
	got, err := parseCertExpiry(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 26, 20, 0, 0, 0, time.Local); !got.Equal(want) {
		t.Errorf("expiry %v, want %v", got, want)
	}
	if _, err := parseCertExpiry("Valid: forever\n"); err == nil {
		t.Error("an unparseable validity line was accepted")
	}
}
