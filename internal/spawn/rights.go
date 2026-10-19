package spawn

import (
	"errors"
	"fmt"
	"time"

	"code.linenisgreat.com/spinclass/internal/grantstore"
	"code.linenisgreat.com/spinclass/internal/grantverify"
	"code.linenisgreat.com/spinclass/internal/profilerights"
	"code.linenisgreat.com/spinclass/internal/session"
	"golang.org/x/crypto/ssh"
)

// enforceProfileRight proves the driver holds the resolved profile and mints
// the child's narrowing link, returning the digest of the grant record that
// authorized the spawn. It runs before the worker worktree exists, so a refusal
// creates nothing.
func enforceProfileRight(driver Driver, resolved ResolvedProfile, childSession string) (string, error) {
	// A principal-less driver cannot be a grantee or a link's grantor, and
	// "who is asking" is the whole question enforcement answers.
	if driver.Principal == "" {
		return "", fmt.Errorf("profile %q refused: profile rights are enforced and the spawner has no principal to hold a grant", resolved.Name)
	}
	auth, err := newProfileAuthorizer().Authorize(driver, resolved)
	if err != nil {
		return "", fmt.Errorf("profile %q refused: %w", resolved.Name, err)
	}
	if err := auth.IssueChild(childSession); err != nil {
		return "", fmt.Errorf("profile %q refused: %w", resolved.Name, err)
	}
	return auth.GrantDigest, nil
}

// productionAuthorizer is the real ProfileAuthorizer. It checks confinement
// BEFORE opening the grant store, so that with the boundary unestablished (the
// only state today) turning enforcement on refuses with that reason and touches
// nothing on disk.
type productionAuthorizer struct {
	confinement profilerights.Confinement
}

func newProductionAuthorizer() ProfileAuthorizer {
	return productionAuthorizer{confinement: profilerights.Unestablished{}}
}

func (p productionAuthorizer) Authorize(driver Driver, profile ResolvedProfile) (ProfileAuthorization, error) {
	if err := p.confinement.Check(); err != nil {
		return ProfileAuthorization{}, fmt.Errorf("profile rights are enforced but cannot be card-rooted: %w", err)
	}
	dir, err := grantstore.DefaultDir()
	if err != nil {
		return ProfileAuthorization{}, err
	}
	store, err := grantstore.Open(dir)
	if err != nil {
		return ProfileAuthorization{}, err
	}
	checker := &profilerights.Checker{
		Store:              store,
		RootKeys:           func() ([]ssh.PublicKey, error) { return grantverify.LoadSigners(grantverify.DefaultSignersPath()) },
		SessionOfPrincipal: session.SessionKeyOfPrincipal,
		Confinement:        p.confinement,
		Now:                time.Now,
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
	return ProfileAuthorization{
		GrantDigest: a.RecordDigest,
		IssueChild: func(childSession string) error {
			if childSession == "" {
				return errors.New("no child session key to bind the link to")
			}
			_, _, err := checker.IssueChildLink(a, driver.Principal, childSession, profile.Name, profile.Digest)
			return err
		},
	}, nil
}
