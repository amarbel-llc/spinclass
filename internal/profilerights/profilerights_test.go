package profilerights

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/grantstore"
	"code.linenisgreat.com/spinclass/internal/operatoract"
	"code.linenisgreat.com/spinclass/internal/profilegrant"
	"code.linenisgreat.com/spinclass/internal/sshsig/sshsigtest"
	"golang.org/x/crypto/ssh"
)

const (
	driverPrincipal = "driver-principal"
	profile         = "cheap"
	digest          = "sha256-aaaa"
)

var now = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

type passes struct{}

func (passes) Check() error { return nil }

type env struct {
	t     *testing.T
	dir   string
	store *grantstore.Store
	card  ed25519.PrivateKey
	c     *Checker
	// principals maps a principal to its session key, as the session index would.
	principals map[string]string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	store, err := grantstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	pub, card, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, dir: dir, store: store, card: card, principals: map[string]string{}}
	e.c = &Checker{
		Store:    store,
		RootKeys: func() ([]ssh.PublicKey, error) { return []ssh.PublicKey{sshsigtest.AuthorizedKey(pub)}, nil },
		SessionOfPrincipal: func(p string) (string, bool) {
			s, ok := e.principals[p]
			return s, ok
		},
		Confinement: passes{},
		Now:         func() time.Time { return now },
	}
	return e
}

// bless stores a card-signed root granting profiles to grantee and binds it.
func (e *env) bless(signer ed25519.PrivateKey, grantee string, profiles map[string]string, onward profilegrant.Onward, expires time.Time) profilegrant.Record {
	e.t.Helper()
	g := profilegrant.Grant{
		Grantor: "operator", Grantee: grantee, Session: "-", Profiles: profiles,
		Onward: onward, Expires: expires, Nonce: "00112233445566778899aabbccddeeff",
		Widens: "-", Prev: "-",
	}
	text := g.CanonicalText()
	d, err := profilegrant.DigestOf(text)
	if err != nil {
		e.t.Fatal(err)
	}
	act, err := profilegrant.ActText(g, d)
	if err != nil {
		e.t.Fatal(err)
	}
	rec := profilegrant.Record{Digest: d, Text: text, Root: true, Sig: sshsigtest.Sign(signer, operatoract.NamespaceEscalation, []byte(act))}
	if err := e.store.Put(rec); err != nil {
		e.t.Fatal(err)
	}
	if err := e.store.Bind(grantee, d); err != nil {
		e.t.Fatal(err)
	}
	return rec
}

func (e *env) root(onward profilegrant.Onward) profilegrant.Record {
	return e.bless(e.card, driverPrincipal, map[string]string{profile: digest, "review": "sha256-bbbb"}, onward, now.Add(time.Hour))
}

func mustAuthorize(t *testing.T, c *Checker, principal, session string) *Authorization {
	t.Helper()
	a, err := c.Authorize(principal, session, profile, digest)
	if err != nil {
		t.Fatalf("Authorize(%q, %q): %v", principal, session, err)
	}
	return a
}

func TestHappyPathAndTheChildCanThenAuthorizeThroughItsSession(t *testing.T) {
	e := newEnv(t)
	root := e.root(profilegrant.OnwardGrant)

	a := mustAuthorize(t, e.c, driverPrincipal, "")
	if a.RecordDigest != root.Digest || !a.MayPassOn {
		t.Fatalf("got %+v", a)
	}

	link, issued, err := e.c.IssueChildLink(a, driverPrincipal, "repo/child", profile, digest)
	if err != nil || !issued {
		t.Fatalf("IssueChildLink: issued=%v err=%v", issued, err)
	}
	if cur, ok := e.store.Current("repo/child"); !ok || cur.Digest != link.Digest {
		t.Fatal("link is not bound to the child's session")
	}
	if got := link.Sig; len(got) != 0 || link.Root {
		t.Fatalf("link must be an unsigned non-root record: %+v", link)
	}

	// The child, running as its own principal in that session, holds the profile
	// but may not pass it on.
	e.principals["child-principal"] = "repo/child"
	child := mustAuthorize(t, e.c, "child-principal", "repo/child")
	if child.MayPassOn {
		t.Error("an onward-none link allows passing on")
	}
	if _, issued, err := e.c.IssueChildLink(child, "child-principal", "repo/grandchild", profile, digest); issued || err != nil {
		t.Errorf("grandchild link: issued=%v err=%v", issued, err)
	}
}

func TestUnestablishedConfinementRefusesBeforeAnythingElse(t *testing.T) {
	e := newEnv(t)
	e.root(profilegrant.OnwardGrant)
	e.c.Confinement = Unestablished{}
	e.c.RootKeys = func() ([]ssh.PublicKey, error) {
		t.Fatal("root keys were read before the confinement check")
		return nil, nil
	}
	_, err := e.c.Authorize(driverPrincipal, "", profile, digest)
	if err == nil {
		t.Fatal("unestablished confinement authorized a spawn")
	}
	for _, want := range []string{"cannot be card-rooted", "spinclass#338", "clown#244", "posh"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

func TestRootKeysFailureFailsClosed(t *testing.T) {
	e := newEnv(t)
	e.root(profilegrant.OnwardGrant)
	e.c.RootKeys = func() ([]ssh.PublicKey, error) { return nil, errors.New("signers file /x: missing") }
	if _, err := e.c.Authorize(driverPrincipal, "", profile, digest); err == nil || !strings.Contains(err.Error(), "signers") {
		t.Fatalf("got %v", err)
	}
}

func TestRefusesWithoutAnyIdentityOrGrant(t *testing.T) {
	e := newEnv(t)
	if _, err := e.c.Authorize("", "", profile, digest); err == nil {
		t.Error("empty principal and session authorized")
	}
	_, err := e.c.Authorize(driverPrincipal, "repo/s", profile, digest)
	if err == nil || !strings.Contains(err.Error(), "sc profile-grant-bless") || !strings.Contains(err.Error(), "sc profile-grant-import") {
		t.Fatalf("missing-grant error should say how to fix it: %v", err)
	}
}

func TestExpiredRootRefused(t *testing.T) {
	e := newEnv(t)
	e.bless(e.card, driverPrincipal, map[string]string{profile: digest}, profilegrant.OnwardGrant, now.Add(-time.Minute))
	if _, err := e.c.Authorize(driverPrincipal, "", profile, digest); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("got %v", err)
	}
}

func TestRootSignedByAnotherKeyRefused(t *testing.T) {
	e := newEnv(t)
	_, impostor, _ := ed25519.GenerateKey(rand.Reader)
	e.bless(impostor, driverPrincipal, map[string]string{profile: digest}, profilegrant.OnwardGrant, now.Add(time.Hour))
	if _, err := e.c.Authorize(driverPrincipal, "", profile, digest); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("got %v", err)
	}
}

func TestProfileNotHeldListsWhatIsHeld(t *testing.T) {
	e := newEnv(t)
	e.root(profilegrant.OnwardNone)
	_, err := e.c.Authorize(driverPrincipal, "", "secret", "sha256-cccc")
	if err == nil || !strings.Contains(err.Error(), "cheap, review") {
		t.Fatalf("got %v", err)
	}
}

func TestChangedDigestIsAHardError(t *testing.T) {
	e := newEnv(t)
	e.root(profilegrant.OnwardGrant)
	_, err := e.c.Authorize(driverPrincipal, "", profile, "sha256-new")
	want := "profile cheap changed since it was granted (granted sha256-aaaa, now sha256-new): an authority must re-pin it"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestOnwardNoneGivesNoLinkAndNoChildAuthority(t *testing.T) {
	e := newEnv(t)
	e.root(profilegrant.OnwardNone)
	a := mustAuthorize(t, e.c, driverPrincipal, "")
	if a.MayPassOn {
		t.Fatal("onward none may pass on")
	}
	rec, issued, err := e.c.IssueChildLink(a, driverPrincipal, "repo/child", profile, digest)
	if issued || err != nil || rec.Digest != "" {
		t.Fatalf("issued=%v err=%v rec=%+v", issued, err, rec)
	}
	if _, ok := e.store.Current("repo/child"); ok {
		t.Error("a link was bound for an onward-none spawner")
	}
	e.principals["child-principal"] = "repo/child"
	if _, err := e.c.Authorize("child-principal", "repo/child", profile, digest); err == nil {
		t.Error("the child of an onward-none holder was authorized")
	}
}

func TestIssueChildLinkNeedsAGrantorPrincipal(t *testing.T) {
	e := newEnv(t)
	e.root(profilegrant.OnwardGrant)
	a := mustAuthorize(t, e.c, driverPrincipal, "")
	if _, _, err := e.c.IssueChildLink(a, "", "repo/child", profile, digest); err == nil {
		t.Error("link issued with no grantor")
	}
}

func TestBadChildSessionLeavesNothingBound(t *testing.T) {
	e := newEnv(t)
	e.root(profilegrant.OnwardGrant)
	a := mustAuthorize(t, e.c, driverPrincipal, "")
	if _, _, err := e.c.IssueChildLink(a, driverPrincipal, "repo/has space", profile, digest); err == nil {
		t.Fatal("a session key outside the identity allowlist produced a link")
	}
}

// A session-keyed grant is only the driver's when the driver's principal maps to
// that session; otherwise a shell could cd into another worktree and claim its
// grant by cwd alone.
func TestSessionGrantNeedsThePrincipalToMapToThatSession(t *testing.T) {
	e := newEnv(t)
	e.bless(e.card, "repo/other", map[string]string{profile: digest}, profilegrant.OnwardNone, now.Add(time.Hour))

	cases := []struct {
		name      string
		principal string
		mapped    string // "" = no mapping
		wantOK    bool
	}{
		{"principal maps elsewhere", "intruder", "repo/mine", false},
		{"principal has no mapping", "stranger", "", false},
		{"principal maps to the session", "owner", "repo/other", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.mapped != "" {
				e.principals[c.principal] = c.mapped
			}
			_, err := e.c.Authorize(c.principal, "repo/other", profile, digest)
			if (err == nil) != c.wantOK {
				t.Errorf("Authorize err = %v, wantOK %v", err, c.wantOK)
			}
		})
	}

	// With no principal the session record is never used either.
	if _, err := e.c.Authorize("", "repo/other", profile, digest); err == nil {
		t.Error("a session-only claim authorized without a principal")
	}
}

func TestPrincipalBoundGrantWorksWithoutAnySession(t *testing.T) {
	e := newEnv(t)
	e.root(profilegrant.OnwardNone)
	// An unrelated, unmapped session claim does not disturb the principal lookup.
	mustAuthorize(t, e.c, driverPrincipal, "repo/anything")
	mustAuthorize(t, e.c, driverPrincipal, "")
}

func TestIssueChildLinkRefusesAKeyThatAlreadyHasABinding(t *testing.T) {
	e := newEnv(t)
	e.root(profilegrant.OnwardGrant)
	a := mustAuthorize(t, e.c, driverPrincipal, "")

	first, issued, err := e.c.IssueChildLink(a, driverPrincipal, "repo/child", profile, digest)
	if err != nil || !issued {
		t.Fatalf("first link: issued=%v err=%v", issued, err)
	}
	if _, _, err := e.c.IssueChildLink(a, driverPrincipal, "repo/child", profile, digest); err == nil {
		t.Fatal("a second link overwrote an existing binding")
	}
	if cur, ok := e.store.Current("repo/child"); !ok || cur.Digest != first.Digest {
		t.Error("the original binding was disturbed")
	}

	// A key bound to something else entirely (an operator-blessed grant) is
	// likewise not inherited or replaced.
	e.bless(e.card, "repo/blessed", map[string]string{profile: digest}, profilegrant.OnwardNone, now.Add(time.Hour))
	if _, _, err := e.c.IssueChildLink(a, driverPrincipal, "repo/blessed", profile, digest); err == nil {
		t.Error("a link was minted over a blessed grant's key")
	}
}

func TestRevokeSessionUnbindsOnlyWhenTheStoreExists(t *testing.T) {
	e := newEnv(t)
	e.root(profilegrant.OnwardGrant)
	a := mustAuthorize(t, e.c, driverPrincipal, "")
	if _, _, err := e.c.IssueChildLink(a, driverPrincipal, "repo/child", profile, digest); err != nil {
		t.Fatal(err)
	}

	if err := RevokeSessionIn(e.dir, "repo/child"); err != nil {
		t.Fatalf("RevokeSessionIn: %v", err)
	}
	if _, ok := e.store.Current("repo/child"); ok {
		t.Error("binding survived revocation")
	}
	if _, ok := e.store.Current(driverPrincipal); !ok {
		t.Error("revoking one session unbound another grantee")
	}
	// Idempotent.
	if err := RevokeSessionIn(e.dir, "repo/child"); err != nil {
		t.Errorf("second revoke: %v", err)
	}
}

func TestRevokeSessionIsANoOpWithoutAStoreAndCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "no", "grants")
	if err := RevokeSessionIn(dir, "repo/child"); err != nil {
		t.Fatalf("RevokeSessionIn on a missing store: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(dir)); err == nil {
		t.Error("revocation created directories for a store that does not exist")
	}
}

func TestRevokeSessionReportsAnUnusableStore(t *testing.T) {
	file := filepath.Join(t.TempDir(), "grants")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RevokeSessionIn(file, "repo/child"); err == nil {
		t.Error("a store path that is a file reported success")
	}
}

func TestTamperedStoredRootCountsAsAbsent(t *testing.T) {
	e := newEnv(t)
	root := e.root(profilegrant.OnwardGrant)
	path := filepath.Join(e.dir, root.Digest)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Flip the onward right inside the stored text; the digest no longer matches.
	tampered := strings.Replace(string(data), "onward: grant", "onward: none ", 1)
	if tampered == string(data) {
		t.Fatal("test did not tamper with anything")
	}
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.Authorize(driverPrincipal, "", profile, digest); err == nil || !strings.Contains(err.Error(), "no profile grant") {
		t.Fatalf("got %v", err)
	}
}
