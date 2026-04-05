package pr

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
)

// PRInfo holds metadata about a pull request.
type PRInfo struct {
	HeadRefName       string `json:"headRefName"`
	IsCrossRepository bool   `json:"isCrossRepository"`
	Title             string `json:"title"`
	Number            int    `json:"number"`
}

// ParseIdentifier parses a PR identifier string into owner, repo, and number.
// For bare numbers (e.g. "42"), owner and repo are empty strings.
// For GitHub URLs (e.g. "https://github.com/owner/repo/pull/42"), all three
// are populated.
func ParseIdentifier(identifier string) (owner, repo string, number int, err error) {
	// Try bare number first.
	if n, parseErr := strconv.Atoi(identifier); parseErr == nil {
		return "", "", n, nil
	}

	// Try URL.
	u, parseErr := url.Parse(identifier)
	if parseErr != nil || u.Host != "github.com" {
		return "", "", 0, fmt.Errorf("invalid PR identifier %q: must be a number or github.com URL", identifier)
	}

	// Expected path: /owner/repo/pull/number[/]
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return "", "", 0, fmt.Errorf("invalid GitHub PR URL %q: expected /owner/repo/pull/number", identifier)
	}

	n, parseErr := strconv.Atoi(parts[3])
	if parseErr != nil {
		return "", "", 0, fmt.Errorf("invalid PR number in URL %q: %w", identifier, parseErr)
	}

	return parts[0], parts[1], n, nil
}

type commandRunner func(name string, args ...string) ([]byte, error)

func defaultRunner(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		if execErr, ok := err.(*exec.Error); ok {
			return nil, fmt.Errorf("%s is not installed: %w (install from https://cli.github.com)", execErr.Name, err)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh pr view failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

func resolveWithRunner(identifier, repoPath string, runner commandRunner) (PRInfo, error) {
	owner, repo, number, err := ParseIdentifier(identifier)
	if err != nil {
		return PRInfo{}, err
	}

	args := []string{
		"pr", "view", strconv.Itoa(number),
		"--json", "headRefName,isCrossRepository,title,number",
	}
	if owner != "" && repo != "" {
		args = append(args, "--repo", owner+"/"+repo)
	} else {
		args = append(args, "--repo", RemoteRepo(repoPath))
	}

	out, err := runner("gh", args...)
	if err != nil {
		return PRInfo{}, err
	}

	var info PRInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return PRInfo{}, fmt.Errorf("parsing gh output: %w", err)
	}

	if info.IsCrossRepository {
		return PRInfo{}, fmt.Errorf(
			"fork PRs are not yet supported (PR #%d is from a fork); fetch the branch manually",
			info.Number,
		)
	}

	return info, nil
}

// Resolve fetches PR metadata from GitHub using the gh CLI.
// identifier is either a bare number ("42") or a GitHub PR URL.
// repoPath is the local git repo path, used to derive the remote when
// identifier is a bare number.
func Resolve(identifier, repoPath string) (PRInfo, error) {
	return resolveWithRunner(identifier, repoPath, defaultRunner)
}

// RemoteRepo derives the GitHub owner/repo slug from the origin remote URL.
func RemoteRepo(repoPath string) string {
	cmd := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return repoSlugFromRemoteURL(strings.TrimSpace(string(out)))
}

func repoSlugFromRemoteURL(remoteURL string) string {
	// Handle SSH: git@github.com:owner/repo.git
	if strings.HasPrefix(remoteURL, "git@") {
		parts := strings.SplitN(remoteURL, ":", 2)
		if len(parts) == 2 {
			return strings.TrimSuffix(parts[1], ".git")
		}
	}
	// Handle HTTPS: https://github.com/owner/repo.git
	u, err := url.Parse(remoteURL)
	if err != nil {
		return remoteURL
	}
	return strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
}
