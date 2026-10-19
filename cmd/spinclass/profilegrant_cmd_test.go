package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/server"
	"code.linenisgreat.com/spinclass/internal/grantverify"
	"code.linenisgreat.com/spinclass/internal/operatoract"
	"code.linenisgreat.com/spinclass/internal/profilegrant"
	"golang.org/x/crypto/ssh"
)

func sshStr(b []byte) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(len(b))), b...)
}

// testCard stands in for the operator's card: it signs in the real sshsig
// format with a throwaway ed25519 key, so verification exercises the production
// verifier and not a mock of it.
type testCard struct {
	priv  ed25519.PrivateKey
	pub   ssh.PublicKey
	calls int
	ns    string
	text  string
	err   error
}

func newTestCard(t *testing.T) *testCard {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return &testCard{priv: priv, pub: sp}
}

func (c *testCard) Sign(_ context.Context, namespace, text string) ([]byte, error) {
	c.calls++
	c.ns, c.text = namespace, text
	if c.err != nil {
		return nil, c.err
	}
	digest := sha512.Sum512([]byte(text))
	signed := append([]byte("SSHSIG"), sshStr([]byte(namespace))...)
	signed = append(signed, sshStr(nil)...)
	signed = append(signed, sshStr([]byte("sha512"))...)
	signed = append(signed, sshStr(digest[:])...)
	sig := ed25519.Sign(c.priv, signed)

	blob := append([]byte("SSHSIG"), binary.BigEndian.AppendUint32(nil, 1)...)
	blob = append(blob, sshStr(c.pub.Marshal())...)
	blob = append(blob, sshStr([]byte(namespace))...)
	blob = append(blob, sshStr(nil)...)
	blob = append(blob, sshStr([]byte("sha512"))...)
	blob = append(blob, sshStr(append(sshStr([]byte("ssh-ed25519")), sshStr(sig)...))...)

	enc := base64.StdEncoding.EncodeToString(blob)
	out := "-----BEGIN SSH SIGNATURE-----\n"
	for len(enc) > 70 {
		out += enc[:70] + "\n"
		enc = enc[70:]
	}
	return []byte(out + enc + "\n-----END SSH SIGNATURE-----\n"), nil
}

var testNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func testInput() blessInput {
	return blessInput{
		Grantee:  "swift-elder",
		Session:  "-",
		Profiles: map[string]string{"worker": "clown-profile-digest-v1@sha256-aaaa"},
		TTL:      time.Hour,
		Onward:   profilegrant.OnwardNone,
		Now:      testNow,
		Nonce:    "0123456789abcdef0123456789abcdef",
	}
}

func walkRoot(t *testing.T, rec profilegrant.Record, keys []ssh.PublicKey, extra ...profilegrant.Record) error {
	t.Helper()
	known := map[string]profilegrant.Record{rec.Digest: rec}
	for _, e := range extra {
		known[e.Digest] = e
	}
	_, err := profilegrant.Walk(testNow, rec,
		func(d string) (profilegrant.Record, bool) { r, ok := known[d]; return r, ok },
		profilegrant.NewVerifier(grantverify.NewRootVerifier(keys), profilegrant.LocalLinkVerifier{}),
		profilegrant.WalkOptions{})
	return err
}

func TestBlessRecordSignsActTextAndVerifiesBeforeEmitting(t *testing.T) {
	card := newTestCard(t)
	var shown strings.Builder
	in := testInput()
	in.ActOut = &shown

	rec, err := blessRecord(context.Background(), card, []ssh.PublicKey{card.pub}, in)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Root || rec.Digest == "" || len(rec.Sig) == 0 {
		t.Fatalf("incomplete record: %+v", rec)
	}
	if card.ns != operatoract.NamespaceEscalation {
		t.Fatalf("signed under %q", card.ns)
	}
	grant, err := profilegrant.ParseCanonicalText(rec.Text)
	if err != nil {
		t.Fatal(err)
	}
	wantAct, err := profilegrant.ActText(grant, rec.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if card.text != wantAct {
		t.Fatalf("the card was shown\n%s\nwant\n%s", card.text, wantAct)
	}
	if !strings.Contains(shown.String(), wantAct) {
		t.Fatal("the operator was not shown the act text before the card was asked")
	}
	if grant.Grantor != "operator" || grant.Prev != "-" || grant.Widens != "-" {
		t.Fatalf("not an initial root grant: %+v", grant)
	}
	if want := testNow.Add(time.Hour); !grant.Expires.Equal(want) {
		t.Fatalf("expires %s, want %s", grant.Expires, want)
	}
	if err := walkRoot(t, rec, []ssh.PublicKey{card.pub}); err != nil {
		t.Fatalf("independent verification: %v", err)
	}
}

func TestBlessRecordRefusesASignatureByAnotherKey(t *testing.T) {
	card, other := newTestCard(t), newTestCard(t)
	rec, err := blessRecord(context.Background(), card, []ssh.PublicKey{other.pub}, testInput())
	if err == nil {
		t.Fatal("a record signed by a key outside the signers file was emitted")
	}
	if rec.Digest != "" || len(rec.Sig) != 0 {
		t.Fatalf("a record came back with the error: %+v", rec)
	}
	if !strings.Contains(err.Error(), "not emitting") {
		t.Fatalf("error does not say it was withheld: %v", err)
	}
}

func TestBlessRecordWithoutSignersNeverAsksTheCard(t *testing.T) {
	card := newTestCard(t)
	if _, err := blessRecord(context.Background(), card, nil, testInput()); err == nil {
		t.Fatal("expected a refusal")
	}
	if card.calls != 0 {
		t.Fatal("the card was asked for a record that could not be verified")
	}
	// --no-verify is the explicit way past it.
	in := testInput()
	in.SkipVerify = true
	rec, err := blessRecord(context.Background(), card, nil, in)
	if err != nil || len(rec.Sig) == 0 {
		t.Fatalf("no-verify: %v %+v", err, rec)
	}
}

func TestBlessRecordPropagatesCardRefusals(t *testing.T) {
	card := newTestCard(t)
	card.err = &operatoract.CancelledError{Detail: "no PIN from the askpass"}
	_, err := blessRecord(context.Background(), card, []ssh.PublicKey{card.pub}, testInput())
	var cancelled *operatoract.CancelledError
	if !errors.As(err, &cancelled) {
		t.Fatalf("got %v", err)
	}
}

func TestBlessRecordBounds(t *testing.T) {
	card := newTestCard(t)
	keys := []ssh.PublicKey{card.pub}
	for name, mutate := range map[string]func(*blessInput){
		"zero ttl":          func(in *blessInput) { in.TTL = 0 },
		"ttl over a week":   func(in *blessInput) { in.TTL = 7*24*time.Hour + time.Second },
		"bad onward":        func(in *blessInput) { in.Onward = "always" },
		"injected grantee":  func(in *blessInput) { in.Grantee = "x\nallow: everyone" },
		"widens, no record": func(in *blessInput) { in.Widens = "piggy-record-digest-v1@sha256-zzz" },
		"no profiles":       func(in *blessInput) { in.Profiles = nil },
	} {
		in := testInput()
		mutate(&in)
		if _, err := blessRecord(context.Background(), card, keys, in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if card.calls != 0 {
		t.Fatalf("the card was asked %d times for requests that were invalid up front", card.calls)
	}
	in := testInput()
	in.TTL = 7 * 24 * time.Hour
	if _, err := blessRecord(context.Background(), card, keys, in); err != nil {
		t.Fatalf("a seven day ttl is the maximum, not over it: %v", err)
	}
}

func TestBlessRecordWidening(t *testing.T) {
	card := newTestCard(t)
	keys := []ssh.PublicKey{card.pub}
	first, err := blessRecord(context.Background(), card, keys, testInput())
	if err != nil {
		t.Fatal(err)
	}

	wider := testInput()
	wider.Profiles = map[string]string{
		"worker": "clown-profile-digest-v1@sha256-aaaa",
		"admin":  "clown-profile-digest-v1@sha256-bbbb",
	}
	wider.Nonce = "fedcba9876543210fedcba9876543210"
	wider.Widens = first.Digest
	wider.Widened = &first
	rec, err := blessRecord(context.Background(), card, keys, wider)
	if err != nil {
		t.Fatalf("widening with the earlier record supplied: %v", err)
	}
	if !strings.Contains(card.text, "widening of "+first.Digest) {
		t.Fatalf("act text does not say it is a widening:\n%s", card.text)
	}
	if err := walkRoot(t, rec, keys, first); err != nil {
		t.Fatal(err)
	}

	// A widening that drops a profile of the widened record is not a widening.
	narrower := wider
	narrower.Profiles = map[string]string{"admin": "clown-profile-digest-v1@sha256-bbbb"}
	narrower.Nonce = "00000000000000000000000000000001"
	if _, err := blessRecord(context.Background(), card, keys, narrower); err == nil {
		t.Fatal("a widening dropping a held profile was emitted")
	}

	// The wrong record under the digest is refused up front.
	wrong := wider
	wrong.Widened = &rec
	if _, err := blessRecord(context.Background(), card, keys, wrong); err == nil {
		t.Fatal("a mismatched widened record was accepted")
	}
}

func stubSeams(t *testing.T, card *testCard, signersPath string) {
	t.Helper()
	oldSigner, oldSigners, oldResolve := profileGrantSigner, profileGrantSigners, profileGrantResolve
	t.Cleanup(func() {
		profileGrantSigner, profileGrantSigners, profileGrantResolve = oldSigner, oldSigners, oldResolve
	})
	profileGrantSigner = func() (operatoract.Signer, error) { return card, nil }
	profileGrantSigners = func() string { return signersPath }
	profileGrantResolve = func(name string) (string, error) {
		if name == "missing" {
			return "", errors.New("clown profile not found")
		}
		return "clown-profile-digest-v1@sha256-" + name, nil
	}
}

func writeSignersFor(t *testing.T, pub ssh.PublicKey) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "signers")
	line := "operator namespaces=\"piggy-escalation-v1\" " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + "\n"
	if err := os.WriteFile(p, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func runBless(t *testing.T, f blessFlags) error {
	t.Helper()
	args, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return runProfileGrantBless(context.Background(), args)
}

func TestRunProfileGrantBlessEndToEnd(t *testing.T) {
	card := newTestCard(t)
	stubSeams(t, card, writeSignersFor(t, card.pub))
	out := filepath.Join(t.TempDir(), "record.json")

	err := runBless(t, blessFlags{Grantee: "swift-elder", Profile: []string{"worker", "reviewer"}, Out: out, Onward: "grant", TTL: "2h"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var rec profilegrant.Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	grant, err := profilegrant.ParseCanonicalText(rec.Text)
	if err != nil {
		t.Fatal(err)
	}
	if grant.Onward != profilegrant.OnwardGrant || len(grant.Profiles) != 2 ||
		grant.Profiles["reviewer"] != "clown-profile-digest-v1@sha256-reviewer" {
		t.Fatalf("grant not as requested: %+v", grant)
	}
	if err := walkRootNow(rec, card.pub); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(out); info.Mode().Perm() != 0o600 {
		t.Fatalf("record file mode %v", info.Mode().Perm())
	}
}

func walkRootNow(rec profilegrant.Record, pub ssh.PublicKey) error {
	_, err := profilegrant.Walk(time.Now(), rec,
		func(d string) (profilegrant.Record, bool) { return rec, d == rec.Digest },
		profilegrant.NewVerifier(grantverify.NewRootVerifier([]ssh.PublicKey{pub}), profilegrant.LocalLinkVerifier{}),
		profilegrant.WalkOptions{})
	return err
}

func TestRunProfileGrantBlessRefusesBeforeTheCardIsAsked(t *testing.T) {
	card := newTestCard(t)
	absent := filepath.Join(t.TempDir(), "no-signers")
	stubSeams(t, card, absent)

	cases := map[string]blessFlags{
		"no grantee":       {Profile: []string{"worker"}},
		"no profile":       {Grantee: "g"},
		"unknown profile":  {Grantee: "g", Profile: []string{"missing"}},
		"bad ttl":          {Grantee: "g", Profile: []string{"worker"}, TTL: "soon"},
		"widens no record": {Grantee: "g", Profile: []string{"worker"}, Widens: "d"},
		"record no widens": {Grantee: "g", Profile: []string{"worker"}, WidenedRecord: "x.json"},
		"no signers file":  {Grantee: "g", Profile: []string{"worker"}},
	}
	for name, f := range cases {
		if err := runBless(t, f); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if card.calls != 0 {
		t.Fatalf("the card was asked %d times", card.calls)
	}

	// --no-verify proceeds without a signers file.
	out := filepath.Join(t.TempDir(), "r.json")
	if err := runBless(t, blessFlags{Grantee: "g", Profile: []string{"worker"}, NoVerify: true, Out: out}); err != nil {
		t.Fatal(err)
	}
	if card.calls != 1 {
		t.Fatalf("card calls %d, want 1", card.calls)
	}
}

func TestRunProfileGrantBlessWithWrongSignersWritesNothing(t *testing.T) {
	card, other := newTestCard(t), newTestCard(t)
	stubSeams(t, card, writeSignersFor(t, other.pub))
	out := filepath.Join(t.TempDir(), "r.json")
	if err := runBless(t, blessFlags{Grantee: "g", Profile: []string{"worker"}, Out: out}); err == nil {
		t.Fatal("verification against the wrong key passed")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("a record was written despite the failed verification: %v", err)
	}
}

// An agent must not be able to trigger a card act: the command is CLI-only, so
// it must be absent from the MCP tool list the serve command builds.
func TestProfileGrantBlessIsNotAnMCPTool(t *testing.T) {
	app := buildApp()
	cmd, ok := app.GetCommand("profile-grant-bless")
	if !ok {
		t.Fatal("command is not registered")
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
		if strings.Contains(tool.Name, "bless") || strings.Contains(tool.Name, "profile-grant") || strings.Contains(tool.Name, "profile_grant") {
			t.Fatalf("card-signing command is exposed as MCP tool %q", tool.Name)
		}
	}
}
