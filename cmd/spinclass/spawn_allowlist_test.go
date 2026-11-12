package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TEMPORARY (spinclass#384 stopgap): the journal of the spawn allow-list.

func TestAppendSpawnAllowRecordWritesOneJSONLine(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	rec := spawnAllowRecord{
		Time: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC), Principal: "p", Session: "circus/coordinator",
		Repo: "troupe", Model: "haiku", Entry: "p", Note: "overnight", LiveBefore: 2, MaxWorkers: 12,
	}
	if err := appendSpawnAllowRecord(rec); err != nil {
		t.Fatal(err)
	}
	if err := appendSpawnAllowRecord(rec); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "spinclass", spawnAllowLogName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"principal":"p"`) || !strings.Contains(lines[0], `"model":"haiku"`) ||
		!strings.Contains(lines[0], `"repo":"troupe"`) || !strings.Contains(lines[0], `"time":"2026-11-12T12:00:00Z"`) {
		t.Errorf("journal = %q, want two JSON lines carrying principal, repo, model and time", string(data))
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v (%v), want 0600", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v (%v), want 0700", info.Mode().Perm(), err)
	}
}

// A journal that cannot be written is an error, which the hook turns into the
// prompt: the relaxation is never used unrecorded.
func TestAppendSpawnAllowRecordFailsWhenTheStateDirIsUnwritable(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "state-is-a-file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", blocker)
	if err := appendSpawnAllowRecord(spawnAllowRecord{Principal: "p"}); err == nil {
		t.Error("want an error when the journal directory cannot be created")
	}
}

// The journal is only for a call the allow-list admitted: an attended driver
// writes nothing, and an unattended driver the list does not admit is an error
// (so the hook asks) with nothing written.
func TestJournalSpawnAllowUse(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	logPath := filepath.Join(state, "spinclass", spawnAllowLogName)

	t.Setenv("CLOWN_UNATTENDED", "")
	if err := journalSpawnAllowUse(map[string]any{"repo": "r"}, t.TempDir()); err != nil {
		t.Errorf("attended driver: err = %v, want nil", err)
	}
	t.Setenv("CLOWN_UNATTENDED", "1")
	t.Setenv("CLOWN_SESSION_ID", "")
	if err := journalSpawnAllowUse(map[string]any{"repo": "r"}, t.TempDir()); err == nil {
		t.Error("an unattended driver the list does not admit must error")
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Error("nothing may be journaled for a call the list did not admit")
	}
}
