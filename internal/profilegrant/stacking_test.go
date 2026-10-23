package profilegrant

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// D1 (circus docs/plans/2026-10-22-d1-capability-artifact-and-grant-chain.md)
// stacking vectors at the Narrows level. The parent (a session S) holds
// manager-builder@d1; the child link S -> W binds engineer-reviewer@d2, whose
// _base is manager-builder.

// stackChains is a BaseChain over a fixed table: profile@digest -> its chain.
func stackChains(table map[string][]BaseLink) func(name, digest string) ([]BaseLink, error) {
	return func(name, digest string) ([]BaseLink, error) {
		chain, ok := table[name+"@"+digest]
		if !ok {
			return nil, errors.New("no such profile definition")
		}
		return chain, nil
	}
}

func stackParent() Grant {
	g := rootGrant()
	g.Profiles = map[string]string{"manager-builder": "d1"}
	return g
}

func stackChild(profiles map[string]string, mutate func(*Grant)) Grant {
	c := Grant{
		Grantor:  "root-principal",
		Grantee:  "worker",
		Session:  "repo/worker",
		Profiles: profiles,
		Onward:   OnwardNone,
		Expires:  expiry.Add(-time.Hour),
		Nonce:    "cc",
		Widens:   "-",
		Prev:     "parent-digest",
	}
	if mutate != nil {
		mutate(&c)
	}
	return c
}

var stackTable = map[string][]BaseLink{
	"engineer-reviewer@d2": {{Name: "manager-builder", Digest: "d1"}},
	// two hops: reviewer-lite narrows engineer-reviewer, which narrows manager-builder
	"reviewer-lite@d3": {{Name: "engineer-reviewer", Digest: "d2"}, {Name: "manager-builder", Digest: "d1"}},
	// widens its base on the first edge (V-D1-6 / V-D1-10: adds a write tool)
	"widener@d4": {{Name: "manager-builder", Digest: "d1", Widenings: []string{"adds server folio", "drops exclusion moxy:folio_write"}}},
	// its base is held, but under another digest than the one the parent holds
	"stale-base@d5": {{Name: "manager-builder", Digest: "d0"}},
	// its chain reaches nothing the parent holds
	"orphan@d6": {{Name: "something-else", Digest: "dx"}},
	// a widening on the second edge, past a narrowing first edge
	"deep-widener@d7": {{Name: "engineer-reviewer", Digest: "d2"}, {Name: "manager-builder", Digest: "d1", Widenings: []string{"adds env key TOKEN"}}},
}

func TestNarrowsStacksAChildProfileOnTheParentsBase(t *testing.T) {
	opts := WalkOptions{BaseChain: stackChains(stackTable)}
	tests := []struct {
		name     string
		profiles map[string]string
		mutate   func(*Grant)
		wantErr  string
	}{
		// V-D1-2: the narrowing link S -> W verifies.
		{"V-D1-2 one hop", map[string]string{"engineer-reviewer": "d2"}, nil, ""},
		{"two hops", map[string]string{"reviewer-lite": "d3"}, nil, ""},
		{"held and stacked together", map[string]string{"manager-builder": "d1", "engineer-reviewer": "d2"}, nil, ""},
		// V-D1-6 / V-D1-10: a child profile that adds a node or a tool kind over its base.
		{"V-D1-6 widening edge", map[string]string{"widener": "d4"}, nil, "widens base manager-builder by: adds server folio; drops exclusion moxy:folio_write"},
		{"widening past the first edge", map[string]string{"deep-widener": "d7"}, nil, "widens base manager-builder by: adds env key TOKEN"},
		// The base is held, but the chain reaches it at another digest: fail closed.
		{"base under another digest", map[string]string{"stale-base": "d5"}, nil, "the parent holds base manager-builder at digest d1, but stale-base's base chain reaches it at d0"},
		{"chain reaches nothing held", map[string]string{"orphan": "d6"}, nil, "profile orphan not held by parent, and its base chain (something-else) reaches no profile the parent holds"},
		{"unknown definition", map[string]string{"engineer-reviewer": "d9"}, nil, "profile engineer-reviewer not held by parent; its base chain cannot be checked: no such profile definition"},
		// V-D1-8: a child expiring later than its parent is refused, stacked or not.
		{"V-D1-8 later expiry", map[string]string{"engineer-reviewer": "d2"}, func(c *Grant) { c.Expires = expiry.Add(time.Second) }, "child expiry after parent expiry"},
		// A profile the parent HOLDS keeps today's rule: same digest or refused.
		{"held under another digest", map[string]string{"manager-builder": "d0"}, nil, "profile manager-builder digest differs (parent d1, child d0)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Narrows(stackParent(), "parent-digest", stackChild(tt.profiles, tt.mutate), opts)
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

// With no base-chain resolver the rule is today's: a profile not held is
// refused, so every existing caller is unchanged until it opts in.
func TestNarrowsWithoutABaseChainKeepsTodaysRule(t *testing.T) {
	err := Narrows(stackParent(), "parent-digest", stackChild(map[string]string{"engineer-reviewer": "d2"}, nil), WalkOptions{})
	if err == nil || !strings.Contains(err.Error(), "profile engineer-reviewer not held by parent") {
		t.Fatalf("error %v, want today's refusal", err)
	}
}

// A base chain longer than the walk's bound is refused rather than followed.
func TestNarrowsRefusesAnOverlongBaseChain(t *testing.T) {
	long := make([]BaseLink, maxBaseDepth+1)
	for i := range long {
		long[i] = BaseLink{Name: "b" + strings.Repeat("x", i), Digest: "d"}
	}
	opts := WalkOptions{BaseChain: func(string, string) ([]BaseLink, error) { return long, nil }}
	err := Narrows(stackParent(), "parent-digest", stackChild(map[string]string{"deep": "dd"}, nil), opts)
	if err == nil || !strings.Contains(err.Error(), "base chain longer than") {
		t.Fatalf("error %v, want an overlong-chain refusal", err)
	}
}

// V-D1-2 end to end: the card root holds manager-builder; the link S -> W
// binds engineer-reviewer through its base and the walk verifies.
func TestWalkVerifiesAStackedLink(t *testing.T) {
	rootG := rootGrant()
	rootG.Profiles = map[string]string{"manager-builder": "d1"}
	root := rootRecord(rootG)
	link := linkRecord(root, linkGrant("root-principal", "worker", map[string]string{"engineer-reviewer": "d2"}))
	opts := WalkOptions{BaseChain: stackChains(stackTable)}
	got, err := Walk(now, link, store(root, link), signedTextVerifier(), opts)
	if err != nil {
		t.Fatalf("stacked chain rejected: %v", err)
	}
	if !Holds(got, "engineer-reviewer") || Holds(got, "manager-builder") {
		t.Errorf("effective grant %+v; the worker holds exactly its own profile", got.Profiles)
	}
	if _, err := Walk(now, link, store(root, link), signedTextVerifier(), WalkOptions{}); err == nil {
		t.Error("the stacked chain verified with no base-chain resolver")
	}
}

// V-D1-2b (solid-olive): the same profile with fewer rights than its profile
// derives must stay REFUSED until rights are explicit record lines (D1 section
// 4). Rights are derived from profile names in the interim, so a record whose
// rights line differs is a parse refusal. Do not make this pass by leniency.
func TestStackedRecordWithFewerRightsThanItsProfileStaysRefused(t *testing.T) {
	text := stackChild(map[string]string{"engineer-reviewer": "d2", "manager-builder": "d1"}, nil).CanonicalText()
	fewer := strings.Replace(text, "rights: spinclass:profile:engineer-reviewer spinclass:profile:manager-builder",
		"rights: spinclass:profile:engineer-reviewer", 1)
	if fewer == text {
		t.Fatal("test setup: the rights line was not found")
	}
	if _, err := ParseCanonicalText(fewer); err == nil || !strings.Contains(err.Error(), "do not match profiles") {
		t.Fatalf("a record with fewer rights than its profiles parsed (err %v); V-D1-2b must stay red until rights are record lines", err)
	}
}
