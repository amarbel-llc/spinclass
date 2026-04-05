package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeInput(toolName string, toolInput map[string]any, cwd string) []byte {
	input := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       toolName,
		"tool_input":      toolInput,
		"cwd":             cwd,
	}
	data, _ := json.Marshal(input)
	return data
}

func TestDisallowMainWorktreeOffAllowsEverything(t *testing.T) {
	mainRepo := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(mainRepo, "secret.go")
	input := makeInput("Read", map[string]any{"file_path": target}, outside)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, outside, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output when flag is off, got %q", stdout.String())
	}
}

func TestDisallowMainWorktreeOnDeniesMainRepoPath(t *testing.T) {
	mainRepo := t.TempDir()
	worktreeCwd := t.TempDir()
	target := filepath.Join(mainRepo, "main.go")
	input := makeInput("Read", map[string]any{"file_path": target}, worktreeCwd)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, worktreeCwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected deny output for path in main worktree")
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("expected valid JSON, got %q: %v", stdout.String(), err)
	}
	hso, ok := result["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatal("expected hookSpecificOutput in output")
	}
	if hso["permissionDecision"] != "deny" {
		t.Errorf("expected permissionDecision deny, got %v", hso["permissionDecision"])
	}
	reason, ok := hso["permissionDecisionReason"].(string)
	if !ok || reason == "" {
		t.Fatal("expected permissionDecisionReason in output")
	}
	if !strings.Contains(reason, "main worktree") {
		t.Errorf("expected permissionDecisionReason to mention main worktree, got %q", reason)
	}
}

func TestDisallowMainWorktreeOnAllowsWorktreePath(t *testing.T) {
	mainRepo := t.TempDir()
	worktreeCwd := t.TempDir()
	target := filepath.Join(worktreeCwd, "file.go")
	input := makeInput("Read", map[string]any{"file_path": target}, worktreeCwd)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, worktreeCwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output for worktree path, got %q", stdout.String())
	}
}

func TestDisallowMainWorktreeOnAllowsUnrelatedPath(t *testing.T) {
	mainRepo := t.TempDir()
	worktreeCwd := t.TempDir()
	unrelated := t.TempDir()
	target := filepath.Join(unrelated, "file.go")
	input := makeInput("Read", map[string]any{"file_path": target}, worktreeCwd)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, worktreeCwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output for unrelated path, got %q", stdout.String())
	}
}

func TestDisallowMainWorktreeEmptyMainRepoAllows(t *testing.T) {
	worktreeCwd := t.TempDir()
	target := filepath.Join(worktreeCwd, "file.go")
	input := makeInput("Read", map[string]any{"file_path": target}, worktreeCwd)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, "", worktreeCwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output with empty main repo, got %q", stdout.String())
	}
}

func TestDisallowMainWorktreeGlobInMainRepo(t *testing.T) {
	mainRepo := t.TempDir()
	worktreeCwd := t.TempDir()
	input := makeInput("Glob", map[string]any{"path": mainRepo}, worktreeCwd)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, worktreeCwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected deny output for Glob targeting main worktree")
	}
}

func TestDisallowMainWorktreeFindInMainRepo(t *testing.T) {
	mainRepo := t.TempDir()
	worktreeCwd := t.TempDir()
	input := makeInput("Find", map[string]any{"path": mainRepo}, worktreeCwd)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, worktreeCwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected deny output for Find targeting main worktree")
	}
}

func TestDisallowMainWorktreeBashAbsolutePathInMainRepo(t *testing.T) {
	mainRepo := t.TempDir()
	worktreeCwd := t.TempDir()
	target := filepath.Join(mainRepo, "src/main.go")
	input := makeInput("Bash", map[string]any{"command": "cat " + target}, worktreeCwd)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, worktreeCwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected deny output for Bash command targeting main worktree")
	}
}

func TestDisallowMainWorktreeSymlinkResolution(t *testing.T) {
	mainRepo := t.TempDir()
	worktreeCwd := t.TempDir()
	target := filepath.Join(mainRepo, "real.go")
	os.WriteFile(target, []byte("package main"), 0o644)
	link := filepath.Join(worktreeCwd, "link.go")
	os.Symlink(target, link)
	input := makeInput("Read", map[string]any{"file_path": link}, worktreeCwd)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, worktreeCwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected deny output for symlink resolving to main worktree")
	}
}

func TestDisallowMainWorktreeNonExistentFileInMainRepo(t *testing.T) {
	mainRepo := t.TempDir()
	worktreeCwd := t.TempDir()
	subdir := filepath.Join(mainRepo, "src")
	os.MkdirAll(subdir, 0o755)
	target := filepath.Join(subdir, "new.go")
	input := makeInput("Write", map[string]any{"file_path": target}, worktreeCwd)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, worktreeCwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected deny output for new file targeting main worktree")
	}
}

func TestDisallowMainWorktreeAllowsSessionWorktreeInsideMainRepo(t *testing.T) {
	mainRepo := t.TempDir()
	sessionWorktree := filepath.Join(mainRepo, ".worktrees", "my-session")
	os.MkdirAll(sessionWorktree, 0o755)
	target := filepath.Join(sessionWorktree, "file.go")
	input := makeInput("Read", map[string]any{"file_path": target}, sessionWorktree)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, sessionWorktree, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output for session worktree path inside main repo, got %q", stdout.String())
	}
}

func TestDisallowMainWorktreeAllowsSessionWorktreeExactPath(t *testing.T) {
	mainRepo := t.TempDir()
	sessionWorktree := filepath.Join(mainRepo, ".worktrees", "my-session")
	os.MkdirAll(sessionWorktree, 0o755)
	input := makeInput("Glob", map[string]any{"path": sessionWorktree}, sessionWorktree)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, sessionWorktree, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output for session worktree exact path, got %q", stdout.String())
	}
}

func TestDisallowMainWorktreeDeniesOtherWorktreeInsideMainRepo(t *testing.T) {
	mainRepo := t.TempDir()
	sessionWorktree := filepath.Join(mainRepo, ".worktrees", "my-session")
	otherWorktree := filepath.Join(mainRepo, ".worktrees", "other-session")
	os.MkdirAll(sessionWorktree, 0o755)
	os.MkdirAll(otherWorktree, 0o755)
	target := filepath.Join(otherWorktree, "file.go")
	input := makeInput("Read", map[string]any{"file_path": target}, sessionWorktree)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, sessionWorktree, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected deny output for path in a different worktree")
	}
}

func TestDisallowMainWorktreeDeniesMainRepoRootDirectly(t *testing.T) {
	mainRepo := t.TempDir()
	sessionWorktree := filepath.Join(mainRepo, ".worktrees", "my-session")
	os.MkdirAll(sessionWorktree, 0o755)
	input := makeInput("Glob", map[string]any{"path": mainRepo}, sessionWorktree)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, sessionWorktree, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected deny output for main repo root path")
	}
}

func TestDisallowMainWorktreeDenyMessageIncludesSessionWorktree(t *testing.T) {
	mainRepo := t.TempDir()
	sessionWorktree := filepath.Join(mainRepo, ".worktrees", "my-session")
	os.MkdirAll(sessionWorktree, 0o755)
	target := filepath.Join(mainRepo, "main.go")
	input := makeInput("Read", map[string]any{"file_path": target}, sessionWorktree)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, sessionWorktree, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	json.Unmarshal(stdout.Bytes(), &result)
	hso := result["hookSpecificOutput"].(map[string]any)
	reason := hso["permissionDecisionReason"].(string)
	if !strings.Contains(reason, sessionWorktree) {
		t.Errorf("expected deny reason to include session worktree path %q, got %q", sessionWorktree, reason)
	}
}

func TestBashCdToMainWorktreeDeniesWithSuggestion(t *testing.T) {
	mainRepo := t.TempDir()
	worktreeCwd := t.TempDir()
	cmd := "cd " + mainRepo + " && just build"
	input := makeInput("Bash", map[string]any{"command": cmd}, worktreeCwd)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, worktreeCwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected deny output for cd to main worktree")
	}
	var result map[string]any
	json.Unmarshal(stdout.Bytes(), &result)
	hso := result["hookSpecificOutput"].(map[string]any)
	if hso["permissionDecision"] != "deny" {
		t.Errorf("expected deny, got %v", hso["permissionDecision"])
	}
	reason := hso["permissionDecisionReason"].(string)
	if !strings.Contains(reason, "just build") {
		t.Errorf("expected suggestion to contain 'just build', got %q", reason)
	}
	if !strings.Contains(reason, "session worktree") {
		t.Errorf("expected reason to mention session worktree, got %q", reason)
	}
}

func TestBashCdToMainWorktreeWithSemicolon(t *testing.T) {
	mainRepo := t.TempDir()
	worktreeCwd := t.TempDir()
	cmd := "cd " + mainRepo + " ; just test"
	input := makeInput("Bash", map[string]any{"command": cmd}, worktreeCwd)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, worktreeCwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected deny output for cd to main worktree with semicolon")
	}
	var result map[string]any
	json.Unmarshal(stdout.Bytes(), &result)
	reason := result["hookSpecificOutput"].(map[string]any)["permissionDecisionReason"].(string)
	if !strings.Contains(reason, "just test") {
		t.Errorf("expected suggestion to contain 'just test', got %q", reason)
	}
}

func TestBashCdToMainWorktreeSubdir(t *testing.T) {
	mainRepo := t.TempDir()
	worktreeCwd := t.TempDir()
	subdir := filepath.Join(mainRepo, "src")
	os.MkdirAll(subdir, 0o755)
	cmd := "cd " + subdir + " && make"
	input := makeInput("Bash", map[string]any{"command": cmd}, worktreeCwd)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, worktreeCwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected deny output for cd to main worktree subdir")
	}
	var result map[string]any
	json.Unmarshal(stdout.Bytes(), &result)
	reason := result["hookSpecificOutput"].(map[string]any)["permissionDecisionReason"].(string)
	if !strings.Contains(reason, "make") {
		t.Errorf("expected suggestion to contain 'make', got %q", reason)
	}
}

func TestBashCdToSessionWorktreeAllowed(t *testing.T) {
	mainRepo := t.TempDir()
	worktreeCwd := t.TempDir()
	cmd := "cd " + worktreeCwd + " && just build"
	input := makeInput("Bash", map[string]any{"command": cmd}, worktreeCwd)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, worktreeCwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output for cd to session worktree, got %q", stdout.String())
	}
}

func TestBashCdToUnrelatedDirAllowed(t *testing.T) {
	mainRepo := t.TempDir()
	worktreeCwd := t.TempDir()
	unrelated := t.TempDir()
	cmd := "cd " + unrelated + " && ls"
	input := makeInput("Bash", map[string]any{"command": cmd}, worktreeCwd)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, worktreeCwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output for cd to unrelated dir, got %q", stdout.String())
	}
}

func TestBashCdOnlyNoRestCommand(t *testing.T) {
	mainRepo := t.TempDir()
	worktreeCwd := t.TempDir()
	cmd := "cd " + mainRepo
	input := makeInput("Bash", map[string]any{"command": cmd}, worktreeCwd)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, worktreeCwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected deny output for bare cd to main worktree")
	}
}

func TestBashCdWithQuotedPath(t *testing.T) {
	mainRepo := t.TempDir()
	worktreeCwd := t.TempDir()
	cmd := `cd "` + mainRepo + `" && just`
	input := makeInput("Bash", map[string]any{"command": cmd}, worktreeCwd)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, worktreeCwd, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected deny output for cd with quoted path to main worktree")
	}
	var result map[string]any
	json.Unmarshal(stdout.Bytes(), &result)
	reason := result["hookSpecificOutput"].(map[string]any)["permissionDecisionReason"].(string)
	if !strings.Contains(reason, "just") {
		t.Errorf("expected suggestion to contain 'just', got %q", reason)
	}
}

func TestStopHookEventRouteApproves(t *testing.T) {
	input, _ := json.Marshal(map[string]any{
		"hook_event_name": "Stop",
		"session_id":      "test-session-123",
		"cwd":             t.TempDir(),
	})

	var out bytes.Buffer
	err := Run(bytes.NewReader(input), &out, "", "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No stop-hook configured -> approve (no output)
	if out.Len() != 0 {
		t.Errorf("expected no output for Stop with no stop-hook, got %q", out.String())
	}
}

func TestStopHookBlocksOnFailure(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)

	// Create a sweatfile with a failing stop-hook
	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "sweatfile"), []byte("[hooks]\nstop = \"false\""), 0o644)

	input, _ := json.Marshal(map[string]any{
		"hook_event_name": "Stop",
		"session_id":      "block-test-session",
		"cwd":             cwd,
	})

	var out bytes.Buffer
	err := Run(bytes.NewReader(input), &out, "", "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Len() == 0 {
		t.Fatal("expected block output for failing stop-hook")
	}

	var result map[string]any
	json.Unmarshal(out.Bytes(), &result)
	if result["decision"] != "block" {
		t.Errorf("expected block decision, got %v", result["decision"])
	}

	// Sentinel file should exist
	sentinel := filepath.Join(tmpDir, "stop-hook-block-test-session")
	if _, err := os.Stat(sentinel); os.IsNotExist(err) {
		t.Error("expected sentinel file to be created")
	}
}

func TestStopHookApprovesOnSecondInvocation(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)

	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "sweatfile"), []byte("[hooks]\nstop = \"false\""), 0o644)

	// Create sentinel file (simulating first invocation already happened)
	sentinel := filepath.Join(tmpDir, "stop-hook-approve-test-session")
	os.WriteFile(sentinel, []byte("previous failure output"), 0o644)

	input, _ := json.Marshal(map[string]any{
		"hook_event_name": "Stop",
		"session_id":      "approve-test-session",
		"cwd":             cwd,
	})

	var out bytes.Buffer
	err := Run(bytes.NewReader(input), &out, "", "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Sentinel exists -> approve (no output)
	if out.Len() != 0 {
		t.Errorf("expected no output on second invocation, got %q", out.String())
	}
}

func TestPostToolUseWritesLog(t *testing.T) {
	logHome := t.TempDir()
	t.Setenv("XDG_LOG_HOME", logHome)
	t.Setenv("SPINCLASS_SESSION_ID", "myrepo/feature-x")

	worktree := t.TempDir()

	input, _ := json.Marshal(map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "test-session",
		"tool_name":       "Edit",
		"tool_input":      map[string]any{"file_path": "/some/file.go"},
		"cwd":             worktree,
	})

	var out bytes.Buffer
	err := Run(bytes.NewReader(input), &out, "", "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Len() != 0 {
		t.Errorf("expected no output, got %q", out.String())
	}

	logPath := filepath.Join(logHome, "spinclass", "tool-uses", "myrepo--feature-x.jsonl")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("expected log file at %s: %v", logPath, err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}

	var logged map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &logged); err != nil {
		t.Fatalf("expected valid JSON log line: %v", err)
	}
	if logged["tool_name"] != "Edit" {
		t.Errorf("expected tool_name Edit, got %v", logged["tool_name"])
	}
}

func TestPostToolUseAppendsToLog(t *testing.T) {
	logHome := t.TempDir()
	t.Setenv("XDG_LOG_HOME", logHome)
	t.Setenv("SPINCLASS_SESSION_ID", "repo/append-test")

	worktree := t.TempDir()

	for _, tool := range []string{"Edit", "Bash"} {
		input, _ := json.Marshal(map[string]any{
			"hook_event_name": "PostToolUse",
			"session_id":      "test-session",
			"tool_name":       tool,
			"tool_input":      map[string]any{},
			"cwd":             worktree,
		})
		var out bytes.Buffer
		if err := Run(bytes.NewReader(input), &out, "", "", false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	logPath := filepath.Join(logHome, "spinclass", "tool-uses", "repo--append-test.jsonl")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("expected log file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines, got %d", len(lines))
	}
}

func TestPostToolUseNoSessionIsSilent(t *testing.T) {
	t.Setenv("SPINCLASS_SESSION_ID", "")
	cwd := t.TempDir()

	input, _ := json.Marshal(map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "test-session",
		"tool_name":       "Read",
		"tool_input":      map[string]any{},
		"cwd":             cwd,
	})

	var out bytes.Buffer
	err := Run(bytes.NewReader(input), &out, "", "", false)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestPostToolUseLogsFromSubdir(t *testing.T) {
	logHome := t.TempDir()
	t.Setenv("XDG_LOG_HOME", logHome)
	t.Setenv("SPINCLASS_SESSION_ID", "repo/subdir-test")

	worktree := t.TempDir()
	subdir := filepath.Join(worktree, "src", "pkg")
	os.MkdirAll(subdir, 0o755)

	input, _ := json.Marshal(map[string]any{
		"hook_event_name": "PostToolUse",
		"session_id":      "test-session",
		"tool_name":       "Grep",
		"tool_input":      map[string]any{},
		"cwd":             subdir,
	})

	var out bytes.Buffer
	err := Run(bytes.NewReader(input), &out, "", "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logPath := filepath.Join(logHome, "spinclass", "tool-uses", "repo--subdir-test.jsonl")
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		t.Fatal("expected log file to be created when CWD is a subdirectory")
	}
}

func TestStopHookApprovesOnSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)

	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "sweatfile"), []byte("[hooks]\nstop = \"true\""), 0o644)

	input, _ := json.Marshal(map[string]any{
		"hook_event_name": "Stop",
		"session_id":      "success-test-session",
		"cwd":             cwd,
	})

	var out bytes.Buffer
	err := Run(bytes.NewReader(input), &out, "", "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Len() != 0 {
		t.Errorf("expected no output for passing stop-hook, got %q", out.String())
	}

	// No sentinel should exist on success
	sentinel := filepath.Join(tmpDir, "stop-hook-success-test-session")
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Error("expected no sentinel file for successful stop-hook")
	}
}
