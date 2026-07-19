package main

import (
	"context"
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// TestRunUpdateDescriptionHealsUntrackedWorktree pins #161 on the CLI side:
// a worktree with no spinclass state (harness run directly, never via
// sc start/resume) auto-heals into a tracked session carrying the new
// description, rather than failing on the missing index.
func TestRunUpdateDescriptionHealsUntrackedWorktree(t *testing.T) {
	testgit.RequireGit(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPINCLASS_SESSION_ID", "") // exercise the <repo>/<branch> fallback key
	repoDir := filepath.Join(t.TempDir(), "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.MustInit(t, repoDir)
	wtPath := filepath.Join(repoDir, ".worktrees", "feat")
	if out, err := osexec.Command("git", "-C", repoDir, "worktree", "add", "-b", "feat", wtPath).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	t.Chdir(wtPath)

	// No state was ever written for this worktree.
	if _, err := session.Read(repoDir, "feat"); err == nil {
		t.Fatal("precondition: expected no session state for the fresh worktree")
	}

	if err := runUpdateDescription("triage the flaky test", ""); err != nil {
		t.Fatalf("runUpdateDescription healed path: %v", err)
	}

	got, err := session.Read(repoDir, "feat")
	if err != nil {
		t.Fatalf("session.Read after heal: %v", err)
	}
	if got.Description != "triage the flaky test" {
		t.Errorf("description = %q, want healed value", got.Description)
	}
	if got.SessionKey != "myrepo/feat" {
		t.Errorf("SessionKey = %q, want myrepo/feat (the <repo>/<branch> fallback)", got.SessionKey)
	}
}

// remoteCannedJSON is the wire payload the healthy stub host serves —
// the session.ListRow array shape locked by internal/session's tests.
const remoteCannedJSON = `[{"id":"crisp-catalpa","session_key":"spinclass/crisp-catalpa","state":"active","description":"fix login bug","repo":"spinclass"},{"id":"molten-mango","session_key":"clown/molten-mango","state":"inactive","description":"","repo":"clown"}]`

// stubSSHPerDest writes an executable `ssh` script into a temp dir
// prepended to PATH (mirroring internal/remote/query_test.go's stubSSH).
// The script serves canned ListRow JSON when its destination ($1) is
// healthyDest and fails with stderr noise otherwise, so one stub covers
// both the healthy and the unreachable host of a fan-out test.
func stubSSHPerDest(t *testing.T, healthyDest string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "ssh")
	content := "#!/bin/sh\n" +
		"if [ \"$1\" = \"" + healthyDest + "\" ]; then\n" +
		"  echo '" + remoteCannedJSON + "'\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo 'no route to host' >&2\n" +
		"exit 255\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write stub ssh: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// twoRemotes is the standard fan-out fixture: devbox answers, lab does not.
func twoRemotes() []sweatfile.Remote {
	return []sweatfile.Remote{
		{Name: "devbox", SSH: "devbox.example"},
		{Name: "lab"},
	}
}

// TestRunUpdateDescriptionImplicit verifies the CLI auto-detect path writes a
// main checkout's live implicit session via WriteImplicit (state-<rand>.json),
// not session.Write — which would litter a wrongly-keyed state.json next to
// the real file and leave the live session untouched (#139).
func TestRunUpdateDescriptionImplicit(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	// A bare tempdir has no .git file, so worktree.IsWorktree is false and the
	// implicit fallback fires before any git call.
	repo := t.TempDir()
	st := session.State{
		Kind:         session.KindImplicit,
		PID:          os.Getpid(),
		SessionState: session.StateActive,
		RepoPath:     repo,
		WorktreePath: repo,
		Branch:       "master",
		SessionKey:   "myrepo/master-cafe1234",
	}
	if err := session.WriteImplicit(st, "cafe1234"); err != nil {
		t.Fatalf("WriteImplicit: %v", err)
	}
	t.Chdir(repo)

	if err := runUpdateDescription("triage the flaky test", ""); err != nil {
		t.Fatalf("runUpdateDescription: %v", err)
	}

	got, _, ferr := session.FindImplicitAtCwd(repo)
	if ferr != nil || got == nil {
		t.Fatalf("FindImplicitAtCwd after update: state=%v err=%v", got, ferr)
	}
	if got.Description != "triage the flaky test" {
		t.Errorf("persisted description = %q, want %q", got.Description, "triage the flaky test")
	}
	if _, err := os.Stat(filepath.Join(repo, ".spinclass", "state.json")); !os.IsNotExist(err) {
		t.Errorf("stray wrongly-keyed state.json exists (stat err=%v); implicit sessions must only have state-<rand>.json", err)
	}
}

// TestRunUpdateDescriptionImplicitByIDErrors verifies the --id path refuses an
// implicit session instead of corrupting its storage: the randID is only
// derivable at the checkout (from the state filename), so a target-resolved
// implicit state cannot be written safely (#139).
func TestRunUpdateDescriptionImplicitByIDErrors(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repo := t.TempDir()
	st := session.State{
		Kind:         session.KindImplicit,
		PID:          os.Getpid(),
		SessionState: session.StateActive,
		RepoPath:     repo,
		WorktreePath: repo,
		Branch:       "master",
		SessionKey:   "myrepo/master-cafe1234",
	}
	if err := session.WriteImplicit(st, "cafe1234"); err != nil {
		t.Fatalf("WriteImplicit: %v", err)
	}
	// cwd deliberately elsewhere: target the session by key, as a user
	// updating a remote-to-them session would.
	t.Chdir(t.TempDir())

	err := runUpdateDescription("new desc", "myrepo/master-cafe1234")
	if err == nil {
		t.Fatal("expected error for --id targeting an implicit session, got nil")
	}
	if !strings.Contains(err.Error(), "implicit") {
		t.Errorf("error %q does not mention implicit", err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, ".spinclass", "state.json")); !os.IsNotExist(statErr) {
		t.Errorf("stray wrongly-keyed state.json exists (stat err=%v)", statErr)
	}
	got, _, ferr := session.FindImplicitAtCwd(repo)
	if ferr != nil || got == nil {
		t.Fatalf("FindImplicitAtCwd: state=%v err=%v", got, ferr)
	}
	if got.Description != "" {
		t.Errorf("description = %q, want unchanged empty", got.Description)
	}
}

func TestQueryRemotesPerHostIsolation(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	stubSSHPerDest(t, "devbox.example")

	rows, diags := queryRemotes(context.Background(), twoRemotes(), nil)

	if len(rows) != 2 {
		t.Fatalf("rows: got %d (%+v), want 2 from the healthy host", len(rows), rows)
	}
	for _, r := range rows {
		if r.Remote != "devbox" {
			t.Errorf("row %s: Remote = %q, want %q", r.ID, r.Remote, "devbox")
		}
	}
	if rows[0].ID != "crisp-catalpa" || rows[1].ID != "molten-mango" {
		t.Errorf("row ids: got %q, %q", rows[0].ID, rows[1].ID)
	}

	if len(diags) != 1 {
		t.Fatalf("diagnostics: got %d (%q), want 1 for the unreachable host", len(diags), diags)
	}
	if !strings.HasPrefix(diags[0], "lab: unreachable (") {
		t.Errorf("diagnostic: got %q, want 'lab: unreachable (<short err>)'", diags[0])
	}
	if strings.Contains(diags[0], "\n") {
		t.Errorf("diagnostic must be one line, got %q", diags[0])
	}

	// The healthy host's rows were cached for completion; the failed
	// host wrote nothing.
	cached, err := os.ReadFile(filepath.Join(stateHome, "spinclass", "remotes", "devbox.json"))
	if err != nil {
		t.Fatalf("devbox cache: %v", err)
	}
	var cachedRows []session.ListRow
	if err := json.Unmarshal(cached, &cachedRows); err != nil {
		t.Fatalf("devbox cache parse: %v", err)
	}
	if len(cachedRows) != 2 || cachedRows[0].ID != "crisp-catalpa" {
		t.Errorf("devbox cache rows: got %+v", cachedRows)
	}
	if _, err := os.Stat(filepath.Join(stateHome, "spinclass", "remotes", "lab.json")); !os.IsNotExist(err) {
		t.Errorf("lab cache: want no file for unreachable host, stat err = %v", err)
	}
}

func TestQueryRemotesEmptyConfig(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	rows, diags := queryRemotes(context.Background(), nil, nil)
	if len(rows) != 0 || len(diags) != 0 {
		t.Errorf("no remotes configured: got rows %+v diags %q, want none", rows, diags)
	}
}

func TestRunListResultTextIncludesRemoteRows(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // empty local index
	stubSSHPerDest(t, "devbox.example")

	res, err := runListResult(context.Background(), false, "tap", nil, twoRemotes())
	if err != nil {
		t.Fatalf("runListResult: %v", err)
	}
	if res.IsErr {
		t.Fatalf("runListResult: unexpected error result: %s", res.Text)
	}
	if !strings.Contains(res.Text, "devbox:crisp-catalpa\tactive") {
		t.Errorf("text output missing prefixed remote row:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "fix login bug") {
		t.Errorf("text output missing remote row description:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "lab: unreachable (") {
		t.Errorf("text output missing unreachable diagnostic:\n%s", res.Text)
	}
}

// TestRunListResultTextSpawnedByAnnotation verifies the `sc list` text
// rows carry the spawn lineage hint (FDR 0006): a session whose state
// records SpawnedBy gets a trailing `spawned-by:<key>` annotation, and a
// session without it stays byte-identical to the legacy row shape.
func TestRunListResultTextSpawnedByAnnotation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	base := t.TempDir()
	repo := filepath.Join(base, "spinclass")
	spawnedWT := filepath.Join(repo, ".worktrees", "spawned-walnut")
	plainWT := filepath.Join(repo, ".worktrees", "plain-pine")
	for _, dir := range []string{spawnedWT, plainWT} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	states := []session.State{
		{
			SessionState: session.StateInactive,
			RepoPath:     repo,
			WorktreePath: spawnedWT,
			Branch:       "spawned-walnut",
			SessionKey:   "spinclass/spawned-walnut",
			SpawnedBy:    "spinclass/bright-cedar",
		},
		{
			SessionState: session.StateInactive,
			RepoPath:     repo,
			WorktreePath: plainWT,
			Branch:       "plain-pine",
			SessionKey:   "spinclass/plain-pine",
		},
	}
	for _, s := range states {
		if err := session.Write(s); err != nil {
			t.Fatalf("session.Write(%s): %v", s.Branch, err)
		}
	}

	res, err := runListResult(context.Background(), false, "tap", nil, nil)
	if err != nil {
		t.Fatalf("runListResult: %v", err)
	}
	if res.IsErr {
		t.Fatalf("runListResult: unexpected error result: %s", res.Text)
	}

	var spawnedLine, plainLine string
	for _, line := range strings.Split(res.Text, "\n") {
		if strings.HasPrefix(line, "spinclass/spawned-walnut\t") {
			spawnedLine = line
		}
		if strings.HasPrefix(line, "spinclass/plain-pine\t") {
			plainLine = line
		}
	}
	if spawnedLine == "" || plainLine == "" {
		t.Fatalf("rows missing from text output:\n%s", res.Text)
	}
	if !strings.HasSuffix(spawnedLine, "\tspawned-by:spinclass/bright-cedar") {
		t.Errorf("spawned row missing annotation suffix: %q", spawnedLine)
	}
	if strings.Contains(plainLine, "spawned-by:") {
		t.Errorf("plain row unexpectedly annotated: %q", plainLine)
	}
}

func TestRunListResultJSONTagsRemoteRows(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // empty local index
	stubSSHPerDest(t, "devbox.example")

	res, err := runListResult(context.Background(), false, "json", nil, twoRemotes())
	if err != nil {
		t.Fatalf("runListResult: %v", err)
	}
	if res.IsErr {
		t.Fatalf("runListResult: unexpected error result: %s", res.Text)
	}

	// Output must stay machine-readable: a bare ListRow array, no
	// diagnostic lines mixed in.
	var rows []session.ListRow
	if err := json.Unmarshal([]byte(res.Text), &rows); err != nil {
		t.Fatalf("json output not a clean ListRow array: %v\n%s", err, res.Text)
	}
	if len(rows) != 2 {
		t.Fatalf("json rows: got %d (%+v), want 2 remote rows", len(rows), rows)
	}
	for _, r := range rows {
		if r.Remote != "devbox" {
			t.Errorf("json row %s: remote = %q, want %q", r.ID, r.Remote, "devbox")
		}
	}

	// The wire field is present for remote rows...
	if !strings.Contains(res.Text, `"remote":"devbox"`) {
		t.Errorf("json output missing remote field:\n%s", res.Text)
	}
	// ...and absent text-wise for the unreachable host (no row, no diag).
	if strings.Contains(res.Text, "lab") {
		t.Errorf("json output should not mention the unreachable host:\n%s", res.Text)
	}
}
