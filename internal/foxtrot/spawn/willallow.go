package spawn

import (
	"errors"
	"fmt"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/clown"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// TEMPORARY (spinclass#384 stopgap, docs/plans/2026-11-12-spawn-allowlist-stopgap.md):
// the one place the "an unattended driver may not spawn" bound (spinclass#476)
// is relaxed. Delete this file, allowdiag.go, their tests, the SpawnAllow
// schema, the trusted accessor, the seam branches in cmd/spinclass, the hook
// journaling calls and the validate check when ajour plus profiles enforce who
// may spawn unattended workers; the bound is then exactly as in 036b9e2.

// Seams for tests.
var (
	allowSessionOf    = confinement.SessionKeyOfPrincipal
	allowListSessions = func() ([]session.State, error) { return session.ListAll(nil) }
	allowNow          = func() time.Time { return time.Now().UTC() }
	// allowClownCounts is clown's presence index: live clown harnesses per
	// session key. A spawned worker's recorded PID is routinely dead while its
	// harness runs (#153), so "running" is the state `sc list` shows, which
	// folds this in (ResolveDisplayState).
	allowClownCounts = func(now time.Time) map[string]int {
		out := map[string]int{}
		for key, ps := range clown.PresenceByDecoration(now) {
			out[key] = len(ps)
		}
		return out
	}
)

// The reasons a listed (or unidentifiable) caller is refused; AllowRefusal.Kind.
const (
	RefusalNoMapping        = "no-mapping"
	RefusalNoRepo           = "no-repo"
	RefusalUnreadable       = "unreadable"
	RefusalExpired          = "expired"
	RefusalCap              = "cap"
	RefusalCount            = "count"
	RefusalWorkerNotDontAsk = "worker-not-dontask"
	RefusalDriverAttended   = "driver-attended"
)

// AllowRefusal is why the allow-list did not pass a spawn, in words the caller
// can read (the ask text) and fields the journal records.
type AllowRefusal struct {
	Kind       string
	Reason     string
	SessionKey string
	// Running and Cap are set for RefusalCap.
	Running, Cap int
}

func (r *AllowRefusal) Error() string { return r.Reason }

// AllowUse is why DriverMaySpawn said yes: the entry that matched and the
// driver's running-worker count, for the journal.
type AllowUse struct {
	Entry      sweatfile.SpawnAllowEntry
	SessionKey string
	// Live is the number of the driver's workers running at the time (the
	// historical field name, kept for the journal's live_workers).
	Live int
}

// allowEval is everything both DriverMaySpawn and the diagnosis need.
type allowEval struct {
	refusal *AllowRefusal // set when evaluation stopped before matching
	// hasEntries is true when a trusted layer lists at least one valid entry.
	hasEntries bool
	key        string
	matched    []sweatfile.SpawnAllowEntry // valid entries naming the caller, expired or not
	unexpired  []sweatfile.SpawnAllowEntry
	expiredAt  time.Time // latest expiry among the matched entries, when all expired
	running    int
}

func evaluateAllow(home, cwd, principal string) allowEval {
	var ev allowEval
	refuse := func(kind, format string, a ...any) allowEval {
		ev.refusal = &AllowRefusal{Kind: kind, Reason: fmt.Sprintf(format, a...), SessionKey: ev.key}
		return ev
	}
	repo := repoFor(cwd)
	if repo == "" {
		return refuse(RefusalNoRepo, "the spawn allow-list is read from the repo's trusted sweatfile layers, and this session's working directory is not in a repo")
	}
	h, err := sweatfileio.LoadHierarchy(home, repo)
	if err != nil {
		return refuse(RefusalUnreadable, "the spawn allow-list could not be read: %v", err)
	}
	entries, _ := sweatfileio.TrustedSpawnAllow(h, home, repo)
	var valid []sweatfile.SpawnAllowEntry
	for _, e := range entries {
		if e.Problem == "" {
			valid = append(valid, e)
		}
	}
	ev.hasEntries = len(valid) > 0
	if principal == "" {
		return refuse(RefusalNoMapping, "the spawn allow-list could not identify this session: it has no principal")
	}
	key, err := allowSessionOf(principal)
	if err != nil {
		return refuse(RefusalNoMapping, "the spawn allow-list could not identify this session: its principal has no live frontend mapping (%v)", err)
	}
	ev.key = key
	now := allowNow()
	for _, e := range valid {
		if (e.Principal != "" && e.Principal == principal) || (e.Session != "" && e.Session == key) {
			ev.matched = append(ev.matched, e)
			if e.Expired(now) {
				if e.Expires.After(ev.expiredAt) {
					ev.expiredAt = e.Expires
				}
			} else {
				ev.unexpired = append(ev.unexpired, e)
			}
		}
	}
	if len(ev.unexpired) == 0 {
		return ev
	}
	states, err := allowListSessions()
	if err != nil {
		return refuse(RefusalCount, "the spawn allow-list could not count this session's running workers: %v", err)
	}
	counts := allowClownCounts(now)
	for i := range states {
		s := &states[i]
		if s.SpawnedByPrincipal != principal || s.Kind == session.KindImplicit {
			continue
		}
		switch s.ResolveDisplayState(counts[s.SessionKey]) {
		case session.StateActive, session.StateRunningDetached:
			ev.running++
		}
	}
	return ev
}

// DriverMaySpawn reports whether the calling session, which is unattended, is
// listed in a trusted layer's [[permissions.spawn-allow]] and may therefore
// spawn a dontAsk worker without a prompt. It is nil only when ALL hold: the
// principal resolves to a live session through the frontend mapping (the agent
// cannot write it); the driver sits in a repo whose trusted layers are read;
// an entry that validates names the principal (or its session), has not
// expired, and the number of the caller's workers RUNNING (not idle or closed
// ones; the state `sc list` shows as active or running-detached) is under the
// entry's cap. Every doubt is an error, which keeps the prompt; it is an
// *AllowRefusal naming why, which the ask text and the journal carry.
func DriverMaySpawn(home, cwd, principal string) (AllowUse, error) {
	ev := evaluateAllow(home, cwd, principal)
	if ev.refusal != nil {
		return AllowUse{}, ev.refusal
	}
	if len(ev.matched) == 0 {
		return AllowUse{}, errors.New("no valid spawn-allow entry lists the caller")
	}
	if len(ev.unexpired) == 0 {
		return AllowUse{}, expiredRefusal(ev)
	}
	for _, e := range ev.unexpired {
		if ev.running < e.MaxWorkers {
			return AllowUse{Entry: e, SessionKey: ev.key, Live: ev.running}, nil
		}
	}
	return AllowUse{}, capRefusal(ev)
}

func expiredRefusal(ev allowEval) *AllowRefusal {
	return &AllowRefusal{
		Kind:       RefusalExpired,
		SessionKey: ev.key,
		Reason:     fmt.Sprintf("the spawn allow-list entry for this session expired at %s", ev.expiredAt.Format(time.RFC3339)),
	}
}

func capRefusal(ev allowEval) *AllowRefusal {
	limit := 0
	for _, e := range ev.unexpired {
		limit = max(limit, e.MaxWorkers)
	}
	return &AllowRefusal{
		Kind:       RefusalCap,
		SessionKey: ev.key,
		Running:    ev.running,
		Cap:        limit,
		Reason: fmt.Sprintf("the spawn allow-list cap is reached: this session has %d workers running, of %d allowed (max-workers counts running workers; idle and closed ones do not)",
			ev.running, limit),
	}
}
