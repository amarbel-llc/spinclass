// Package profilerights enforces the profile right (FDR 0034, Phase 2): a spawn
// that names a profile succeeds only if the spawner's grant chain, rooted in a
// card-signed record, holds that profile pinned to its current content digest.
// It composes the pure chain rules (profilegrant), the record store
// (grantstore) and the card verifier (grantverify); it contains no policy of
// its own beyond the order in which it consults them.
package profilerights

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/profilegrant"
	"code.linenisgreat.com/spinclass/internal/alfa/grantstore"
	"code.linenisgreat.com/spinclass/internal/bravo/grantverify"
	"golang.org/x/crypto/ssh"
)

// Confinement answers whether a grant may be treated as card-rooted at all:
// the card root is only real if a worker cannot reach or paint the operator's
// prompt (FDR 0034, "Where the card root is real").
type Confinement interface{ Check() error }

// Unestablished is the production Confinement until the agent-scope boundary
// exists. It always fails, so enabling enforcement today refuses safely instead
// of silently trusting a root a worker could have forged at the prompt.
type Unestablished struct{}

// Check always returns an error.
func (Unestablished) Check() error {
	return errors.New("a grant cannot be treated as card-rooted until the operator-prompt surface " +
		"is out of reach of workers (FDR 0034 \"Where the card root is real\"): the agent-scope " +
		"boundary is not established (spinclass#338, clown#244, posh attach authorization)")
}

// Checker authorizes spawns against stored grant chains.
type Checker struct {
	Store *grantstore.Store
	// RootKeys returns the card keys a root signature must verify under.
	RootKeys func() ([]ssh.PublicKey, error)
	// SessionOfPrincipal joins a link naming a session key as grantee to the
	// next link whose grantor is that session's principal.
	SessionOfPrincipal func(principal string) (sessionKey string, ok bool)
	// BaseChain resolves a profile definition's _base chain (D1 stacking,
	// profilegrant.WalkOptions.BaseChain). nil: only held profiles authorize.
	BaseChain   func(profile, digest string) ([]profilegrant.BaseLink, error)
	Confinement Confinement
	Now         func() time.Time
}

// Authorization is the outcome of a successful Authorize.
type Authorization struct {
	// RecordDigest is the record the walk started from: the spawner's own.
	RecordDigest string
	// Effective is that record's grant, verified up to the card root.
	Effective profilegrant.Grant
	// MayPassOn is whether the spawner's grant allows minting a child link.
	MayPassOn bool

	keys []ssh.PublicKey
}

func (c *Checker) walkOptions() profilegrant.WalkOptions {
	return profilegrant.WalkOptions{SessionOfPrincipal: c.SessionOfPrincipal, BaseChain: c.BaseChain}
}

func (c *Checker) verifier(keys []ssh.PublicKey) profilegrant.Verifier {
	return profilegrant.NewVerifier(grantverify.NewRootVerifier(keys), profilegrant.LocalLinkVerifier{})
}

// Authorize checks that the driver holds profile pinned to pinnedDigest. The
// confinement check runs first and alone decides whether anything else is
// worth reading; every later failure also refuses (fail closed).
func (c *Checker) Authorize(driverPrincipal, driverSession, profile, pinnedDigest string) (*Authorization, error) {
	if err := c.Confinement.Check(); err != nil {
		return nil, fmt.Errorf("profile rights are enforced but cannot be card-rooted: %w", err)
	}
	keys, err := c.RootKeys()
	if err != nil {
		return nil, fmt.Errorf("profile rights are enforced but the card's signers cannot be loaded: %w", err)
	}
	if driverPrincipal == "" && driverSession == "" {
		return nil, errors.New("profile rights are enforced but the spawner has neither a principal nor a session to hold a grant")
	}

	rec, ok := c.currentRecord(driverSession, driverPrincipal)
	if !ok {
		return nil, fmt.Errorf("no profile grant for this session or principal (session %q, principal %q); "+
			"an operator holding the card runs `sc profile-grant-bless` and imports the record with `sc profile-grant-import`",
			driverSession, driverPrincipal)
	}

	effective, err := profilegrant.Walk(c.Now(), rec, c.Store.Resolver(), c.verifier(keys), c.walkOptions())
	if err != nil {
		return nil, fmt.Errorf("profile grant chain does not verify: %w", err)
	}
	if !profilegrant.Holds(effective, profile) {
		if c.BaseChain == nil {
			return nil, fmt.Errorf("profile %q is not held; this spawner holds: %s", profile, heldList(effective))
		}
		// D1 stacking: a profile that narrows one the spawner holds is authorized
		// by that grant; its link then verifies through the same chain.
		if err := profilegrant.ReachesHeldBase(effective, profile, pinnedDigest, c.walkOptions()); err != nil {
			return nil, fmt.Errorf("profile %q is not held and does not stack on a held one (this spawner holds: %s): %w", profile, heldList(effective), err)
		}
	} else if granted := effective.Profiles[profile]; granted != pinnedDigest {
		return nil, fmt.Errorf("profile %s changed since it was granted (granted %s, now %s): an authority must re-pin it",
			profile, granted, pinnedDigest)
	}
	return &Authorization{
		RecordDigest: rec.Digest,
		Effective:    effective,
		MayPassOn:    effective.Onward == profilegrant.OnwardGrant,
		keys:         keys,
	}, nil
}

// currentRecord finds the record the driver holds. A principal-bound record is
// always the principal's own. A session-bound record is the driver's only when
// the principal provably belongs to that session: the session key comes from
// the cwd and the principal from the environment, so without the join a driver
// could claim another worktree's grant by standing in it.
//
// Residual: under clown the sanctioned MCP path has a fixed environment, but
// the `sc spawn` CLI trusts its caller's environment (CLOWN_SESSION_ID), so a
// same-uid process can still present any principal there (FDR 0032 D17).
func (c *Checker) currentRecord(session, principal string) (profilegrant.Record, bool) {
	if session != "" && principal != "" && c.SessionOfPrincipal != nil {
		if mapped, ok := c.SessionOfPrincipal(principal); ok && mapped == session {
			if rec, ok := c.Store.Current(session); ok {
				return rec, true
			}
		}
	}
	if principal != "" {
		return c.Store.Current(principal)
	}
	return profilegrant.Record{}, false
}

func heldList(g profilegrant.Grant) string {
	names := make([]string, 0, len(g.Profiles))
	for n := range g.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// IssueChildLink mints the narrowing link that gives the child its own right
// to the profile, or reports (zero, false, nil) when the spawner may not pass
// the right on: the child then runs under the profile but holds nothing to
// spawn with (FDR 0034 Phase 2 design item 9). A link is stored, bound to the
// child's session and re-verified through the same walk a later spawn would
// make; a link that does not verify is unbound rather than left usable. The
// link's grantor is the spawner's PRINCIPAL; the walk joins it to the spawner's
// record through SessionOfPrincipal.
func (c *Checker) IssueChildLink(a *Authorization, driverPrincipal, childSession, profile, digest string) (profilegrant.Record, bool, error) {
	if !a.MayPassOn {
		return profilegrant.Record{}, false, nil
	}
	if driverPrincipal == "" {
		return profilegrant.Record{}, false, errors.New("cannot issue a child link: the spawner has no principal to be its grantor")
	}
	// A key must not inherit or overwrite a binding: a blessed grant, a
	// previous link, or a reused session key would otherwise be silently
	// replaced by (or leak into) this child's authority.
	if _, bound := c.Store.Current(childSession); bound {
		return profilegrant.Record{}, false, fmt.Errorf("cannot issue a child link: session %q already has a grant binding", childSession)
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return profilegrant.Record{}, false, fmt.Errorf("cannot issue a child link: %w", err)
	}
	link := profilegrant.Grant{
		Grantor:  driverPrincipal,
		Grantee:  childSession,
		Session:  childSession,
		Profiles: map[string]string{profile: digest},
		Onward:   profilegrant.OnwardNone,
		Expires:  a.Effective.Expires,
		Nonce:    hex.EncodeToString(nonce),
		Widens:   "-",
		Prev:     a.RecordDigest,
	}
	text := link.CanonicalText()
	recDigest, err := profilegrant.DigestOf(text)
	if err != nil {
		return profilegrant.Record{}, false, fmt.Errorf("cannot issue a child link: %w", err)
	}
	rec := profilegrant.Record{Digest: recDigest, Text: text}
	if err := c.Store.Put(rec); err != nil {
		return profilegrant.Record{}, false, fmt.Errorf("cannot issue a child link: %w", err)
	}
	if err := c.Store.Bind(childSession, recDigest); err != nil {
		return profilegrant.Record{}, false, fmt.Errorf("cannot issue a child link: %w", err)
	}
	if _, err := profilegrant.Walk(c.Now(), rec, c.Store.Resolver(), c.verifier(a.keys), c.walkOptions()); err != nil {
		_ = c.Store.Unbind(childSession)
		return profilegrant.Record{}, false, fmt.Errorf("the child link does not verify, so it was not bound: %w", err)
	}
	return rec, true, nil
}
