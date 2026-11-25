// Package keyholdersweep tells the key holder pairs that belong to a session
// from the ones nobody will ever close: units or directories whose session record
// is gone. `sc keyholder-stop --all --dry-run` reports them and `sc clean` stops
// them (docs/plans/2026-11-25-key-holder.md, "What off does").
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
	// has no directory at all, so no launch can be in flight).
	Orphan bool
	// Why says, in a few words, what makes it an orphan or what keeps it.
	Why string
}

// Classify marks each found pair as owned by a live session or an orphan. live
// is the set of session keys with a live (not closed) record.
func Classify(found []keyholder.Found, live map[string]bool, minAge time.Duration) []Pair {
	out := make([]Pair, 0, len(found))
	for _, f := range found {
		p := Pair{Found: f}
		switch {
		case f.Session != "" && live[f.Session]:
			p.Why = "session " + f.Session + " is live"
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

// Orphans finds every pair and returns the ones with no live session.
func Orphans(ctx context.Context) ([]Pair, error) {
	found, err := NewLauncher().Find(ctx)
	if err != nil {
		return nil, err
	}
	live, err := LiveSessionKeys()
	if err != nil {
		return nil, err
	}
	var orphans []Pair
	for _, p := range Classify(found, live, OrphanMinAge) {
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
