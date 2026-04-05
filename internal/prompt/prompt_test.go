package prompt

import (
	"strings"
	"testing"
)

func TestRenderBase(t *testing.T) {
	data := BaseData{
		RepoName:  "bob",
		RemoteURL: "git@github.com:amarbel-llc/bob.git",
		Branch:    "feature-x",
		SessionID: "bob/feature-x",
	}

	got, err := RenderBase(data)
	if err != nil {
		t.Fatalf("RenderBase() error: %v", err)
	}

	if got == "" {
		t.Fatal("RenderBase() returned empty string")
	}

	for _, want := range []string{
		"bob",
		"git@github.com:amarbel-llc/bob.git",
		"feature-x",
		"bob/feature-x",
		"spinclass worktree session",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderBase() missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderBaseWithForkInfo(t *testing.T) {
	data := BaseData{
		RepoName:   "bob",
		RemoteURL:  "git@github.com:someone/bob.git",
		Branch:     "fix-bug",
		SessionID:  "bob/fix-bug",
		IsFork:     true,
		OwnerType:  "User",
		OwnerLogin: "someone",
	}

	got, err := RenderBase(data)
	if err != nil {
		t.Fatalf("RenderBase() error: %v", err)
	}

	for _, want := range []string{"Fork:", "User", "someone"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderBase() missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderBaseOmitsForkWhenFalse(t *testing.T) {
	data := BaseData{
		RepoName:  "bob",
		RemoteURL: "git@github.com:amarbel-llc/bob.git",
		Branch:    "feature-x",
		SessionID: "bob/feature-x",
	}

	got, err := RenderBase(data)
	if err != nil {
		t.Fatalf("RenderBase() error: %v", err)
	}

	if strings.Contains(got, "Fork:") {
		t.Errorf("RenderBase() should omit Fork when IsFork=false, got:\n%s", got)
	}
}

func TestRenderIssue(t *testing.T) {
	data := IssueData{
		Number: 42,
		Title:  "Fix login bug",
		State:  "OPEN",
		Labels: "bug, auth",
		URL:    "https://github.com/amarbel-llc/bob/issues/42",
		Body:   "Login fails when password contains special chars.",
	}

	got, err := RenderIssue(data)
	if err != nil {
		t.Fatalf("RenderIssue() error: %v", err)
	}

	for _, want := range []string{
		"Issue #42",
		"Fix login bug",
		"OPEN",
		"bug, auth",
		"Login fails",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderIssue() missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderIssueOmitsLabelsWhenEmpty(t *testing.T) {
	data := IssueData{
		Number: 10,
		Title:  "No labels",
		State:  "OPEN",
		URL:    "https://github.com/amarbel-llc/bob/issues/10",
		Body:   "Body text.",
	}

	got, err := RenderIssue(data)
	if err != nil {
		t.Fatalf("RenderIssue() error: %v", err)
	}

	if strings.Contains(got, "Labels:") {
		t.Errorf("RenderIssue() should omit Labels when empty, got:\n%s", got)
	}
}

func TestRenderPR(t *testing.T) {
	data := PRData{
		Number:  100,
		Title:   "Add feature X",
		State:   "OPEN",
		BaseRef: "master",
		HeadRef: "feature-x",
		Labels:  "enhancement",
		URL:     "https://github.com/amarbel-llc/bob/pull/100",
		Body:    "This PR adds feature X.",
	}

	got, err := RenderPR(data)
	if err != nil {
		t.Fatalf("RenderPR() error: %v", err)
	}

	for _, want := range []string{
		"PR #100",
		"Add feature X",
		"master",
		"feature-x",
		"This PR adds feature X.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderPR() missing %q in:\n%s", want, got)
		}
	}
}
