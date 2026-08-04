package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestServeMergeThisSessionStdioIntegrity is the regression test for #27.
//
// It spawns `spinclass serve` in a worktree whose pre-merge hook emits both
// stdout and stderr lines, then drives `tools/call merge-this-session` over
// the MCP stdio transport. The hook output MUST stay off fd 1; otherwise
// the MCP client declares the transport closed on the first non-JSON line
// and every subsequent tool call fails.
//
// The assertion is the strongest possible: every non-empty line the server
// writes to stdout must parse as a JSON-RPC message. If the bug is present,
// the hook's `echo HOOKOUT` lands on stdout before the response and fails
// the parse.
func TestServeMergeThisSessionStdioIntegrity(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell hook not portable to Windows")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}

	bin := buildSpinclassBinary(t)
	repoDir, wtPath := setupWorktreeWithHook(t, "feature-it", "echo HOOKOUT_STDOUT; echo HOOKOUT_STDERR 1>&2")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "serve")
	cmd.Dir = wtPath
	cmd.Env = append(
		os.Environ(),
		// Neutralize clown job-wakeup emits: the dev/CI env runs under clown,
		// and the spawned serve must not wake the developer's real session.
		"CLOWN_BIN=",
		"HOME="+filepath.Dir(repoDir),
		"GIT_CEILING_DIRECTORIES="+filepath.Dir(repoDir),
		"XDG_STATE_HOME="+t.TempDir(),
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start spinclass serve: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	send := func(msg any) {
		b, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := stdin.Write(append(b, '\n')); err != nil {
			t.Fatalf("write %s: %v", b, err)
		}
	}

	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "spinclass-test", "version": "0"},
		},
	})
	send(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "merge-this-session",
			"arguments": map[string]any{"local_only": true}, // keep the test local; push is now the default (#126)
		},
	})

	type rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	sawToolResponse := false
	deadline := time.After(45 * time.Second)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			var msg rpc
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				t.Errorf("non-JSON line on stdout (this is the #27 bug!): %q\nerror: %v", line, err)
				return
			}
			if msg.JSONRPC != "2.0" {
				t.Errorf("bad jsonrpc version on line: %q", line)
				return
			}
			// id=2 is the tools/call response
			if string(msg.ID) == "2" {
				sawToolResponse = true
				return
			}
		}
	}()

	select {
	case <-done:
	case <-deadline:
		t.Fatalf("timeout waiting for tools/call response; stderr:\n%s", stderr.String())
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		t.Errorf("scan error: %v", err)
	}

	if !sawToolResponse {
		t.Errorf("never received response to tools/call (id=2); stderr:\n%s", stderr.String())
	}

	// Stderr should contain the hook's output (it either went there via the
	// safety net's stdout-redirect to stderr, or via the hookWriter being
	// rendered into the tool result — either way, the bytes exist).
	// We don't make this a hard assertion because stdout capture into the
	// tool result is the primary path; stderr is only a catch.
}

// buildSpinclassBinary compiles ./cmd/spinclass into a temp dir and returns
// the path.
func buildSpinclassBinary(t *testing.T) string {
	t.Helper()

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "spinclass")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/spinclass")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// findRepoRoot walks up from the test's working directory until it finds
// go.mod.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above test dir")
		}
		dir = parent
	}
}

// setupWorktreeWithHook creates an isolated git repo with a sweatfile that
// sets the given pre-merge hook command, plus a worktree with a commit ready
// to merge. The branch name is used both for the worktree directory and the
// branch ref.
func setupWorktreeWithHook(t *testing.T, branchName, hookScript string) (repoDir, wtPath string) {
	t.Helper()
	return setupWorktreeWithSweatfile(t, branchName, fmt.Sprintf("[hooks]\npre-merge = %q\n", hookScript))
}

// TestServeCheckThisSession exercises the `check-this-session` MCP tool.
// It spawns `spinclass serve` in a worktree whose pre-merge hook emits the
// marker `CHECK_OUTPUT` on stdout, drives `tools/call check-this-session`
// over the MCP stdio transport, and asserts that the tool returned a
// success result whose textual content includes the marker.
func TestServeCheckThisSession(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell hook not portable to Windows")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}

	bin := buildSpinclassBinary(t)
	sweatfileBody := "[hooks]\npre-merge = \"echo CHECK_OUTPUT\"\ndisable-merge = true\n"
	repoDir, wtPath := setupWorktreeWithSweatfile(t, "feature-check", sweatfileBody)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "serve")
	cmd.Dir = wtPath
	cmd.Env = append(
		os.Environ(),
		// Neutralize clown job-wakeup emits: the dev/CI env runs under clown,
		// and the spawned serve must not wake the developer's real session.
		"CLOWN_BIN=",
		"HOME="+filepath.Dir(repoDir),
		"GIT_CEILING_DIRECTORIES="+filepath.Dir(repoDir),
		"XDG_STATE_HOME="+t.TempDir(),
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start spinclass serve: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	send := func(msg any) {
		b, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := stdin.Write(append(b, '\n')); err != nil {
			t.Fatalf("write %s: %v", b, err)
		}
	}

	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "spinclass-test", "version": "0"},
		},
	})
	send(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "check-this-session",
			"arguments": map[string]any{},
		},
	})

	type contentItem struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type toolResult struct {
		Content []contentItem `json:"content"`
		IsError bool          `json:"isError"`
	}
	type rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var toolResp rpc
	sawToolResponse := false
	deadline := time.After(45 * time.Second)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			var msg rpc
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				t.Errorf("non-JSON line on stdout: %q\nerror: %v", line, err)
				return
			}
			if msg.JSONRPC != "2.0" {
				t.Errorf("bad jsonrpc version on line: %q", line)
				return
			}
			if string(msg.ID) == "2" {
				toolResp = msg
				sawToolResponse = true
				return
			}
		}
	}()

	select {
	case <-done:
	case <-deadline:
		t.Fatalf("timeout waiting for tools/call response; stderr:\n%s", stderr.String())
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		t.Errorf("scan error: %v", err)
	}

	if !sawToolResponse {
		t.Fatalf("never received response to tools/call (id=2); stderr:\n%s", stderr.String())
	}

	if len(toolResp.Error) > 0 {
		t.Fatalf("tools/call returned JSON-RPC error: %s; stderr:\n%s", string(toolResp.Error), stderr.String())
	}

	var res toolResult
	if err := json.Unmarshal(toolResp.Result, &res); err != nil {
		t.Fatalf("unmarshal tool result: %v\nresult: %s", err, string(toolResp.Result))
	}

	if res.IsError {
		var combined strings.Builder
		for _, c := range res.Content {
			combined.WriteString(c.Text)
		}
		t.Fatalf("check-this-session returned isError=true; content:\n%s\nstderr:\n%s", combined.String(), stderr.String())
	}

	var combined strings.Builder
	for _, c := range res.Content {
		combined.WriteString(c.Text)
	}
	if !strings.Contains(combined.String(), "CHECK_OUTPUT") {
		t.Errorf("expected tool result to contain %q, got:\n%s", "CHECK_OUTPUT", combined.String())
	}
}

// setupWorktreeWithSweatfile is a sibling of setupWorktreeWithHook that lets
// the caller provide arbitrary sweatfile contents. It creates an isolated git
// repo with the provided sweatfile body at the repo root, plus a worktree
// branched from main with one commit ready to merge.
func setupWorktreeWithSweatfile(t *testing.T, branchName, sweatfileBody string) (repoDir, wtPath string) {
	t.Helper()

	root := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", root)
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gitconfig"))

	repoDir = filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}

	run(repoDir, "init", "-b", "main")
	run(repoDir, "config", "user.email", "test@test")
	run(repoDir, "config", "user.name", "Test")
	run(repoDir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(repoDir, "add", "file.txt")
	run(repoDir, "commit", "-m", "initial")

	if err := os.WriteFile(filepath.Join(repoDir, "sweatfile"), []byte(sweatfileBody), 0o644); err != nil {
		t.Fatal(err)
	}

	wtPath = filepath.Join(repoDir, ".worktrees", branchName)
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		t.Fatal(err)
	}
	run(repoDir, "worktree", "add", "-b", branchName, wtPath)
	if err := os.WriteFile(filepath.Join(wtPath, "new.txt"), []byte("wt content"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(wtPath, "add", "new.txt")
	run(wtPath, "commit", "-m", "worktree commit")

	return repoDir, wtPath
}

// TestServeMergeThisSessionHiddenWhenDisabled spawns serve in a worktree
// whose sweatfile sets [hooks].disable-merge = true, then queries
// tools/list and asserts merge-this-session is absent while
// check-this-session is present.
func TestServeMergeThisSessionHiddenWhenDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell hook not portable to Windows")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}

	bin := buildSpinclassBinary(t)
	sweatfileBody := "[hooks]\npre-merge = \"true\"\ndisable-merge = true\n"
	repoDir, wtPath := setupWorktreeWithSweatfile(t, "feature-disabled", sweatfileBody)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "serve")
	cmd.Dir = wtPath
	cmd.Env = append(
		os.Environ(),
		// Neutralize clown job-wakeup emits: the dev/CI env runs under clown,
		// and the spawned serve must not wake the developer's real session.
		"CLOWN_BIN=",
		"HOME="+filepath.Dir(repoDir),
		"GIT_CEILING_DIRECTORIES="+filepath.Dir(repoDir),
		"XDG_STATE_HOME="+t.TempDir(),
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start spinclass serve: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	send := func(msg any) {
		b, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := stdin.Write(append(b, '\n')); err != nil {
			t.Fatalf("write %s: %v", b, err)
		}
	}

	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "spinclass-test", "version": "0"},
		},
	})
	send(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	})

	type toolEntry struct {
		Name string `json:"name"`
	}
	type toolsListResult struct {
		Tools []toolEntry `json:"tools"`
	}
	type rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var listResp rpc
	sawListResponse := false
	deadline := time.After(45 * time.Second)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			var msg rpc
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				t.Errorf("non-JSON line on stdout: %q\nerror: %v", line, err)
				return
			}
			if msg.JSONRPC != "2.0" {
				t.Errorf("bad jsonrpc version on line: %q", line)
				return
			}
			if string(msg.ID) == "2" {
				listResp = msg
				sawListResponse = true
				return
			}
		}
	}()

	select {
	case <-done:
	case <-deadline:
		t.Fatalf("timeout waiting for tools/list response; stderr:\n%s", stderr.String())
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		t.Errorf("scan error: %v", err)
	}

	if !sawListResponse {
		t.Fatalf("never received response to tools/list (id=2); stderr:\n%s", stderr.String())
	}

	if len(listResp.Error) > 0 {
		t.Fatalf("tools/list returned JSON-RPC error: %s; stderr:\n%s", string(listResp.Error), stderr.String())
	}

	var res toolsListResult
	if err := json.Unmarshal(listResp.Result, &res); err != nil {
		t.Fatalf("unmarshal tools/list result: %v\nresult: %s", err, string(listResp.Result))
	}

	names := make(map[string]bool, len(res.Tools))
	for _, t := range res.Tools {
		names[t.Name] = true
	}

	if names["merge-this-session"] {
		nameList := make([]string, 0, len(res.Tools))
		for _, t := range res.Tools {
			nameList = append(nameList, t.Name)
		}
		t.Errorf("expected merge-this-session to be HIDDEN when [hooks].disable-merge=true, but it was registered. tools: %v", nameList)
	}

	if !names["check-this-session"] {
		nameList := make([]string, 0, len(res.Tools))
		for _, t := range res.Tools {
			nameList = append(nameList, t.Name)
		}
		t.Errorf("expected check-this-session to be PRESENT, but it was missing. tools: %v", nameList)
	}
}

// TestServeCheckThisSessionHiddenByDefault is the symmetric counterpart to
// TestServeMergeThisSessionHiddenWhenDisabled: in a worktree without
// [hooks].disable-merge, merge-this-session must be present and
// check-this-session must be absent.
func TestServeCheckThisSessionHiddenByDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell hook not portable to Windows")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}

	bin := buildSpinclassBinary(t)
	repoDir, wtPath := setupWorktreeWithHook(t, "feature-default", "true")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "serve")
	cmd.Dir = wtPath
	cmd.Env = append(
		os.Environ(),
		// Neutralize clown job-wakeup emits: the dev/CI env runs under clown,
		// and the spawned serve must not wake the developer's real session.
		"CLOWN_BIN=",
		"HOME="+filepath.Dir(repoDir),
		"GIT_CEILING_DIRECTORIES="+filepath.Dir(repoDir),
		"XDG_STATE_HOME="+t.TempDir(),
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start spinclass serve: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	send := func(msg any) {
		b, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := stdin.Write(append(b, '\n')); err != nil {
			t.Fatalf("write %s: %v", b, err)
		}
	}

	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "spinclass-test", "version": "0"},
		},
	})
	send(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	})

	type toolEntry struct {
		Name string `json:"name"`
	}
	type toolsListResult struct {
		Tools []toolEntry `json:"tools"`
	}
	type rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var listResp rpc
	sawListResponse := false
	deadline := time.After(45 * time.Second)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			var msg rpc
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				t.Errorf("non-JSON line on stdout: %q\nerror: %v", line, err)
				return
			}
			if msg.JSONRPC != "2.0" {
				t.Errorf("bad jsonrpc version on line: %q", line)
				return
			}
			if string(msg.ID) == "2" {
				listResp = msg
				sawListResponse = true
				return
			}
		}
	}()

	select {
	case <-done:
	case <-deadline:
		t.Fatalf("timeout waiting for tools/list response; stderr:\n%s", stderr.String())
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		t.Errorf("scan error: %v", err)
	}

	if !sawListResponse {
		t.Fatalf("never received response to tools/list (id=2); stderr:\n%s", stderr.String())
	}

	if len(listResp.Error) > 0 {
		t.Fatalf("tools/list returned JSON-RPC error: %s; stderr:\n%s", string(listResp.Error), stderr.String())
	}

	var res toolsListResult
	if err := json.Unmarshal(listResp.Result, &res); err != nil {
		t.Fatalf("unmarshal tools/list result: %v\nresult: %s", err, string(listResp.Result))
	}

	names := make(map[string]bool, len(res.Tools))
	for _, t := range res.Tools {
		names[t.Name] = true
	}

	if !names["merge-this-session"] {
		nameList := make([]string, 0, len(res.Tools))
		for _, t := range res.Tools {
			nameList = append(nameList, t.Name)
		}
		t.Errorf("expected merge-this-session to be PRESENT when [hooks].disable-merge is unset, but it was missing. tools: %v", nameList)
	}

	if names["check-this-session"] {
		nameList := make([]string, 0, len(res.Tools))
		for _, t := range res.Tools {
			nameList = append(nameList, t.Name)
		}
		t.Errorf("expected check-this-session to be HIDDEN when [hooks].disable-merge is unset, but it was registered. tools: %v", nameList)
	}
}

// TestServeCheckThisSessionAsync exercises the async start+poll flow: it starts
// check-this-session-async (whose hook sleeps then prints CHECK_OUTPUT), expects
// an immediate job-id response, then polls session-job-status until the job
// reports succeeded with the hook output. This proves the long hook never
// blocks the start call and that the result is retrievable via polling.
func TestServeCheckThisSessionAsync(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell hook not portable to Windows")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}

	bin := buildSpinclassBinary(t)
	// A hook that blocks before emitting, so an early status poll catches
	// "running" and a later one catches "succeeded" with the marker.
	sweatfileBody := "[hooks]\npre-merge = \"sleep 2; echo CHECK_OUTPUT\"\ndisable-merge = true\n"
	repoDir, wtPath := setupWorktreeWithSweatfile(t, "feature-async", sweatfileBody)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "serve")
	cmd.Dir = wtPath
	cmd.Env = append(
		os.Environ(),
		// Neutralize clown job-wakeup emits: the dev/CI env runs under clown,
		// and the spawned serve must not wake the developer's real session.
		"CLOWN_BIN=",
		"HOME="+filepath.Dir(repoDir),
		"GIT_CEILING_DIRECTORIES="+filepath.Dir(repoDir),
		"XDG_STATE_HOME="+t.TempDir(),
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start spinclass serve: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	send := func(msg any) {
		b, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := stdin.Write(append(b, '\n')); err != nil {
			t.Fatalf("write %s: %v", b, err)
		}
	}

	type contentItem struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type toolResult struct {
		Content []contentItem `json:"content"`
		IsError bool          `json:"isError"`
	}
	type rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	// readResp blocks reading stdout lines until the response with wantID
	// arrives, skipping notifications / other ids. Fails on EOF.
	readResp := func(wantID string) rpc {
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			var msg rpc
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				t.Fatalf("non-JSON line on stdout: %q\nerror: %v", line, err)
			}
			if string(msg.ID) == wantID {
				return msg
			}
		}
		t.Fatalf("stdout closed before response id=%s; stderr:\n%s", wantID, stderr.String())
		return rpc{}
	}

	text := func(r rpc) (string, bool) {
		var res toolResult
		if err := json.Unmarshal(r.Result, &res); err != nil {
			t.Fatalf("unmarshal tool result: %v\nresult: %s", err, string(r.Result))
		}
		var b strings.Builder
		for _, c := range res.Content {
			b.WriteString(c.Text)
		}
		return b.String(), res.IsError
	}

	send(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "spinclass-test", "version": "0"},
		},
	})
	send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})

	// Start the async check.
	send(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "check-this-session-async", "arguments": map[string]any{}},
	})
	startTxt, startErr := text(readResp("2"))
	if startErr {
		t.Fatalf("async start returned isError; content:\n%s\nstderr:\n%s", startTxt, stderr.String())
	}
	if !strings.Contains(startTxt, "started background check job") {
		t.Fatalf("unexpected start response: %q", startTxt)
	}

	// Poll session-job-status until the job leaves the running state.
	id := 3
	var final string
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": "tools/call",
			"params": map[string]any{"name": "session-job-status", "arguments": map[string]any{}},
		})
		body, _ := text(readResp(strconv.Itoa(id)))
		id++
		if strings.Contains(body, "running") {
			time.Sleep(400 * time.Millisecond)
			continue
		}
		final = body
		break
	}

	if final == "" {
		t.Fatalf("job never left running state in time; stderr:\n%s", stderr.String())
	}
	if !strings.Contains(final, "succeeded") {
		t.Errorf("expected final status to be succeeded, got:\n%s", final)
	}
	if !strings.Contains(final, "CHECK_OUTPUT") {
		t.Errorf("expected final status to contain hook output CHECK_OUTPUT, got:\n%s", final)
	}
}

// TestServeCheckThisSessionAsyncCancel starts an async check whose hook would
// sleep far longer than the test, then cancels it via session-job-cancel and
// asserts the job reports cancelled well before the hook's natural duration
// (proving the hook subprocess was actually killed, not waited out).
func TestServeCheckThisSessionAsyncCancel(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell hook not portable to Windows")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}

	bin := buildSpinclassBinary(t)
	sweatfileBody := "[hooks]\npre-merge = \"sleep 120; echo SHOULD_NOT_FINISH\"\ndisable-merge = true\n"
	repoDir, wtPath := setupWorktreeWithSweatfile(t, "feature-async-cancel", sweatfileBody)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "serve")
	cmd.Dir = wtPath
	cmd.Env = append(
		os.Environ(),
		// Neutralize clown job-wakeup emits: the dev/CI env runs under clown,
		// and the spawned serve must not wake the developer's real session.
		"CLOWN_BIN=",
		"HOME="+filepath.Dir(repoDir),
		"GIT_CEILING_DIRECTORIES="+filepath.Dir(repoDir),
		"XDG_STATE_HOME="+t.TempDir(),
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start spinclass serve: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	send := func(msg any) {
		b, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := stdin.Write(append(b, '\n')); err != nil {
			t.Fatalf("write %s: %v", b, err)
		}
	}

	type contentItem struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type toolResult struct {
		Content []contentItem `json:"content"`
		IsError bool          `json:"isError"`
	}
	type rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	readResp := func(wantID string) rpc {
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			var msg rpc
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				t.Fatalf("non-JSON line on stdout: %q\nerror: %v", line, err)
			}
			if string(msg.ID) == wantID {
				return msg
			}
		}
		t.Fatalf("stdout closed before response id=%s; stderr:\n%s", wantID, stderr.String())
		return rpc{}
	}
	text := func(r rpc) string {
		var res toolResult
		if err := json.Unmarshal(r.Result, &res); err != nil {
			t.Fatalf("unmarshal tool result: %v\nresult: %s", err, string(r.Result))
		}
		var b strings.Builder
		for _, c := range res.Content {
			b.WriteString(c.Text)
		}
		return b.String()
	}

	send(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "spinclass-test", "version": "0"},
		},
	})
	send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})

	send(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "check-this-session-async", "arguments": map[string]any{}},
	})
	if got := text(readResp("2")); !strings.Contains(got, "started background check job") {
		t.Fatalf("unexpected start response: %q", got)
	}

	// Let the hook get going, then cancel.
	send(map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "session-job-status", "arguments": map[string]any{}},
	})
	if got := text(readResp("3")); !strings.Contains(got, "running") {
		t.Logf("status before cancel was not running (job may have raced): %q", got)
	}

	cancelStart := time.Now()
	send(map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{"name": "session-job-cancel", "arguments": map[string]any{}},
	})
	_ = readResp("4")

	id := 5
	var final string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": id, "method": "tools/call",
			"params": map[string]any{"name": "session-job-status", "arguments": map[string]any{}},
		})
		body := text(readResp(strconv.Itoa(id)))
		id++
		if strings.Contains(body, "running") {
			time.Sleep(300 * time.Millisecond)
			continue
		}
		final = body
		break
	}

	if final == "" {
		t.Fatalf("job never left running state after cancel; stderr:\n%s", stderr.String())
	}
	// An in-process cancel is a producer teardown, so the terminal is `aborted`
	// (RFC-0018), not the pre-RFC-0018 `cancelled`.
	if !strings.Contains(final, "aborted") {
		t.Errorf("expected aborted status, got:\n%s", final)
	}
	// The hook (sleep 120) must have been killed, not waited out: the cancel
	// round-trip completes in seconds and the result carries a ctx-kill
	// signature. Go surfaces a context-killed exec as either "context canceled"
	// or "signal: killed" depending on timing, so accept both. (We can't assert
	// on the echo marker — it also appears in the echoed hook command in the
	// result.)
	if !strings.Contains(final, "context canceled") && !strings.Contains(final, "signal: killed") {
		t.Errorf("expected a killed-hook signature (context canceled / signal: killed), got:\n%s", final)
	}
	if elapsed := time.Since(cancelStart); elapsed > 30*time.Second {
		t.Errorf("cancel took too long (%s); hook may not have been killed", elapsed)
	}
}
