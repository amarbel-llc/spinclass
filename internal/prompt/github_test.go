package prompt

import (
	"testing"
)

func TestParseRepoInfoSSH(t *testing.T) {
	info := ParseRepoInfo("git@github.com:amarbel-llc/bob.git", "/path/to/bob")
	if info.RepoName != "bob" {
		t.Errorf("RepoName = %q, want %q", info.RepoName, "bob")
	}
	if info.RemoteURL != "git@github.com:amarbel-llc/bob.git" {
		t.Errorf("RemoteURL = %q, want original URL", info.RemoteURL)
	}
}

func TestParseRepoInfoHTTPS(t *testing.T) {
	info := ParseRepoInfo("https://github.com/amarbel-llc/bob.git", "/path/to/bob")
	if info.RepoName != "bob" {
		t.Errorf("RepoName = %q, want %q", info.RepoName, "bob")
	}
}

func TestParseRepoInfoNoSuffix(t *testing.T) {
	info := ParseRepoInfo("git@github.com:amarbel-llc/bob", "/path/to/bob")
	if info.RepoName != "bob" {
		t.Errorf("RepoName = %q, want %q", info.RepoName, "bob")
	}
}

func TestParseRepoInfoFallsBackToDirname(t *testing.T) {
	info := ParseRepoInfo("", "/home/user/repos/my-project")
	if info.RepoName != "my-project" {
		t.Errorf("RepoName = %q, want %q", info.RepoName, "my-project")
	}
}
