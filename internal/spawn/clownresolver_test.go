package spawn

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/clown/pkg/profilelookup"
	"code.linenisgreat.com/piggy/go/pkgs/markl"
)

// fixedDigest is bytes 0x00..0x1f: a 32-byte sha256-sized value with no
// structure to hide an endianness or truncation bug.
func fixedDigest() []byte {
	d := make([]byte, 32)
	for i := range d {
		d[i] = byte(i)
	}
	return d
}

func fakeLookup(res *profilelookup.Resolved, err error) func(string, profilelookup.Options) (*profilelookup.Resolved, error) {
	return func(string, profilelookup.Options) (*profilelookup.Resolved, error) { return res, err }
}

func resolvedWith(sel profilelookup.Selection) *profilelookup.Resolved {
	return &profilelookup.Resolved{
		Name:         "cheap",
		Selection:    sel,
		Canonical:    []byte(`{"env":{"SECRET":"hunter2"}}`),
		DigestSHA256: hex.EncodeToString(fixedDigest()),
	}
}

func TestProfileDigestMarklIDGolden(t *testing.T) {
	got, err := profileDigestMarklID(hex.EncodeToString(fixedDigest()))
	if err != nil {
		t.Fatal(err)
	}
	const want = "clown-profile-digest-v1@sha256-qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0s7lcgm6"
	if got != want {
		t.Errorf("digest = %q, want %q", got, want)
	}
}

func TestProfileDigestMarklIDRoundTrips(t *testing.T) {
	text, err := profileDigestMarklID(hex.EncodeToString(fixedDigest()))
	if err != nil {
		t.Fatal(err)
	}
	var id markl.Id
	if err := id.Set(text); err != nil {
		t.Fatalf("Set(%q): %v", text, err)
	}
	if !bytes.Equal(id.GetBytes(), fixedDigest()) {
		t.Errorf("decoded bytes = %x, want %x", id.GetBytes(), fixedDigest())
	}
}

func TestProfileDigestMarklIDRejectsBadInput(t *testing.T) {
	for _, in := range []string{"zz", "00ff", ""} {
		if got, err := profileDigestMarklID(in); err == nil {
			t.Errorf("profileDigestMarklID(%q) = %q, want an error", in, got)
		}
	}
}

func TestClownResolverMapsSelection(t *testing.T) {
	required := profilelookup.RequiredServers()
	cases := []struct {
		name     string
		sel      profilelookup.Selection
		wantKept []string
		wantNil  bool
	}{
		{name: "no saved selection keeps everything", sel: profilelookup.Selection{}, wantNil: true},
		{
			name:     "selection passes through",
			sel:      profilelookup.Selection{Selective: true, Kept: append(slices.Clone(required), "x/y")},
			wantKept: append(slices.Clone(required), "x/y"),
		},
		{name: "a selection that kept nothing is non-nil", sel: profilelookup.Selection{Selective: true}, wantKept: []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := clownResolver{lookup: fakeLookup(resolvedWith(c.sel), nil)}
			got, err := r.Resolve("cheap")
			if err != nil {
				t.Fatal(err)
			}
			if c.wantNil != (got.KeptServers == nil) || !slices.Equal(got.KeptServers, c.wantKept) {
				t.Errorf("KeptServers = %#v, want %#v (nil=%v)", got.KeptServers, c.wantKept, c.wantNil)
			}
			if got.Name != "cheap" || !strings.HasPrefix(got.Digest, ProfileDigestPurpose+"@sha256-") {
				t.Errorf("got Name=%q Digest=%q", got.Name, got.Digest)
			}
		})
	}
}

func TestClownResolverRefusesCrippledRequiredServer(t *testing.T) {
	required := profilelookup.RequiredServers()
	sel := profilelookup.Selection{
		Selective: true,
		Kept:      required,
		Excluded:  map[string][]string{required[1]: {"send"}},
	}
	_, err := clownResolver{lookup: fakeLookup(resolvedWith(sel), nil)}.Resolve("cheap")
	if err == nil || !strings.Contains(err.Error(), required[1]) {
		t.Fatalf("err = %v, want a refusal naming %s", err, required[1])
	}
}

func TestClownResolverMapsErrors(t *testing.T) {
	notFound := fakeLookup(nil, errors.Join(profilelookup.ErrNotFound, errors.New("known: a, b")))
	if _, err := (clownResolver{lookup: notFound}).Resolve("nope"); !errors.Is(err, ErrProfileNotFound) {
		t.Errorf("err = %v, want ErrProfileNotFound", err)
	}

	skew := &profilelookup.SkewError{RegistryPath: "/r/profiles.toml", LibraryProtocol: 1}
	_, err := clownResolver{lookup: fakeLookup(nil, skew)}.Resolve("cheap")
	if err == nil || !strings.Contains(err.Error(), skew.Error()) || !strings.Contains(err.Error(), `"cheap"`) {
		t.Errorf("err = %v, want the library skew message verbatim and the profile name", err)
	}

	_, err = clownResolver{lookup: fakeLookup(nil, errors.New("boom"))}.Resolve("cheap")
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), `"cheap"`) {
		t.Errorf("err = %v, want the cause wrapped with the profile name", err)
	}
}

func TestClownResolverNeverLeaksCanonical(t *testing.T) {
	sel := profilelookup.Selection{Selective: true, Kept: profilelookup.RequiredServers(), Excluded: map[string][]string{profilelookup.RequiredServers()[0]: {"t"}}}
	_, err := clownResolver{lookup: fakeLookup(resolvedWith(sel), nil)}.Resolve("cheap")
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("err = %v, must refuse without echoing canonical bytes", err)
	}
}

// TestClownResolverSkewsOnUnstampedRegistry drives the real library: a
// registry no clown has stamped is refused until `clown profile` restamps it.
func TestClownResolverSkewsOnUnstampedRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.toml")
	if err := os.WriteFile(path, []byte("[[profiles]]\nname = \"cheap\"\nprovider = \"claude\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newClownResolver()
	r.registryPath = path

	_, err := r.Resolve("cheap")
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "clown profile") {
		t.Fatalf("err = %v, want the library's restamp hint naming the registry", err)
	}
}
