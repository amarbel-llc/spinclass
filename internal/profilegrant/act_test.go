package profilegrant

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const goldenDigest = "piggy-record-digest-v1@sha256-example"

func TestActTextGolden(t *testing.T) {
	g := rootGrant()
	g.Onward = OnwardNone
	got, err := ActText(g, goldenDigest)
	if err != nil {
		t.Fatal(err)
	}
	const want = `spinclass profile grant (escalation): initial grant
record: piggy-record-digest-v1@sha256-example
allow: root-principal (no session)
to spawn workers with these profiles, each pinned to its content digest:
  cheap = d-cheap
  review = d-review
may pass these on: no
until: 2026-10-20T14:00:00Z
Signing this is an operator grant of these rights, bounded as stated above.
`
	if got != want {
		t.Fatalf("act text:\n%s\nwant:\n%s", got, want)
	}

	g.Session = "repo/branch"
	g.Grantee = "repo/branch"
	g.Onward = OnwardGrant
	g.Widens = "piggy-record-digest-v1@sha256-earlier"
	got, err = ActText(g, goldenDigest)
	if err != nil {
		t.Fatal(err)
	}
	const wantWidening = `spinclass profile grant (escalation): widening of piggy-record-digest-v1@sha256-earlier
record: piggy-record-digest-v1@sha256-example
allow: repo/branch (session repo/branch)
to spawn workers with these profiles, each pinned to its content digest:
  cheap = d-cheap
  review = d-review
may pass these on: yes
until: 2026-10-20T14:00:00Z
Signing this is an operator grant of these rights, bounded as stated above.
`
	if got != wantWidening {
		t.Fatalf("widening act text:\n%s\nwant:\n%s", got, wantWidening)
	}
	for _, line := range []string{
		"allow: repo/branch (session repo/branch)\n",
		"may pass these on: yes\n",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("act text lacks %q:\n%s", line, got)
		}
	}
}

func TestActTextInvariants(t *testing.T) {
	g := rootGrant()
	g.Nonce = "a\tb"
	got, err := ActText(g, mustDigest(g.CanonicalText()))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > maxTextBytes || !utf8.ValidString(got) {
		t.Fatalf("size %d or encoding invalid", len(got))
	}
	for _, r := range got {
		if r != '\n' && r != '\t' && (r < 0x20 || r == 0x7f) {
			t.Fatalf("control character %U in act text", r)
		}
	}
	again, _ := ActText(g, mustDigest(g.CanonicalText()))
	if again != got {
		t.Fatal("act text is not deterministic")
	}
}

func TestActTextRejectsBadGrants(t *testing.T) {
	mutate := func(f func(*Grant)) Grant { g := rootGrant(); f(&g); return g }
	tests := []struct {
		name   string
		g      Grant
		digest string
	}{
		{"no profiles", mutate(func(g *Grant) { g.Profiles = nil }), goldenDigest},
		{"bad onward", mutate(func(g *Grant) { g.Onward = "maybe" }), goldenDigest},
		{"zero expiry", mutate(func(g *Grant) { g.Expires = time.Time{} }), goldenDigest},
		{"empty grantee", mutate(func(g *Grant) { g.Grantee = "" }), goldenDigest},
		{"newline in grantee", mutate(func(g *Grant) { g.Grantee = "x\nallow: evil" }), goldenDigest},
		{"tab in session", mutate(func(g *Grant) { g.Session = "a\tb" }), goldenDigest},
		{"newline in profile name", mutate(func(g *Grant) { g.Profiles = map[string]string{"a\nb": "d"} }), goldenDigest},
		{"empty digest", rootGrant(), ""},
		{"digest with newline", rootGrant(), "x\ny"},
		{"oversize", mutate(func(g *Grant) { g.Grantee = strings.Repeat("a", 5000) }), goldenDigest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := ActText(tt.g, tt.digest); err == nil {
				t.Fatalf("accepted:\n%s", got)
			}
		})
	}
}

func TestActTextDiffersPerField(t *testing.T) {
	base, err := ActText(rootGrant(), goldenDigest)
	if err != nil {
		t.Fatal(err)
	}
	variants := map[string]func(*Grant){
		"onward":  func(g *Grant) { g.Onward = OnwardNone },
		"session": func(g *Grant) { g.Session = "repo/branch" },
		"expires": func(g *Grant) { g.Expires = g.Expires.Add(time.Second) },
		"widens":  func(g *Grant) { g.Widens = "piggy-record-digest-v1@sha256-earlier" },
		"grantee": func(g *Grant) { g.Grantee = "other" },
	}
	for name, f := range variants {
		g := rootGrant()
		f(&g)
		got, err := ActText(g, goldenDigest)
		if err != nil {
			t.Fatal(err)
		}
		if got == base {
			t.Errorf("changing %s did not change the act text", name)
		}
	}
	if other, _ := ActText(rootGrant(), "piggy-record-digest-v1@sha256-other"); other == base {
		t.Error("changing the digest did not change the act text")
	}
}

func TestDigestOf(t *testing.T) {
	a, err := DigestOf("one\n")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := DigestOf("two\n")
	again, _ := DigestOf("one\n")
	if !strings.HasPrefix(a, "piggy-record-digest-v1@sha256-") {
		t.Errorf("digest %q lacks purpose/format prefix", a)
	}
	if a == b || a != again {
		t.Errorf("digest not a function of the text: %q %q %q", a, b, again)
	}
}
