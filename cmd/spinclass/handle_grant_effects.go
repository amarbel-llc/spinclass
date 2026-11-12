package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/clown"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// What a grant does besides writing the child's record (spinclass#480,
// docs/plans/2026-11-12-480-immediate-handle-grants.md, operator ruling
// 2026-11-12): a grant is effective when recorded, so the recipient must be a
// live principal, every grant and release is journaled, and the recipient and
// the child's other holders are woken.

// handleRecipientLive reports why principal is not a live recipient, or nil.
// It replaces FDR 0032 D12's inert pending state for a misaddressed grant: a
// grant to an unknown or dead key is refused rather than parked. A seam for
// tests.
var handleRecipientLive = func(principal string) error {
	for _, p := range clown.ReadPresence(time.Now()) {
		if p.SessionKey == principal {
			return nil
		}
	}
	if _, err := confinement.SessionKeyOfPrincipal(principal); err == nil {
		return nil
	}
	return fmt.Errorf("%s is not a live principal on this host (no live clown presence record and no live session maps to it); "+
		"a grant is effective at once (spinclass#480), so it is refused rather than recorded for a key that may be wrong. "+
		"Check the key against `whoami` or `chat_list` in the recipient's session", principal)
}

const handleJournalName = "journal.ndjson"

// handleJournalRecord is one line of the handle journal, shaped like an FDR
// 0032 D8 record (actor, act, target, assurance) so D14 can later index or sign
// it.
type handleJournalRecord struct {
	Time      time.Time `json:"ts"`
	Act       string    `json:"act"` // grant, release, grant-not-written
	Child     string    `json:"child"`
	Principal string    `json:"principal"` // the principal granted or released
	Rights    string    `json:"rights,omitempty"`
	By        string    `json:"by"`        // the acting principal, or "operator"
	Assurance string    `json:"assurance"` // session or tty; later touch (D10/D11)
	Note      string    `json:"note,omitempty"`
}

const (
	handleAssuranceSession = "session"
	handleAssuranceTTY     = "tty"
)

// appendHandleJournal appends rec to
// $XDG_STATE_HOME/spinclass/handles/journal.ndjson (0600 in a 0700 directory).
// Any failure is returned: a grant refuses on it (fail-closed), a release only
// warns (fail-open, so a full disk cannot keep authority alive).
func appendHandleJournal(rec handleJournalRecord) error {
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		return fmt.Errorf("handle journal: %w", err)
	}
	path := filepath.Join(stateDir, "spinclass", "handles", handleJournalName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("handle journal: %w", err)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("handle journal: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("handle journal: %w", err)
	}
	return f.Close()
}

// notifyHandleGrant wakes the recipient of a grant and the child's other
// holders. Best-effort: a missed wake does not undo the grant, and the journal
// and list-handles are the durable record. holders is the child's holder list
// after the grant.
func notifyHandleGrant(child, to, rights, by string, at time.Time, holders []string) {
	granter := by
	if granter == "" {
		granter = "unknown"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = notifyPrincipalFn(ctx, to, by, fmt.Sprintf(
		"handle granted: you now hold %s with %s (granted by %s)\n\n"+
			"granted_by: %s\ngranted_at: %s\nchild: %s\nrights: %s\n"+
			"You receive this session's exit wakes from now on. To refuse it: release-session-handle child=%s",
		child, rights, granter, granter, at.UTC().Format(time.RFC3339), child, rights, child))
	for _, h := range holders {
		if h == to || h == by {
			continue
		}
		_ = notifyPrincipalFn(ctx, h, by, fmt.Sprintf(
			"handle granted on %s: %s now holds it with %s (granted by %s)", child, to, rights, granter))
	}
}

// grantedHandleText is the result line for a grant that was recorded.
func grantedHandleText(child *session.State, to string) string {
	return fmt.Sprintf("granted handle on %s to %s (effective now; rights: %s). %s was woken with the grant, as were the other holders (%s).",
		child.Key(), to, child.HandleRights[to], to, strings.Join(otherThan(child.Holders, to), ", "))
}

func otherThan(list []string, drop string) []string {
	var out []string
	for _, s := range list {
		if s != drop {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return []string{"none"}
	}
	return out
}
