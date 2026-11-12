// Package hostqueue is the host-wide admission queue for gates (#424): a
// ledger of flock-held markers, one per admitted gate, and a predicate that
// admits a waiter when the count of live markers is below the cap and the host
// probe (load, memory, memory pressure, store free space) passes, with a settle
// window between admissions. It is the mergelock pattern one level up: no
// daemon; a crashed holder's flock is released by the kernel and its marker is
// reaped by the next reader.
package hostqueue

import (
	"fmt"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/hostload"
)

// Config is the [host-queue] policy (trusted layers only). Zero values are not
// meaningful; start from DefaultConfig.
type Config struct {
	MaxGates      int
	MemoryFloor   uint64  // bytes: hold below this
	MemoryResume  uint64  // bytes: once held for memory, resume only above this
	PressureAvg10 float64 // PSI memory some avg10 above which memory is under pressure
	SwapCeiling   uint64  // bytes of swap in use above which memory is under pressure, where PSI is absent
	DiskFloor     uint64  // bytes free on the store filesystem below which gates wait
	LoadThreshold float64 // per-CPU load1 above which gates wait
	Settle        time.Duration
	Poll          time.Duration
	MaxWait       time.Duration // 0: no limit
	IdleGrace     time.Duration // 0: off
}

const gib = 1 << 30

// AdmissionMarker is text only a binary with the admission predicate contains
// (Decide's gate-count refusal). host-load looks for it in a live serve's
// executable to find sessions whose gates run unqueued; a test pins it to the
// refusal text.
const AdmissionMarker = "gate slots taken"

// DefaultConfig is the operator's ruled defaults (2026-11-10): one gate at a
// time, memory held below 4 GiB and resumed above 6 GiB.
func DefaultConfig() Config {
	return Config{
		MaxGates:      1,
		MemoryFloor:   4 * gib,
		MemoryResume:  6 * gib,
		PressureAvg10: 10.0,
		SwapCeiling:   4 * gib,
		DiskFloor:     30 * gib,
		LoadThreshold: 1.0,
		Settle:        60 * time.Second,
		Poll:          10 * time.Second,
		MaxWait:       60 * time.Minute,
		IdleGrace:     10 * time.Minute,
	}
}

// Reason names why a waiter is held. ReasonNone admits.
type Reason string

const (
	ReasonNone     Reason = ""
	ReasonHold     Reason = "hold"
	ReasonQueue    Reason = "queue" // an earlier waiter is ahead (spinclass#482)
	ReasonGates    Reason = "gates"
	ReasonMemory   Reason = "memory"
	ReasonPressure Reason = "pressure"
	ReasonSwap     Reason = "swap"
	ReasonDisk     Reason = "disk"
	ReasonLoad     Reason = "load"
	ReasonSettle   Reason = "settle"
)

// View is the ledger as the predicate sees it, read under the ledger lock.
type View struct {
	Live       []Marker
	Waiting    []Marker // for display; the predicate does not read it
	Held       bool
	HoldReason string
}

// WaiterState is what the predicate remembers about one waiter between polls.
type WaiterState struct {
	// Seq is this waiter's place in the queue (Ledger.NextSeq). Zero means the
	// caller has no place (a test, or a caller outside the queue): the waiter
	// is treated as first in line.
	Seq uint64
	// HeldForMemory is set once the waiter was refused for memory: it then
	// needs MemoryResume, not MemoryFloor (hysteresis).
	HeldForMemory bool
	// ProbeFailingSince is when the waiter's probe terms (not the gate count,
	// the hold or the settle window) began failing without a break; zero when
	// they pass. idle-grace counts from it.
	ProbeFailingSince time.Time
}

// Decision is one evaluation of the predicate.
type Decision struct {
	Admit  bool
	Reason Reason
	Detail string // one line for the waiter's log and wait reason
	// Grace is set when the waiter is admitted by idle-grace: nothing spinclass
	// runs is holding the host and waiting cannot help.
	Grace bool
	// Next is the waiter's state for its next poll.
	Next WaiterState
}

// Decide is the admission predicate, pure over its inputs. Order: the
// operator's hold, the queue (a waiter with an earlier place is admitted
// first, spinclass#482), the gate count, then the probe terms (memory,
// pressure or swap, disk, load), then the settle window. idle-grace admits past
// failing probe terms when no gate is live and nothing is held, and only for
// the waiter at the head of the queue. Every refusal's Detail begins with the
// waiter's place ("position N of M: ...").
func Decide(v View, snap hostload.Snapshot, cfg Config, w WaiterState, now time.Time) Decision {
	next := w
	pos, total, ahead := position(v.Waiting, w.Seq)
	place := ""
	if w.Seq != 0 {
		place = fmt.Sprintf("position %d of %d: ", pos, total)
	}
	if v.Held {
		next.ProbeFailingSince = time.Time{}
		detail := "held by the operator"
		if v.HoldReason != "" {
			detail += ": " + v.HoldReason
		}
		return Decision{Reason: ReasonHold, Detail: place + detail, Next: next}
	}
	if pos > 1 {
		next.ProbeFailingSince = time.Time{}
		return Decision{Reason: ReasonQueue, Detail: fmt.Sprintf("%sbehind %s", place, ahead), Next: next}
	}
	if len(v.Live) >= cfg.MaxGates {
		next.ProbeFailingSince = time.Time{}
		return Decision{Reason: ReasonGates, Detail: place + fmt.Sprintf("%d of %d gate slots taken (%s)", len(v.Live), cfg.MaxGates, holders(v.Live)), Next: next}
	}

	reason, detail := probeReason(snap, cfg, w.HeldForMemory)
	if reason == ReasonMemory {
		next.HeldForMemory = true
	} else if reason == ReasonNone {
		next.HeldForMemory = false
	}
	if reason != ReasonNone {
		if next.ProbeFailingSince.IsZero() {
			next.ProbeFailingSince = now
		}
		if cfg.IdleGrace > 0 && len(v.Live) == 0 && now.Sub(next.ProbeFailingSince) >= cfg.IdleGrace {
			return Decision{Admit: true, Reason: reason, Grace: true, Detail: fmt.Sprintf(
				"admitted by idle-grace after %s: %s, but no gate is running, so waiting cannot help", cfg.IdleGrace, detail), Next: next}
		}
		return Decision{Reason: reason, Detail: place + detail, Next: next}
	}
	next.ProbeFailingSince = time.Time{}

	if newest := newestAdmission(v.Live); !newest.IsZero() && now.Sub(newest) < cfg.Settle {
		return Decision{Reason: ReasonSettle, Detail: place + fmt.Sprintf("settling: the last gate was admitted %s ago (settle %s)", now.Sub(newest).Round(time.Second), cfg.Settle), Next: next}
	}
	return Decision{Admit: true, Next: next}
}

// position is a waiter's place in the queue: pos counts it and every waiter
// ahead (a lower Seq; one with no Seq predates numbering and is ahead of all),
// total is the waiters in line, at least pos, and ahead names the session
// directly in front. A Seq of 0 is a caller outside the queue: first in line.
func position(waiting []Marker, seq uint64) (pos, total int, ahead string) {
	if seq == 0 {
		return 1, max(len(waiting), 1), ""
	}
	pos = 1
	var front Marker
	for _, m := range waiting {
		if m.Seq == seq || (m.Seq != 0 && m.Seq > seq) {
			continue
		}
		pos++
		if front.Seq == 0 || (m.Seq != 0 && m.Seq > front.Seq) {
			front = m
		}
	}
	name := front.Session
	if name == "" {
		name = "an earlier gate"
	}
	return pos, max(len(waiting), pos), name
}

// probeReason is the first failing probe term, or ReasonNone.
func probeReason(snap hostload.Snapshot, cfg Config, heldForMemory bool) (Reason, string) {
	floor, line := cfg.MemoryFloor, "floor"
	if heldForMemory {
		floor, line = cfg.MemoryResume, "resume line"
	}
	if snap.MemAvailable < floor {
		return ReasonMemory, fmt.Sprintf("waiting for memory (%s available, %s %s)", gibString(snap.MemAvailable), line, gibString(floor))
	}
	if snap.HasPressure {
		if snap.PressureMemorySomeAvg10 > cfg.PressureAvg10 {
			return ReasonPressure, fmt.Sprintf("waiting for memory pressure to ease (PSI some avg10 %.2f, limit %.2f)", snap.PressureMemorySomeAvg10, cfg.PressureAvg10)
		}
	} else if snap.SwapUsed > cfg.SwapCeiling {
		return ReasonSwap, fmt.Sprintf("waiting for swap use to fall (%s in use, ceiling %s)", gibString(snap.SwapUsed), gibString(cfg.SwapCeiling))
	}
	if snap.HasStoreFree && snap.StoreFreeBytes < cfg.DiskFloor {
		return ReasonDisk, fmt.Sprintf("waiting for disk (%s free on the store, floor %s)", gibString(snap.StoreFreeBytes), gibString(cfg.DiskFloor))
	}
	if snap.LoadPerCPU > cfg.LoadThreshold {
		return ReasonLoad, fmt.Sprintf("waiting for load (per-CPU load1 %.2f, threshold %.2f)", snap.LoadPerCPU, cfg.LoadThreshold)
	}
	return ReasonNone, ""
}

func newestAdmission(live []Marker) time.Time {
	var newest time.Time
	for _, m := range live {
		if m.AdmittedAt.After(newest) {
			newest = m.AdmittedAt
		}
	}
	return newest
}

func holders(live []Marker) string {
	names := make([]string, 0, len(live))
	for _, m := range live {
		names = append(names, m.Session)
	}
	return strings.Join(names, ", ")
}

func gibString(b uint64) string {
	return fmt.Sprintf("%.1f GiB", float64(b)/gib)
}
