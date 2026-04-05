package perms

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type claudeSettings struct {
	Permissions struct {
		Allow []string `json:"allow"`
	} `json:"permissions"`
}

// LoadClaudeSettings reads the allow list from a Claude settings.local.json
// file. Returns nil and no error when the file does not exist.
func LoadClaudeSettings(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var settings claudeSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}

	return settings.Permissions.Allow, nil
}

// SaveClaudeSettings writes the allow list back to a Claude settings.local.json
// file, preserving any other top-level keys. Creates parent directories as needed.
func SaveClaudeSettings(path string, rules []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	// Read existing file to preserve non-permission fields
	var doc map[string]any
	if existing, err := os.ReadFile(path); err == nil {
		json.Unmarshal(existing, &doc)
	}
	if doc == nil {
		doc = make(map[string]any)
	}

	permsMap, _ := doc["permissions"].(map[string]any)
	if permsMap == nil {
		permsMap = make(map[string]any)
	}
	permsMap["allow"] = rules
	doc["permissions"] = permsMap

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}

	data = append(data, '\n')

	return os.WriteFile(path, data, 0o644)
}

// DiffRules returns rules present in after but not in before, preserving the
// order from after.
func DiffRules(before, after []string) []string {
	beforeSet := make(map[string]bool, len(before))
	for _, r := range before {
		beforeSet[r] = true
	}

	var diff []string
	for _, r := range after {
		if !beforeSet[r] {
			diff = append(diff, r)
		}
	}

	if diff == nil {
		diff = []string{}
	}

	return diff
}

// RemoveRules returns rules with toRemove entries filtered out, preserving the
// original order.
func RemoveRules(rules, toRemove []string) []string {
	removeSet := make(map[string]bool, len(toRemove))
	for _, r := range toRemove {
		removeSet[r] = true
	}

	var result []string
	for _, r := range rules {
		if !removeSet[r] {
			result = append(result, r)
		}
	}

	if result == nil {
		result = []string{}
	}

	return result
}

// LoadRulesFromLog reads the JSONL tool-use log and returns deduplicated
// permission strings derived from each entry. Returns nil and no error when
// the file does not exist.
func LoadRulesFromLog(logPath string) ([]string, error) {
	f, err := os.Open(logPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	seen := make(map[string]bool)
	var rules []string

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry struct {
			ToolName  string         `json:"tool_name"`
			ToolInput map[string]any `json:"tool_input"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}

		rule := BuildPermissionString(entry.ToolName, entry.ToolInput)
		if !seen[rule] {
			seen[rule] = true
			rules = append(rules, rule)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return rules, nil
}

// ComputeReviewableRules returns rules derived from the tool-use log that are
// not already covered by global Claude settings, curated tier files, or
// auto-injected worktree-scoped rules.
func ComputeReviewableRules(
	logPath, globalSettingsPath, tiersDir, repo, worktreePath string,
) ([]string, error) {
	worktreeRules, err := LoadRulesFromLog(logPath)
	if err != nil {
		return nil, err
	}

	globalRules, err := LoadClaudeSettings(globalSettingsPath)
	if err != nil {
		return nil, err
	}

	tierRules := LoadTiers(tiersDir, repo)

	// Collect all rules that should exclude a log-derived rule from review.
	// These are glob-style rules (e.g. "Read(/path/*)") that need pattern
	// matching against the specific permission strings from the log.
	var excludeRules []string
	excludeRules = append(excludeRules, globalRules...)
	excludeRules = append(excludeRules, tierRules...)

	// Auto-injected worktree-scoped rules
	home, _ := os.UserHomeDir()
	if home != "" {
		excludeRules = append(excludeRules, fmt.Sprintf("Read(%s/.claude/*)", home))
	}
	if worktreePath != "" {
		excludeRules = append(excludeRules, fmt.Sprintf("Read(%s/*)", worktreePath))
		excludeRules = append(excludeRules, fmt.Sprintf("Edit(%s/*)", worktreePath))
		excludeRules = append(excludeRules, fmt.Sprintf("Write(%s/*)", worktreePath))
	}

	var result []string
	for _, r := range worktreeRules {
		// Parse the log-derived rule back into tool name + input to check
		// against exclude rules using pattern matching.
		ruleTool, ruleArg := parseRule(r)
		toolInput := rebuildToolInput(ruleTool, ruleArg)
		if !MatchesAnyRule(excludeRules, ruleTool, toolInput) {
			result = append(result, r)
		}
	}

	if result == nil {
		result = []string{}
	}

	return result, nil
}

// rebuildToolInput reconstructs a tool_input map from a parsed permission
// string so that MatchesAnyRule can match it against glob-style exclude rules.
func rebuildToolInput(toolName, arg string) map[string]any {
	if arg == "" {
		return nil
	}

	switch toolName {
	case "Bash":
		return map[string]any{"command": arg}
	case "Read", "Edit", "Write":
		return map[string]any{"file_path": arg}
	case "WebFetch":
		return map[string]any{"url": arg}
	default:
		return nil
	}
}

// GlobalClaudeSettingsPath returns the path to the user-level Claude
// settings.local.json file.
func GlobalClaudeSettingsPath() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "settings.local.json")
}

// ToolUseLogPath returns the XDG log path for a session's tool-use log.
// The session key format is "repo/branch", sanitized to "repo--branch.jsonl".
func ToolUseLogPath(repoName, branch string) string {
	base := os.Getenv("XDG_LOG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			return ""
		}
		base = filepath.Join(home, ".local", "log")
	}
	filename := strings.ReplaceAll(repoName+"/"+branch, "/", "--") + ".jsonl"
	return filepath.Join(base, "spinclass", "tool-uses", filename)
}
