package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/spinclass/internal/remote"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/testgit"
)

// TestCompleteWorktreeTargetsInRepoSorted: when cwd is inside a git
// repo, the completer scopes to that repo's sessions and orders them
// active-first, alphabetical-second.
func TestCompleteWorktreeTargetsInRepoSorted(t *testing.T) {
	testgit.RequireGit(t)
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repoA := filepath.Join(root, "alpha")
	repoB := filepath.Join(root, "beta")
	for _, r := range []string{repoA, repoB} {
		testgit.MustInit(t, r)
	}

	// Two sessions in repoA (one inactive, one active), one in repoB.
	live := filepath.Join(repoA, ".worktrees", "active-feature")
	stale := filepath.Join(repoA, ".worktrees", "stale-feature")
	other := filepath.Join(repoB, ".worktrees", "other")
	for _, p := range []string{live, stale, other} {
		testgit.MustWorktreeAdd(t, repoOf(p), p, filepath.Base(p))
	}

	for _, st := range []session.State{
		{
			RepoPath:     repoA,
			WorktreePath: stale,
			Branch:       "stale-feature",
			SessionState: session.StateInactive,
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC(),
		},
		{
			RepoPath:     repoA,
			WorktreePath: live,
			Branch:       "active-feature",
			PID:          1, // pid=1 always alive on Linux
			SessionState: session.StateActive,
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC(),
		},
		{
			RepoPath:     repoB,
			WorktreePath: other,
			Branch:       "other",
			SessionState: session.StateInactive,
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC(),
		},
	} {
		if err := session.Write(st); err != nil {
			t.Fatal(err)
		}
	}

	t.Chdir(repoA)
	got := completeWorktreeTargets()

	if _, ok := got["other"]; ok {
		t.Errorf("completer leaked repoB session 'other' into repoA scope: %v", got)
	}
	if len(got) != 2 {
		t.Errorf("got %d entries, want 2: %v", len(got), got)
	}

	// Labels stay clean inside-repo (no repo-basename suffix).
	for id, label := range got {
		if strings.Contains(label, "(alpha)") {
			t.Errorf("in-repo label %q for %q should not include repo basename", label, id)
		}
	}
}

// TestCompleteWorktreeTargetsOutsideRepoIncludesRepoBasenameInLabel:
// outside any repo, every non-abandoned session is offered, with the
// repo basename appended to the label so duplicates across repos
// disambiguate.
func TestCompleteWorktreeTargetsOutsideRepoIncludesRepoBasenameInLabel(t *testing.T) {
	testgit.RequireGit(t)
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repoA := filepath.Join(root, "alpha")
	repoB := filepath.Join(root, "beta")
	for _, r := range []string{repoA, repoB} {
		testgit.MustInit(t, r)
	}

	wtA := filepath.Join(repoA, ".worktrees", "shared-name")
	wtB := filepath.Join(repoB, ".worktrees", "shared-name")
	testgit.MustWorktreeAdd(t, repoA, wtA, "shared-a")
	testgit.MustWorktreeAdd(t, repoB, wtB, "shared-b")

	for _, st := range []session.State{
		{
			RepoPath:     repoA,
			WorktreePath: wtA,
			Branch:       "shared-a",
			Description:  "alpha description",
			SessionState: session.StateInactive,
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC(),
		},
		{
			RepoPath:     repoB,
			WorktreePath: wtB,
			Branch:       "shared-b",
			SessionState: session.StateInactive,
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC(),
		},
	} {
		if err := session.Write(st); err != nil {
			t.Fatal(err)
		}
	}

	// Pick a directory outside any git repo and set GIT_CEILING_DIRECTORIES
	// so DetectRepo can't accidentally walk up to a host repo.
	outside := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", outside)
	t.Chdir(outside)

	got := completeWorktreeTargets()
	if len(got) != 1 {
		t.Errorf("got %d entries, want 1 (both sessions share the worktree dirname): %v", len(got), got)
	}
	for _, label := range got {
		if !strings.Contains(label, "(alpha)") && !strings.Contains(label, "(beta)") {
			t.Errorf("outside-repo label %q is missing repo basename", label)
		}
	}
}

func repoOf(wtPath string) string {
	return filepath.Dir(filepath.Dir(wtPath))
}

// TestCompleteRemoteTargetsCacheOnly: remote completion entries come from
// the per-remote cache files ONLY. A recording `ssh` stub sits first on
// PATH; if completion ever networks, the stub's record file appears and
// the test fails — completion must stay instant and offline-safe.
func TestCompleteRemoteTargetsCacheOnly(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	stubDir := t.TempDir()
	record := filepath.Join(stubDir, "ssh-invoked")
	script := "#!/bin/sh\necho \"$@\" >> " + record + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(stubDir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub ssh: %v", err)
	}
	t.Setenv("PATH", stubDir+":"+os.Getenv("PATH"))

	if err := remote.WriteCache("devbox", []session.ListRow{
		{ID: "crisp-catalpa", SessionKey: "spinclass/crisp-catalpa", State: "active", Description: "fix login bug", Repo: "spinclass"},
		{ID: "molten-mango", SessionKey: "clown/molten-mango", State: "inactive", Description: "", Repo: "clown"},
	}); err != nil {
		t.Fatalf("seed devbox cache: %v", err)
	}

	got := completeRemoteTargets([]sweatfile.Remote{
		{Name: "devbox", SSH: "devbox.example"},
		{Name: "lab"}, // never listed: no cache file, silently no entries
	})

	want := map[string]string{
		"devbox:crisp-catalpa": "[active] crisp-catalpa — fix login bug (devbox)",
		"devbox:molten-mango":  "[inactive] molten-mango (devbox)",
	}
	if len(got) != len(want) {
		t.Errorf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for key, label := range want {
		if got[key] != label {
			t.Errorf("entry %q: got %q, want %q", key, got[key], label)
		}
	}

	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Errorf("completion invoked ssh (record file exists, stat err = %v) — must be cache-only", err)
	}
}

// TestCompleteRemoteTargetsNoRemotes: no configured remotes (including a
// failed config load upstream, which degrades to nil) yields no entries
// and never errors.
func TestCompleteRemoteTargetsNoRemotes(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if got := completeRemoteTargets(nil); len(got) != 0 {
		t.Errorf("nil remotes: got %v, want no entries", got)
	}
}
