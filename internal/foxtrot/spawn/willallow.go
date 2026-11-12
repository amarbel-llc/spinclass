package spawn

import (
	"errors"
	"fmt"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// TEMPORARY (spinclass#384 stopgap, docs/plans/2026-11-12-spawn-allowlist-stopgap.md):
// the one place the "an unattended driver may not spawn" bound (spinclass#476)
// is relaxed. Delete this file, its test, the SpawnAllow schema, the trusted
// accessor, the seam branch in cmd/spinclass, the hook journaling call and the
// validate check when ajour plus profiles enforce who may spawn unattended
// workers; the bound is then exactly as in 036b9e2.

// Seams for tests.
var (
	allowSessionOf    = confinement.SessionKeyOfPrincipal
	allowListSessions = func() ([]session.State, error) { return session.ListAll(nil) }
	allowNow          = func() time.Time { return time.Now().UTC() }
)

// AllowUse is why DriverMaySpawn said yes: the entry that matched and the
// driver's live-worker count, for the journal.
type AllowUse struct {
	Entry      sweatfile.SpawnAllowEntry
	SessionKey string
	Live       int
}

// DriverMaySpawn reports whether the calling session, which is unattended, is
// listed in a trusted layer's [[permissions.spawn-allow]] and may therefore
// spawn a dontAsk worker without a prompt. It is nil only when ALL hold: the
// principal resolves to a live session through the frontend mapping (the agent
// cannot write it); the driver sits in a repo whose trusted layers are read;
// an entry that validates names the principal (or its session), has not
// expired, and its live-worker count is under the entry's cap. Every doubt (an
// unreadable hierarchy, an unlistable session set) is an error, which keeps the
// prompt. The error says why, for debugging; the hook never shows it as a
// reason to the model.
func DriverMaySpawn(home, cwd, principal string) (AllowUse, error) {
	if principal == "" {
		return AllowUse{}, errors.New("the caller has no principal")
	}
	key, err := allowSessionOf(principal)
	if err != nil {
		return AllowUse{}, fmt.Errorf("the caller's principal resolves to no live session: %w", err)
	}
	repo := repoFor(cwd)
	if repo == "" {
		return AllowUse{}, errors.New("the caller is not in a repo, so no trusted layers are read")
	}
	h, err := sweatfileio.LoadHierarchy(home, repo)
	if err != nil {
		return AllowUse{}, fmt.Errorf("reading the driver's sweatfile hierarchy: %w", err)
	}
	entries, _ := sweatfileio.TrustedSpawnAllow(h, home, repo)
	now := allowNow()
	var matched []sweatfile.SpawnAllowEntry
	for _, e := range entries {
		if e.Problem != "" || e.Expired(now) {
			continue
		}
		if (e.Principal != "" && e.Principal == principal) || (e.Session != "" && e.Session == key) {
			matched = append(matched, e)
		}
	}
	if len(matched) == 0 {
		return AllowUse{}, errors.New("no valid, unexpired spawn-allow entry lists the caller")
	}
	states, err := allowListSessions()
	if err != nil {
		return AllowUse{}, fmt.Errorf("counting the caller's workers: %w", err)
	}
	live := 0
	for i := range states {
		s := &states[i]
		if s.SpawnedByPrincipal == principal && s.Kind != session.KindImplicit && !s.IsTombstone() && s.ResolveState() != session.StateAbandoned {
			live++
		}
	}
	for _, e := range matched {
		if live < e.MaxWorkers {
			return AllowUse{Entry: e, SessionKey: key, Live: live}, nil
		}
	}
	return AllowUse{}, fmt.Errorf("the caller already has %d live workers, at or over every matching entry's cap", live)
}
