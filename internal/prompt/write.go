package prompt

import (
	"fmt"
	"os"
	"path/filepath"
)

// PluginFragment is a user-defined `start-<name>` command's markdown
// contribution to the session's system_prompt_append.d/ directory.
type PluginFragment struct {
	Name    string // becomes the filename stem: 3-start-<Name>.md
	Content string // written verbatim
}

type WriteOptions struct {
	WorktreePath    string
	RepoPath        string
	RemoteURL       string
	Branch          string
	SessionID       string
	IsFork          bool
	OwnerType       string
	OwnerLogin      string
	Issue           *IssueData
	PR              *PRData
	PluginFragments []PluginFragment
}

func WriteSessionContext(opts WriteOptions) error {
	dir := filepath.Join(opts.WorktreePath, ".spinclass", "system_prompt_append.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating system_prompt_append.d: %w", err)
	}

	baseData := BaseData{
		RepoName:   ParseRepoInfo(opts.RemoteURL, opts.RepoPath).RepoName,
		RemoteURL:  opts.RemoteURL,
		Branch:     opts.Branch,
		SessionID:  opts.SessionID,
		IsFork:     opts.IsFork,
		OwnerType:  opts.OwnerType,
		OwnerLogin: opts.OwnerLogin,
	}

	baseContent, err := RenderBase(baseData)
	if err != nil {
		return fmt.Errorf("rendering base template: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "0-base.md"), []byte(baseContent), 0o644); err != nil {
		return fmt.Errorf("writing 0-base.md: %w", err)
	}

	if opts.Issue != nil {
		content, err := RenderIssue(*opts.Issue)
		if err != nil {
			return fmt.Errorf("rendering issue template: %w", err)
		}
		filename := fmt.Sprintf("1-issue-%d.md", opts.Issue.Number)
		if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", filename, err)
		}
	}

	if opts.PR != nil {
		content, err := RenderPR(*opts.PR)
		if err != nil {
			return fmt.Errorf("rendering PR template: %w", err)
		}
		filename := fmt.Sprintf("1-pr-%d.md", opts.PR.Number)
		if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", filename, err)
		}
	}

	for _, frag := range opts.PluginFragments {
		if frag.Name == "" || frag.Content == "" {
			continue
		}
		filename := fmt.Sprintf("3-start-%s.md", frag.Name)
		if err := os.WriteFile(filepath.Join(dir, filename), []byte(frag.Content), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", filename, err)
		}
	}

	return nil
}
