package spawn

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/grantstore"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	"code.linenisgreat.com/spinclass/internal/bravo/grantverify"
	"code.linenisgreat.com/spinclass/internal/charlie/profilerights"
	"golang.org/x/crypto/ssh"
)

// enforceProfileRight proves the driver holds the resolved profile and mints
// the child's narrowing link, returning the digest of the grant record that
// authorized the spawn. It runs before the worker worktree exists, so a refusal
// creates nothing.
//
// The returned undo (never nil on success) unbinds the minted link; the caller
// runs it if a later step of the spawn fails, so no link outlives a worker that
// never started.
func enforceProfileRight(driver Driver, resolved ResolvedProfile, childSession string) (grant string, undo func(), err error) {
	// A principal-less driver cannot be a grantee or a link's grantor, and
	// "who is asking" is the whole question enforcement answers.
	if driver.Principal == "" {
		return "", nil, fmt.Errorf("profile %q refused: profile rights are enforced and the spawner has no principal to hold a grant", resolved.Name)
	}
	auth, err := newProfileAuthorizer().Authorize(driver, resolved)
	if err != nil {
		return "", nil, fmt.Errorf("profile %q refused: %w", resolved.Name, err)
	}
	undo = func() {
		if auth.Undo != nil {
			auth.Undo()
		}
	}
	if err := auth.IssueChild(childSession); err != nil {
		undo()
		return "", nil, fmt.Errorf("profile %q refused: %w", resolved.Name, err)
	}
	return auth.GrantDigest, undo, nil
}

// productionAuthorizer is the real ProfileAuthorizer. It checks confinement
// BEFORE opening the grant store, so that while the boundary does not hold
// (every host today) turning enforcement on refuses with the failing facts and
// touches nothing on disk.
type productionAuthorizer struct {
	confinementFor func(Driver) profilerights.Confinement
}

func newProductionAuthorizer() ProfileAuthorizer {
	return productionAuthorizer{confinementFor: func(d Driver) profilerights.Confinement {
		return confinement.ForDriver{SessionKey: d.SessionKey, Principal: d.Principal}
	}}
}

func (p productionAuthorizer) Authorize(driver Driver, profile ResolvedProfile) (ProfileAuthorization, error) {
	conf := p.confinementFor(driver)
	if err := conf.Check(); err != nil {
		return ProfileAuthorization{}, fmt.Errorf("profile rights are enforced but cannot be card-rooted: %w", err)
	}
	// The trust anchors come from the passwd home, never the spawner's env
	// (audit row 34-31): `sc spawn` runs with whatever env its caller has.
	dir, err := grantstore.TrustedDir()
	if err != nil {
		return ProfileAuthorization{}, err
	}
	signersPath, err := grantverify.TrustedSignersPath()
	if err != nil {
		return ProfileAuthorization{}, err
	}
	store, err := grantstore.Open(dir)
	if err != nil {
		return ProfileAuthorization{}, err
	}
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		return ProfileAuthorization{}, err
	}
	checker := &profilerights.Checker{
		Store:    store,
		RootKeys: func() ([]ssh.PublicKey, error) { return grantverify.LoadSigners(signersPath) },
		// The frontend's principal mapping, never the worktree-local session
		// state the confined agent can rewrite (FDR 0035).
		SessionOfPrincipal: confinement.SessionOfPrincipal(stateDir, confinement.LiveSession),
		// D1 stacking: a profile narrowing one the driver holds is authorized
		// by that grant (FDR 0034 "Verification").
		BaseChain:   baseChainVia(resolveProfile),
		Confinement: conf,
		Now:         time.Now,
	}
	return adaptChecker(checker, driver, profile)
}

// adaptChecker runs the checker for one driver and profile and wraps the
// result in the spawn package's vocabulary.
func adaptChecker(checker *profilerights.Checker, driver Driver, profile ResolvedProfile) (ProfileAuthorization, error) {
	a, err := checker.Authorize(driver.Principal, driver.SessionKey, profile.Name, profile.Digest)
	if err != nil {
		return ProfileAuthorization{}, err
	}
	var minted string // the session key a link was bound to, for Undo
	return ProfileAuthorization{
		GrantDigest: a.RecordDigest,
		IssueChild: func(childSession string) error {
			if childSession == "" {
				return errors.New("no child session key to bind the link to")
			}
			_, issued, err := checker.IssueChildLink(a, driver.Principal, childSession, profile.Name, profile.Digest)
			if err == nil && issued {
				minted = childSession
			}
			return err
		},
		Undo: func() {
			if minted == "" {
				return
			}
			if err := checker.Store.Unbind(minted); err != nil {
				slog.Warn("could not unbind the child link of a failed spawn", "session", minted, "err", err)
			}
			minted = ""
		},
	}, nil
}
