package main

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// The goldens are REAL output of `sc session-key` against a real key holder pair
// (just debug-session-key-capture): the public key and fingerprint of a
// throwaway minted key, and the state directory rewritten to /state. They are
// what the signing side (circus's session grant) cites. Nothing here depends on
// the key bytes: the tests pin the field set, the shapes, and that this tree's
// encoder still writes exactly these bytes.

func readGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var (
	goldenKeyLine = regexp.MustCompile(`^(ssh-ed25519|ecdsa-sha2-nistp256) [A-Za-z0-9+/=]+( [^\n]*)?$`)
	goldenFP      = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
	goldenCert    = regexp.MustCompile(`^/state/spinclass/keyholder/[0-9a-f]{16}/cert/cert\.pub$`)
)

func TestTheTextGoldenIsExactlyTheKeyLineAndItsFingerprint(t *testing.T) {
	lines := strings.Split(strings.TrimSuffix(readGolden(t, "session-key.txt.golden"), "\n"), "\n")
	if len(lines) != 2 || !goldenKeyLine.MatchString(lines[0]) || !goldenFP.MatchString(lines[1]) {
		t.Fatalf("session-key.txt.golden = %q, want a key line then a SHA256 fingerprint", lines)
	}
}

func TestTheJSONGoldenHasExactlyTheDocumentedFieldsInTheDocumentedShapes(t *testing.T) {
	raw := readGolden(t, "session-key.json.golden")
	var doc struct {
		Session        string     `json:"session"`
		PublicKey      string     `json:"public_key"`
		Fingerprint    string     `json:"fingerprint"`
		CertPath       string     `json:"cert_path"`
		CertExpires    *time.Time `json:"cert_expires"`
		CertMatchesKey *bool      `json:"cert_matches_key"`
		CertProblem    *string    `json:"cert_problem"`
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("the golden does not decode into the documented fields: %v\n%s", err, raw)
	}
	var generic map[string]any
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"session", "public_key", "fingerprint", "cert_path", "cert_expires", "cert_matches_key", "cert_problem"} {
		if _, present := generic[f]; !present {
			t.Errorf("the golden lacks the field %q", f)
		}
	}
	if len(generic) != 7 {
		t.Errorf("the golden has %d fields, want exactly 7: %v", len(generic), generic)
	}
	if parts := strings.Split(doc.Session, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Errorf("session %q is not the canonical <repo>/<branch> key", doc.Session)
	}
	if !goldenKeyLine.MatchString(doc.PublicKey) || !goldenFP.MatchString(doc.Fingerprint) || !goldenCert.MatchString(doc.CertPath) {
		t.Errorf("shapes: public_key %q, fingerprint %q, cert_path %q", doc.PublicKey, doc.Fingerprint, doc.CertPath)
	}
	if doc.CertExpires != nil || doc.CertMatchesKey != nil || doc.CertProblem != nil {
		t.Errorf("with no certificate installed the cert fields are null: %+v", doc)
	}
	if !strings.HasSuffix(raw, "}\n") || strings.Count(raw, "\n") != 1 {
		t.Errorf("the golden is not one JSON line: %q", raw)
	}
}

// Both goldens are what THIS tree's command prints for the key they name, byte
// for byte: the capture recipe and the code cannot drift apart unnoticed.
func TestTheCommandStillWritesTheGoldensByteForByte(t *testing.T) {
	txt := strings.Split(strings.TrimSuffix(readGolden(t, "session-key.txt.golden"), "\n"), "\n")
	var doc struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal([]byte(readGolden(t, "session-key.json.golden")), &doc); err != nil {
		t.Fatal(err)
	}
	e := useSessionKeySeams(t)
	sessionKeyAgentList = func(context.Context, string) ([]byte, error) { return []byte(txt[0] + "\n"), nil }
	sessionKeyFingerprint = func(context.Context, string) (string, error) { return txt[1], nil }
	branch := strings.TrimPrefix(doc.Session, "repo/")
	sessionKeyFind = func(string) (*session.State, error) {
		return &session.State{SessionKey: doc.Session, WorktreePath: "/wt/" + branch}, nil
	}
	if err := os.MkdirAll(keyholder.CertDirIn(keyholder.SessionDir(e.base, doc.Session)), 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(args string) string {
		e.out.Reset()
		if err := runSessionKey(context.Background(), json.RawMessage(args)); err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(e.out.String(), e.base, "/state/spinclass/keyholder")
	}
	if got, want := run(`{"session":"`+branch+`"}`), readGolden(t, "session-key.txt.golden"); got != want {
		t.Errorf("text: got %q, golden %q", got, want)
	}
	if got, want := run(`{"session":"`+branch+`","json":true}`), readGolden(t, "session-key.json.golden"); got != want {
		t.Errorf("json: got %q, golden %q", got, want)
	}
}
