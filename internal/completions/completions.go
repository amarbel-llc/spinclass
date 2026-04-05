package completions

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/amarbel-llc/spinclass/internal/pr"
	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/worktree"
)

// PRs outputs completion entries for open pull requests using the gh CLI.
// Each line is tab-separated: <number>\t#<number>: <title>\n
// When repoPath is empty, no output is produced.
func PRs(w io.Writer, repoPath string) {
	if repoPath == "" {
		return
	}
	slug := pr.RemoteRepo(repoPath)
	if slug == "" {
		return
	}

	out, err := exec.Command(
		"gh", "pr", "list",
		"--json", "number,title",
		"--limit", "30",
		"--repo", slug,
	).Output()
	if err != nil {
		return
	}

	var prs []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	}
	if err := json.Unmarshal(out, &prs); err != nil {
		return
	}

	for _, p := range prs {
		num := strconv.Itoa(p.Number)
		fmt.Fprintf(w, "%s\t#%s: %s\n", num, num, p.Title)
	}
}

// Sessions outputs completion entries from the session state directory.
// Each line is tab-separated: <session-key>\t<state>\n
// When repoPath is non-empty, only sessions belonging to that repo are listed.
func Sessions(w io.Writer, repoPath string) {
	states, err := session.ListAll()
	if err != nil {
		return
	}
	for _, s := range states {
		resolved := s.ResolveState()
		if resolved == session.StateAbandoned {
			continue
		}
		if repoPath != "" && s.RepoPath != repoPath {
			continue
		}
		wtID := filepath.Base(s.WorktreePath)
		label := fmt.Sprintf("%s session (%s)", resolved, filepath.Base(s.RepoPath))
		if s.Description != "" {
			label += " — " + s.Description
		}
		fmt.Fprintf(w, "%s\t%s\n", wtID, label)
	}
}

// Local outputs completion entries by scanning worktree directories.
// Falls back to directory scanning when no session state is available.
// When a session exists for a worktree, its state, repo, and description
// are shown instead of the generic "existing worktree" label.
func Local(startDir string, w io.Writer) {
	sessions := sessionIndex()

	// If startDir is a repo, list its worktrees
	gitDir := filepath.Join(startDir, ".git")
	if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
		repoName := filepath.Base(startDir)
		fmt.Fprintf(w, "%s/\tnew worktree\n", repoName)

		for _, wtPath := range worktree.ListWorktrees(startDir) {
			branch := filepath.Base(wtPath)
			fmt.Fprintf(w, "%s\t%s\n", branch, worktreeLabel(wtPath, sessions))
		}
		return
	}

	// Otherwise scan children for repos
	entries, err := os.ReadDir(startDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		child := filepath.Join(startDir, entry.Name())
		childGitDir := filepath.Join(child, ".git")
		if info, err := os.Stat(childGitDir); err != nil || !info.IsDir() {
			continue
		}

		repoName := entry.Name()
		fmt.Fprintf(w, "%s/\tnew worktree\n", repoName)

		for _, wtPath := range worktree.ListWorktrees(child) {
			branch := filepath.Base(wtPath)
			fmt.Fprintf(w, "%s\t%s\n", branch, worktreeLabel(wtPath, sessions))
		}
	}
}

// sessionIndex loads all sessions and indexes them by worktree path.
func sessionIndex() map[string]session.State {
	states, err := session.ListAll()
	if err != nil {
		return nil
	}
	idx := make(map[string]session.State, len(states))
	for _, s := range states {
		idx[s.WorktreePath] = s
	}
	return idx
}

// worktreeLabel returns a completion description for a worktree path.
// When a session exists, it includes the state, repo name, and description.
func worktreeLabel(wtPath string, sessions map[string]session.State) string {
	s, ok := sessions[wtPath]
	if !ok {
		return "existing worktree"
	}
	resolved := s.ResolveState()
	if resolved == session.StateAbandoned {
		return "existing worktree"
	}
	label := fmt.Sprintf("%s session (%s)", resolved, filepath.Base(s.RepoPath))
	if s.Description != "" {
		label += " — " + s.Description
	}
	return label
}
