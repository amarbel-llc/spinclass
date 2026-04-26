package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/spinclass/internal/session"
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
