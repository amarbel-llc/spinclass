package hostqueue

import (
	"fmt"
	"time"
)

// Priority levels (spinclass#483). Order is priority first, then the waiter's
// place; a priority never bypasses any other admission term (the hold, the gate
// cap, memory, pressure, disk, load, settle).
const (
	PriorityLow    = "low"
	PriorityNormal = "normal"
	PriorityHigh   = "high"
)

// DefaultAgingInterval is how long a waiter waits before it counts one level
// higher (operator ruling 2026-11-12): fixed, so an unlimited max-wait still
// ages. A waiter's wait restarts when its priority is set.
const DefaultAgingInterval = 30 * time.Minute

// ParsePriority validates a level.
func ParsePriority(s string) (string, error) {
	switch s {
	case PriorityLow, PriorityNormal, PriorityHigh:
		return s, nil
	}
	return "", fmt.Errorf("priority %q is not one of low, normal, high", s)
}

func rank(p string) int {
	switch p {
	case PriorityLow:
		return 0
	case PriorityHigh:
		return 2
	}
	return 1
}

// EffectiveRank is a waiter's rank (0 low, 1 normal, 2 high) after aging: one
// level higher per interval waited since it began waiting or its priority was
// last set, up to high. Seq-0 markers (from a binary that predates numbering)
// have no priority and rank normal.
func EffectiveRank(m Marker, now time.Time, interval time.Duration) int {
	r := rank(m.Priority)
	if interval <= 0 {
		return r
	}
	from := m.AgeStart
	if from.IsZero() {
		from = m.WaitingSince
		if m.PrioritySetAt.After(from) {
			from = m.PrioritySetAt
		}
	}
	at := now
	if m.Held && !m.HeldAt.IsZero() && m.HeldAt.Before(at) {
		at = m.HeldAt // a held waiter's clock stands still
	}
	if from.IsZero() || !at.After(from) {
		return r
	}
	return min(r+int(at.Sub(from)/interval), 2)
}

// WaitedFor is how long m has really waited at now: since it began waiting, less
// the time it was held (a held waiter's max-wait clock pauses).
func WaitedFor(m Marker, now time.Time) time.Duration {
	d := now.Sub(m.WaitingSince) - m.HeldFor
	if m.Held && !m.HeldAt.IsZero() {
		d -= now.Sub(m.HeldAt)
	}
	return max(d, 0)
}

// PriorityLabel is the level a marker carries, "normal" when unset.
func PriorityLabel(m Marker) string {
	if m.Priority == "" {
		return PriorityNormal
	}
	return m.Priority
}
