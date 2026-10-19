package grantstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/profilegrant"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "grants")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func recordOf(t *testing.T, g profilegrant.Grant, root bool) profilegrant.Record {
	t.Helper()
	text := g.CanonicalText()
	digest, err := profilegrant.DigestOf(text)
	if err != nil {
		t.Fatal(err)
	}
	return profilegrant.Record{Digest: digest, Text: text, Root: root}
}

func grant(grantor, grantee, nonce, prev string, onward profilegrant.Onward) profilegrant.Grant {
	return profilegrant.Grant{
		Grantor:  grantor,
		Grantee:  grantee,
		Session:  "-",
		Profiles: map[string]string{"cheap": "clown-profile-digest-v1@sha256-abc"},
		Onward:   onward,
		Expires:  time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
		Nonce:    nonce,
		Widens:   "-",
		Prev:     prev,
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	rec := recordOf(t, grant("op", "repo/a", "n1", "-", profilegrant.OnwardGrant), true)
	rec.Sig = []byte{1, 2, 3}
	if err := s.Put(rec); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get(rec.Digest)
	if !ok {
		t.Fatal("record not found after Put")
	}
	if got.Digest != rec.Digest || got.Text != rec.Text || !got.Root || string(got.Sig) != "\x01\x02\x03" {
		t.Errorf("round trip mismatch: %+v", got)
	}
}

func TestPutIdempotentAndConflict(t *testing.T) {
	s, _ := openTemp(t)
	rec := recordOf(t, grant("op", "repo/a", "n1", "-", profilegrant.OnwardGrant), true)
	if err := s.Put(rec); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(rec); err != nil {
		t.Errorf("identical Put must be idempotent: %v", err)
	}
	other := rec
	other.Root = false // same digest, different content
	if err := s.Put(other); err == nil {
		t.Error("conflicting Put under the same digest must fail")
	}
}

func TestPutRejectsBadRecords(t *testing.T) {
	s, _ := openTemp(t)
	good := recordOf(t, grant("op", "repo/a", "n1", "-", profilegrant.OnwardGrant), true)

	wrong := good
	wrong.Digest = "piggy-record-digest-v1@sha256-notthedigest"
	if err := s.Put(wrong); err == nil {
		t.Error("wrong-digest Put must fail")
	}

	unparsable := profilegrant.Record{Text: "not a grant\n"}
	unparsable.Digest, _ = profilegrant.DigestOf(unparsable.Text)
	if err := s.Put(unparsable); err == nil {
		t.Error("unparsable text must fail")
	}

	for _, bad := range []string{"../escape", "a/b", ".hidden", "", "a b", `a\b`} {
		rec := good
		rec.Digest = bad
		if err := s.Put(rec); err == nil {
			t.Errorf("digest %q must be rejected", bad)
		}
		if _, ok := s.Get(bad); ok {
			t.Errorf("Get(%q) must not find anything", bad)
		}
	}
}

func TestTamperedFileNotReturned(t *testing.T) {
	s, dir := openTemp(t)
	rec := recordOf(t, grant("op", "repo/a", "n1", "-", profilegrant.OnwardGrant), true)
	if err := s.Put(rec); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, rec.Digest)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), "onward: grant", "onward: none", 1)
	if tampered == string(data) {
		t.Fatal("test setup: nothing replaced")
	}
	if err := os.WriteFile(path, []byte(tampered), fileMode); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(rec.Digest); ok {
		t.Error("tampered record must not be returned")
	}
	if err := os.WriteFile(path, []byte("{not json"), fileMode); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(rec.Digest); ok {
		t.Error("corrupted record must not be returned")
	}
}

func TestBindCurrentUnbind(t *testing.T) {
	s, _ := openTemp(t)
	rec := recordOf(t, grant("op", "repo/a", "n1", "-", profilegrant.OnwardGrant), true)
	if err := s.Put(rec); err != nil {
		t.Fatal(err)
	}
	if err := s.Bind("repo/a", rec.Digest); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Current("repo/a")
	if !ok || got.Digest != rec.Digest {
		t.Fatalf("Current = %+v, %v; want %s", got, ok, rec.Digest)
	}
	if err := s.Unbind("repo/a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Current("repo/a"); ok {
		t.Error("Current after Unbind must be not found")
	}
	if err := s.Unbind("repo/a"); err != nil {
		t.Errorf("Unbind of an absent binding must not error: %v", err)
	}
}

func TestBindRejections(t *testing.T) {
	s, _ := openTemp(t)
	rec := recordOf(t, grant("op", "repo/a", "n1", "-", profilegrant.OnwardGrant), true)
	if err := s.Put(rec); err != nil {
		t.Fatal(err)
	}
	if err := s.Bind("repo/b", rec.Digest); err == nil {
		t.Error("Bind with a mismatching grantee must fail")
	}
	if err := s.Bind("repo/a", "piggy-record-digest-v1@sha256-missing"); err == nil {
		t.Error("Bind to a record not in the store must fail")
	}
	for _, bad := range []string{"../x", "a b", ".dot", "", "a\\b"} {
		if err := s.Bind(bad, rec.Digest); err == nil {
			t.Errorf("Bind grantee %q must be rejected", bad)
		}
		if _, ok := s.Current(bad); ok {
			t.Errorf("Current(%q) must not find anything", bad)
		}
		if err := s.Unbind(bad); err == nil {
			t.Errorf("Unbind grantee %q must be rejected", bad)
		}
	}
}

func TestSwappedIndexFileNotTrusted(t *testing.T) {
	s, dir := openTemp(t)
	a := recordOf(t, grant("op", "repo/a", "n1", "-", profilegrant.OnwardGrant), true)
	b := recordOf(t, grant("op", "repo/b", "n2", "-", profilegrant.OnwardGrant), true)
	for _, r := range []profilegrant.Record{a, b} {
		if err := s.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Bind("repo/a", a.Digest); err != nil {
		t.Fatal(err)
	}
	// Point a's index entry at b's (valid) record.
	if err := os.WriteFile(filepath.Join(dir, "index", "repo%2Fa"), []byte(b.Digest), fileMode); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Current("repo/a"); ok {
		t.Error("an index entry pointing at another grantee's record must not be returned")
	}
}

func TestPermissions(t *testing.T) {
	s, dir := openTemp(t)
	rec := recordOf(t, grant("op", "repo/a", "n1", "-", profilegrant.OnwardGrant), true)
	if err := s.Put(rec); err != nil {
		t.Fatal(err)
	}
	if err := s.Bind("repo/a", rec.Digest); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		path string
		want os.FileMode
	}{
		{dir, dirMode},
		{filepath.Join(dir, "index"), dirMode},
		{filepath.Join(dir, rec.Digest), fileMode},
		{filepath.Join(dir, "index", "repo%2Fa"), fileMode},
	}
	for _, c := range checks {
		info, err := os.Stat(c.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != c.want {
			t.Errorf("%s mode = %o, want %o", c.path, got, c.want)
		}
	}
}

func TestDefaultDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/x/state")
	got, err := DefaultDir()
	if err != nil || got != "/x/state/spinclass/grants" {
		t.Errorf("DefaultDir = %q, %v", got, err)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/h")
	got, err = DefaultDir()
	if err != nil || got != "/h/.local/state/spinclass/grants" {
		t.Errorf("DefaultDir fallback = %q, %v", got, err)
	}
}

func TestResolverWalksChainThroughStore(t *testing.T) {
	s, _ := openTemp(t)
	root := recordOf(t, grant("operator", "repo/a", "n1", "-", profilegrant.OnwardGrant), true)
	link := recordOf(t, grant("repo/a", "repo/b", "n2", root.Digest, profilegrant.OnwardNone), false)
	for _, r := range []profilegrant.Record{root, link} {
		if err := s.Put(r); err != nil {
			t.Fatal(err)
		}
	}

	verifier := profilegrant.NewVerifier(
		func(string, []byte) error { return nil }, // fake card: accepts any root
		profilegrant.LocalLinkVerifier{},
	)
	start, ok := s.Resolver()(link.Digest)
	if !ok {
		t.Fatal("link not resolvable")
	}
	eff, err := profilegrant.Walk(time.Now(), start, s.Resolver(), verifier, profilegrant.WalkOptions{})
	if err != nil {
		t.Fatalf("Walk through the store: %v", err)
	}
	if eff.Grantee != "repo/b" || !profilegrant.Holds(eff, "cheap") {
		t.Errorf("effective grant = %+v", eff)
	}
}
