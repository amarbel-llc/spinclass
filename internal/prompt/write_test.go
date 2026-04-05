package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteBaseContext(t *testing.T) {
	dir := t.TempDir()
	scDir := filepath.Join(dir, ".spinclass")

	opts := WriteOptions{
		WorktreePath: dir,
		RepoPath:     "/home/user/repos/bob",
		RemoteURL:    "git@github.com:amarbel-llc/bob.git",
		Branch:       "feature-x",
		SessionID:    "bob/feature-x",
	}

	if err := WriteSessionContext(opts); err != nil {
		t.Fatalf("WriteSessionContext() error: %v", err)
	}

	basePath := filepath.Join(scDir, "system_prompt_append.d", "0-base.md")
	data, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("reading base file: %v", err)
	}

	content := string(data)
	for _, want := range []string{"bob", "feature-x", "spinclass worktree session"} {
		if !strings.Contains(content, want) {
			t.Errorf("base file missing %q", want)
		}
	}
}

func TestWriteIssueContext(t *testing.T) {
	dir := t.TempDir()
	scDir := filepath.Join(dir, ".spinclass")

	opts := WriteOptions{
		WorktreePath: dir,
		RepoPath:     "/home/user/repos/bob",
		RemoteURL:    "git@github.com:amarbel-llc/bob.git",
		Branch:       "feature-x",
		SessionID:    "bob/feature-x",
		Issue: &IssueData{
			Number: 42,
			Title:  "Fix bug",
			State:  "OPEN",
			URL:    "https://github.com/amarbel-llc/bob/issues/42",
			Body:   "Bug description.",
		},
	}

	if err := WriteSessionContext(opts); err != nil {
		t.Fatalf("WriteSessionContext() error: %v", err)
	}

	issuePath := filepath.Join(scDir, "system_prompt_append.d", "1-issue-42.md")
	data, err := os.ReadFile(issuePath)
	if err != nil {
		t.Fatalf("reading issue file: %v", err)
	}

	if !strings.Contains(string(data), "Issue #42") {
		t.Errorf("issue file missing issue number")
	}
}

func TestWritePRContext(t *testing.T) {
	dir := t.TempDir()
	scDir := filepath.Join(dir, ".spinclass")

	opts := WriteOptions{
		WorktreePath: dir,
		RepoPath:     "/home/user/repos/bob",
		RemoteURL:    "git@github.com:amarbel-llc/bob.git",
		Branch:       "feature-x",
		SessionID:    "bob/feature-x",
		PR: &PRData{
			Number:  100,
			Title:   "Add feature",
			State:   "OPEN",
			BaseRef: "master",
			HeadRef: "feature-x",
			URL:     "https://github.com/amarbel-llc/bob/pull/100",
			Body:    "PR body.",
		},
	}

	if err := WriteSessionContext(opts); err != nil {
		t.Fatalf("WriteSessionContext() error: %v", err)
	}

	prPath := filepath.Join(scDir, "system_prompt_append.d", "1-pr-100.md")
	data, err := os.ReadFile(prPath)
	if err != nil {
		t.Fatalf("reading PR file: %v", err)
	}

	if !strings.Contains(string(data), "PR #100") {
		t.Errorf("PR file missing PR number")
	}

	issueGlob, _ := filepath.Glob(filepath.Join(scDir, "system_prompt_append.d", "1-issue-*.md"))
	if len(issueGlob) > 0 {
		t.Error("should not have issue file when PR is set")
	}
}

func TestWriteNoOptionalContext(t *testing.T) {
	dir := t.TempDir()
	scDir := filepath.Join(dir, ".spinclass")

	opts := WriteOptions{
		WorktreePath: dir,
		RepoPath:     "/home/user/repos/bob",
		RemoteURL:    "git@github.com:amarbel-llc/bob.git",
		Branch:       "feature-x",
		SessionID:    "bob/feature-x",
	}

	if err := WriteSessionContext(opts); err != nil {
		t.Fatalf("WriteSessionContext() error: %v", err)
	}

	matches, _ := filepath.Glob(filepath.Join(scDir, "system_prompt_append.d", "1-*.md"))
	if len(matches) > 0 {
		t.Errorf("expected no optional context files, got: %v", matches)
	}
}
