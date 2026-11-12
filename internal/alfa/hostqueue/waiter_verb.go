package hostqueue

import (
	"fmt"
	"time"
)

// Per-waiter management verbs (spinclass#483): priority, hold-waiter,
// release-waiter and cancel. Each is the same act: authorize, find the one
// waiter, write the journal line FIRST (the verb fails closed without it), send
// a command to the waiter, and wait up to a poll plus a margin for the waiter to
// take it. What the waiter did is read back from the ledger, not assumed.

// WaiterResult is what a per-waiter verb did.
type WaiterResult struct {
	Seq uint64
	Old string
	New string
	// Applied is true when the waiter took the command within the wait.
	Applied bool
	// TooLate is true for a cancel that lost the race: the gate was admitted
	// before it could take the command. Nothing was cancelled.
	TooLate bool
}

// findWaiter is the one waiter of session.
func (l Ledger) findWaiter(session string) (Marker, error) {
	view, err := l.View(false)
	if err != nil {
		return Marker{}, err
	}
	var matches []Marker
	for _, m := range view.Waiting {
		if m.Session == session {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 0:
		return Marker{}, fmt.Errorf("%w for %q", ErrNoWaiter, session)
	case 1:
		return matches[0], nil
	}
	return Marker{}, fmt.Errorf("%d gates of %q are waiting; name the session of one gate", len(matches), session)
}

// waiterState reads back where the waiter with m's place is now: still waiting
// (with its current marker), or gone, in which case admitted says whether a
// live gate carries its place.
func (l Ledger) waiterState(m Marker) (still *Marker, admitted bool) {
	view, err := l.View(false)
	if err != nil {
		return nil, false
	}
	for _, w := range view.Waiting {
		if w.Seq == m.Seq && w.Session == m.Session {
			w := w
			return &w, false
		}
	}
	for _, g := range view.Live {
		if g.Seq == m.Seq && g.Session == m.Session {
			return nil, true
		}
	}
	return nil, false
}

// waiterVerb runs one per-waiter verb. plan returns the journal's old and new
// values and the command; judge says, from where the waiter is afterwards,
// whether the command took (and, for a cancel, whether it was too late).
func (l Ledger) waiterVerb(
	a Actor, v Verb, session string, now time.Time, wait time.Duration,
	plan func(m Marker) (old, new string, c Command),
	judge func(still *Marker, admitted bool) (applied, tooLate bool),
) (WaiterResult, error) {
	if err := Authorize(v, a); err != nil {
		return WaiterResult{}, err
	}
	m, err := l.findWaiter(session)
	if err != nil {
		return WaiterResult{}, err
	}
	old, newVal, c := plan(m)
	res := WaiterResult{Seq: m.Seq, Old: old, New: newVal}

	rec := JournalRecord{Time: now.UTC(), Act: string(v), Target: session, Seq: m.Seq, Old: old, New: newVal}
	rec.Actor, rec.Assurance = assuranceOf(a)
	if err := l.AppendJournal(rec); err != nil {
		return res, fmt.Errorf("the journal cannot be written, so nothing was changed: %w", err)
	}
	c.Session, c.Seq, c.PID, c.WaitingSince, c.At, c.Actor = session, m.Seq, m.PID, m.WaitingSince, now.UTC(), rec.Actor
	path, err := l.SendCommand(c)
	if err != nil {
		return res, err
	}
	deadline := time.Now().Add(wait)
	for CommandPending(path) && time.Now().Before(deadline) {
		if still, _ := l.waiterState(m); still == nil {
			break // the waiter is gone: admitted, cancelled or dead
		}
		time.Sleep(50 * time.Millisecond)
	}
	still, admitted := l.waiterState(m)
	res.Applied, res.TooLate = judge(still, admitted)
	if !res.Applied {
		undo := rec
		undo.Act, undo.Old, undo.New = string(v)+"-unapplied", newVal, old
		if res.TooLate {
			undo.Act = string(v) + "-too-late"
		}
		_ = l.AppendJournal(undo)
	}
	return res, nil
}

// RequestPriority sets a waiter's priority as actor. Raising and lowering are
// both operator acts in v1 (Authorize).
func (l Ledger) RequestPriority(a Actor, session, level string, now time.Time, wait time.Duration) (WaiterResult, error) {
	if err := Authorize(VerbPriority, a); err != nil {
		return WaiterResult{}, err
	}
	level, err := ParsePriority(level)
	if err != nil {
		return WaiterResult{}, err
	}
	return l.waiterVerb(a, VerbPriority, session, now, wait,
		func(m Marker) (string, string, Command) {
			return PriorityLabel(m), level, Command{Verb: cmdVerbPri, Priority: level}
		},
		func(still *Marker, _ bool) (bool, bool) { return still != nil && PriorityLabel(*still) == level, false })
}

// RequestHoldWaiter holds (or, with hold false, releases) one waiter: it keeps
// its place but is skipped while held, its max-wait clock and its aging pause,
// and the hold expires at max-wait (the waiter's own look applies that).
func (l Ledger) RequestHoldWaiter(a Actor, session string, hold bool, now time.Time, wait time.Duration) (WaiterResult, error) {
	verb, cmd := VerbHoldWaiter, cmdVerbHold
	if !hold {
		verb, cmd = VerbReleaseWaiter, cmdVerbRelease
	}
	label := func(held bool) string {
		if held {
			return "held"
		}
		return "not held"
	}
	return l.waiterVerb(a, verb, session, now, wait,
		func(m Marker) (string, string, Command) {
			return label(m.Held), label(hold), Command{Verb: cmd}
		},
		func(still *Marker, _ bool) (bool, bool) { return still != nil && still.Held == hold, false })
}

// RequestCancel ends one waiter's wait: that gate fails with "cancelled", its
// attestation stays buffered. A cancel that loses the race (the gate was
// admitted inside its poll) is ignored: the result is TooLate, and the journal
// says so.
func (l Ledger) RequestCancel(a Actor, session string, now time.Time, wait time.Duration) (WaiterResult, error) {
	return l.waiterVerb(a, VerbCancel, session, now, wait,
		func(Marker) (string, string, Command) { return "waiting", "cancelled", Command{Verb: cmdVerbCancel} },
		func(still *Marker, admitted bool) (bool, bool) {
			switch {
			case still != nil:
				return false, false // never took it
			case admitted:
				return false, true // too late: it is running
			}
			return true, false // gone from the line without a slot: cancelled
		})
}
