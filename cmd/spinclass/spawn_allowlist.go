package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	"code.linenisgreat.com/spinclass/internal/foxtrot/spawn"
)

// TEMPORARY (spinclass#384 stopgap, docs/plans/2026-11-12-spawn-allowlist-stopgap.md):
// the journal of the spawn allow-list. Delete with the allow-list when ajour
// plus profiles enforce who may spawn unattended workers.

const spawnAllowLogName = "spawn-allowlist.log"

// The journal's outcomes.
const (
	spawnAllowAllowed = "allowed"
	spawnAllowRefused = "refused"
)

// spawnAllowRecord is one use of the allow-list: a spawn-session call the hook
// admitted for an unattended driver only because a trusted layer lists it
// (outcome allowed), or a call the list did not pass although the caller is
// listed or could not be identified for it (outcome refused, with kind and
// reason).
type spawnAllowRecord struct {
	Time      time.Time `json:"time"`
	Outcome   string    `json:"outcome"`
	Principal string    `json:"principal"`
	Session   string    `json:"session,omitempty"`
	Repo      string    `json:"repo,omitempty"`
	Model     string    `json:"model,omitempty"`
	Entry     string    `json:"entry,omitempty"`
	Note      string    `json:"note,omitempty"`
	// LiveBefore is the number of the caller's workers running when the call was
	// judged (the name predates counting running workers only).
	LiveBefore int `json:"live_workers"`
	MaxWorkers int `json:"max_workers"`
	// Kind and Reason are set for a refusal (spawn.Refusal* kinds).
	Kind   string `json:"kind,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// spawnAllowNow is a seam for tests.
var spawnAllowNow = func() time.Time { return time.Now().UTC() }

// journalSpawnAllowUse is the perms.SpawnAllowJournal seam. It records the use
// when the allow-list is what lets this call pass, and fails (the hook then
// asks) if the list no longer admits it or the record cannot be written. A call
// from an attended driver, or a confined spawn, does not use the list.
func journalSpawnAllowUse(toolInput map[string]any, cwd string) error {
	if !spawn.DriverUnattended() || spawnWillConfine(toolInput, cwd) {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	principal := currentPrincipal()
	use, err := spawn.DriverMaySpawn(home, cwd, principal)
	if err != nil {
		return err
	}
	entry := use.Entry.Principal
	if use.Entry.Session != "" {
		entry = "session:" + use.Entry.Session
	}
	return appendSpawnAllowRecord(spawnAllowRecord{
		Time:       spawnAllowNow(),
		Outcome:    spawnAllowAllowed,
		Principal:  principal,
		Session:    use.SessionKey,
		Repo:       toolInputString(toolInput, "repo"),
		Model:      toolInputString(toolInput, "model"),
		Entry:      entry,
		Note:       use.Entry.Note,
		LiveBefore: use.Live,
		MaxWorkers: use.Entry.MaxWorkers,
	})
}

// journalSpawnAllowRefusal is the perms.SpawnAllowRefusalJournal seam: when the
// allow-list did not pass a call although the caller is listed (or could not be
// identified for it), it records why. Best-effort: the ask happens regardless,
// so a failed write only costs the record.
func journalSpawnAllowRefusal(toolInput map[string]any, cwd string) {
	r, ok := diagnoseSpawnAllow(toolInput, cwd)
	if !ok {
		return
	}
	_ = appendSpawnAllowRecord(spawnAllowRecord{
		Time:       spawnAllowNow(),
		Outcome:    spawnAllowRefused,
		Principal:  currentPrincipal(),
		Session:    r.SessionKey,
		Repo:       toolInputString(toolInput, "repo"),
		Model:      toolInputString(toolInput, "model"),
		LiveBefore: r.Running,
		MaxWorkers: r.Cap,
		Kind:       r.Kind,
		Reason:     r.Reason,
	})
}

func toolInputString(toolInput map[string]any, key string) string {
	s, _ := toolInput[key].(string)
	return s
}

// appendSpawnAllowRecord appends one JSON line to
// $XDG_STATE_HOME/spinclass/spawn-allowlist.log (0600 in a 0700 directory, like
// spawn-unconfined.log). Any failure is returned.
func appendSpawnAllowRecord(rec spawnAllowRecord) error {
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		return fmt.Errorf("spawn allow-list journal: %w", err)
	}
	path := filepath.Join(stateDir, "spinclass", spawnAllowLogName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("spawn allow-list journal: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("spawn allow-list journal: %w", err)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("spawn allow-list journal: %w", err)
	}
	return f.Close()
}
