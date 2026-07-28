package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/spawnhandshake"
	"code.linenisgreat.com/spinclass/internal/testfs"
	"code.linenisgreat.com/spinclass/internal/testgit"
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

func makeInputWithAgentID(toolName string, toolInput map[string]any, cwd, agentID string) []byte {
	input := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       toolName,
		"tool_input":      toolInput,
		"cwd":             cwd,
		"agent_id":        agentID,
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
	testfs.MustWriteFile(t, target, []byte("package main"), 0o644)
	link := filepath.Join(worktreeCwd, "link.go")
	testfs.MustSymlink(t, target, link)
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
	testfs.MustMkdirAll(t, subdir, 0o755)
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
	testfs.MustMkdirAll(t, sessionWorktree, 0o755)
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
	testfs.MustMkdirAll(t, sessionWorktree, 0o755)
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
	testfs.MustMkdirAll(t, sessionWorktree, 0o755)
	testfs.MustMkdirAll(t, otherWorktree, 0o755)
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
	testfs.MustMkdirAll(t, sessionWorktree, 0o755)
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
	testfs.MustMkdirAll(t, sessionWorktree, 0o755)
	target := filepath.Join(mainRepo, "main.go")
	input := makeInput("Read", map[string]any{"file_path": target}, sessionWorktree)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, sessionWorktree, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]any
	testfs.MustUnmarshal(t, stdout.Bytes(), &result)
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
	testfs.MustUnmarshal(t, stdout.Bytes(), &result)
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
	testfs.MustUnmarshal(t, stdout.Bytes(), &result)
	reason := result["hookSpecificOutput"].(map[string]any)["permissionDecisionReason"].(string)
	if !strings.Contains(reason, "just test") {
		t.Errorf("expected suggestion to contain 'just test', got %q", reason)
	}
}

func TestBashCdToMainWorktreeSubdir(t *testing.T) {
	mainRepo := t.TempDir()
	worktreeCwd := t.TempDir()
	subdir := filepath.Join(mainRepo, "src")
	testfs.MustMkdirAll(t, subdir, 0o755)
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
	testfs.MustUnmarshal(t, stdout.Bytes(), &result)
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
	testfs.MustUnmarshal(t, stdout.Bytes(), &result)
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
	testfs.MustWriteFile(t, filepath.Join(cwd, "sweatfile"), []byte("[hooks]\nstop = \"false\""), 0o644)

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
	testfs.MustUnmarshal(t, out.Bytes(), &result)
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
	testfs.MustWriteFile(t, filepath.Join(cwd, "sweatfile"), []byte("[hooks]\nstop = \"false\""), 0o644)

	// Create sentinel file (simulating first invocation already happened)
	sentinel := filepath.Join(tmpDir, "stop-hook-approve-test-session")
	testfs.MustWriteFile(t, sentinel, []byte("previous failure output"), 0o644)

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
	testfs.MustMkdirAll(t, subdir, 0o755)

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
	testfs.MustWriteFile(t, filepath.Join(cwd, "sweatfile"), []byte("[hooks]\nstop = \"true\""), 0o644)

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

func parseHookDecision(t *testing.T, output []byte) (decision, reason string) {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("expected valid JSON, got %q: %v", string(output), err)
	}
	hso, ok := result["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatal("expected hookSpecificOutput in output")
	}
	d, _ := hso["permissionDecision"].(string)
	r, _ := hso["permissionDecisionReason"].(string)
	return d, r
}

func TestWriteSweatfileAsksConfirmation(t *testing.T) {
	worktree := t.TempDir()
	target := filepath.Join(worktree, "sweatfile")
	input := makeInput("Write", map[string]any{"file_path": target}, worktree)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, "", worktree, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected ask output for sweatfile write")
	}
	decision, reason := parseHookDecision(t, stdout.Bytes())
	if decision != "ask" {
		t.Errorf("expected permissionDecision ask, got %q", decision)
	}
	if !strings.Contains(reason, "sweatfile") {
		t.Errorf("expected reason to mention sweatfile, got %q", reason)
	}
}

func TestEditSweatfileAsksConfirmation(t *testing.T) {
	mainRepo := t.TempDir()
	worktree := t.TempDir()
	target := filepath.Join(mainRepo, "sweatfile")
	input := makeInput("Edit", map[string]any{"file_path": target}, worktree)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, worktree, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected ask output for sweatfile edit")
	}
	decision, _ := parseHookDecision(t, stdout.Bytes())
	if decision != "ask" {
		t.Errorf("expected permissionDecision ask, got %q", decision)
	}
}

func TestWriteNonSweatfileAllowed(t *testing.T) {
	worktree := t.TempDir()
	target := filepath.Join(worktree, "main.go")
	input := makeInput("Write", map[string]any{"file_path": target}, worktree)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, "", worktree, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output for non-sweatfile write, got %q", stdout.String())
	}
}

func TestReadSweatfileAllowed(t *testing.T) {
	worktree := t.TempDir()
	target := filepath.Join(worktree, "sweatfile")
	input := makeInput("Read", map[string]any{"file_path": target}, worktree)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, "", worktree, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output for sweatfile read, got %q", stdout.String())
	}
}

func TestReadSpinclassDirDenied(t *testing.T) {
	worktree := t.TempDir()
	target := filepath.Join(worktree, ".spinclass", "system_prompt_append.d", "1-base.md")
	input := makeInput("Read", map[string]any{"file_path": target}, worktree)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, "", worktree, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected deny output for .spinclass dir read")
	}
	decision, reason := parseHookDecision(t, stdout.Bytes())
	if decision != "deny" {
		t.Errorf("expected permissionDecision deny, got %q", decision)
	}
	if !strings.Contains(reason, ".spinclass") {
		t.Errorf("expected reason to mention .spinclass, got %q", reason)
	}
}

func TestWriteSpinclassDirDenied(t *testing.T) {
	worktree := t.TempDir()
	target := filepath.Join(worktree, ".spinclass", "env")
	input := makeInput("Write", map[string]any{"file_path": target}, worktree)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, "", worktree, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected deny output for .spinclass dir write")
	}
	decision, _ := parseHookDecision(t, stdout.Bytes())
	if decision != "deny" {
		t.Errorf("expected permissionDecision deny, got %q", decision)
	}
}

func TestMergeThisSessionAllowedWhenPreMergeHookSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := filepath.Join(home, "cwd")
	testfs.MustMkdirAll(t, cwd, 0o755)
	testfs.MustWriteFile(t, filepath.Join(cwd, "sweatfile"),
		[]byte("[hooks]\npre-merge = \"just test\""), 0o644)

	input := makeInput("mcp__plugin_spinclass_spinclass__merge-this-session", map[string]any{}, cwd)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected allow output when pre-merge hook is set")
	}
	decision, reason := parseHookDecision(t, stdout.Bytes())
	if decision != "allow" {
		t.Errorf("expected permissionDecision allow, got %q", decision)
	}
	if !strings.Contains(reason, "pre-merge") {
		t.Errorf("expected reason to mention pre-merge, got %q", reason)
	}
}

func TestMergeThisSessionFallsThroughWhenNoPreMergeHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// cwd under home so the sweatfile parent walk stops at $HOME and does
	// not reach any sweatfile that happens to exist higher up the tree
	// (e.g. when TMPDIR points inside this repo).
	cwd := filepath.Join(home, "cwd")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}

	input := makeInput("mcp__plugin_spinclass_spinclass__merge-this-session", map[string]any{}, cwd)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output without pre-merge hook, got %q", stdout.String())
	}
}

func TestMergeThisSessionFallsThroughWhenPreMergeHookEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := filepath.Join(home, "cwd")
	testfs.MustMkdirAll(t, cwd, 0o755)
	testfs.MustWriteFile(t, filepath.Join(cwd, "sweatfile"),
		[]byte("[hooks]\npre-merge = \"\""), 0o644)

	input := makeInput("mcp__plugin_spinclass_spinclass__merge-this-session", map[string]any{}, cwd)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output when pre-merge hook is empty string, got %q", stdout.String())
	}
}

func TestCheckThisSessionAllowedWhenPreMergeHookSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := filepath.Join(home, "cwd")
	testfs.MustMkdirAll(t, cwd, 0o755)
	testfs.MustWriteFile(t, filepath.Join(cwd, "sweatfile"),
		[]byte("[hooks]\npre-merge = \"just test\"\ndisable-merge = true"), 0o644)

	input := makeInput("mcp__plugin_spinclass_spinclass__check-this-session", map[string]any{}, cwd)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected allow output when pre-merge hook is set")
	}
	decision, reason := parseHookDecision(t, stdout.Bytes())
	if decision != "allow" {
		t.Errorf("expected permissionDecision allow, got %q", decision)
	}
	if !strings.Contains(reason, "pre-merge") {
		t.Errorf("expected reason to mention pre-merge, got %q", reason)
	}
}

func TestCheckThisSessionFallsThroughWhenNoPreMergeHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// cwd under home so the sweatfile parent walk stops at $HOME and does
	// not reach any sweatfile that happens to exist higher up the tree
	// (e.g. when TMPDIR points inside this repo).
	cwd := filepath.Join(home, "cwd")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}

	input := makeInput("mcp__plugin_spinclass_spinclass__check-this-session", map[string]any{}, cwd)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output without pre-merge hook, got %q", stdout.String())
	}
}

func TestCheckThisSessionFallsThroughWhenPreMergeHookEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := filepath.Join(home, "cwd")
	testfs.MustMkdirAll(t, cwd, 0o755)
	testfs.MustWriteFile(t, filepath.Join(cwd, "sweatfile"),
		[]byte("[hooks]\npre-merge = \"\"\ndisable-merge = true"), 0o644)

	input := makeInput("mcp__plugin_spinclass_spinclass__check-this-session", map[string]any{}, cwd)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output when pre-merge hook is empty string, got %q", stdout.String())
	}
}

func TestNothingButTheTruthAllowedWhenPreMergeSkillsSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := filepath.Join(home, "cwd")
	testfs.MustMkdirAll(t, cwd, 0o755)
	testfs.MustWriteFile(t, filepath.Join(cwd, "sweatfile"),
		[]byte("[[pre-merge-skills]]\nname = \"eng:code-reviewer\"\nrationale = \"Required.\""), 0o644)

	input := makeInput("mcp__plugin_spinclass_spinclass__nothing-but-the-truth", map[string]any{}, cwd)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected allow output when pre-merge-skills is non-empty")
	}
	decision, reason := parseHookDecision(t, stdout.Bytes())
	if decision != "allow" {
		t.Errorf("expected permissionDecision allow, got %q", decision)
	}
	if !strings.Contains(reason, "pre-merge-skills") {
		t.Errorf("expected reason to mention pre-merge-skills, got %q", reason)
	}
}

func TestNothingButTheTruthFallsThroughWhenNoSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := filepath.Join(home, "cwd")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}

	input := makeInput("mcp__plugin_spinclass_spinclass__nothing-but-the-truth", map[string]any{}, cwd)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output without pre-merge-skills, got %q", stdout.String())
	}
}

func TestNothingButTheTruthFallsThroughWhenSkillsAreOnlySentinels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := filepath.Join(home, "cwd")
	testfs.MustMkdirAll(t, cwd, 0o755)
	testfs.MustWriteFile(t, filepath.Join(cwd, "sweatfile"),
		[]byte("[[pre-merge-skills]]\nname = \"removed\""), 0o644)

	input := makeInput("mcp__plugin_spinclass_spinclass__nothing-but-the-truth", map[string]any{}, cwd)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output when only sentinel entries are present, got %q", stdout.String())
	}
}

func TestSubagentDeniedMergeThisSession(t *testing.T) {
	cwd := t.TempDir()
	input := makeInputWithAgentID("mcp__plugin_spinclass_spinclass__merge-this-session", map[string]any{}, cwd, "agent-abc123")
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decision, _ := parseHookDecision(t, stdout.Bytes())
	if decision != "deny" {
		t.Errorf("expected deny for subagent calling merge-this-session, got %q", decision)
	}
}

func TestSubagentDeniedCheckThisSession(t *testing.T) {
	cwd := t.TempDir()
	input := makeInputWithAgentID("mcp__plugin_spinclass_spinclass__check-this-session", map[string]any{}, cwd, "agent-abc123")
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decision, _ := parseHookDecision(t, stdout.Bytes())
	if decision != "deny" {
		t.Errorf("expected deny for subagent calling check-this-session, got %q", decision)
	}
}

func TestSubagentDeniedNothingButTheTruth(t *testing.T) {
	cwd := t.TempDir()
	input := makeInputWithAgentID("mcp__plugin_spinclass_spinclass__nothing-but-the-truth", map[string]any{}, cwd, "agent-abc123")
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decision, _ := parseHookDecision(t, stdout.Bytes())
	if decision != "deny" {
		t.Errorf("expected deny for subagent calling nothing-but-the-truth, got %q", decision)
	}
}

func TestMainAgentAllowedMergeThisSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := filepath.Join(home, "cwd")
	testfs.MustMkdirAll(t, cwd, 0o755)
	testfs.MustWriteFile(t, filepath.Join(cwd, "sweatfile"),
		[]byte("[hooks]\npre-merge = \"just test\""), 0o644)

	input := makeInput("mcp__plugin_spinclass_spinclass__merge-this-session", map[string]any{}, cwd)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decision, _ := parseHookDecision(t, stdout.Bytes())
	if decision != "allow" {
		t.Errorf("expected allow for main agent calling merge-this-session, got %q", decision)
	}
}

func TestEditSpinclassDirDenied(t *testing.T) {
	mainRepo := t.TempDir()
	worktree := t.TempDir()
	target := filepath.Join(mainRepo, ".spinclass", "config.json")
	input := makeInput("Edit", map[string]any{"file_path": target}, worktree)
	var stdout bytes.Buffer
	err := Run(bytes.NewReader(input), &stdout, mainRepo, worktree, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected deny output for .spinclass dir edit")
	}
	decision, _ := parseHookDecision(t, stdout.Bytes())
	if decision != "deny" {
		t.Errorf("expected permissionDecision deny, got %q", decision)
	}
}

func TestListToolAutoApproved(t *testing.T) {
	cwd := t.TempDir()
	input := makeInput("mcp__plugin_spinclass_spinclass__list", map[string]any{}, cwd)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected allow output for list tool")
	}
	decision, reason := parseHookDecision(t, stdout.Bytes())
	if decision != "allow" {
		t.Errorf("expected permissionDecision allow for list, got %q", decision)
	}
	if reason == "" {
		t.Error("expected a permissionDecisionReason")
	}
}

func TestUpdateDescriptionToolAutoApproved(t *testing.T) {
	cwd := t.TempDir()
	input := makeInput("mcp__plugin_spinclass_spinclass__update-this-session-description", map[string]any{}, cwd)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected allow output for update-this-session-description tool")
	}
	decision, _ := parseHookDecision(t, stdout.Bytes())
	if decision != "allow" {
		t.Errorf("expected permissionDecision allow for update-this-session-description, got %q", decision)
	}
}

func TestValidateToolAutoApproved(t *testing.T) {
	cwd := t.TempDir()
	input := makeInput("mcp__plugin_spinclass_spinclass__validate", map[string]any{}, cwd)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected allow output for validate tool")
	}
	decision, _ := parseHookDecision(t, stdout.Bytes())
	if decision != "allow" {
		t.Errorf("expected permissionDecision allow for validate, got %q", decision)
	}
}

func TestJobWaitToolAutoApproved(t *testing.T) {
	cwd := t.TempDir()
	input := makeInput("mcp__plugin_spinclass_spinclass__session-job-wait", map[string]any{}, cwd)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected allow output for session-job-wait tool")
	}
	decision, _ := parseHookDecision(t, stdout.Bytes())
	if decision != "allow" {
		t.Errorf("expected permissionDecision allow for session-job-wait, got %q", decision)
	}
}

// spawn-session / fork-session are always-ask: an `ask` decision forces a
// prompt regardless of any allow-list, so no spinclass-reachable config can
// make these token-consuming worker launches run silently (#151).
func TestSpawnSessionAsksConfirmation(t *testing.T) {
	cwd := t.TempDir()
	input := makeInput("mcp__plugin_spinclass_spinclass__spawn-session", map[string]any{}, cwd)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decision, reason := parseHookDecision(t, stdout.Bytes())
	if decision != "ask" {
		t.Errorf("expected permissionDecision ask for spawn-session, got %q", decision)
	}
	if reason == "" {
		t.Error("expected a permissionDecisionReason")
	}
}

func TestForkSessionAsksConfirmation(t *testing.T) {
	cwd := t.TempDir()
	input := makeInput("mcp__plugin_spinclass_spinclass__fork-session", map[string]any{}, cwd)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decision, _ := parseHookDecision(t, stdout.Bytes())
	if decision != "ask" {
		t.Errorf("expected permissionDecision ask for fork-session, got %q", decision)
	}
}

// The always-ask floor applies to subagents too: the subagent-deny block does
// not name spawn/fork, so the main switch's ask still fires.
func TestSpawnSessionAsksEvenForSubagent(t *testing.T) {
	cwd := t.TempDir()
	input := makeInputWithAgentID("mcp__plugin_spinclass_spinclass__spawn-session", map[string]any{}, cwd, "agent-abc123")
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decision, _ := parseHookDecision(t, stdout.Bytes())
	if decision != "ask" {
		t.Errorf("expected permissionDecision ask for subagent spawn-session, got %q", decision)
	}
}

// These benign session-management tools are not in the subagent deny guard,
// so a subagent should be auto-approved too.
func TestSubagentAllowedUpdateDescription(t *testing.T) {
	cwd := t.TempDir()
	input := makeInputWithAgentID(
		"mcp__plugin_spinclass_spinclass__update-this-session-description",
		map[string]any{}, cwd, "agent-abc123",
	)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decision, _ := parseHookDecision(t, stdout.Bytes())
	if decision != "allow" {
		t.Errorf("expected allow for subagent update-this-session-description, got %q", decision)
	}
}

// close-child-session splits on its `force` argument (#249): reaping a worker
// that finished cleanly is routine and should never interrupt the agent, but
// forcing discards the child's uncommitted changes and unmerged commits and is
// a human's call. Exercised end to end through Run, not just perms.AlwaysAsk,
// because the ordering matters — the ask check has to run BEFORE the
// auto-approve switch that now also lists this tool, or force would be
// swallowed by it.
func TestCloseChildSessionAsksOnlyWhenForcing(t *testing.T) {
	tests := []struct {
		name         string
		toolInput    map[string]any
		wantDecision string
	}{
		{"clean reap", map[string]any{"child": "repo/branch"}, "allow"},
		{"explicit force=false", map[string]any{"child": "repo/branch", "force": false}, "allow"},
		{"force=true", map[string]any{"child": "repo/branch", "force": true}, "ask"},
		// Fails closed: a non-boolean force must not slip through the
		// auto-approve path just because a type assertion returned false.
		{"non-boolean force", map[string]any{"child": "repo/branch", "force": "true"}, "ask"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwd := t.TempDir()
			input := makeInput(
				"mcp__plugin_spinclass_spinclass__close-child-session", tt.toolInput, cwd,
			)
			var stdout bytes.Buffer
			if err := Run(bytes.NewReader(input), &stdout, "", cwd, false); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			decision, reason := parseHookDecision(t, stdout.Bytes())
			if decision != tt.wantDecision {
				t.Fatalf("expected %q for %v, got %q", tt.wantDecision, tt.toolInput, decision)
			}
			if decision == "ask" && reason == "" {
				t.Error("an ask must explain what the human is being asked to approve")
			}
		})
	}
}

func parseRewrite(t *testing.T, output []byte) (decision string, updatedInput map[string]any, sysMsg string) {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("expected valid JSON, got %q: %v", string(output), err)
	}
	hso, ok := result["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("expected hookSpecificOutput in output, got %q", string(output))
	}
	decision, _ = hso["permissionDecision"].(string)
	updatedInput, _ = hso["updatedInput"].(map[string]any)
	sysMsg, _ = result["systemMessage"].(string)
	return decision, updatedInput, sysMsg
}

func TestRewritePathString(t *testing.T) {
	main := resolvePath(t.TempDir())
	worktree := resolvePath(t.TempDir())
	cases := []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		{"under main", filepath.Join(main, "foo.go"), filepath.Join(worktree, "foo.go"), true},
		{"nested under main", filepath.Join(main, "a", "b", "c.go"), filepath.Join(worktree, "a", "b", "c.go"), true},
		{"main root itself", main, worktree, true},
		{"already in worktree", filepath.Join(worktree, "x.go"), filepath.Join(worktree, "x.go"), false},
		{"outside both", filepath.Join(resolvePath(t.TempDir()), "z.go"), "", false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := rewritePathString(tc.in, main, worktree)
			if changed != tc.changed {
				t.Fatalf("changed = %v, want %v (got %q)", changed, tc.changed, got)
			}
			if tc.changed && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if !tc.changed && got != tc.in {
				t.Errorf("unchanged value mutated: got %q, want %q", got, tc.in)
			}
		})
	}
}

func TestRewritePathStringExemptsWorktreeSpaceAndSpinclass(t *testing.T) {
	main := resolvePath(t.TempDir())
	worktree := filepath.Join(main, ".worktrees", "me")
	testfs.MustMkdirAll(t, worktree, 0o755)

	if _, c := rewritePathString(filepath.Join(main, ".worktrees", "other", "x.go"), main, worktree); c {
		t.Error("a sibling worktree path must not be rewritten")
	}
	if _, c := rewritePathString(filepath.Join(main, ".spinclass", "env"), main, worktree); c {
		t.Error("a .spinclass path must not be rewritten")
	}
	if _, c := rewritePathString(filepath.Join(worktree, "x.go"), main, worktree); c {
		t.Error("the session worktree's own path must not be rewritten")
	}
	got, c := rewritePathString(filepath.Join(main, "foo.go"), main, worktree)
	if !c || got != filepath.Join(worktree, "foo.go") {
		t.Errorf("a parent-checkout file should rewrite into the worktree: got %q changed=%v", got, c)
	}
}

func TestRewriteWriteRedirectsIntoWorktree(t *testing.T) {
	main := t.TempDir()
	worktree := t.TempDir()
	input := makeInput("Write", map[string]any{"file_path": filepath.Join(main, "foo.go")}, worktree)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, main, worktree, false, WithWorktreePathRewrite(true)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("expected rewrite output for a parent-checkout write")
	}
	decision, updated, sysMsg := parseRewrite(t, stdout.Bytes())
	if decision != "allow" {
		t.Errorf("expected allow, got %q", decision)
	}
	want := filepath.Join(resolvePath(worktree), "foo.go")
	if updated["file_path"] != want {
		t.Errorf("expected file_path %q, got %v", want, updated["file_path"])
	}
	if sysMsg == "" {
		t.Error("expected a systemMessage surfacing the rewrite")
	}
}

func TestRewriteReadRedirectsIntoWorktree(t *testing.T) {
	main := t.TempDir()
	worktree := t.TempDir()
	input := makeInput("Read", map[string]any{"file_path": filepath.Join(main, "bar.go")}, worktree)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, main, worktree, false, WithWorktreePathRewrite(true)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decision, updated, _ := parseRewrite(t, stdout.Bytes())
	if decision != "allow" {
		t.Errorf("expected allow, got %q", decision)
	}
	if want := filepath.Join(resolvePath(worktree), "bar.go"); updated["file_path"] != want {
		t.Errorf("expected file_path %q, got %v", want, updated["file_path"])
	}
}

func TestRewriteMoxyFolioWritePreservesOtherArgs(t *testing.T) {
	main := t.TempDir()
	worktree := t.TempDir()
	input := makeInput("mcp__plugin_moxy_moxy__folio_write",
		map[string]any{"file_path": filepath.Join(main, "x.go"), "content": "hi"}, worktree)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, main, worktree, false, WithWorktreePathRewrite(true)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, updated, _ := parseRewrite(t, stdout.Bytes())
	if want := filepath.Join(resolvePath(worktree), "x.go"); updated["file_path"] != want {
		t.Errorf("expected file_path %q, got %v", want, updated["file_path"])
	}
	if updated["content"] != "hi" {
		t.Errorf("expected content preserved, got %v", updated["content"])
	}
}

func TestRewriteGetHubbedCopyDestPath(t *testing.T) {
	main := t.TempDir()
	worktree := t.TempDir()
	input := makeInput("mcp__plugin_moxy_moxy__get-hubbed_copy-file",
		map[string]any{"src_path": "Formula/x.rb", "dest_path": filepath.Join(main, "out.rb")}, worktree)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, main, worktree, false, WithWorktreePathRewrite(true)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, updated, _ := parseRewrite(t, stdout.Bytes())
	if want := filepath.Join(resolvePath(worktree), "out.rb"); updated["dest_path"] != want {
		t.Errorf("expected dest_path %q, got %v", want, updated["dest_path"])
	}
	if updated["src_path"] != "Formula/x.rb" {
		t.Errorf("expected in-repo src_path untouched, got %v", updated["src_path"])
	}
}

func TestRewriteBashCommand(t *testing.T) {
	main := t.TempDir()
	worktree := t.TempDir()
	target := filepath.Join(main, "src", "main.go")
	input := makeInput("Bash", map[string]any{"command": "cat " + target}, worktree)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, main, worktree, false, WithWorktreePathRewrite(true)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decision, updated, _ := parseRewrite(t, stdout.Bytes())
	if decision != "allow" {
		t.Errorf("expected allow, got %q", decision)
	}
	want := "cat " + filepath.Join(resolvePath(worktree), "src", "main.go")
	if updated["command"] != want {
		t.Errorf("expected command %q, got %v", want, updated["command"])
	}
}

func TestRewriteLeavesWorktreePathUntouched(t *testing.T) {
	main := t.TempDir()
	worktree := t.TempDir()
	input := makeInput("Write", map[string]any{"file_path": filepath.Join(worktree, "ok.go")}, worktree)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, main, worktree, false, WithWorktreePathRewrite(true)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output for an in-worktree path, got %q", stdout.String())
	}
}

func TestRewriteSpinclassDirStillDenied(t *testing.T) {
	main := t.TempDir()
	worktree := t.TempDir()
	target := filepath.Join(main, ".spinclass", "env")
	input := makeInput("Write", map[string]any{"file_path": target}, worktree)
	var stdout bytes.Buffer
	if err := Run(bytes.NewReader(input), &stdout, main, worktree, false, WithWorktreePathRewrite(true)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decision, _ := parseHookDecision(t, stdout.Bytes())
	if decision != "deny" {
		t.Errorf("expected .spinclass deny to win over rewrite, got %q", decision)
	}
}

func TestRewriteNoOpForImplicitSession(t *testing.T) {
	worktree := t.TempDir()
	otherMain := t.TempDir()
	input := makeInput("Write", map[string]any{"file_path": filepath.Join(otherMain, "foo.go")}, worktree)
	var stdout bytes.Buffer
	// Implicit session: mainRepoRoot == "" — rewrite must not fire.
	if err := Run(bytes.NewReader(input), &stdout, "", worktree, false, WithWorktreePathRewrite(true)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output for an implicit session, got %q", stdout.String())
	}
}

func TestRewriteDisabledFallsThrough(t *testing.T) {
	main := t.TempDir()
	worktree := t.TempDir()
	input := makeInput("Write", map[string]any{"file_path": filepath.Join(main, "foo.go")}, worktree)
	var stdout bytes.Buffer
	// Rewrite explicitly disabled + disallow-main-worktree off: nothing fires.
	if err := Run(bytes.NewReader(input), &stdout, main, worktree, false, WithWorktreePathRewrite(false)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no output when rewrite disabled and disallow off, got %q", stdout.String())
	}
}

func TestRewriteWinsOverDisallowMainWorktree(t *testing.T) {
	main := t.TempDir()
	worktree := t.TempDir()
	input := makeInput("Write", map[string]any{"file_path": filepath.Join(main, "foo.go")}, worktree)
	var stdout bytes.Buffer
	// Both rewrite and disallow-main-worktree on: rewrite runs first and wins.
	if err := Run(bytes.NewReader(input), &stdout, main, worktree, true, WithWorktreePathRewrite(true)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decision, updated, _ := parseRewrite(t, stdout.Bytes())
	if decision != "allow" {
		t.Errorf("expected rewrite (allow) to win over disallow deny, got %q", decision)
	}
	if want := filepath.Join(resolvePath(worktree), "foo.go"); updated["file_path"] != want {
		t.Errorf("expected file_path %q, got %v", want, updated["file_path"])
	}
}

// gitRun execs a git command in dir and fails the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// initImplicitTestRepo creates a git repo on the "master" branch in a fresh
// temp dir (a deliberate main checkout, NOT a linked worktree). The returned
// path is symlink-resolved (filepath.EvalSymlinks) so it matches what
// gitToplevel returns from `git rev-parse --show-toplevel` (git canonicalizes
// the path). Without this, runSessionStart's Gate 2 — which compares the hook's
// cwd against gitToplevel(cwd) — bails on platforms where t.TempDir() sits under
// a symlink: macOS /var/folders/... is a symlink to /private/var/..., so the raw
// temp path != the resolved toplevel. Inside a spinclass session TMPDIR points at
// the worktree-local .tmp/ (no symlink) and the two already agree; resolving here
// makes the test pass in both environments. EvalSymlinks is a no-op when there is
// no symlink to resolve.
func initImplicitTestRepo(t *testing.T) string {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init")
	gitRun(t, repo, "config", "user.email", "test@example.com")
	gitRun(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-m", "initial")
	gitRun(t, repo, "branch", "-m", "master")
	return repo
}

// initImplicitTestWorktree creates a repo and a linked worktree off it, then
// returns the worktree path (a .git FILE, so worktree.IsWorktree reports true).
func initImplicitTestWorktree(t *testing.T) string {
	t.Helper()
	repo := initImplicitTestRepo(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(base, "wt")
	gitRun(t, repo, "worktree", "add", "-b", "feature", wt)
	return wt
}

func TestSessionStartMaterializesImplicit(t *testing.T) {
	repo := initImplicitTestRepo(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	input, _ := json.Marshal(map[string]any{
		"hook_event_name": "SessionStart",
		"session_id":      "abc123def456",
		"cwd":             repo,
		"source":          "startup",
	})
	var out bytes.Buffer
	if err := Run(bytes.NewReader(input), &out, "", "", false); err != nil {
		t.Fatal(err)
	}

	rand := implicitRand("abc123def456")
	statePath := filepath.Join(repo, ".spinclass", "state-"+rand+".json")
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("implicit session not materialized: %v", err)
	}
	data, _ := os.ReadFile(statePath)
	var st session.State
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if st.Kind != session.KindImplicit {
		t.Errorf("kind = %q, want implicit", st.Kind)
	}
	if st.Branch != "master" {
		t.Errorf("branch hint = %q, want master", st.Branch)
	}
	// Key is <repo>/<rand> — the branch ("master") is a hint, not part of it.
	wantKey := filepath.Base(repo) + "/" + rand
	if st.SessionKey != wantKey {
		t.Errorf("session_key = %q, want %q", st.SessionKey, wantKey)
	}
}

// TestSessionStartMaterializesOnNonDefaultBranch guards the fix for the
// over-restrictive default-branch gate: a main checkout (.git is a directory)
// is a first-class implicit session on ANY branch, not just main/master. The
// discriminator is .git-is-a-directory (Gate 1), never the current branch. It
// also pins the key shape: the slash-bearing branch "feature/wip" is captured
// as a display hint in State.Branch but NEVER leaks into the key, which stays
// <repo>/<rand> (a single slash, from <repo>/).
func TestSessionStartMaterializesOnNonDefaultBranch(t *testing.T) {
	repo := initImplicitTestRepo(t)
	// Move the MAIN checkout (not a worktree) onto a non-default branch whose
	// name contains a slash — the case that would corrupt a <branch>-in-key.
	gitRun(t, repo, "checkout", "-b", "feature/wip")
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	input, _ := json.Marshal(map[string]any{
		"hook_event_name": "SessionStart",
		"session_id":      "nondefaultbranch",
		"cwd":             repo,
		"source":          "startup",
	})
	if err := Run(bytes.NewReader(input), &bytes.Buffer{}, "", "", false); err != nil {
		t.Fatal(err)
	}

	rand := implicitRand("nondefaultbranch")
	statePath := filepath.Join(repo, ".spinclass", "state-"+rand+".json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("implicit session not materialized on non-default branch: %v", err)
	}
	var st session.State
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	// Branch is captured as a hint, slash and all.
	if st.Branch != "feature/wip" {
		t.Errorf("branch hint = %q, want feature/wip", st.Branch)
	}
	// ...but the key is branch-free: <repo>/<rand>, exactly one slash.
	wantKey := filepath.Base(repo) + "/" + rand
	if st.SessionKey != wantKey {
		t.Errorf("session_key = %q, want %q", st.SessionKey, wantKey)
	}
	if strings.Count(st.SessionKey, "/") != 1 {
		t.Errorf("session_key %q must contain exactly one slash (branch must not leak in)", st.SessionKey)
	}
}

// TestSessionStartMaterializesUnderSymlinkedCwd guards the fix for the latent
// Gate 2 fragility (amarbel-llc/eng#168): the gate compares the raw hook cwd
// against gitToplevel(cwd), and `git rev-parse --show-toplevel` canonicalizes
// symlinks while filepath.Clean does not. So when the checkout sits under a
// symlinked path (symlinked $HOME/TMPDIR, macOS /var -> /private/var) the two
// sides disagreed and no implicit session materialized. Here we drive the hook
// with a symlink that points at the real checkout as cwd and assert a session
// IS created. Fails before resolving both sides; passes after.
func TestSessionStartMaterializesUnderSymlinkedCwd(t *testing.T) {
	repo := initImplicitTestRepo(t)
	// Symlink in a separate (resolved) temp dir, pointing at the real checkout.
	linkBase, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	symlinked := filepath.Join(linkBase, "checkout-link")
	if err := os.Symlink(repo, symlinked); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	input, _ := json.Marshal(map[string]any{
		"hook_event_name": "SessionStart",
		"session_id":      "symlinkcwd",
		"cwd":             symlinked,
		"source":          "startup",
	})
	if err := Run(bytes.NewReader(input), &bytes.Buffer{}, "", "", false); err != nil {
		t.Fatal(err)
	}

	// The state file lands in the REAL checkout (.spinclass follows the symlink),
	// so assert via the resolved repo path.
	rand := implicitRand("symlinkcwd")
	statePath := filepath.Join(repo, ".spinclass", "state-"+rand+".json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("implicit session not materialized under symlinked cwd: %v", err)
	}
	var st session.State
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	// The key uses the CANONICAL repo basename, never the symlink's name.
	wantKey := filepath.Base(repo) + "/" + rand
	if st.SessionKey != wantKey {
		t.Errorf("session_key = %q, want %q", st.SessionKey, wantKey)
	}
}

func TestSessionEndRemovesImplicit(t *testing.T) {
	repo := initImplicitTestRepo(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	// Materialize first via SessionStart.
	startInput, _ := json.Marshal(map[string]any{
		"hook_event_name": "SessionStart", "session_id": "sid999", "cwd": repo, "source": "startup",
	})
	if err := Run(bytes.NewReader(startInput), &bytes.Buffer{}, "", "", false); err != nil {
		t.Fatal(err)
	}
	rand := implicitRand("sid999")
	local := filepath.Join(repo, ".spinclass", "state-"+rand+".json")
	if _, err := os.Stat(local); err != nil {
		t.Fatalf("precondition: state should exist after SessionStart: %v", err)
	}

	// End via SessionEnd.
	endInput, _ := json.Marshal(map[string]any{
		"hook_event_name": "SessionEnd", "session_id": "sid999", "cwd": repo, "reason": "other",
	})
	if err := Run(bytes.NewReader(endInput), &bytes.Buffer{}, "", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(local); !os.IsNotExist(err) {
		t.Fatalf("implicit session not removed on SessionEnd: %v", err)
	}
}

func TestSessionEndNoopWhenNoState(t *testing.T) {
	// SessionEnd for a session_id that never materialized must not error.
	// runSessionEnd needs only a non-empty cwd; RemoveImplicit tolerates a
	// missing state file, so a plain temp dir (no git repo) suffices.
	repo := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	endInput, _ := json.Marshal(map[string]any{
		"hook_event_name": "SessionEnd", "session_id": "never-existed", "cwd": repo, "reason": "other",
	})
	if err := Run(bytes.NewReader(endInput), &bytes.Buffer{}, "", "", false); err != nil {
		t.Fatalf("SessionEnd for a nonexistent session should be a silent no-op, got: %v", err)
	}
}

func TestSessionStartNoopInsideWorktree(t *testing.T) {
	wt := initImplicitTestWorktree(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	input, _ := json.Marshal(map[string]any{
		"hook_event_name": "SessionStart", "session_id": "zzz", "cwd": wt, "source": "startup",
	})
	var out bytes.Buffer
	if err := Run(bytes.NewReader(input), &out, "", "", false); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(wt, ".spinclass", "state-*.json"))
	if len(matches) != 0 {
		t.Fatalf("should not materialize inside a worktree, got %v", matches)
	}
}

// initSpawnedWorktree creates a repo plus an sc-style worktree at
// <repo>/.worktrees/feature (the layout session.Read reconstructs from
// repoPath+branch) and writes a worktree session state with the given
// SpawnedBy. Paths are symlink-resolved for the same reason as
// initImplicitTestRepo: git.CommonDir returns git's canonicalized path.
func initSpawnedWorktree(t *testing.T, spawnedBy string) (repo, wt string) {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	testgit.MustInit(t, repo)
	wt = filepath.Join(repo, ".worktrees", "feature")
	testgit.MustWorktreeAdd(t, repo, wt, "feature")
	st := session.State{
		PID:          0,
		SessionState: session.StateActive,
		RepoPath:     repo,
		WorktreePath: wt,
		Branch:       "feature",
		SessionKey:   "myrepo/feature",
		SpawnedBy:    spawnedBy,
		StartedAt:    time.Now(),
	}
	if err := session.Write(st); err != nil {
		t.Fatalf("session.Write: %v", err)
	}
	return repo, wt
}

// fireSessionStart runs the hook entrypoint with a SessionStart payload.
func fireSessionStart(t *testing.T, cwd string) {
	t.Helper()
	input, _ := json.Marshal(map[string]any{
		"hook_event_name": "SessionStart",
		"session_id":      "spawn-test-session",
		"cwd":             cwd,
		"source":          "startup",
	})
	var out bytes.Buffer
	if err := Run(bytes.NewReader(input), &out, "", "", false); err != nil {
		t.Fatal(err)
	}
}

func TestSessionStartSpawnHello(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo, wt := initSpawnedWorktree(t, "driver/key")

	fireSessionStart(t, wt)

	// The driver's WaitForHello must see the worker's hello, pair-scoped to
	// driver/key ← myrepo/feature, and the hello must carry the worker's
	// session id (fireSessionStart's session_id) so the driver can reattach.
	poshID, err := spawnhandshake.WaitForHello("driver/key", "myrepo/feature", time.Now().Add(-time.Minute), time.Second)
	if err != nil {
		t.Fatalf("expected a spawn hello for the driver: %v", err)
	}
	if poshID != "spawn-test-session" {
		t.Errorf("hello posh session id = %q, want %q (the worker's session_id)", poshID, "spawn-test-session")
	}

	st, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatalf("session.Read: %v", err)
	}
	if st.HelloSentAt == nil {
		t.Error("HelloSentAt not set after hello")
	}
	if st.PID == 0 {
		t.Error("PID not adopted (still 0)")
	}
	if st.SessionState != session.StateActive {
		t.Errorf("state = %q, want active", st.SessionState)
	}
}

func TestSessionStartSpawnHelloDedupes(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo, wt := initSpawnedWorktree(t, "driver/key")

	fireSessionStart(t, wt)
	st1, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatalf("session.Read: %v", err)
	}
	if st1.HelloSentAt == nil {
		t.Fatal("HelloSentAt not set after first SessionStart")
	}

	fireSessionStart(t, wt) // resume/clear/compact re-fire must NOT re-send

	st2, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatalf("session.Read: %v", err)
	}
	// Dedup is the HelloSentAt guard: a re-send would stamp a new HelloSentAt,
	// so an unchanged timestamp proves the second fire did not re-send.
	if st2.HelloSentAt == nil || !st2.HelloSentAt.Equal(*st1.HelloSentAt) {
		t.Errorf("HelloSentAt changed on re-fire (hello re-sent): %v -> %v", st1.HelloSentAt, st2.HelloSentAt)
	}
}

func TestSessionStartWorktreeWithoutSpawnedByNoHello(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo, wt := initSpawnedWorktree(t, "")

	fireSessionStart(t, wt)

	// No SpawnedBy → no hello: the driver's wait must time out.
	if _, err := spawnhandshake.WaitForHello("driver/key", "myrepo/feature", time.Now().Add(-time.Minute), 200*time.Millisecond); err == nil {
		t.Fatal("expected no hello without SpawnedBy")
	}
	st, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatalf("session.Read: %v", err)
	}
	if st.HelloSentAt != nil {
		t.Error("HelloSentAt set on a non-spawned session")
	}
	if st.PID != 0 {
		t.Errorf("state touched: PID = %d, want 0", st.PID)
	}
}

func TestSessionStartSpawnHelloSendFailureLeavesUnmarked(t *testing.T) {
	// Write the state under a working XDG_STATE_HOME, then point
	// XDG_STATE_HOME at a regular FILE so spawnhandshake.SendHello's MkdirAll
	// fails.
	// session.Read/Write key off the worktree-local state.json, so the
	// post-hook state assertions still work.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo, wt := initSpawnedWorktree(t, "driver/key")

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", blocker)

	fireSessionStart(t, wt) // must swallow the send failure

	st, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatalf("session.Read: %v", err)
	}
	if st.HelloSentAt != nil {
		t.Error("HelloSentAt set despite send failure")
	}
	if st.PID != 0 {
		t.Errorf("state adopted despite send failure: PID = %d, want 0", st.PID)
	}
}

func TestSessionStartNoopWhenDisabled(t *testing.T) {
	repo := initImplicitTestRepo(t)
	// Set HOME to an empty temp dir so the sweatfile cascade only sees the
	// repo-level sweatfile we write here (the hierarchy loader walks from
	// $HOME down to cwd; bounding it at an empty HOME isolates the test).
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// LoadHierarchy reads the repo-level config from <repo>/sweatfile.
	if err := os.WriteFile(filepath.Join(repo, "sweatfile"),
		[]byte("[hooks]\ndisable-implicit-sessions = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{
		"hook_event_name": "SessionStart", "session_id": "disabledtest", "cwd": repo, "source": "startup",
	})
	var out bytes.Buffer
	if err := Run(bytes.NewReader(input), &out, "", "", false); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(repo, ".spinclass", "state-*.json"))
	if len(matches) != 0 {
		t.Fatalf("disabled knob should suppress materialization, got %v", matches)
	}
}
