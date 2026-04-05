package completions

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

func TestLocalListsRepos(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a repo as a child of tmpDir
	os.MkdirAll(filepath.Join(tmpDir, "myrepo", ".git"), 0o755)

	var buf bytes.Buffer
	Local(tmpDir, &buf)

	output := buf.String()
	if !strings.Contains(output, "myrepo/") {
		t.Errorf("expected repo listing, got %q", output)
	}
	if !strings.Contains(output, "new worktree") {
		t.Errorf("expected 'new worktree' description, got %q", output)
	}
}

func TestLocalListsExistingWorktrees(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "myrepo")
	os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755)
	wtDir := filepath.Join(repoDir, worktree.WorktreesDir, "feature-x")
	os.MkdirAll(wtDir, 0o755)
	os.WriteFile(filepath.Join(wtDir, ".git"), []byte("gitdir: ../../.git/worktrees/feature-x\n"), 0o644)

	var buf bytes.Buffer
	Local(tmpDir, &buf)

	output := buf.String()
	if !strings.Contains(output, "feature-x\t") {
		t.Errorf("expected existing worktree name, got %q", output)
	}
	if !strings.Contains(output, "existing worktree") {
		t.Errorf("expected 'existing worktree' description, got %q", output)
	}
}

func TestLocalHandlesMultipleRepos(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "repo-a", ".git"), 0o755)
	os.MkdirAll(filepath.Join(tmpDir, "repo-b", ".git"), 0o755)

	var buf bytes.Buffer
	Local(tmpDir, &buf)

	output := buf.String()
	if !strings.Contains(output, "repo-a/") {
		t.Errorf("expected repo-a, got %q", output)
	}
	if !strings.Contains(output, "repo-b/") {
		t.Errorf("expected repo-b, got %q", output)
	}
}

func TestLocalOutputIsTabSeparated(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "myrepo", ".git"), 0o755)

	var buf bytes.Buffer
	Local(tmpDir, &buf)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) == 0 {
		t.Fatal("expected output lines")
	}
	if !strings.Contains(lines[0], "\t") {
		t.Errorf("expected tab-separated output, got %q", lines[0])
	}
}

func TestLocalHandlesNoRepos(t *testing.T) {
	tmpDir := t.TempDir()

	var buf bytes.Buffer
	Local(tmpDir, &buf)

	if buf.Len() != 0 {
		t.Errorf("expected empty output, got %q", buf.String())
	}
}

func TestSessionsListsFromStateDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	// Create the worktree path so ResolveState doesn't mark it abandoned
	wtPath := filepath.Join(dir, "myrepo", ".worktrees", "feat-a")
	os.MkdirAll(wtPath, 0o755)

	s := session.State{
		PID:          0,
		SessionState: session.StateInactive,
		RepoPath:     filepath.Join(dir, "myrepo"),
		WorktreePath: wtPath,
		Branch:       "feat-a",
		SessionKey:   "myrepo/feat-a",
		Entrypoint:   []string{"/bin/sh"},
	}
	if err := session.Write(s); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	Sessions(&buf, "")

	output := buf.String()
	if !strings.Contains(output, "feat-a") {
		t.Errorf("expected session branch in output, got %q", output)
	}
	if !strings.Contains(output, "myrepo") {
		t.Errorf("expected repo name in output, got %q", output)
	}
}

func TestSessionsSkipsAbandoned(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	s := session.State{
		PID:          99999,
		SessionState: session.StateActive,
		RepoPath:     "/nonexistent/repo",
		WorktreePath: "/nonexistent/repo/.worktrees/gone",
		Branch:       "gone",
		SessionKey:   "repo/gone",
		Entrypoint:   []string{"/bin/sh"},
	}
	if err := session.Write(s); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	Sessions(&buf, "")

	output := buf.String()
	if strings.Contains(output, "gone") {
		t.Errorf("expected abandoned session to be excluded, got %q", output)
	}
}

func TestSessionsFiltersByRepo(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	repoA := filepath.Join(dir, "repo-a")
	repoB := filepath.Join(dir, "repo-b")

	// Create worktree paths so sessions aren't marked abandoned
	wtA := filepath.Join(repoA, ".worktrees", "feat-a")
	wtB := filepath.Join(repoB, ".worktrees", "feat-b")
	os.MkdirAll(wtA, 0o755)
	os.MkdirAll(wtB, 0o755)

	for _, s := range []session.State{
		{
			PID:          0,
			SessionState: session.StateInactive,
			RepoPath:     repoA,
			WorktreePath: wtA,
			Branch:       "feat-a",
			SessionKey:   "repo-a/feat-a",
			Entrypoint:   []string{"/bin/sh"},
		},
		{
			PID:          0,
			SessionState: session.StateInactive,
			RepoPath:     repoB,
			WorktreePath: wtB,
			Branch:       "feat-b",
			SessionKey:   "repo-b/feat-b",
			Entrypoint:   []string{"/bin/sh"},
		},
	} {
		if err := session.Write(s); err != nil {
			t.Fatal(err)
		}
	}

	// Unfiltered: both sessions
	var all bytes.Buffer
	Sessions(&all, "")
	if !strings.Contains(all.String(), "feat-a") || !strings.Contains(all.String(), "feat-b") {
		t.Errorf("unfiltered should list both sessions, got %q", all.String())
	}

	// Filtered to repo-a: only feat-a
	var filtered bytes.Buffer
	Sessions(&filtered, repoA)
	if !strings.Contains(filtered.String(), "feat-a") {
		t.Errorf("filtered should include feat-a, got %q", filtered.String())
	}
	if strings.Contains(filtered.String(), "feat-b") {
		t.Errorf("filtered should exclude feat-b, got %q", filtered.String())
	}
}

func TestLocalFromInsideRepo(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "myrepo")
	os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755)
	wtDir := filepath.Join(repoDir, worktree.WorktreesDir, "feat")
	os.MkdirAll(wtDir, 0o755)
	os.WriteFile(filepath.Join(wtDir, ".git"), []byte("gitdir: ../../.git/worktrees/feat\n"), 0o644)

	var buf bytes.Buffer
	Local(repoDir, &buf)

	output := buf.String()
	if !strings.Contains(output, "myrepo/") {
		t.Errorf("expected repo listing from inside repo, got %q", output)
	}
	if !strings.Contains(output, "feat") {
		t.Errorf("expected worktree listing from inside repo, got %q", output)
	}
}

func TestLocalShowsSessionInfo(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	repoDir := filepath.Join(dir, "myrepo")
	os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755)
	wtDir := filepath.Join(repoDir, worktree.WorktreesDir, "feat-a")
	os.MkdirAll(wtDir, 0o755)
	os.WriteFile(filepath.Join(wtDir, ".git"), []byte("gitdir: ../../.git/worktrees/feat-a\n"), 0o644)

	s := session.State{
		PID:          0,
		SessionState: session.StateInactive,
		RepoPath:     repoDir,
		WorktreePath: wtDir,
		Branch:       "feat-a",
		SessionKey:   "myrepo/feat-a",
		Description:  "fix login bug",
		Entrypoint:   []string{"/bin/sh"},
	}
	if err := session.Write(s); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	Local(dir, &buf)

	output := buf.String()
	if !strings.Contains(output, "inactive session (myrepo)") {
		t.Errorf("expected session state and repo in output, got %q", output)
	}
	if !strings.Contains(output, "fix login bug") {
		t.Errorf("expected description in output, got %q", output)
	}
}

func TestLocalWithoutSessionShowsExistingWorktree(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	repoDir := filepath.Join(dir, "myrepo")
	os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755)
	wtDir := filepath.Join(repoDir, worktree.WorktreesDir, "feat-b")
	os.MkdirAll(wtDir, 0o755)
	os.WriteFile(filepath.Join(wtDir, ".git"), []byte("gitdir: ../../.git/worktrees/feat-b\n"), 0o644)

	var buf bytes.Buffer
	Local(dir, &buf)

	output := buf.String()
	if !strings.Contains(output, "feat-b\texisting worktree") {
		t.Errorf("expected 'existing worktree' for worktree without session, got %q", output)
	}
}
