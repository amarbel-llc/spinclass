package sweatfile

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/amarbel-llc/spinclass/internal/embeds"
)

func (sweatfile Sweatfile) Apply(worktreePath string) error {
	defaults := GetDefault()
	merged := sweatfile.MergeWith(defaults)

	if err := ApplyClaudeSettings(worktreePath, merged); err != nil {
		return fmt.Errorf("applying claude settings: %w", err)
	}

	if err := sweatfile.writeSpinclassEnv(worktreePath); err != nil {
		return fmt.Errorf("writing .spinclass.env: %w", err)
	}

	if err := sweatfile.prepareDirenv(worktreePath); err != nil {
		return err
	}

	return nil
}

func resolveSpinclassBinDir(worktreePath string) (string, error) {
	dir, err := gitCommonDir(worktreePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "spinclass", "bin"), nil
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

	dirSpinclassBin, err := resolveSpinclassBinDir(worktreePath)
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
	direnvPath, ok := resolveDirenv()
	if !ok {
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

// resolveDirenv returns the absolute path to the direnv binary, with
// the build-time-pinned value (from `lib.mkSpinclass`) taking
// precedence over PATH lookup. Returns ("", false) when direnv is
// unavailable in either location — callers treat that as "no direnv,
// skip envrc handling".
func resolveDirenv() (string, bool) {
	if pinned := embeds.DirenvBin(); pinned != "" {
		return pinned, true
	}
	path, err := exec.LookPath("direnv")
	if err != nil {
		return "", false
	}
	return path, true
}

func (sf Sweatfile) RunCreateHook(worktreePath string, w io.Writer) error {
	cmd := sf.CreateHookCommand()
	return runHook(cmd, worktreePath, w)
}

func (sf Sweatfile) RunPreMergeHook(worktreePath string, w io.Writer) error {
	cmd := sf.PreMergeHookCommand()
	return runHook(cmd, worktreePath, w)
}

func (sf Sweatfile) RunOnAttachHook(worktreePath string, w io.Writer) error {
	cmd := sf.OnAttachHookCommand()
	return runHook(cmd, worktreePath, w)
}

func (sf Sweatfile) RunOnDetachHook(worktreePath string, w io.Writer) error {
	cmd := sf.OnDetachHookCommand()
	return runHook(cmd, worktreePath, w)
}

func runHook(cmd *string, worktreePath string, w io.Writer) error {
	if cmd == nil || *cmd == "" {
		return nil
	}

	script := stripEmptyLines(*cmd)
	if script == "" {
		return nil
	}

	if w == nil {
		w = io.Discard
	}

	c := exec.Command("sh", "-c", script)
	c.Dir = worktreePath
	// Inherit os.Environ so SPINCLASS_* variables set by callers (or by
	// the running session) propagate into the hook. Always append WORKTREE
	// for backwards compatibility with existing hook scripts.
	c.Env = append(os.Environ(), "WORKTREE="+worktreePath)
	c.Stdout = w
	c.Stderr = w

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

	// Auto-approve any user-declared MCP servers from the sweatfile's
	// effective allow-list (sweatfile [[mcps]] entries plus allowed-mcps).
	// The spinclass MCP server itself is loaded via the clown plugin and
	// does not need a session-local entry here.
	var enabledMCPs []string
	seen := map[string]bool{}
	for _, name := range sweatfile.EffectiveAllowedMCPs() {
		if !seen[name] {
			seen[name] = true
			enabledMCPs = append(enabledMCPs, name)
		}
	}
	if len(enabledMCPs) > 0 {
		doc["enabledMcpjsonServers"] = enabledMCPs
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
