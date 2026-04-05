package prompt

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type RepoInfo struct {
	RepoName   string
	RemoteURL  string
	IsFork     bool
	OwnerType  string
	OwnerLogin string
}

func ParseRepoInfo(remoteURL, repoPath string) RepoInfo {
	info := RepoInfo{
		RemoteURL: remoteURL,
		RepoName:  filepath.Base(repoPath),
	}

	if remoteURL != "" {
		name := remoteURL
		name = strings.TrimSuffix(name, ".git")
		if idx := strings.LastIndex(name, "/"); idx >= 0 {
			name = name[idx+1:]
		} else if idx := strings.LastIndex(name, ":"); idx >= 0 {
			name = name[idx+1:]
			if slash := strings.LastIndex(name, "/"); slash >= 0 {
				name = name[slash+1:]
			}
		}
		if name != "" {
			info.RepoName = name
		}
	}

	return info
}

type ghRepoView struct {
	IsFork bool `json:"isFork"`
	Owner  struct {
		Type  string `json:"type"`
		Login string `json:"login"`
	} `json:"owner"`
}

func FetchRepoMetadata(repoPath string) (isFork bool, ownerType, ownerLogin string) {
	slug := repoSlug(repoPath)
	if slug == "" {
		return false, "", ""
	}

	out, err := exec.Command(
		"gh", "repo", "view", slug,
		"--json", "isFork,owner",
	).Output()
	if err != nil {
		return false, "", ""
	}

	var view ghRepoView
	if json.Unmarshal(out, &view) != nil {
		return false, "", ""
	}

	return view.IsFork, view.Owner.Type, view.Owner.Login
}

func repoSlug(repoPath string) string {
	out, err := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	remote := strings.TrimSpace(string(out))

	if strings.HasPrefix(remote, "git@") {
		parts := strings.SplitN(remote, ":", 2)
		if len(parts) == 2 {
			return strings.TrimSuffix(parts[1], ".git")
		}
	}
	remote = strings.TrimSuffix(remote, ".git")
	if idx := strings.Index(remote, "github.com/"); idx >= 0 {
		return remote[idx+len("github.com/"):]
	}
	return ""
}

type ghIssueView struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	URL    string `json:"url"`
	Body   string `json:"body"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func FetchIssue(identifier string, repoPath string) (IssueData, error) {
	slug := repoSlug(repoPath)
	args := []string{
		"issue", "view", identifier,
		"--json", "number,title,state,url,body,labels",
	}
	if slug != "" {
		args = append(args, "--repo", slug)
	}

	out, err := exec.Command("gh", args...).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return IssueData{}, fmt.Errorf("gh issue view failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return IssueData{}, fmt.Errorf("gh issue view: %w", err)
	}

	var view ghIssueView
	if err := json.Unmarshal(out, &view); err != nil {
		return IssueData{}, fmt.Errorf("parsing gh output: %w", err)
	}

	var labels []string
	for _, l := range view.Labels {
		labels = append(labels, l.Name)
	}

	return IssueData{
		Number: view.Number,
		Title:  view.Title,
		State:  view.State,
		URL:    view.URL,
		Body:   view.Body,
		Labels: strings.Join(labels, ", "),
	}, nil
}

type ghPRView struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	State       string `json:"state"`
	URL         string `json:"url"`
	Body        string `json:"body"`
	HeadRefName string `json:"headRefName"`
	BaseRefName string `json:"baseRefName"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func FetchPR(identifier string, repoPath string) (PRData, error) {
	slug := repoSlug(repoPath)
	args := []string{
		"pr", "view", identifier,
		"--json", "number,title,state,url,body,headRefName,baseRefName,labels",
	}
	if slug != "" {
		args = append(args, "--repo", slug)
	}

	out, err := exec.Command("gh", args...).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return PRData{}, fmt.Errorf("gh pr view failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return PRData{}, fmt.Errorf("gh pr view: %w", err)
	}

	var view ghPRView
	if err := json.Unmarshal(out, &view); err != nil {
		return PRData{}, fmt.Errorf("parsing gh output: %w", err)
	}

	var labels []string
	for _, l := range view.Labels {
		labels = append(labels, l.Name)
	}

	return PRData{
		Number:  view.Number,
		Title:   view.Title,
		State:   view.State,
		BaseRef: view.BaseRefName,
		HeadRef: view.HeadRefName,
		URL:     view.URL,
		Body:    view.Body,
		Labels:  strings.Join(labels, ", "),
	}, nil
}
