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

// spawnAllowRecord is one use of the allow-list: a spawn-session call the hook
// admitted for an unattended driver only because a trusted layer lists it.
type spawnAllowRecord struct {
	Time       time.Time `json:"time"`
	Principal  string    `json:"principal"`
	Session    string    `json:"session"`
	Repo       string    `json:"repo,omitempty"`
	Model      string    `json:"model,omitempty"`
	Entry      string    `json:"entry"`
	Note       string    `json:"note,omitempty"`
	LiveBefore int       `json:"live_workers"`
	MaxWorkers int       `json:"max_workers"`
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
	str := func(k string) string {
		s, _ := toolInput[k].(string)
		return s
	}
	entry := use.Entry.Principal
	if use.Entry.Session != "" {
		entry = "session:" + use.Entry.Session
	}
	return appendSpawnAllowRecord(spawnAllowRecord{
		Time:       spawnAllowNow(),
		Principal:  principal,
		Session:    use.SessionKey,
		Repo:       str("repo"),
		Model:      str("model"),
		Entry:      entry,
		Note:       use.Entry.Note,
		LiveBefore: use.Live,
		MaxWorkers: use.Entry.MaxWorkers,
	})
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
