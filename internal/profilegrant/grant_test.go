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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseCanonicalText(tt.text); err == nil {
				t.Fatalf("parse accepted:\n%s", tt.text)
			}
		})
	}
}

func TestParseAcceptsTab(t *testing.T) {
	g := rootGrant()
	g.Nonce = "a\tb"
	if _, err := ParseCanonicalText(g.CanonicalText()); err != nil {
		t.Fatalf("tab in value rejected: %v", err)
	}
}

func TestNarrows(t *testing.T) {
	parent := rootGrant()
	parent.Profiles = map[string]string{"cheap": "X", "review": "d-review"}
	parent.Onward = OnwardNone
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
	grantingParent := parent
	grantingParent.Onward = OnwardGrant

	tests := []struct {
		name    string
		parent  Grant
		child   Grant
		wantErr string
	}{
		{"subset equal expiry", parent, child(func(*Grant) {}), ""},
		{"earlier expiry", parent, child(func(c *Grant) { c.Expires = expiry.Add(-time.Hour) }), ""},
		{"onward grant from granting parent", grantingParent, child(func(c *Grant) { c.Onward = OnwardGrant }), ""},
		{"wrong prev", parent, child(func(c *Grant) { c.Prev = "other" }), `child prev "other" is not parent digest "parent-digest"`},
		{"grantor is not the parent's grantee", parent, child(func(c *Grant) { c.Grantor = "interloper" }), `child grantor "interloper" is not parent grantee "root-principal"`},
		{"profile not held", parent, child(func(c *Grant) { c.Profiles = map[string]string{"expensive": "Z"} }), "profile expensive not held by parent"},
		{"extra profile", parent, child(func(c *Grant) { c.Profiles = map[string]string{"cheap": "X", "other": "Y"} }), "profile other not held by parent"},
		{"digest differs", parent, child(func(c *Grant) { c.Profiles = map[string]string{"cheap": "Y"} }), "profile cheap digest differs (parent X, child Y)"},
		{"expiry later", parent, child(func(c *Grant) { c.Expires = expiry.Add(time.Second) }), "child expiry after parent expiry"},
		{"onward widened", parent, child(func(c *Grant) { c.Onward = OnwardGrant }), "onward grant not permitted: parent holds none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Narrows(tt.parent, "parent-digest", tt.child)
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

func TestHolds(t *testing.T) {
	g := rootGrant()
	if !Holds(g, "cheap") || Holds(g, "expensive") {
		t.Fatal("Holds does not follow the profile set")
	}
}

// fakeVerifier accepts "root-sig" as the card signature and "link:<grantor>"
// as a software-key signature.
type fakeVerifier struct{}

func (fakeVerifier) VerifyRoot(_ string, sig []byte) error {
	if string(sig) != "root-sig" {
		return errors.New("bad card signature")
	}
	return nil
}

func (fakeVerifier) VerifyLink(grantor, _ string, sig []byte) error {
	if string(sig) != "link:"+grantor {
		return fmt.Errorf("bad signature by %s", grantor)
	}
	return nil
}

func rootRecord(g Grant) Record {
	return Record{Digest: "d-root", Text: g.CanonicalText(), Sig: []byte("root-sig"), Root: true}
}

func linkRecord(digest, prev string, g Grant) Record {
	g.Prev = prev
	return Record{Digest: digest, Text: g.CanonicalText(), Sig: []byte("link:" + g.Grantor)}
}

func linkGrant(grantor, grantee string, profiles map[string]string) Grant {
	return Grant{
		Grantor: grantor, Grantee: grantee, Session: "-",
		Profiles: profiles, Onward: OnwardGrant, Expires: expiry, Nonce: grantee,
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
	a := linkRecord("d-a", "d-root", linkGrant("root-principal", "a", map[string]string{"cheap": "d-cheap", "review": "d-review"}))
	b := linkRecord("d-b", "d-a", linkGrant("a", "b", map[string]string{"cheap": "d-cheap", "review": "d-review"}))
	c := linkRecord("d-c", "d-b", linkGrant("b", "c", map[string]string{"cheap": "d-cheap"}))
	return []Record{root, a, b, c}, c
}

func TestWalkHappyPath(t *testing.T) {
	records, leaf := threeLinkChain()
	got, err := Walk(now, leaf, store(records...), fakeVerifier{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Grantee != "c" || !Holds(got, "cheap") || Holds(got, "review") {
		t.Fatalf("effective grant is not the start grant: %+v", got)
	}
}

func TestWalkRootAlone(t *testing.T) {
	root := rootRecord(rootGrant())
	if _, err := Walk(now, root, store(root), fakeVerifier{}); err != nil {
		t.Fatal(err)
	}
}

func TestWalkFailures(t *testing.T) {
	tests := []struct {
		name    string
		build   func() (start Record, records []Record)
		wantErr string
	}{
		{"expired link", func() (Record, []Record) {
			records, leaf := threeLinkChain()
			g := linkGrant("b", "c", map[string]string{"cheap": "d-cheap"})
			g.Expires = now.Add(-time.Minute)
			leaf = linkRecord("d-c", "d-b", g)
			return leaf, append(records, leaf)
		}, "record d-c: expired"},
		{"expired at the instant", func() (Record, []Record) {
			records, leaf := threeLinkChain()
			g := linkGrant("b", "c", map[string]string{"cheap": "d-cheap"})
			g.Expires = now
			leaf = linkRecord("d-c", "d-b", g)
			return leaf, append(records, leaf)
		}, "record d-c: expired"},
		{"bad link signature", func() (Record, []Record) {
			records, leaf := threeLinkChain()
			leaf.Sig = []byte("forged")
			return leaf, append(records, leaf)
		}, "record d-c: signature"},
		{"bad root signature", func() (Record, []Record) {
			records, leaf := threeLinkChain()
			records[0].Sig = []byte("forged")
			return leaf, records
		}, "record d-root: signature: bad card signature"},
		{"missing parent", func() (Record, []Record) {
			records, leaf := threeLinkChain()
			return leaf, records[:2] // d-b absent
		}, "parent d-b not found"},
		{"widened edge", func() (Record, []Record) {
			records, _ := threeLinkChain()
			wide := linkRecord("d-w", "d-a", linkGrant("a", "w", map[string]string{"cheap": "d-cheap", "expensive": "d-exp"}))
			return wide, append(records, wide)
		}, "profile expensive not held by parent"},
		{"widened digest", func() (Record, []Record) {
			records, _ := threeLinkChain()
			wide := linkRecord("d-w", "d-a", linkGrant("a", "w", map[string]string{"cheap": "d-other"}))
			return wide, append(records, wide)
		}, "profile cheap digest differs"},
		{"cycle", func() (Record, []Record) {
			x := linkRecord("d-x", "d-y", linkGrant("y", "x", map[string]string{"cheap": "d-cheap"}))
			y := linkRecord("d-y", "d-x", linkGrant("x", "y", map[string]string{"cheap": "d-cheap"}))
			return x, []Record{x, y}
		}, "cycle"},
		{"root with prev", func() (Record, []Record) {
			g := rootGrant()
			g.Prev = "d-other"
			root := rootRecord(g)
			return root, []Record{root}
		}, "root record has prev"},
		{"non-root with dash prev", func() (Record, []Record) {
			g := linkGrant("p", "x", map[string]string{"cheap": "d-cheap"})
			g.Prev = "-"
			rec := Record{Digest: "d-x", Text: g.CanonicalText(), Sig: []byte("link:p")}
			return rec, []Record{rec}
		}, "non-root record has prev"},
		{"unparseable text", func() (Record, []Record) {
			rec := Record{Digest: "d-x", Text: "garbage\n", Sig: []byte("x")}
			return rec, []Record{rec}
		}, "record d-x: parse"},
		{"depth cap", func() (Record, []Record) {
			profiles := map[string]string{"cheap": "d-cheap"}
			records := []Record{rootRecord(rootGrant())}
			prev, grantor := "d-root", "root-principal"
			var last Record
			for i := 0; i < maxChainDepth; i++ { // root + 32 links = 33 records
				digest, grantee := fmt.Sprintf("d-%d", i), fmt.Sprintf("g%d", i)
				last = linkRecord(digest, prev, linkGrant(grantor, grantee, profiles))
				records = append(records, last)
				prev, grantor = digest, grantee
			}
			return last, records
		}, "chain deeper than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, records := tt.build()
			_, err := Walk(now, start, store(records...), fakeVerifier{})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestWalkAtDepthCapSucceeds(t *testing.T) {
	profiles := map[string]string{"cheap": "d-cheap"}
	records := []Record{rootRecord(rootGrant())}
	prev, grantor := "d-root", "root-principal"
	var last Record
	for i := 0; i < maxChainDepth-1; i++ { // root + 31 links = 32 records
		digest, grantee := fmt.Sprintf("d-%d", i), fmt.Sprintf("g%d", i)
		last = linkRecord(digest, prev, linkGrant(grantor, grantee, profiles))
		records = append(records, last)
		prev, grantor = digest, grantee
	}
	if _, err := Walk(now, last, store(records...), fakeVerifier{}); err != nil {
		t.Fatal(err)
	}
}
