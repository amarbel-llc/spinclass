package hostqueue

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Queue management (spinclass#483, docs/plans/2026-11-12-483-host-queue-management.md).
// The host queue is spinclass's interim scheduler (FDR 0038; FDR 0032 D19 gives
// scheduling to ringmaster eventually). Every management verb goes through
// Authorize, so the future `spinclass:queue` object right is one more case
// there, not a rewrite. The ledger files are NOT a security boundary against
// same-uid code; the tool is the check.

// Verb is one queue management act. The table below is each verb's declaration:
// its name and what it is aimed at, for the operator-act profiles that will
// admit or refuse it (ajour, plan G9).
type Verb string

const (
	VerbHold          Verb = "hold"           // the whole queue
	VerbRelease       Verb = "release"        // the whole queue
	VerbPriority      Verb = "priority"       // one waiter
	VerbHoldWaiter    Verb = "hold-waiter"    // one waiter
	VerbReleaseWaiter Verb = "release-waiter" // one waiter
	VerbCancel        Verb = "cancel"         // one waiter
)

// VerbSpec declares a verb.
type VerbSpec struct {
	Verb   Verb
	Target string // "queue" or "waiter"
	// FailsOpen is true when a journal that cannot be written does not stop the
	// act (the whole-queue brake, plan G8); every other verb fails closed.
	FailsOpen bool
}

// Verbs lists every management verb with its declaration.
var Verbs = []VerbSpec{
	{Verb: VerbHold, Target: "queue", FailsOpen: true},
	{Verb: VerbRelease, Target: "queue", FailsOpen: true},
	{Verb: VerbPriority, Target: "waiter"},
	{Verb: VerbHoldWaiter, Target: "waiter"},
	{Verb: VerbReleaseWaiter, Target: "waiter"},
	{Verb: VerbCancel, Target: "waiter"},
}

// ActorTTY is the actor of an act made by the operator at a terminal. The
// assurance is as strong as isatty (the #478 stopgap), not an authenticated
// act; the eventual authority is the card path (FDR 0032 D10/D11).
const ActorTTY = "tty"

// Actor is who asks for a verb. Principal is empty for the operator at a
// terminal.
type Actor struct {
	Kind      string // ActorTTY today
	Principal string // a session's principal, once the queue right exists
}

// Authorize is the one check every verb passes. v1: only the operator at a
// terminal. Adding the `spinclass:queue` right later is a case here.
func Authorize(v Verb, a Actor) error {
	if a.Kind == ActorTTY {
		return nil
	}
	return fmt.Errorf("queue %s: only the operator at a terminal may manage the host queue (no session holds a queue right yet)", v)
}

// JournalRecord is one line of the journal, shaped like an FDR 0032 D8 record.
type JournalRecord struct {
	Time      time.Time `json:"time"`
	Actor     string    `json:"actor"` // ActorTTY, or a principal
	Act       string    `json:"act"`
	Target    string    `json:"target,omitempty"` // a session, when the act has one
	Seq       uint64    `json:"seq,omitempty"`
	Old       string    `json:"old,omitempty"`
	New       string    `json:"new,omitempty"`
	Assurance string    `json:"assurance"`
}

const journalName = "journal.ndjson"

// JournalPath is the journal, beside the hold file under
// $XDG_STATE_HOME/spinclass/host-queue.
func (l Ledger) JournalPath() string {
	return filepath.Join(filepath.Dir(l.HoldPath), journalName)
}

// AppendJournal appends one record. Mode 0600 in a 0700 directory.
func (l Ledger) AppendJournal(r JournalRecord) error {
	if l.HoldPath == "" {
		return errors.New("the ledger has no state directory for a journal")
	}
	path := l.JournalPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(data, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

func assuranceOf(a Actor) (actor, assurance string) {
	if a.Kind == ActorTTY {
		return ActorTTY, "tty"
	}
	return a.Principal, "principal"
}

// Outcome is what a management act did.
type Outcome struct {
	Changed bool
	// Warning is set when the act took effect but its journal line could not be
	// written (a fail-open verb): the caller prints it loudly.
	Warning string
}

// PlaceHold places the operator's hold with the reason, as actor. It fails OPEN on a
// journal that cannot be written: the hold is the brake.
func (l Ledger) PlaceHold(a Actor, reason string, now time.Time) (Outcome, error) {
	if err := Authorize(VerbHold, a); err != nil {
		return Outcome{}, err
	}
	oldHeld, oldReason := l.Hold()
	if err := l.SetHold(reason); err != nil {
		return Outcome{}, err
	}
	rec := JournalRecord{Time: now.UTC(), Act: string(VerbHold), New: reason}
	if oldHeld {
		rec.Old = oldReason
	}
	rec.Actor, rec.Assurance = assuranceOf(a)
	return Outcome{Changed: true, Warning: journalWarning(l.AppendJournal(rec))}, nil
}

// LiftHold clears the hold, as actor; like PlaceHold it fails open on the
// journal. Clearing an absent hold changes nothing and writes nothing.
func (l Ledger) LiftHold(a Actor, now time.Time) (Outcome, error) {
	if err := Authorize(VerbRelease, a); err != nil {
		return Outcome{}, err
	}
	held, reason := l.Hold()
	if !held {
		return Outcome{}, nil
	}
	if err := l.ClearHold(); err != nil {
		return Outcome{}, err
	}
	rec := JournalRecord{Time: now.UTC(), Act: string(VerbRelease), Old: reason}
	rec.Actor, rec.Assurance = assuranceOf(a)
	return Outcome{Changed: true, Warning: journalWarning(l.AppendJournal(rec))}, nil
}

func journalWarning(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("WARNING: the act took effect but its journal line could not be written (%v)", err)
}
