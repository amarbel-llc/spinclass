package spawn

import (
	"bytes"
	"encoding/hex"
	"errors"
	"maps"
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

// The pin spinclass hands clown (#407, clown FDR 0022): the recorded digest as
// clown's hex, and on a resume the session it is bound to. CLOWN_PROFILE is
// deliberately absent: --profile names the profile.
func TestProfileLaunchEnv(t *testing.T) {
	hexDigest := hex.EncodeToString(fixedDigest())
	const resumeID = "1d3a5c7e-9b0f-4d2a-8e6c-0a1b2c3d4e5f"

	if want, got := []string{"CLOWN_PROFILE_DIGEST=" + hexDigest, "CLOWN_PROFILE_SESSION=" + resumeID}, ProfileLaunchEnv(hexDigest, resumeID); !slices.Equal(got, want) {
		t.Errorf("resume env = %v, want %v", got, want)
	}
	if want, got := []string{"CLOWN_PROFILE_DIGEST=" + hexDigest}, ProfileLaunchEnv(hexDigest, ""); !slices.Equal(got, want) {
		t.Errorf("fresh-launch env = %v, want %v", got, want)
	}
	if got := ProfileLaunchEnv("", resumeID); got != nil {
		t.Errorf("no profile: got %v, want nil", got)
	}
}

// The resolver carries clown's own digest verbatim beside the recorded markl
// form: that string is what clown compares CLOWN_PROFILE_DIGEST with.
func TestClownResolverCarriesClownsHexDigest(t *testing.T) {
	r := clownResolver{lookup: fakeLookup(resolvedWith(profilelookup.Selection{}), nil)}
	rp, err := r.Resolve("cheap")
	if err != nil {
		t.Fatal(err)
	}
	if want := hex.EncodeToString(fixedDigest()); rp.DigestHex != want {
		t.Errorf("DigestHex = %q, want %q", rp.DigestHex, want)
	}
}

// A profile without clown's digest cannot be pinned, so resolution refuses it
// rather than letting a worker launch unpinned (#407).
func TestResolveProfileRefusesAProfileWithoutAClownDigest(t *testing.T) {
	withResolver(t, profileResolverFunc(func(name string) (ResolvedProfile, error) {
		return ResolvedProfile{Name: name, Digest: "digest-x", DigestHex: ""}, nil
	}))
	if _, err := resolveProfile("cheap"); err == nil || !strings.Contains(err.Error(), "clown digest") {
		t.Fatalf("err = %v, want a refusal naming the missing clown digest", err)
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

func TestProfileNotFoundNamesLinkedClownRev(t *testing.T) {
	prev := LinkedClownRev
	t.Cleanup(func() { LinkedClownRev = prev })
	LinkedClownRev = "8a2b9fe"

	err := ProfileNotFound("canary-grit")
	if !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("err = %v, want ErrProfileNotFound", err)
	}
	want := `clown profile not found: "canary-grit" (spinclass links clown 8a2b9fe; the clown that runs the spawn may be newer)`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestLinkedClownRevDevFallback(t *testing.T) {
	if LinkedClownRev == "" {
		t.Error("LinkedClownRev must never be empty; dev builds read 'unknown (not a nix build)'")
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

func TestClownResolverMapsEnvAndBase(t *testing.T) {
	baseDigest := make([]byte, 32)
	baseDigest[0] = 0xff
	res := resolvedWith(profilelookup.Selection{})
	res.Name = "mine"
	res.Source = profilelookup.SourceUser
	res.Env = map[string]string{"KEY": "aa"}
	res.Base = &profilelookup.Resolved{
		Name: "base-b", Source: profilelookup.SourceBuiltin, Model: "opus",
		Env:          map[string]string{"KEY": "aa"},
		DigestSHA256: hex.EncodeToString(baseDigest),
	}

	got, err := clownResolver{lookup: fakeLookup(res, nil)}.Resolve("mine")
	if err != nil {
		t.Fatal(err)
	}
	if !got.EnvKnown || !maps.Equal(got.Env, res.Env) {
		t.Errorf("EnvKnown=%v Env=%v, want true and %v", got.EnvKnown, got.Env, res.Env)
	}
	got.Env["KEY"] = "mutated"
	if res.Env["KEY"] != "aa" {
		t.Error("Env aliases the library's map; it must be cloned")
	}
	wantBaseDigest, err := profileDigestMarklID(hex.EncodeToString(baseDigest))
	if err != nil {
		t.Fatal(err)
	}
	if got.Base != "base-b" || got.BaseProfile == nil {
		t.Fatalf("Base=%q BaseProfile=%v, want base-b and a resolved base", got.Base, got.BaseProfile)
	}
	if got.BaseProfile.Digest != wantBaseDigest || got.BaseProfile.Source != "builtin" ||
		got.BaseProfile.Model != "opus" || !got.BaseProfile.EnvKnown {
		t.Errorf("BaseProfile = %+v, want digest %s, source builtin, model opus, env known", got.BaseProfile, wantBaseDigest)
	}
}

func TestClownResolverNoBase(t *testing.T) {
	got, err := clownResolver{lookup: fakeLookup(resolvedWith(profilelookup.Selection{}), nil)}.Resolve("cheap")
	if err != nil {
		t.Fatal(err)
	}
	if got.Base != "" || got.BaseProfile != nil {
		t.Errorf("Base=%q BaseProfile=%v, want neither", got.Base, got.BaseProfile)
	}
}

func TestClownResolverRefusesABrokenBase(t *testing.T) {
	baseErr := &profilelookup.BaseError{Profile: "mine", Base: "gone", Reason: `no builtin profile is named "gone"`}
	_, err := clownResolver{lookup: fakeLookup(nil, baseErr)}.Resolve("mine")
	if err == nil || !strings.Contains(err.Error(), `"mine"`) || !strings.Contains(err.Error(), baseErr.Error()) {
		t.Errorf("err = %v, want a refusal naming the profile and carrying %q", err, baseErr.Error())
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
