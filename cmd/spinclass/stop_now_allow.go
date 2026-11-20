package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	"code.linenisgreat.com/spinclass/internal/charlie/perms"
)

// The operator's blessing for a stop now (docs/plans/2026-11-20-stop-start-rights.md):
// a session listed in a trusted [[permissions.stop-now-allow]] entry stops now
// without the human prompt. It is checked at the always-ask floor and again,
// with a journal line, at the hook's allow point, as the spawn allow-list is.

const stopNowAllowLogName = "stop-now-allowlist.log"

func init() {
	perms.StopNowAllowed = func(_ map[string]any, cwd string) bool {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		_, _, err = stopNowAllowedFor(home, cwd, currentPrincipal())
		return err == nil
	}
	perms.StopNowJournal = journalStopNowUse
}

// stopNowAllowedFor returns the trusted entry that lists the caller and the
// caller's session key, or why none does. Every doubt is an error, which keeps
// the prompt: the caller must resolve to a session through the frontend
// mapping (which the agent cannot write), and the entry must validate and be
// unexpired.
func stopNowAllowedFor(home, cwd, principal string) (sweatfile.SpawnAllowEntry, string, error) {
	if principal == "" {
		return sweatfile.SpawnAllowEntry{}, "", errors.New("the stop-now allow-list could not identify this session: it has no principal")
	}
	repo, err := git.CommonDir(cwd)
	if err != nil || repo == "" {
		return sweatfile.SpawnAllowEntry{}, "", fmt.Errorf("the stop-now allow-list is read from the repo's trusted sweatfile layers, and %s is not in a repo", cwd)
	}
	h, err := sweatfileio.LoadHierarchy(home, repo)
	if err != nil {
		return sweatfile.SpawnAllowEntry{}, "", fmt.Errorf("the stop-now allow-list could not be read: %w", err)
	}
	key, err := confinement.SessionKeyOfPrincipal(principal)
	if err != nil {
		return sweatfile.SpawnAllowEntry{}, "", fmt.Errorf("the stop-now allow-list could not identify this session: its principal has no live frontend mapping (%v)", err)
	}
	entries, _ := sweatfileio.TrustedStopNowAllow(h, home, repo)
	now := time.Now().UTC()
	for _, e := range entries {
		if e.Problem != "" || e.Expired(now) {
			continue
		}
		if (e.Principal != "" && e.Principal == principal) || (e.Session != "" && e.Session == key) {
			return e, key, nil
		}
	}
	return sweatfile.SpawnAllowEntry{}, key, errors.New("no valid, unexpired stop-now-allow entry lists this session")
}

type stopNowAllowRecord struct {
	Time      time.Time `json:"time"`
	Outcome   string    `json:"outcome"`
	Principal string    `json:"principal"`
	Session   string    `json:"session,omitempty"`
	// Target is the child a stop-child-session call names; empty for a self-stop.
	Target string `json:"target,omitempty"`
	Entry  string `json:"entry"`
	Note   string `json:"note,omitempty"`
}

// journalStopNowUse is the perms.StopNowJournal seam: it re-checks the list and
// appends one JSON line to $XDG_STATE_HOME/spinclass/stop-now-allowlist.log
// (0600 in a 0700 directory). Any failure is returned, and the hook then asks.
func journalStopNowUse(toolInput map[string]any, cwd string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	principal := currentPrincipal()
	e, key, err := stopNowAllowedFor(home, cwd, principal)
	if err != nil {
		return err
	}
	entry := e.Principal
	if e.Session != "" {
		entry = "session:" + e.Session
	}
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		return fmt.Errorf("stop-now allow-list journal: %w", err)
	}
	path := filepath.Join(stateDir, "spinclass", stopNowAllowLogName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("stop-now allow-list journal: %w", err)
	}
	line, err := json.Marshal(stopNowAllowRecord{
		Time: time.Now().UTC(), Outcome: "allowed", Principal: principal, Session: key,
		Target: toolInputString(toolInput, "child"), Entry: entry, Note: e.Note,
	})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("stop-now allow-list journal: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("stop-now allow-list journal: %w", err)
	}
	return f.Close()
}
