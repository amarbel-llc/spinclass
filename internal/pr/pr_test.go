package pr

import (
	"os/exec"
	"strings"
	"testing"
)

func TestParseIdentifierBareNumber(t *testing.T) {
	owner, repo, number, err := ParseIdentifier("42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "" || repo != "" {
		t.Errorf("expected empty owner/repo for bare number, got %q/%q", owner, repo)
	}
	if number != 42 {
		t.Errorf("number = %d, want 42", number)
	}
}

func TestParseIdentifierURL(t *testing.T) {
	owner, repo, number, err := ParseIdentifier("https://github.com/myorg/myrepo/pull/123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "myorg" {
		t.Errorf("owner = %q, want %q", owner, "myorg")
	}
	if repo != "myrepo" {
		t.Errorf("repo = %q, want %q", repo, "myrepo")
	}
	if number != 123 {
		t.Errorf("number = %d, want 123", number)
	}
}

func TestParseIdentifierURLTrailingSlash(t *testing.T) {
	owner, repo, number, err := ParseIdentifier("https://github.com/myorg/myrepo/pull/7/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "myorg" || repo != "myrepo" || number != 7 {
		t.Errorf("got (%q, %q, %d), want (myorg, myrepo, 7)", owner, repo, number)
	}
}

func TestParseIdentifierInvalid(t *testing.T) {
	_, _, _, err := ParseIdentifier("not-a-number")
	if err == nil {
		t.Error("expected error for invalid identifier")
	}
}

func TestParseIdentifierBadURL(t *testing.T) {
	_, _, _, err := ParseIdentifier("https://github.com/owner/repo/issues/42")
	if err == nil {
		t.Error("expected error for non-PR URL")
	}
}

func TestResolveParsesPRJSON(t *testing.T) {
	jsonResponse := `{
		"headRefName": "fix-login",
		"isCrossRepository": false,
		"title": "Fix login bug",
		"number": 42
	}`

	runner := func(name string, args ...string) ([]byte, error) {
		return []byte(jsonResponse), nil
	}

	info, err := resolveWithRunner("42", "/tmp/repo", runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.HeadRefName != "fix-login" {
		t.Errorf("HeadRefName = %q, want %q", info.HeadRefName, "fix-login")
	}
	if info.IsCrossRepository {
		t.Error("IsCrossRepository should be false")
	}
	if info.Title != "Fix login bug" {
		t.Errorf("Title = %q, want %q", info.Title, "Fix login bug")
	}
	if info.Number != 42 {
		t.Errorf("Number = %d, want 42", info.Number)
	}
}

func TestResolveForkPRReturnsError(t *testing.T) {
	jsonResponse := `{
		"headRefName": "fork-branch",
		"isCrossRepository": true,
		"title": "Fork contribution",
		"number": 99
	}`

	runner := func(name string, args ...string) ([]byte, error) {
		return []byte(jsonResponse), nil
	}

	_, err := resolveWithRunner("99", "/tmp/repo", runner)
	if err == nil {
		t.Fatal("expected error for fork PR")
	}
	if !strings.Contains(err.Error(), "fork") {
		t.Errorf("error should mention fork, got: %v", err)
	}
}

func TestResolveGhNotInstalled(t *testing.T) {
	runner := func(name string, args ...string) ([]byte, error) {
		return nil, &exec.Error{Name: "gh", Err: exec.ErrNotFound}
	}

	_, err := resolveWithRunner("42", "/tmp/repo", runner)
	if err == nil {
		t.Fatal("expected error when gh not found")
	}
	if !strings.Contains(err.Error(), "gh") {
		t.Errorf("error should mention gh, got: %v", err)
	}
}

func TestRepoSlugFromRemoteURLSSH(t *testing.T) {
	got := repoSlugFromRemoteURL("git@github.com:myorg/myrepo.git")
	if got != "myorg/myrepo" {
		t.Errorf("got %q, want %q", got, "myorg/myrepo")
	}
}

func TestRepoSlugFromRemoteURLHTTPS(t *testing.T) {
	got := repoSlugFromRemoteURL("https://github.com/myorg/myrepo.git")
	if got != "myorg/myrepo" {
		t.Errorf("got %q, want %q", got, "myorg/myrepo")
	}
}

func TestRepoSlugFromRemoteURLNoSuffix(t *testing.T) {
	got := repoSlugFromRemoteURL("https://github.com/myorg/myrepo")
	if got != "myorg/myrepo" {
		t.Errorf("got %q, want %q", got, "myorg/myrepo")
	}
}
