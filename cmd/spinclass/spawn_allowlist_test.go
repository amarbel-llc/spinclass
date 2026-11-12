package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/charlie/perms"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// TEMPORARY (spinclass#384 stopgap): the journal of the spawn allow-list.

const spawnToolName = "mcp__plugin_spinclass_spinclass__spawn-session"

const allowTrustedKey = "[session-entry]\nunattended-permission-mode = \"dontAsk\"\n"

// allowEntryFor lists the fixture's session (worker/kid, principal
// selfPrincipal) with extra entry lines.
func allowEntryFor(extra string) string {
	return "[permissions]\n\n[[permissions.spawn-allow]]\nsession = \"worker/kid\"\n" + extra
}

// allowFixture stands up the caller (worker/kid, mapped to selfPrincipal) with
// the given global sweatfile, as an unattended session when unattended.
func allowFixture(t *testing.T, global string, unattended bool) (repoPath string) {
	t.Helper()
	repoPath, _ = selfCloseFixture(t, false)
	writeGlobalSweatfile(t, os.Getenv("HOME"), global)
	if unattended {
		t.Setenv("CLOWN_UNATTENDED", "1")
	} else {
		t.Setenv("CLOWN_UNATTENDED", "")
	}
	return repoPath
}

// addCallersWorker records a worker the caller spawned, in the given recorded
// state; running-detached is running, inactive with no harness is idle.
func addCallersWorker(t *testing.T, repoPath, branch, state string) {
	t.Helper()
	wt := filepath.Join(repoPath, ".worktrees", branch)
	testgit.MustWorktreeAdd(t, repoPath, wt, branch)
	if err := session.Write(session.State{
		SessionState:       state,
		RepoPath:           repoPath,
		WorktreePath:       wt,
		Branch:             branch,
		SessionKey:         "worker/" + branch,
		SpawnedByPrincipal: selfPrincipal,
		Entrypoint:         []string{"/bin/sh"},
		StartedAt:          time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}

func readAllowJournal(t *testing.T) []spawnAllowRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(os.Getenv("XDG_STATE_HOME"), "spinclass", spawnAllowLogName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []spawnAllowRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r spawnAllowRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("journal line %q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

// The end-to-end rule through the installed seams: a listed unattended caller
// passes under its cap, and counts RUNNING workers only (an idle one never
// reaches it), but is refused at the cap with the reason in the ask text and in
// the journal. Operator ruling 2026-11-12.
func TestSpawnAllowCountsRunningWorkersAndNamesTheCapRefusal(t *testing.T) {
	repoPath := allowFixture(t, allowTrustedKey+allowEntryFor("max-workers = 1\n"), true)

	// An idle worker (inactive, no harness) does not count against a cap of 1.
	addCallersWorker(t, repoPath, "idle1", session.StateInactive)
	if reason, ask := perms.AlwaysAsk(spawnToolName, map[string]any{}, repoPath); ask {
		t.Fatalf("a listed caller with only an idle worker must pass, got ask %q", reason)
	}

	// A running one reaches it.
	addCallersWorker(t, repoPath, "busy1", session.StateRunningDetached)
	reason, ask := perms.AlwaysAsk(spawnToolName, map[string]any{"model": "haiku"}, repoPath)
	if !ask || !strings.HasPrefix(reason, perms.SpawnAskReasonPrefix) ||
		!strings.Contains(reason, "cap is reached: this session has 1 workers running, of 1 allowed") {
		t.Fatalf("reason = %q (ask=%v), want the generic ask naming the cap", reason, ask)
	}

	journalSpawnAllowRefusal(map[string]any{"model": "haiku", "repo": "troupe"}, repoPath)
	recs := readAllowJournal(t)
	if len(recs) != 1 {
		t.Fatalf("journal = %+v, want one refusal record", recs)
	}
	r := recs[0]
	if r.Outcome != spawnAllowRefused || r.Kind != "cap" || r.LiveBefore != 1 || r.MaxWorkers != 1 ||
		r.Principal != selfPrincipal || r.Session != "worker/kid" || r.Model != "haiku" || r.Repo != "troupe" ||
		!strings.Contains(r.Reason, "cap is reached") {
		t.Errorf("record = %+v", r)
	}
}

// Every reason a listed caller is refused reaches the ask text and the
// journal; a caller the list does not name, and a call it passes, get neither.
func TestSpawnAllowRefusalReasons(t *testing.T) {
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	cases := []struct {
		name       string
		global     string
		unattended bool
		principal  string // CLOWN_SESSION_ID override; "" keeps the mapped one
		wantKind   string
		wantIn     string
	}{
		{
			name: "expired", global: allowTrustedKey + allowEntryFor("expires = \""+past+"\"\n"), unattended: true,
			wantKind: "expired", wantIn: "expired at",
		},
		{
			name: "no live mapping", global: allowTrustedKey + allowEntryFor(""), unattended: true, principal: "unmapped-principal",
			wantKind: "no-mapping", wantIn: "no live frontend mapping",
		},
		{
			name: "worker not dontAsk", global: allowEntryFor(""), unattended: true,
			wantKind: "worker-not-dontask", wantIn: "would not start in dontAsk",
		},
		{
			name: "driver attended", global: allowEntryFor(""), unattended: false,
			wantKind: "driver-attended", wantIn: "is attended, so the spawn allow-list is not consulted",
		},
		{name: "unlisted caller", global: allowTrustedKey + "[permissions]\n\n[[permissions.spawn-allow]]\nsession = \"other/branch\"\n", unattended: true},
		{name: "passes: listed, under cap, dontAsk worker", global: allowTrustedKey + allowEntryFor(""), unattended: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repoPath := allowFixture(t, tc.global, tc.unattended)
			if tc.principal != "" {
				t.Setenv("CLOWN_SESSION_ID", tc.principal)
			}
			reason, ask := perms.AlwaysAsk(spawnToolName, map[string]any{}, repoPath)
			journalSpawnAllowRefusal(map[string]any{}, repoPath)
			recs := readAllowJournal(t)
			if tc.wantKind == "" {
				if ask && strings.Contains(reason, "allow-list") {
					t.Errorf("nothing to explain, but the ask says %q", reason)
				}
				if len(recs) != 0 {
					t.Errorf("journal = %+v, want nothing", recs)
				}
				return
			}
			if !ask || !strings.HasPrefix(reason, perms.SpawnAskReasonPrefix) || !strings.Contains(reason, tc.wantIn) {
				t.Errorf("reason = %q (ask=%v), want the generic ask naming %q", reason, ask, tc.wantIn)
			}
			if len(recs) != 1 || recs[0].Outcome != spawnAllowRefused || recs[0].Kind != tc.wantKind || !strings.Contains(recs[0].Reason, tc.wantIn) {
				t.Errorf("journal = %+v, want one %s refusal", recs, tc.wantKind)
			}
		})
	}
}

// A call that asks for a confined worker, or whose arguments do not parse, has
// nothing to explain: the generic ask stands.
func TestSpawnAllowRefusalIgnoresConfinedAndMalformedCalls(t *testing.T) {
	repoPath := allowFixture(t, allowEntryFor(""), true)
	for name, in := range map[string]map[string]any{
		"confined":          {"confined": true},
		"non-bool":          {"confined": "yes"},
		"repo not a string": {"repo": 3.0},
	} {
		if note := spawnAllowRefusalNote(in, repoPath); note != "" {
			t.Errorf("%s: note = %q, want none", name, note)
		}
	}
}

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
