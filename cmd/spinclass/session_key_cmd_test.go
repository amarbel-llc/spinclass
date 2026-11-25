package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

const khPub = "ssh-ed25519 AAAAC3NzaFAKEAAA holder:reader"

// khListing is what ssh-keygen -L prints for a certificate of the key whose
// fingerprint is SHA256:FAKEFP, valid until 2026-11-26T20:00:00 local time.
const khListing = `        Type: ssh-ed25519-cert-v01@openssh.com user certificate
        Public key: ED25519-CERT SHA256:FAKEFP
        Valid: from 2026-11-25T20:00:00 to 2026-11-26T20:00:00
`

const khCertLine = "ssh-ed25519-cert-v01@openssh.com AAAAIHNzaC1lZDI1NTE5LWNlcnQ comment\n"

type sessionKeyEnv struct {
	out, errOut *bytes.Buffer
	base        string
	sock        string
	pair        keyholder.Pair
	live        bool
	listing     string
	now         time.Time
}

func useSessionKeySeams(t *testing.T) *sessionKeyEnv {
	t.Helper()
	e := &sessionKeyEnv{
		out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, base: t.TempDir(), sock: "/run/holder/agent.sock", live: true,
		listing: khListing, now: time.Date(2026, 11, 26, 8, 0, 0, 0, time.Local),
	}
	e.pair = keyholder.Pair{AgentSock: e.sock, Fingerprint: "SHA256:FAKEFP"}
	oFind, oOut, oErr, oBase, oNow := sessionKeyFind, sessionKeyOut, sessionKeyErr, sessionKeyBase, sessionKeyNow
	oRun, oList, oFP, oCert := sessionKeyRunning, sessionKeyAgentList, sessionKeyFingerprint, sessionKeyCertList
	t.Cleanup(func() {
		sessionKeyFind, sessionKeyOut, sessionKeyErr, sessionKeyBase, sessionKeyNow = oFind, oOut, oErr, oBase, oNow
		sessionKeyRunning, sessionKeyAgentList, sessionKeyFingerprint, sessionKeyCertList = oRun, oList, oFP, oCert
	})
	sessionKeyFind = func(target string) (*session.State, error) {
		return &session.State{SessionKey: "repo/" + strings.TrimPrefix(target, "repo/"), WorktreePath: "/wt/" + target}, nil
	}
	sessionKeyOut = func() io.Writer { return e.out }
	sessionKeyErr = func() io.Writer { return e.errOut }
	sessionKeyBase = func() string { return e.base }
	sessionKeyNow = func() time.Time { return e.now }
	sessionKeyRunning = func(context.Context, string, string) (keyholder.Pair, bool, error) { return e.pair, e.live, nil }
	sessionKeyAgentList = func(_ context.Context, sock string) ([]byte, error) {
		if sock != e.sock {
			t.Errorf("ssh-add was pointed at %q, want the session's holder socket %q", sock, e.sock)
		}
		return []byte(khPub + "\n"), nil
	}
	sessionKeyFingerprint = func(context.Context, string) (string, error) { return "SHA256:FAKEFP", nil }
	sessionKeyCertList = func(context.Context, []byte) (string, error) { return e.listing, nil }
	return e
}

func (e *sessionKeyEnv) makeSessionDir(t *testing.T, name string) {
	t.Helper()
	if err := os.MkdirAll(keyholder.CertDirIn(keyholder.SessionDir(e.base, "repo/"+name)), 0o700); err != nil {
		t.Fatal(err)
	}
}

func (e *sessionKeyEnv) installCert(t *testing.T, name, content string) string {
	t.Helper()
	e.makeSessionDir(t, name)
	p := keyholder.CertPath(e.base, "repo/"+name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *sessionKeyEnv) runJSON(t *testing.T, target string) map[string]any {
	t.Helper()
	e.out.Reset()
	if err := runSessionKey(context.Background(), json.RawMessage(`{"session":"`+target+`","json":true}`)); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(e.out.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %q: %v", e.out.String(), err)
	}
	return m
}

func TestSessionKeyPrintsExactlyTheKeyLineAsTheHolderListsItAndItsFingerprint(t *testing.T) {
	e := useSessionKeySeams(t)
	e.makeSessionDir(t, "a")
	if err := runSessionKey(context.Background(), json.RawMessage(`{"session":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if got, want := e.out.String(), khPub+"\nSHA256:FAKEFP\n"; got != want {
		t.Errorf("output %q, want exactly %q (the comment is the holder's own, never rewritten)", got, want)
	}
	if e.errOut.Len() != 0 {
		t.Errorf("with no certificate stderr says %q", e.errOut.String())
	}
}

func TestSessionKeyJSONHasEveryFieldAndTheCanonicalSessionKeyWhateverFormWasPassed(t *testing.T) {
	e := useSessionKeySeams(t)
	e.makeSessionDir(t, "a")
	wantPath := keyholder.CertPath(e.base, "repo/a")
	for _, target := range []string{"a", "repo/a"} {
		m := e.runJSON(t, target)
		if m["session"] != "repo/a" || m["public_key"] != khPub || m["fingerprint"] != "SHA256:FAKEFP" || m["cert_path"] != wantPath {
			t.Errorf("target %q: json = %v", target, m)
		}
		for _, f := range []string{"cert_expires", "cert_matches_key", "cert_problem"} {
			if v, present := m[f]; !present || v != nil {
				t.Errorf("target %q: %s = %v (present %v), want null with no certificate installed", target, f, v, present)
			}
		}
	}
	if !strings.HasSuffix(wantPath, filepath.Join("cert", "cert.pub")) {
		t.Errorf("cert_path %q does not end in cert/cert.pub", wantPath)
	}
}

func TestSessionKeyJSONReadsTheCertificateFromTheInstalledFile(t *testing.T) {
	e := useSessionKeySeams(t)
	e.installCert(t, "a", khCertLine)
	m := e.runJSON(t, "a")
	want, err := keyholder.ParseCertExpiry(khListing)
	if err != nil {
		t.Fatal(err)
	}
	if m["cert_expires"] != want.UTC().Format(time.RFC3339) || m["cert_matches_key"] != true || m["cert_problem"] != nil {
		t.Errorf("json = %v, want the installed certificate's expiry (%s) and a match", m, want.UTC().Format(time.RFC3339))
	}
	if e.errOut.Len() != 0 {
		t.Errorf("a good certificate warned on stderr: %q", e.errOut.String())
	}
}

func TestSessionKeyWarnsOnStderrForAnotherKeyAnExpiredAndAnUnusableCertificateAndStdoutStaysTheTwoLines(t *testing.T) {
	e := useSessionKeySeams(t)
	p := e.installCert(t, "a", khCertLine)
	cases := []struct {
		name  string
		setup func()
		want  string
		match any
	}{
		{"another key", func() { e.listing = strings.Replace(khListing, "SHA256:FAKEFP", "SHA256:OTHER", 1) }, "another key", false},
		{"expired", func() { e.listing = khListing; e.now = time.Date(2026, 11, 28, 0, 0, 0, 0, time.Local) }, "expired", true},
		{"unusable", func() {
			e.now = time.Date(2026, 11, 26, 8, 0, 0, 0, time.Local)
			_ = os.WriteFile(p, []byte("not a certificate\n"), 0o600)
		}, "unusable", nil},
	}
	for _, c := range cases {
		c.setup()
		e.errOut.Reset()
		e.out.Reset()
		if err := runSessionKey(context.Background(), json.RawMessage(`{"session":"a"}`)); err != nil {
			t.Fatal(err)
		}
		if got, want := e.out.String(), khPub+"\nSHA256:FAKEFP\n"; got != want {
			t.Errorf("%s: stdout %q, want exactly %q", c.name, got, want)
		}
		if !strings.Contains(e.errOut.String(), c.want) {
			t.Errorf("%s: stderr %q does not say %q", c.name, e.errOut.String(), c.want)
		}
		m := e.runJSON(t, "a")
		if m["cert_matches_key"] != c.match {
			t.Errorf("%s: cert_matches_key = %v, want %v", c.name, m["cert_matches_key"], c.match)
		}
	}
}

func TestSessionKeyReportsASymlinkedCertificateAndDoesNotFollowIt(t *testing.T) {
	e := useSessionKeySeams(t)
	e.makeSessionDir(t, "a")
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte(khCertLine), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, keyholder.CertPath(e.base, "repo/a")); err != nil {
		t.Fatal(err)
	}
	called := false
	sessionKeyCertList = func(context.Context, []byte) (string, error) { called = true; return e.listing, nil }
	m := e.runJSON(t, "a")
	if called {
		t.Error("a symlinked certificate was handed to ssh-keygen")
	}
	if p, _ := m["cert_problem"].(string); !strings.Contains(p, "symbolic link") || m["cert_matches_key"] != nil || m["cert_expires"] != nil {
		t.Errorf("json = %v, want cert_problem naming the symbolic link and null match and expiry", m)
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

// What sc session-key vouches for is the key this pair minted (B-F10, A-M3): the
// fingerprint recorded at the mint is the truth, and a line the operator will
// paste or read must be one clean key line of a type spinclass accepts.
func TestSessionKeyRefusesAKeyTheHolderDidNotMintAndAnythingNotACleanKeyLine(t *testing.T) {
	long := "ssh-ed25519 AAAAC3NzaFAKEAAA " + strings.Repeat("x", 4100)
	cases := []struct {
		name    string
		listed  string
		recFP   string // the fingerprint recorded at the mint ("" = keep the harness default)
		listFP  string // what ssh-keygen says of the listed key ("" = keep)
		wantErr string
	}{
		{"a key whose fingerprint differs from the recorded one", khPub, "SHA256:RECORDED", "", "changed since the pair started"},
		{"a record with no fingerprint", khPub, "-", "", "no fingerprint recorded"},
		{"an escape sequence in the comment", "ssh-ed25519 AAAAC3NzaFAKEAAA holder:\x1b[31mred", "", "", "control character"},
		{"a carriage return in the comment", "ssh-ed25519 AAAAC3NzaFAKEAAA holder:a\rb", "", "", "control character"},
		{"a tab in the comment", "ssh-ed25519 AAAAC3NzaFAKEAAA holder:a\tb", "", "", "control character"},
		{"a right-to-left override in the comment", "ssh-ed25519 AAAAC3NzaFAKEAAA holder:\xe2\x80\xae", "", "", "control character"},
		{"a line longer than 4 KiB", long, "", "", "longer than"},
		{"an rsa key", "ssh-rsa AAAAC3NzaFAKEAAA holder:reader", "", "", "key type"},
		{"a sk key", "sk-ssh-ed25519@openssh.com AAAAC3NzaFAKEAAA holder:reader", "", "", "key type"},
		{"a key blob that is not base64", "ssh-ed25519 !!!notbase64 holder:reader", "", "", "not base64"},
		{"a line with no key blob", "ssh-ed25519", "", "", "not a public key line"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := useSessionKeySeams(t)
			e.makeSessionDir(t, "a")
			switch c.recFP {
			case "":
			case "-":
				e.pair.Fingerprint = ""
			default:
				e.pair.Fingerprint = c.recFP
			}
			sessionKeyAgentList = func(context.Context, string) ([]byte, error) { return []byte(c.listed + "\n"), nil }
			err := runSessionKey(context.Background(), json.RawMessage(`{"session":"a"}`))
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, c.wantErr)
			}
			if e.out.Len() != 0 {
				t.Errorf("a refused key was printed: %q", e.out.String())
			}
		})
	}
}

// A refused listing never reaches ssh-keygen: the hostile line is checked before
// anything shells out with it on stdin.
func TestSessionKeyChecksTheLineBeforeHandingItToSSHKeygen(t *testing.T) {
	e := useSessionKeySeams(t)
	e.makeSessionDir(t, "a")
	sessionKeyAgentList = func(context.Context, string) ([]byte, error) {
		return []byte("ssh-ed25519 AAAAC3NzaFAKEAAA holder:\x1b[2J\n"), nil
	}
	called := false
	sessionKeyFingerprint = func(context.Context, string) (string, error) { called = true; return "SHA256:FAKEFP", nil }
	if err := runSessionKey(context.Background(), json.RawMessage(`{"session":"a"}`)); err == nil {
		t.Fatal("a line with an escape sequence was accepted")
	}
	if called {
		t.Error("ssh-keygen was handed a line that failed the check")
	}
}

func TestSessionKeyAcceptsEd25519AndNistp256AndPrintsWhatTheHolderHas(t *testing.T) {
	for _, pub := range []string{
		"ssh-ed25519 AAAAC3NzaFAKEAAA holder:piggy-holder 1",
		"ecdsa-sha2-nistp256 AAAAC3NzaFAKEAAA holder:piggy-holder 1",
	} {
		e := useSessionKeySeams(t)
		e.makeSessionDir(t, "a")
		sessionKeyAgentList = func(context.Context, string) ([]byte, error) { return []byte(pub + "\n"), nil }
		if err := runSessionKey(context.Background(), json.RawMessage(`{"session":"a"}`)); err != nil {
			t.Errorf("%q: %v", pub, err)
			continue
		}
		if got, want := e.out.String(), pub+"\nSHA256:FAKEFP\n"; got != want {
			t.Errorf("output %q, want %q", got, want)
		}
	}
}
