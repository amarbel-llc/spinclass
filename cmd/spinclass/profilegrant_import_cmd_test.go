package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/server"
	"code.linenisgreat.com/spinclass/internal/0/profilegrant"
	"code.linenisgreat.com/spinclass/internal/alfa/grantstore"
	"golang.org/x/crypto/ssh"
)

type importEnv struct {
	dir  string
	out  *bytes.Buffer
	card *testCard
}

// stubImportSeams points the command at a temp store and a signers file that
// trusts trusted, with the clock at testNow.
func stubImportSeams(t *testing.T, trusted ssh.PublicKey) *importEnv {
	t.Helper()
	oldDir, oldSigners, oldNow := profileGrantImportStoreDir, profileGrantImportSigners, profileGrantImportNow
	oldStdin, oldOut := profileGrantImportStdin, profileGrantImportOut
	t.Cleanup(func() {
		profileGrantImportStoreDir, profileGrantImportSigners, profileGrantImportNow = oldDir, oldSigners, oldNow
		profileGrantImportStdin, profileGrantImportOut = oldStdin, oldOut
	})
	env := &importEnv{dir: filepath.Join(t.TempDir(), "grants"), out: &bytes.Buffer{}}
	signers := writeSignersFor(t, trusted)
	profileGrantImportStoreDir = func() (string, error) { return env.dir, nil }
	profileGrantImportSigners = func() string { return signers }
	profileGrantImportNow = func() time.Time { return testNow }
	profileGrantImportOut = func() io.Writer { return env.out }
	return env
}

func (e *importEnv) store(t *testing.T) *grantstore.Store {
	t.Helper()
	s, err := grantstore.Open(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func blessForImport(t *testing.T, card *testCard, mutate func(*blessInput)) profilegrant.Record {
	t.Helper()
	in := testInput()
	if mutate != nil {
		mutate(&in)
	}
	rec, err := blessRecord(context.Background(), card, []ssh.PublicKey{card.pub}, in)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func writeRecordsFile(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "rec.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func runImport(t *testing.T, file string) error {
	t.Helper()
	args, err := json.Marshal(map[string]string{"file": file})
	if err != nil {
		t.Fatal(err)
	}
	return runProfileGrantImport(context.Background(), args)
}

func requireErrContaining(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want an error containing %q", err, want)
	}
}

func requireNothingStored(t *testing.T, env *importEnv, recs ...profilegrant.Record) {
	t.Helper()
	s := env.store(t)
	for _, r := range recs {
		if _, ok := s.Get(r.Digest); ok {
			t.Fatalf("record %s was stored", r.Digest)
		}
		g, err := profilegrant.ParseCanonicalText(r.Text)
		if err != nil {
			continue
		}
		if _, ok := s.Current(g.Grantee); ok {
			t.Fatalf("grantee %s was bound", g.Grantee)
		}
	}
}

func TestImportStoresAndBindsAValidRoot(t *testing.T) {
	card := newTestCard(t)
	env := stubImportSeams(t, card.pub)
	rec := blessForImport(t, card, nil)

	if err := runImport(t, writeRecordsFile(t, rec)); err != nil {
		t.Fatal(err)
	}
	s := env.store(t)
	if got, ok := s.Get(rec.Digest); !ok || got.Digest != rec.Digest {
		t.Fatal("record not stored")
	}
	if cur, ok := s.Current("swift-elder"); !ok || cur.Digest != rec.Digest {
		t.Fatal("record not bound as the grantee's current")
	}
	for _, want := range []string{
		rec.Digest, "grantee: swift-elder", "session: -",
		"worker=clown-profile-digest-v1@sha256-aaaa", "onward: none",
		"expires: 2026-10-19T13:00:00Z",
	} {
		if !strings.Contains(env.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, env.out.String())
		}
	}

	// Importing the identical record again is a no-op success.
	if err := runImport(t, writeRecordsFile(t, rec)); err != nil {
		t.Fatalf("re-import: %v", err)
	}
}

func TestImportWideningInTheSameBatch(t *testing.T) {
	card := newTestCard(t)
	env := stubImportSeams(t, card.pub)
	first := blessForImport(t, card, nil)
	wider := blessForImport(t, card, func(in *blessInput) {
		in.Profiles = map[string]string{
			"worker": "clown-profile-digest-v1@sha256-aaaa",
			"admin":  "clown-profile-digest-v1@sha256-bbbb",
		}
		in.Nonce = "fedcba9876543210fedcba9876543210"
		in.Widens = first.Digest
		in.Widened = &first
	})

	// The widening is listed first: the command must order them itself.
	if err := runImport(t, writeRecordsFile(t, []profilegrant.Record{wider, first})); err != nil {
		t.Fatal(err)
	}
	s := env.store(t)
	for _, r := range []profilegrant.Record{first, wider} {
		if _, ok := s.Get(r.Digest); !ok {
			t.Fatalf("record %s not stored", r.Digest)
		}
	}
	if cur, ok := s.Current("swift-elder"); !ok || cur.Digest != wider.Digest {
		t.Fatal("the widening is not the grantee's current record")
	}
	if !strings.Contains(env.out.String(), "current for grantee: false") {
		t.Fatalf("the widened record was reported as current:\n%s", env.out.String())
	}
}

func TestImportWideningNeedsTheWidenedRecord(t *testing.T) {
	card := newTestCard(t)
	env := stubImportSeams(t, card.pub)
	first := blessForImport(t, card, nil)
	wider := blessForImport(t, card, func(in *blessInput) {
		in.Profiles = map[string]string{
			"worker": "clown-profile-digest-v1@sha256-aaaa",
			"admin":  "clown-profile-digest-v1@sha256-bbbb",
		}
		in.Nonce = "fedcba9876543210fedcba9876543210"
		in.Widens = first.Digest
		in.Widened = &first
	})

	requireErrContaining(t, runImport(t, writeRecordsFile(t, wider)), "widened record")
	requireNothingStored(t, env, wider, first)

	// Once the widened record is in the store, the widening imports alone.
	if err := runImport(t, writeRecordsFile(t, first)); err != nil {
		t.Fatal(err)
	}
	if err := runImport(t, writeRecordsFile(t, wider)); err != nil {
		t.Fatalf("widening with the widened record already stored: %v", err)
	}
	if cur, ok := env.store(t).Current("swift-elder"); !ok || cur.Digest != wider.Digest {
		t.Fatal("the widening did not become current")
	}
}

func TestImportRefusesAKeyOutsideTheSignersFile(t *testing.T) {
	card, other := newTestCard(t), newTestCard(t)
	env := stubImportSeams(t, other.pub)
	rec := blessForImport(t, card, nil)

	if err := runImport(t, writeRecordsFile(t, rec)); err == nil {
		t.Fatal("a record signed by an unlisted key was imported")
	}
	requireNothingStored(t, env, rec)
}

func TestImportRefusesNonRootRecords(t *testing.T) {
	card := newTestCard(t)
	env := stubImportSeams(t, card.pub)
	rec := blessForImport(t, card, nil)
	rec.Root = false

	requireErrContaining(t, runImport(t, writeRecordsFile(t, rec)), "minted by spawn")
	requireNothingStored(t, env, rec)
}

func TestImportRefusesTamperedText(t *testing.T) {
	card := newTestCard(t)
	env := stubImportSeams(t, card.pub)
	rec := blessForImport(t, card, nil)
	tampered := rec
	tampered.Text = strings.Replace(rec.Text, "onward: none", "onward: grant", 1)
	if tampered.Text == rec.Text {
		t.Fatal("test did not tamper with the text")
	}

	requireErrContaining(t, runImport(t, writeRecordsFile(t, tampered)), "digest mismatch")
	requireNothingStored(t, env, tampered, rec)
}

func TestImportRefusesAnExpiredRecord(t *testing.T) {
	card := newTestCard(t)
	env := stubImportSeams(t, card.pub)
	rec := blessForImport(t, card, nil)
	profileGrantImportNow = func() time.Time { return testNow.Add(2 * time.Hour) }

	requireErrContaining(t, runImport(t, writeRecordsFile(t, rec)), "expired")
	requireNothingStored(t, env, rec)
}

func TestImportRefusesAGranteeOutsideTheIdentityAllowlist(t *testing.T) {
	card := newTestCard(t)
	env := stubImportSeams(t, card.pub)
	rec := blessForImport(t, card, nil)
	bad := rec
	bad.Text = strings.Replace(rec.Text, "grantee: swift-elder", "grantee: bad grantee", 1)
	d, err := profilegrant.DigestOf(bad.Text)
	if err != nil {
		t.Fatal(err)
	}
	bad.Digest = d

	requireErrContaining(t, runImport(t, writeRecordsFile(t, bad)), "grantee")
	requireNothingStored(t, env, rec)
}

func TestImportReadsStdin(t *testing.T) {
	card := newTestCard(t)
	env := stubImportSeams(t, card.pub)
	rec := blessForImport(t, card, nil)
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	profileGrantImportStdin = func() io.Reader { return bytes.NewReader(b) }

	if err := runImport(t, "-"); err != nil {
		t.Fatal(err)
	}
	if cur, ok := env.store(t).Current("swift-elder"); !ok || cur.Digest != rec.Digest {
		t.Fatal("stdin record not stored and bound")
	}
}

func TestImportWithoutASignersFileStoresNothing(t *testing.T) {
	card := newTestCard(t)
	env := stubImportSeams(t, card.pub)
	rec := blessForImport(t, card, nil)
	profileGrantImportSigners = func() string { return filepath.Join(t.TempDir(), "no-signers") }

	if err := runImport(t, writeRecordsFile(t, rec)); err == nil {
		t.Fatal("imported without a signers file")
	}
	requireNothingStored(t, env, rec)
}

func TestImportOfABadBatchStoresNothing(t *testing.T) {
	card := newTestCard(t)
	env := stubImportSeams(t, card.pub)
	good := blessForImport(t, card, nil)
	bad := blessForImport(t, card, func(in *blessInput) {
		in.Grantee = "other-one"
		in.Nonce = "00000000000000000000000000000002"
	})
	bad.Text += "x"

	if err := runImport(t, writeRecordsFile(t, []profilegrant.Record{good, bad})); err == nil {
		t.Fatal("a batch with a tampered record was imported")
	}
	requireNothingStored(t, env, good)
}

func runImportReplace(t *testing.T, file string) error {
	t.Helper()
	args, err := json.Marshal(map[string]any{"file": file, "replace": true})
	if err != nil {
		t.Fatal(err)
	}
	return runProfileGrantImport(context.Background(), args)
}

func TestImportDoesNotSilentlyReplaceAnExistingBinding(t *testing.T) {
	card := newTestCard(t)
	env := stubImportSeams(t, card.pub)
	first := blessForImport(t, card, nil)
	second := blessForImport(t, card, func(in *blessInput) { in.Nonce = "fedcba9876543210fedcba9876543210" })
	if err := runImport(t, writeRecordsFile(t, first)); err != nil {
		t.Fatal(err)
	}

	requireErrContaining(t, runImport(t, writeRecordsFile(t, second)), "--replace")
	s := env.store(t)
	if cur, ok := s.Current("swift-elder"); !ok || cur.Digest != first.Digest {
		t.Fatal("the existing binding was disturbed")
	}
	if _, ok := s.Get(second.Digest); ok {
		t.Fatal("a refused import stored its record")
	}

	if err := runImportReplace(t, writeRecordsFile(t, second)); err != nil {
		t.Fatalf("--replace: %v", err)
	}
	if cur, ok := env.store(t).Current("swift-elder"); !ok || cur.Digest != second.Digest {
		t.Fatal("--replace did not rebind")
	}
}

func TestImportBindsOnlyAfterEveryRecordIsStored(t *testing.T) {
	card := newTestCard(t)
	env := stubImportSeams(t, card.pub)
	good := blessForImport(t, card, nil)
	other := blessForImport(t, card, func(in *blessInput) {
		in.Grantee = "other-one"
		in.Nonce = "00000000000000000000000000000002"
	})
	// A directory squatting on the second record's file makes its Put fail.
	if err := os.MkdirAll(filepath.Join(env.dir, other.Digest, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	env.store(t)

	if err := runImport(t, writeRecordsFile(t, []profilegrant.Record{good, other})); err == nil {
		t.Fatal("import succeeded despite a failing Put")
	}
	if _, ok := env.store(t).Current("swift-elder"); ok {
		t.Fatal("a grantee was bound before every record was stored")
	}
}

func TestProfileGrantImportIsNotAnMCPTool(t *testing.T) {
	app := buildApp()
	cmd, ok := app.GetCommand("profile-grant-import")
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
		if strings.Contains(tool.Name, "import") || strings.Contains(tool.Name, "profile-grant") || strings.Contains(tool.Name, "profile_grant") {
			t.Fatalf("grant-import command is exposed as MCP tool %q", tool.Name)
		}
	}
}
