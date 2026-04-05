package sweatfile

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/amarbel-llc/spinclass/internal/git"
)

func (sweatfile Sweatfile) Apply(worktreePath string) error {
	defaults := GetDefault()
	merged := sweatfile.MergeWith(defaults)

	if err := ApplyClaudeSettings(worktreePath, merged); err != nil {
		return fmt.Errorf("applying claude settings: %w", err)
	}

	if err := sweatfile.prepareLocalBin(); err != nil {
		return err
	}

	if err := sweatfile.writeSpinclassEnv(worktreePath); err != nil {
		return fmt.Errorf("writing .spinclass.env: %w", err)
	}

	if err := sweatfile.prepareDirenv(worktreePath); err != nil {
		return err
	}

	return nil
}

func resolveSpinclassBinDir() (string, error) {
	gitCommonDir, err := getGitDirCommon()
	if err != nil {
		return "", err
	}
	return filepath.Join(gitCommonDir, "spinclass", "bin"), nil
}

func binaryName() string {
	return filepath.Base(os.Args[0])
}

func (sweatfile Sweatfile) prepareLocalBin() error {
	dirSpinclassBin, err := resolveSpinclassBinDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dirSpinclassBin, 0o755); err != nil {
		return err
	}

	script := fmt.Sprintf("#! /usr/bin/env -S bash -e\nexec %s exec-claude \"$@\"", binaryName())
	if err := os.WriteFile(
		filepath.Join(dirSpinclassBin, "claude"),
		[]byte(script),
		0o644,
	); err != nil {
		return err
	}

	return nil
}

func (sf Sweatfile) writeEnvrc(worktreePath string) error {
	file, err := os.OpenFile(
		filepath.Join(worktreePath, ".envrc"),
		os.O_TRUNC|os.O_CREATE|os.O_WRONLY,
		0o644,
	)
	if err != nil {
		return err
	}
	defer file.Close()

	bufferedWriter := bufio.NewWriter(file)

	var directives []string
	if sf.Direnv != nil && sf.Direnv.Envrc != nil {
		directives = sf.Direnv.Envrc
	} else {
		directives = []string{"source_up"}
		if _, ok := fileExists(filepath.Join(worktreePath, "flake.nix")); ok {
			directives = append(directives, "use flake")
		}
	}

	for _, directive := range directives {
		if _, err := fmt.Fprintln(bufferedWriter, directive); err != nil {
			return err
		}
	}

	if sf.Direnv != nil && len(sf.Direnv.Dotenv) > 0 {
		if _, err := fmt.Fprintln(bufferedWriter, "dotenv .spinclass.env"); err != nil {
			return err
		}
	}

	dirSpinclassBin, err := resolveSpinclassBinDir()
	if err != nil {
		return err
	}
	dirSpinclassBinAbs, err := filepath.Abs(dirSpinclassBin)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(
		bufferedWriter,
		"PATH_add \"%s\"\n",
		dirSpinclassBinAbs,
	); err != nil {
		return err
	}

	return bufferedWriter.Flush()
}

func (sf Sweatfile) writeSpinclassEnv(worktreePath string) error {
	if sf.Direnv == nil || len(sf.Direnv.Dotenv) == 0 {
		return nil
	}

	keys := make([]string, 0, len(sf.Direnv.Dotenv))
	for k := range sf.Direnv.Dotenv {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	file, err := os.OpenFile(
		filepath.Join(worktreePath, ".spinclass.env"),
		os.O_TRUNC|os.O_CREATE|os.O_WRONLY,
		0o644,
	)
	if err != nil {
		return err
	}
	defer file.Close()

	expand := func(key string) string {
		if key == "WORKTREE" {
			return worktreePath
		}
		return os.Getenv(key)
	}

	for _, k := range keys {
		expanded := os.Expand(sf.Direnv.Dotenv[k], expand)
		if _, err := fmt.Fprintf(file, "%s=%s\n", k, expanded); err != nil {
			return err
		}
	}

	return nil
}

func (sf Sweatfile) prepareDirenv(worktreePath string) error {
	direnvPath, err := exec.LookPath("direnv")
	if err != nil {
		return nil
	}

	if err := sf.writeEnvrc(worktreePath); err != nil {
		return err
	}

	cmd := exec.Command(direnvPath, "allow")
	cmd.Dir = worktreePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

func (sf Sweatfile) RunCreateHook(worktreePath string) error {
	cmd := sf.CreateHookCommand()
	return runHook(cmd, worktreePath)
}

func (sf Sweatfile) RunPreMergeHook(worktreePath string) error {
	cmd := sf.PreMergeHookCommand()
	return runHook(cmd, worktreePath)
}

func runHook(cmd *string, worktreePath string) error {
	if cmd == nil || *cmd == "" {
		return nil
	}

	script := stripEmptyLines(*cmd)
	if script == "" {
		return nil
	}

	c := exec.Command("sh", "-c", script)
	c.Dir = worktreePath
	c.Env = append(os.Environ(), "WORKTREE="+worktreePath)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	return c.Run()
}

func stripEmptyLines(s string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func ApplyClaudeSettings(worktreePath string, sweatfile Sweatfile) error {
	settingsPath := filepath.Join(
		worktreePath,
		".claude",
		"settings.local.json",
	)

	doc := make(map[string]any)

	permsMap, _ := doc["permissions"].(map[string]any)

	if permsMap == nil {
		permsMap = make(map[string]any)
	}

	var allRules []string
	if sweatfile.Claude != nil {
		allRules = append(allRules, sweatfile.Claude.Allow...)
	}

	allRules = append(allRules,
		fmt.Sprintf("Read(%s/*)", worktreePath),
		fmt.Sprintf("Edit(%s/*)", worktreePath),
		fmt.Sprintf("Write(%s/*)", worktreePath),
	)

	permsMap["defaultMode"] = "acceptEdits"
	permsMap["allow"] = allRules

	doc["permissions"] = permsMap

	// Auto-approve the spinclass MCP server written to .mcp.json so Claude
	// Code doesn't prompt the user to enable it on first launch.
	doc["enabledMcpjsonServers"] = []string{"spinclass"}

	if git.IsWorktree(worktreePath) {
		hooksMap := map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "*",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": binaryName() + " hooks",
						},
					},
				},
			},
		}

		if cmd := sweatfile.StopHookCommand(); cmd != nil && *cmd != "" {
			hooksMap["Stop"] = []any{
				map[string]any{
					"matcher": "*",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": binaryName() + " hooks",
						},
					},
				},
			}
		}

		if sweatfile.ToolUseLogEnabled() {
			hooksMap["PostToolUse"] = []any{
				map[string]any{
					"matcher": "*",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": binaryName() + " hooks",
						},
					},
				},
			}
		}

		doc["hooks"] = hooksMap
	}

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(settingsPath, append(data, '\n'), 0o644); err != nil {
		return err
	}

	// Create .spinclass/ directory for spinclass-owned data (tool-use log,
	// settings snapshot) separate from Claude Code's .claude/ directory.
	spinclassDir := filepath.Join(worktreePath, ".spinclass")
	if err := os.MkdirAll(spinclassDir, 0o755); err != nil {
		return err
	}

	// Write a snapshot so that `perms review` can diff against the baseline
	// and only surface rules added during the session.
	snapshotPath := filepath.Join(spinclassDir, ".settings-snapshot.json")
	return os.WriteFile(snapshotPath, append(data, '\n'), 0o644)
}
