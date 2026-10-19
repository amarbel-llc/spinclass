package profilegrant

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

var (
	now    = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	expiry = time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC)
)

func rootGrant() Grant {
	return Grant{
		Grantor:  "operator",
		Grantee:  "root-principal",
		Session:  "-",
		Profiles: map[string]string{"cheap": "d-cheap", "review": "d-review"},
		Onward:   OnwardGrant,
		Expires:  expiry,
		Nonce:    "00aa",
		Widens:   "-",
		Prev:     "-",
	}
}

func TestRoundTrip(t *testing.T) {
	g := rootGrant()
	text := g.CanonicalText()
	want := `spinclass profile-grant v1
grantor: operator
grantee: root-principal
session: -
profiles: cheap=d-cheap review=d-review
rights: spinclass:profile:cheap spinclass:profile:review
onward: grant
expires: 2026-10-20T14:00:00Z
nonce: 00aa
widens: -
prev: -
`
	if text != want {
		t.Fatalf("canonical text:\n%s\nwant:\n%s", text, want)
	}
	got, err := ParseCanonicalText(text)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, g) {
		t.Fatalf("round trip: got %+v, want %+v", got, g)
	}
}

func TestParseRejections(t *testing.T) {
	valid := rootGrant().CanonicalText()
	lines := strings.Split(strings.TrimSuffix(valid, "\n"), "\n")
	withLine := func(i int, replacement string) string {
		out := append([]string(nil), lines...)
		out[i] = replacement
		return strings.Join(out, "\n") + "\n"
	}
	swapped := append([]string(nil), lines...)
	swapped[2], swapped[3] = swapped[3], swapped[2]

	tests := []struct {
		name string
		text string
	}{
		{"wrong header", withLine(0, "spinclass profile-grant v2")},
		{"missing trailing newline", strings.TrimSuffix(valid, "\n")},
		{"missing line", strings.Join(lines[:len(lines)-1], "\n") + "\n"},
		{"extra line", valid + "extra: x\n"},
		{"duplicate line", strings.Join(append(append([]string(nil), lines...), lines[len(lines)-1]), "\n") + "\n"},
		{"reordered lines", strings.Join(swapped, "\n") + "\n"},
		{"empty value", withLine(1, "grantor: ")},
		{"rights mismatch", withLine(5, "rights: spinclass:profile:cheap")},
		{"rights unsorted", withLine(5, "rights: spinclass:profile:review spinclass:profile:cheap")},
		{"bad time", withLine(7, "expires: tomorrow")},
		{"non-utc time", withLine(7, "expires: 2026-10-20T14:00:00+02:00")},
		{"onward other", withLine(6, "onward: always")},
		{"empty profile name", withLine(4, "profiles: =d")},
		{"empty profile digest", withLine(4, "profiles: cheap=")},
		{"profile without digest", withLine(4, "profiles: cheap")},
		{"profiles unsorted", withLine(4, "profiles: review=d-review cheap=d-cheap")},
		{"profiles duplicated", withLine(4, "profiles: cheap=a cheap=b")},
		{"control character", withLine(8, "nonce: 00\x07aa")},
		{"carriage return", withLine(8, "nonce: 00aa\r")},
		{"oversize", withLine(8, "nonce: "+strings.Repeat("a", 5000))},
		{"missing widens", strings.Join(append(append([]string(nil), lines[:9]...), lines[10]), "\n") + "\n"},
		{"duplicate widens", strings.Join(append(append([]string(nil), lines[:10]...), lines[9:]...), "\n") + "\n"},
		{"widens after prev", strings.Join(append(append([]string(nil), lines[:9]...), lines[10], lines[9]), "\n") + "\n"},
		{"empty widens", withLine(9, "widens: ")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseCanonicalText(tt.text); err == nil {
				t.Fatalf("parse accepted:\n%s", tt.text)
			}
		})
	}
}

func TestParseRejectsTab(t *testing.T) {
	g := rootGrant()
	g.Nonce = "a\tb"
	if _, err := ParseCanonicalText(g.CanonicalText()); err == nil {
		t.Fatal("tab in value accepted")
	}
}

func TestParseRejectsDisallowedCharsInEveryField(t *testing.T) {
	fields := map[string]func(*Grant, string){
		"grantor": func(g *Grant, v string) { g.Grantor = "a" + v + "b" },
		"grantee": func(g *Grant, v string) { g.Grantee = "a" + v + "b" },
		"session": func(g *Grant, v string) { g.Session = "a" + v + "b" },
		"nonce":   func(g *Grant, v string) { g.Nonce = "a" + v + "b" },
		"widens":  func(g *Grant, v string) { g.Widens = "a" + v + "b" },
		"prev":    func(g *Grant, v string) { g.Prev = "a" + v + "b" },
		"profile name": func(g *Grant, v string) {
			g.Profiles = map[string]string{"a" + v + "b": "d"}
		},
		"profile digest": func(g *Grant, v string) {
			g.Profiles = map[string]string{"cheap": "a" + v + "b"}
		},
	}
	for field, set := range fields {
		for charName, c := range badChars {
			t.Run(field+"/"+charName, func(t *testing.T) {
				g := rootGrant()
				set(&g, c)
				if _, err := ParseCanonicalText(g.CanonicalText()); err == nil {
					t.Fatalf("parse accepted:\n%s", g.CanonicalText())
				}
			})
		}
	}
}

func TestParseRequiresCanonicalForm(t *testing.T) {
	valid := rootGrant().CanonicalText()
	fractional := strings.Replace(valid, "14:00:00Z", "14:00:00.999999999Z", 1)
	if fractional == valid {
		t.Fatal("test setup: replacement did not apply")
	}
	if _, err := ParseCanonicalText(fractional); err == nil ||
		!strings.Contains(err.Error(), "not in canonical form") {
		t.Fatalf("fractional seconds: error %v", err)
	}
	unpadded := strings.Replace(valid, "T14:00:00Z", "T14:0:00Z", 1)
	if _, err := ParseCanonicalText(unpadded); err == nil {
		t.Fatalf("unpadded minute accepted:\n%s", unpadded)
	}
}

func TestNarrows(t *testing.T) {
	parent := rootGrant()
	parent.Profiles = map[string]string{"cheap": "X", "review": "d-review"}
	parent.Onward = OnwardGrant
	child := func(mutate func(*Grant)) Grant {
		c := Grant{
			Grantor:  "root-principal",
			Grantee:  "worker",
			Session:  "repo/branch",
			Profiles: map[string]string{"cheap": "X"},
			Onward:   OnwardNone,
			Expires:  expiry,
			Nonce:    "bb",
			Prev:     "parent-digest",
		}
		mutate(&c)
		return c
	}
	nonePassingParent := parent
	nonePassingParent.Onward = OnwardNone

	tests := []struct {
		name    string
		parent  Grant
		child   Grant
		wantErr string
	}{
		{"subset equal expiry", parent, child(func(*Grant) {}), ""},
		{"earlier expiry", parent, child(func(c *Grant) { c.Expires = expiry.Add(-time.Hour) }), ""},
		{"onward grant from granting parent", parent, child(func(c *Grant) { c.Onward = OnwardGrant }), ""},
		{"none child of granting parent", parent, child(func(c *Grant) { c.Onward = OnwardNone }), ""},
		{"wrong prev", parent, child(func(c *Grant) { c.Prev = "other" }), `child prev "other" is not parent digest "parent-digest"`},
		{"grantor is not the parent's grantee", parent, child(func(c *Grant) { c.Grantor = "interloper" }), `child grantor "interloper" is not parent grantee "root-principal"`},
		{"profile not held", parent, child(func(c *Grant) { c.Profiles = map[string]string{"expensive": "Z"} }), "profile expensive not held by parent"},
		{"extra profile", parent, child(func(c *Grant) { c.Profiles = map[string]string{"cheap": "X", "other": "Y"} }), "profile other not held by parent"},
		{"digest differs", parent, child(func(c *Grant) { c.Profiles = map[string]string{"cheap": "Y"} }), "profile cheap digest differs (parent X, child Y)"},
		{"expiry later", parent, child(func(c *Grant) { c.Expires = expiry.Add(time.Second) }), "child expiry after parent expiry"},
		{"none parent, none child", nonePassingParent, child(func(c *Grant) { c.Onward = OnwardNone }), "parent may not pass these on (onward none)"},
		{"none parent, grant child", nonePassingParent, child(func(c *Grant) { c.Onward = OnwardGrant }), "parent may not pass these on (onward none)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Narrows(tt.parent, "parent-digest", tt.child, WalkOptions{})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestNarrowsSessionKeyBinding(t *testing.T) {
	parent := rootGrant()
	parent.Grantee = "repo/branch" // spawn-time grants name the child's session key
	parent.Session = "repo/branch"
	child := Grant{
		Grantor: "principal-1", Grantee: "g", Session: "-",
		Profiles: map[string]string{"cheap": "d-cheap"}, Onward: OnwardNone,
		Expires: expiry, Nonce: "n", Prev: "pd",
	}
	mapTo := func(session string, ok bool) WalkOptions {
		return WalkOptions{SessionOfPrincipal: func(p string) (string, bool) {
			if p != "principal-1" {
				return "", false
			}
			return session, ok
		}}
	}

	if err := Narrows(parent, "pd", child, mapTo("repo/branch", true)); err != nil {
		t.Fatalf("mapped binding rejected: %v", err)
	}
	err := Narrows(parent, "pd", child, mapTo("other/branch", true))
	if err == nil || !strings.Contains(err.Error(), `not parent grantee "repo/branch"`) ||
		!strings.Contains(err.Error(), `session "other/branch"`) {
		t.Fatalf("different-session mapping: error %v", err)
	}
	err = Narrows(parent, "pd", child, mapTo("", false))
	if err == nil || !strings.Contains(err.Error(), "session mapping has no session") {
		t.Fatalf("unmapped principal: error %v", err)
	}
	err = Narrows(parent, "pd", child, WalkOptions{})
	if err == nil || !strings.Contains(err.Error(), `child grantor "principal-1" is not parent grantee "repo/branch"`) ||
		strings.Contains(err.Error(), "mapping") {
		t.Fatalf("no mapping given: error %v", err)
	}
}

func TestHolds(t *testing.T) {
	g := rootGrant()
	if !Holds(g, "cheap") || Holds(g, "expensive") {
		t.Fatal("Holds does not follow the profile set")
	}
}

func TestLocalLinkVerifier(t *testing.T) {
	var v LocalLinkVerifier
	if err := v.VerifyLink(rootGrant(), nil); err != nil {
		t.Fatalf("empty signature rejected: %v", err)
	}
	if err := v.VerifyLink(rootGrant(), []byte{}); err != nil {
		t.Fatalf("zero-length signature rejected: %v", err)
	}
	err := v.VerifyLink(rootGrant(), []byte("sig"))
	if err == nil || err.Error() != "link carries a signature but no link verifier is configured" {
		t.Fatalf("error %v", err)
	}
}

func mustDigest(text string) string {
	d, err := DigestOf(text)
	if err != nil {
		panic(err)
	}
	return d
}

func mustActText(g Grant, digest string) string {
	s, err := ActText(g, digest)
	if err != nil {
		panic(err)
	}
	return s
}

// signedTextVerifier treats the root signature as the literal text the card
// signed, so it accepts a root iff that text equals the regenerated act text.
func signedTextVerifier() Verifier {
	return NewVerifier(func(actText string, sig []byte) error {
		if string(sig) != actText {
			return errors.New("signed text differs from the regenerated act text")
		}
		return nil
	}, LocalLinkVerifier{})
}

func rootRecord(g Grant) Record {
	text := g.CanonicalText()
	digest := mustDigest(text)
	return Record{Digest: digest, Text: text, Sig: []byte(mustActText(g, digest)), Root: true}
}

func linkRecord(parent Record, g Grant) Record {
	g.Prev = parent.Digest
	text := g.CanonicalText()
	return Record{Digest: mustDigest(text), Text: text}
}

func linkGrant(grantor, grantee string, profiles map[string]string) Grant {
	return Grant{
		Grantor: grantor, Grantee: grantee, Session: "-",
		Profiles: profiles, Onward: OnwardGrant, Expires: expiry, Nonce: grantee, Widens: "-",
	}
}

func TestWalkWidening(t *testing.T) {
	initial := rootRecord(rootGrant())
	wg := rootGrant()
	wg.Nonce = "wide"
	wg.Widens = initial.Digest
	wg.Profiles = map[string]string{"cheap": "d-cheap", "review": "d-review", "expensive": "d-exp"}
	wide := rootRecord(wg)
	v := signedTextVerifier()

	if _, err := Walk(now, wide, store(initial, wide), v, WalkOptions{}); err != nil {
		t.Fatalf("widening rejected: %v", err)
	}
	if _, err := Walk(now, wide, store(wide), v, WalkOptions{}); err == nil ||
		!strings.Contains(err.Error(), "widened record "+initial.Digest+" not found") {
		t.Fatalf("missing widened record: error %v", err)
	}

	other := rootGrant()
	other.Grantee = "someone-else"
	otherRec := rootRecord(other)
	wg2 := wg
	wg2.Widens = otherRec.Digest
	wide2 := rootRecord(wg2)
	if _, err := Walk(now, wide2, store(otherRec, wide2), v, WalkOptions{}); err == nil ||
		!strings.Contains(err.Error(), "grantee") {
		t.Fatalf("grantee mismatch: error %v", err)
	}

	forged := initial
	forged.Text = other.CanonicalText()
	if _, err := Walk(now, wide, store(forged, wide), v, WalkOptions{}); err == nil ||
		!strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("forged widened record: error %v", err)
	}

	lg := linkGrant("root-principal", "a", map[string]string{"cheap": "d-cheap"})
	lg.Widens = initial.Digest
	link := linkRecord(initial, lg)
	if _, err := Walk(now, link, store(initial, link), v, WalkOptions{}); err == nil ||
		!strings.Contains(err.Error(), "non-root record has widens") {
		t.Fatalf("link with widens: error %v", err)
	}

	// Presenting a widening's signature as an initial grant (or vice versa) fails.
	flipped := wg
	flipped.Widens = "-"
	wide.Sig = []byte(mustActText(flipped, wide.Digest))
	if _, err := Walk(now, wide, store(initial, wide), v, WalkOptions{}); err == nil ||
		!strings.Contains(err.Error(), "signed text differs") {
		t.Fatalf("wrong form signed: error %v", err)
	}
}

func TestWalkWideningRejections(t *testing.T) {
	v := signedTextVerifier()
	initial := rootRecord(rootGrant())
	widening := func(mutate func(*Grant)) Record {
		wg := rootGrant()
		wg.Nonce = "wide"
		wg.Widens = initial.Digest
		wg.Profiles = map[string]string{"cheap": "d-cheap", "review": "d-review", "expensive": "d-exp"}
		mutate(&wg)
		return rootRecord(wg)
	}

	t.Run("widened record is not a root", func(t *testing.T) {
		notRoot := initial
		notRoot.Root = false
		wide := widening(func(*Grant) {})
		_, err := Walk(now, wide, store(notRoot, wide), v, WalkOptions{})
		if err == nil || !strings.Contains(err.Error(), "widened record "+initial.Digest+" is not a root record") {
			t.Fatalf("error %v", err)
		}
	})

	t.Run("widened record expired", func(t *testing.T) {
		old := rootGrant()
		old.Expires = now.Add(-time.Minute)
		oldRec := rootRecord(old)
		wide := widening(func(g *Grant) { g.Widens = oldRec.Digest })
		_, err := Walk(now, wide, store(oldRec, wide), v, WalkOptions{})
		if err == nil || !strings.Contains(err.Error(), "widened record "+oldRec.Digest+" expired") {
			t.Fatalf("error %v", err)
		}
	})

	t.Run("widening drops a profile", func(t *testing.T) {
		wide := widening(func(g *Grant) { g.Profiles = map[string]string{"cheap": "d-cheap", "other": "d-o"} })
		_, err := Walk(now, wide, store(initial, wide), v, WalkOptions{})
		if err == nil || !strings.Contains(err.Error(), "drops profile review") {
			t.Fatalf("error %v", err)
		}
	})

	t.Run("re-pinned digest is allowed", func(t *testing.T) {
		wide := widening(func(g *Grant) {
			g.Profiles = map[string]string{"cheap": "d-new", "review": "d-review", "expensive": "d-exp"}
		})
		if _, err := Walk(now, wide, store(initial, wide), v, WalkOptions{}); err != nil {
			t.Fatalf("re-pin rejected: %v", err)
		}
	})
}

func TestWalkOnwardNoneAllowsNoChildren(t *testing.T) {
	v := signedTextVerifier()
	profiles := map[string]string{"cheap": "d-cheap"}
	root := rootRecord(rootGrant())
	leafNone := linkGrant("root-principal", "a", profiles)
	leafNone.Onward = OnwardNone
	a := linkRecord(root, leafNone)

	if _, err := Walk(now, a, store(root, a), v, WalkOptions{}); err != nil {
		t.Fatalf("none-leaf under a granting root rejected: %v", err)
	}

	b := linkRecord(a, linkGrant("a", "b", profiles))
	_, err := Walk(now, b, store(root, a, b), v, WalkOptions{})
	if err == nil || !strings.Contains(err.Error(), "parent may not pass these on (onward none)") {
		t.Fatalf("child of an onward-none link: error %v", err)
	}
}

func store(records ...Record) Resolver {
	byDigest := map[string]Record{}
	for _, r := range records {
		byDigest[r.Digest] = r
	}
	return func(d string) (Record, bool) { r, ok := byDigest[d]; return r, ok }
}

// threeLinkChain builds root -> a -> b -> c, each narrowing the last.
func threeLinkChain() (records []Record, leaf Record) {
	root := rootRecord(rootGrant())
	a := linkRecord(root, linkGrant("root-principal", "a", map[string]string{"cheap": "d-cheap", "review": "d-review"}))
	b := linkRecord(a, linkGrant("a", "b", map[string]string{"cheap": "d-cheap", "review": "d-review"}))
	c := linkRecord(b, linkGrant("b", "c", map[string]string{"cheap": "d-cheap"}))
	return []Record{root, a, b, c}, c
}

func TestWalkHappyPath(t *testing.T) {
	records, leaf := threeLinkChain()
	got, err := Walk(now, leaf, store(records...), signedTextVerifier(), WalkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Grantee != "c" || !Holds(got, "cheap") || Holds(got, "review") {
		t.Fatalf("effective grant is not the start grant: %+v", got)
	}
}

func TestWalkRootAlone(t *testing.T) {
	root := rootRecord(rootGrant())
	if _, err := Walk(now, root, store(root), signedTextVerifier(), WalkOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestWalkMapsSessionKeyGrantee(t *testing.T) {
	root := rootRecord(rootGrant())
	spawnGrant := linkGrant("root-principal", "repo/branch", map[string]string{"cheap": "d-cheap"})
	spawnGrant.Session = "repo/branch"
	spawn := linkRecord(root, spawnGrant)
	// The child's own later link is signed by its principal, not its session key.
	next := linkRecord(spawn, linkGrant("principal-1", "grandchild", map[string]string{"cheap": "d-cheap"}))
	recs := store(root, spawn, next)

	mapping := WalkOptions{SessionOfPrincipal: func(p string) (string, bool) {
		return "repo/branch", p == "principal-1"
	}}
	if _, err := Walk(now, next, recs, signedTextVerifier(), mapping); err != nil {
		t.Fatalf("mapped chain rejected: %v", err)
	}

	elsewhere := WalkOptions{SessionOfPrincipal: func(string) (string, bool) { return "other/branch", true }}
	if _, err := Walk(now, next, recs, signedTextVerifier(), elsewhere); err == nil ||
		!strings.Contains(err.Error(), "narrowing edge") {
		t.Fatalf("principal in a different session: error %v", err)
	}
	if _, err := Walk(now, next, recs, signedTextVerifier(), WalkOptions{}); err == nil ||
		!strings.Contains(err.Error(), `is not parent grantee "repo/branch"`) {
		t.Fatalf("session-key grantee with no mapping: error %v", err)
	}
}

func TestWalkRejectsForgedDigest(t *testing.T) {
	records, leaf := threeLinkChain()
	// A resolver hands back different (still parseable) text under a's digest.
	forged := records[1]
	forged.Text = linkGrant("root-principal", "evil", map[string]string{"cheap": "d-cheap"}).CanonicalText()
	resolve := func(d string) (Record, bool) {
		if d == forged.Digest {
			return forged, true
		}
		return store(records...)(d)
	}
	if _, err := Walk(now, leaf, resolve, signedTextVerifier(), WalkOptions{}); err == nil ||
		!strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("error %v", err)
	}
	// The start record is checked too.
	start := leaf
	start.Digest = records[1].Digest
	if _, err := Walk(now, start, store(records...), signedTextVerifier(), WalkOptions{}); err == nil ||
		!strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("start record: error %v", err)
	}
}

func TestWalkRootSignatureMustCoverRegeneratedActText(t *testing.T) {
	g := rootGrant()
	root := rootRecord(g)
	wider := g
	wider.Profiles = map[string]string{"cheap": "d-cheap", "review": "d-review", "expensive": "d-exp"}
	root.Sig = []byte(mustActText(wider, root.Digest)) // card signed a different grant's text
	if _, err := Walk(now, root, store(root), signedTextVerifier(), WalkOptions{}); err == nil ||
		!strings.Contains(err.Error(), "signed text differs from the regenerated act text") {
		t.Fatalf("different grant text: error %v", err)
	}
	root.Sig = []byte(mustActText(g, "piggy-record-digest-v1@sha256-other")) // signed for another record
	if _, err := Walk(now, root, store(root), signedTextVerifier(), WalkOptions{}); err == nil {
		t.Fatal("act text naming another record accepted")
	}
}

func TestWalkFailures(t *testing.T) {
	profiles := map[string]string{"cheap": "d-cheap"}
	tests := []struct {
		name    string
		build   func() (start Record, records []Record)
		wantErr string
	}{
		{"expired link", func() (Record, []Record) {
			records, _ := threeLinkChain()
			g := linkGrant("b", "c", profiles)
			g.Expires = now.Add(-time.Minute)
			leaf := linkRecord(records[2], g)
			return leaf, append(records, leaf)
		}, "expired"},
		{"expired at the instant", func() (Record, []Record) {
			records, _ := threeLinkChain()
			g := linkGrant("b", "c", profiles)
			g.Expires = now
			leaf := linkRecord(records[2], g)
			return leaf, append(records, leaf)
		}, "expired"},
		{"signed link without a link verifier", func() (Record, []Record) {
			records, leaf := threeLinkChain()
			leaf.Sig = []byte("forged")
			return leaf, append(records, leaf)
		}, "signature: link carries a signature"},
		{"bad root signature", func() (Record, []Record) {
			records, leaf := threeLinkChain()
			records[0].Sig = []byte("forged")
			return leaf, records
		}, "signature: signed text differs"},
		{"missing parent", func() (Record, []Record) {
			records, leaf := threeLinkChain()
			return leaf, records[:2] // b absent
		}, "not found"},
		{"widened edge", func() (Record, []Record) {
			records, _ := threeLinkChain()
			wide := linkRecord(records[1], linkGrant("a", "w", map[string]string{"cheap": "d-cheap", "expensive": "d-exp"}))
			return wide, append(records, wide)
		}, "profile expensive not held by parent"},
		{"widened digest", func() (Record, []Record) {
			records, _ := threeLinkChain()
			wide := linkRecord(records[1], linkGrant("a", "w", map[string]string{"cheap": "d-other"}))
			return wide, append(records, wide)
		}, "profile cheap digest differs"},
		{"root with prev", func() (Record, []Record) {
			g := rootGrant()
			g.Prev = "d-other"
			root := rootRecord(g)
			return root, []Record{root}
		}, "root record has prev"},
		{"non-root with dash prev", func() (Record, []Record) {
			g := linkGrant("p", "x", profiles)
			g.Prev = "-"
			text := g.CanonicalText()
			rec := Record{Digest: mustDigest(text), Text: text}
			return rec, []Record{rec}
		}, "non-root record has prev"},
		{"unparseable text", func() (Record, []Record) {
			text := "garbage\n"
			rec := Record{Digest: mustDigest(text), Text: text}
			return rec, []Record{rec}
		}, "parse"},
		{"depth cap", func() (Record, []Record) {
			return chainOfRecords(maxChainDepth)
		}, "chain deeper than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, records := tt.build()
			_, err := Walk(now, start, store(records...), signedTextVerifier(), WalkOptions{})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

// chainOfRecords returns a root followed by links narrowing nothing, and the
// last link.
func chainOfRecords(links int) (Record, []Record) {
	profiles := map[string]string{"cheap": "d-cheap"}
	prev := rootRecord(rootGrant())
	records := []Record{prev}
	grantor := "root-principal"
	for i := 0; i < links; i++ {
		grantee := fmt.Sprintf("g%d", i)
		prev = linkRecord(prev, linkGrant(grantor, grantee, profiles))
		records = append(records, prev)
		grantor = grantee
	}
	return prev, records
}

func TestWalkAtDepthCapSucceeds(t *testing.T) {
	last, records := chainOfRecords(maxChainDepth - 1) // root + 31 links = 32 records
	if _, err := Walk(now, last, store(records...), signedTextVerifier(), WalkOptions{}); err != nil {
		t.Fatal(err)
	}
}
