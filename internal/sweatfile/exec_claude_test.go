package sweatfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectSystemPromptAppend(t *testing.T) {
	dir := t.TempDir()
	appendDir := filepath.Join(dir, ".spinclass", "system_prompt_append.d")
	if err := os.MkdirAll(appendDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(appendDir, "0-base.md"),
		[]byte("base context"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(appendDir, "1-issue-42.md"),
		[]byte("issue context"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	got, err := collectSystemPromptAppend(dir)
	if err != nil {
		t.Fatalf("collectSystemPromptAppend() error: %v", err)
	}

	if !strings.Contains(got, "base context") {
		t.Error("missing base context")
	}
	if !strings.Contains(got, "issue context") {
		t.Error("missing issue context")
	}

	baseIdx := strings.Index(got, "base context")
	issueIdx := strings.Index(got, "issue context")
	if baseIdx > issueIdx {
		t.Error("base context should come before issue context")
	}
}

func TestCollectSystemPromptAppendWithUserContent(t *testing.T) {
	dir := t.TempDir()
	appendDir := filepath.Join(dir, ".spinclass", "system_prompt_append.d")
	if err := os.MkdirAll(appendDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(appendDir, "0-base.md"),
		[]byte("base"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(appendDir, "2-user.md"),
		[]byte("user prompt"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	got, err := collectSystemPromptAppend(dir)
	if err != nil {
		t.Fatalf("collectSystemPromptAppend() error: %v", err)
	}

	if !strings.Contains(got, "user prompt") {
		t.Error("missing user prompt")
	}

	baseIdx := strings.Index(got, "base")
	userIdx := strings.Index(got, "user prompt")
	if baseIdx > userIdx {
		t.Error("base should come before user prompt")
	}
}

func TestCollectSystemPromptAppendEmpty(t *testing.T) {
	dir := t.TempDir()

	got, err := collectSystemPromptAppend(dir)
	if err != nil {
		t.Fatalf("collectSystemPromptAppend() error: %v", err)
	}

	if got != "" {
		t.Errorf("expected empty string when no .spinclass dir, got: %q", got)
	}
}
