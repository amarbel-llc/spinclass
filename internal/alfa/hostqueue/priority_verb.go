package hostqueue

import (
	"fmt"
	"time"
)

// PriorityResult is what a priority request did.
type PriorityResult struct {
	Seq uint64
	Old string
	New string
	// Applied is true when the waiter took the new priority within the wait. When
	// false the waiter did not apply it in time (it may have been admitted: too
	// late), and nothing about its priority changed.
	Applied bool
}

// RequestPriority sets a waiter's priority as actor. The journal line is written
// first and the verb fails CLOSED without it (plan G8); then a command is sent to
// the waiter, which applies it at its next look (at most a poll). The call waits
// up to wait for that and reports whether it took. Raising and lowering are both
// operator acts in v1 (Authorize).
func (l Ledger) RequestPriority(a Actor, session, level string, now time.Time, wait time.Duration) (PriorityResult, error) {
	if err := Authorize(VerbPriority, a); err != nil {
		return PriorityResult{}, err
	}
	level, err := ParsePriority(level)
	if err != nil {
		return PriorityResult{}, err
	}
	view, err := l.View(false)
	if err != nil {
		return PriorityResult{}, err
	}
	var matches []Marker
	for _, m := range view.Waiting {
		if m.Session == session {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 0:
		return PriorityResult{}, fmt.Errorf("%w for %q", ErrNoWaiter, session)
	case 1:
	default:
		return PriorityResult{}, fmt.Errorf("%d gates of %q are waiting; name the session of one gate", len(matches), session)
	}
	m := matches[0]
	res := PriorityResult{Seq: m.Seq, Old: PriorityLabel(m), New: level}

	rec := JournalRecord{Time: now.UTC(), Act: string(VerbPriority), Target: session, Seq: m.Seq, Old: res.Old, New: level}
	rec.Actor, rec.Assurance = assuranceOf(a)
	if err := l.AppendJournal(rec); err != nil {
		return res, fmt.Errorf("the journal cannot be written, so nothing was changed: %w", err)
	}
	path, err := l.SendCommand(Command{
		Verb: cmdVerbPri, Session: session, Seq: m.Seq, PID: m.PID, WaitingSince: m.WaitingSince,
		Priority: level, At: now.UTC(), Actor: rec.Actor,
	})
	if err != nil {
		return res, err
	}
	deadline := time.Now().Add(wait)
	for CommandPending(path) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	// A command file that is gone was applied, or collected because its waiter
	// was admitted or left first: only the waiter's own marker tells which.
	if after, err := l.View(false); err == nil {
		for _, w := range after.Waiting {
			if w.Seq == m.Seq && w.Session == session && PriorityLabel(w) == level && !CommandPending(path) {
				res.Applied = true
			}
		}
	}
	if !res.Applied {
		undo := rec
		undo.Act, undo.Old, undo.New = "priority-unapplied", level, res.Old
		_ = l.AppendJournal(undo)
	}
	return res, nil
}
