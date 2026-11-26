// Package keyholdersweep tells the key holder pairs that belong to a session
// from the ones nobody will ever close: units or directories whose session record
// is gone. `sc keyholder-stop --all --dry-run` reports them and `sc clean` stops
// them (docs/plans/2026-11-25-key-holder.md, "What off does").
//
// A pair is only ever swept when THIS state root recorded it. Units are per user
// but the state root follows the caller's XDG_STATE_HOME, so a process under
// another state root lists every pair of the account with no directory here:
// those are classified Unowned, reported by name and never stopped by a sweep.
package keyholdersweep

import (
	"context"
	"fmt"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// OrphanMinAge is how old a pair with no session must be before it counts as
// abandoned. A launch starts the pair before it writes the session record, so a
// pair a few seconds old with no record is a launch in progress, not an orphan.
const OrphanMinAge = 10 * time.Minute

// Pair is a found pair with its classification.
type Pair struct {
	keyholder.Found
	// Orphan is true when no live session owns the pair and it is old enough (or
	// has no directory at all, so no launch can be in flight). A pair this state
	// root did not record (Unowned) is never an orphan.
	Orphan bool
	// Why says, in a few words, what makes it an orphan or what keeps it.
	Why string
}

// Classify marks each found pair as owned by a live session, an orphan, or not
// this state root's at all. live is the set of session keys with a live (not
// closed) record.
func Classify(found []keyholder.Found, live map[string]bool, minAge time.Duration) []Pair {
	// A driver has no pair and no record under its hash but its kind file, so a
	// kind-only entry is told from a stray one by the live sessions' hashes.
	liveHashes := make(map[string]bool, len(live))
	for k, ok := range live {
		if ok {
			liveHashes[keyholder.Hash(k)] = true
		}
	}
	out := make([]Pair, 0, len(found))
	for _, f := range found {
		p := Pair{Found: f}
		switch {
		case f.Unowned:
			p.Why = "units this state root recorded nothing for (another XDG_STATE_HOME?): not mine, never stopped by a sweep"
		case f.Session != "" && live[f.Session]:
			p.Why = "session " + f.Session + " is live"
		case f.KindOnly && liveHashes[f.Hash]:
			p.Why = "the kind record of a live session (a driver has no pair)"
		case f.HistoryOnly && liveHashes[f.Hash]:
			p.Why = "the key history of a live session whose pair is stopped: kept on purpose, so its next start is announced as a new key"
		case liveHashes[f.Hash]:
			// A session record maps to this hash: whatever is left of its pair (a lock
			// file, units without a directory, a failed re-mint) the history and kind
			// record under it are the live session's, and a sweep that ended them would
			// let the next restart relaunch onto a dead holder socket and the next resume
			// re-key unannounced. Reported, never swept.
			p.Why = "a live session maps to this hash: reported, never swept (a failed re-mint? resume or restart the session)"
		case f.LockOnly:
			p.Orphan, p.Why = true, "a leftover lock file with no pair (removed unless a process holds it)"
		case f.KindOnly:
			p.Orphan, p.Why = true, "a stray kind record with no pair and no key history (removed unless a process holds the lock)"
		case f.HistoryOnly:
			p.Orphan, p.Why = true, "a key history with no pair and no session record (a session removed without `sc close`; removed unless a process holds the lock)"
		case f.Dir == "":
			p.Orphan, p.Why = true, "units with no directory and no session"
		case f.Age < minAge:
			p.Why = fmt.Sprintf("no live session, but only %s old (a launch may be in progress)", f.Age.Round(time.Second))
		case f.Session == "":
			p.Orphan, p.Why = true, "a directory with no readable record"
		default:
			p.Orphan, p.Why = true, "session "+f.Session+" is closed or gone"
		}
		out = append(out, p)
	}
	return out
}

// LiveSessionKeys returns the keys of the sessions that have a live record: a
// tombstone is a closed session and does not own a pair.
func LiveSessionKeys() (map[string]bool, error) {
	states, err := session.ListAll(nil)
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	live := make(map[string]bool, len(states))
	for i := range states {
		if states[i].IsTombstone() {
			continue
		}
		live[states[i].Key()] = true
	}
	return live, nil
}

// NewLauncher is the launcher a stop or a sweep uses. Neither starts a program,
// so neither needs the trusted fibby and piggy paths: it works after the host's
// configuration (and so the paths) is gone, which is exactly when the leftovers
// have to be cleared. A var so a test can stand a fake runner in.
var NewLauncher = func() *keyholder.Launcher {
	return &keyholder.Launcher{Base: keyholder.StateBase(), Run: keyholder.SystemdRunner{}}
}

// Survey finds every pair and classifies it: owned by a live session, an orphan,
// or not this state root's.
func Survey(ctx context.Context) ([]Pair, error) {
	found, err := NewLauncher().Find(ctx)
	if err != nil {
		return nil, err
	}
	live, err := LiveSessionKeys()
	if err != nil {
		return nil, err
	}
	return Classify(found, live, OrphanMinAge), nil
}

// Orphans finds every pair and returns the ones with no live session. Pairs this
// state root did not record are never among them.
func Orphans(ctx context.Context) ([]Pair, error) {
	pairs, err := Survey(ctx)
	if err != nil {
		return nil, err
	}
	var orphans []Pair
	for _, p := range pairs {
		if p.Orphan {
			orphans = append(orphans, p)
		}
	}
	return orphans, nil
}

// Describe is a pair as one line: its hash, session and state.
func Describe(p Pair) string {
	session := p.Session
	if session == "" {
		session = "unknown"
	}
	state := "stopped"
	if p.Running {
		state = "running"
	}
	return fmt.Sprintf("%s session=%s %s (%s)", p.Hash, session, state, strings.TrimSpace(p.Why))
}
