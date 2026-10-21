package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/purse-first/libs/go-mcp/server"
)

type escapeHarness struct {
	stdout, stderr *bytes.Buffer
}

// forceEscapeTerminal makes the hatch believe a human is at the keyboard and
// feeds confirmation from typed.
func forceEscapeTerminal(t *testing.T, typed string) escapeHarness {
	t.Helper()
	h := escapeHarness{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	oldTerm, oldIn, oldOut, oldErr := gritEscapeIsTerminal, gritEscapeConfirmIn, gritEscapeStdout, gritEscapeStderr
	t.Cleanup(func() {
		gritEscapeIsTerminal, gritEscapeConfirmIn, gritEscapeStdout, gritEscapeStderr = oldTerm, oldIn, oldOut, oldErr
	})
	gritEscapeIsTerminal = func() bool { return true }
	gritEscapeConfirmIn = func() io.Reader { return strings.NewReader(typed) }
	gritEscapeStdout = func() io.Writer { return h.stdout }
	gritEscapeStderr = func() io.Writer { return h.stderr }
	return h
}

func runEscape(tool, args string, yes bool) error {
	raw, _ := json.Marshal(gritEscapeFlags{Tool: tool, Args: args, Yes: yes})
	return runGritEscape(context.Background(), raw)
}

func readEscapeLog(t *testing.T) []map[string]any {
	t.Helper()
	path, err := gritEscapeLogPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		records = append(records, rec)
	}
	return records
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func TestGritEscapeRefusesWithoutATerminal(t *testing.T) {
	repo := isolatedGitRepo(t)
	t.Chdir(repo)
	old := gritEscapeIsTerminal
	t.Cleanup(func() { gritEscapeIsTerminal = old })
	gritEscapeIsTerminal = func() bool { return false }
	err := runEscape("stage", `{"subcommand":"status"}`, true)
	if err == nil || !strings.Contains(err.Error(), "terminals") {
		t.Fatalf("want a terminal refusal, got %v", err)
	}
	if path, _ := gritEscapeLogPath(); path != "" {
		if _, statErr := os.Stat(path); statErr == nil {
			t.Error("a refused run must not have touched the log")
		}
	}
}

func TestGritEscapeRunsReadAndWriteOutsideASessionAndLogsEach(t *testing.T) {
	repo := isolatedGitRepo(t)
	t.Chdir(repo)
	h := forceEscapeTerminal(t, "")
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runEscape("stage", `{"subcommand":"status"}`, false); err != nil {
		t.Fatalf("read verb: %v", err)
	}
	if !strings.Contains(h.stdout.String(), "new.txt") {
		t.Errorf("status output missing on stdout: %q", h.stdout.String())
	}
	if err := runEscape("stage", `{"subcommand":"add","paths":["new.txt"]}`, true); err != nil {
		t.Fatalf("write verb outside a session must not be blocked by NoSession: %v", err)
	}
	if !strings.Contains(gitOut(t, repo, "diff", "--cached", "--name-only"), "new.txt") {
		t.Error("the write did not stage the file")
	}

	records := readEscapeLog(t)
	if len(records) != 2 {
		t.Fatalf("want one log line per call, got %d", len(records))
	}
	for i, want := range []struct{ sub, verdict, outcome string }{{"status", "allow", "ok"}, {"add", "ask", "ok"}} {
		rec := records[i]
		if rec["tool"] != "stage" || rec["subcommand"] != want.sub || rec["verdict"] != want.verdict || rec["outcome"] != want.outcome {
			t.Errorf("record %d = %v, want %+v", i, rec, want)
		}
		for _, field := range []string{"time", "cwd", "args", "confirmed", "confirm_required"} {
			if _, ok := rec[field]; !ok {
				t.Errorf("record %d lacks %q", i, field)
			}
		}
	}
	path, _ := gritEscapeLogPath()
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("log must exist with mode 0600, got %v %v", info, err)
	}
}

func TestGritEscapeAskVerbNeedsTypedConfirmation(t *testing.T) {
	repo := isolatedGitRepo(t)
	t.Chdir(repo)
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := `{"subcommand":"add","paths":["new.txt"]}`

	h := forceEscapeTerminal(t, "n\n")
	if err := runEscape("stage", args, false); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("a declined confirmation must fail, got %v", err)
	}
	if gitOut(t, repo, "diff", "--cached", "--name-only") != "" {
		t.Error("a declined confirmation still ran the verb")
	}
	if !strings.Contains(h.stderr.String(), "stage add") {
		t.Errorf("the verb must be shown on stderr, got %q", h.stderr.String())
	}

	forceEscapeTerminal(t, "y\n")
	if err := runEscape("stage", args, false); err != nil {
		t.Fatalf("confirmed run: %v", err)
	}
	if !strings.Contains(gitOut(t, repo, "diff", "--cached", "--name-only"), "new.txt") {
		t.Error("the confirmed verb did not run")
	}

	records := readEscapeLog(t)
	if len(records) != 2 || records[0]["confirmed"] != false || records[0]["outcome"] != "error" || records[1]["confirmed"] != true || records[1]["outcome"] != "ok" {
		t.Errorf("log does not record the decline then the confirmation: %v", records)
	}
}

func TestGritEscapeStillRefusesDestructiveDefaultBranchOps(t *testing.T) {
	repo := isolatedGitRepo(t)
	t.Chdir(repo)
	forceEscapeTerminal(t, "")
	before := gitOut(t, repo, "rev-parse", "HEAD")
	err := runEscape("branch", `{"subcommand":"reset-hard","ref":"HEAD"}`, true)
	if err == nil || !strings.Contains(err.Error(), "default branch") {
		t.Fatalf("want the default-branch refusal, got %v", err)
	}
	if gitOut(t, repo, "rev-parse", "HEAD") != before {
		t.Error("HEAD moved")
	}
	records := readEscapeLog(t)
	if len(records) != 1 || records[0]["outcome"] != "error" {
		t.Errorf("the refusal must be logged as an error: %v", records)
	}
}

func TestGritEscapeFailsClosedWhenTheLogCannotBeWritten(t *testing.T) {
	repo := isolatedGitRepo(t)
	t.Chdir(repo)
	forceEscapeTerminal(t, "")
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", blocker)
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runEscape("stage", `{"subcommand":"add","paths":["new.txt"]}`, true)
	if err == nil || !strings.Contains(err.Error(), "escape log") {
		t.Fatalf("want a log refusal, got %v", err)
	}
	if gitOut(t, repo, "diff", "--cached", "--name-only") != "" {
		t.Error("the operation ran although it could not be logged")
	}
}

func TestGritEscapeTakesArgsOnlyFromArgvAndNeverReadsStdin(t *testing.T) {
	repo := isolatedGitRepo(t)
	t.Chdir(repo)
	h := forceEscapeTerminal(t, "")
	// stdin carries a valid object, yet no args means {} and a missing
	// subcommand: the hatch must not have read it.
	oldIn := os.Stdin
	t.Cleanup(func() { os.Stdin = oldIn })
	r, w, _ := os.Pipe()
	_, _ = w.WriteString(`{"subcommand":"status"}`)
	w.Close()
	os.Stdin = r
	app := buildApp()
	err := app.RunCLI(context.Background(), []string{"grit", "stage"}, nil)
	if err == nil || !strings.Contains(err.Error(), "subcommand is required") {
		t.Fatalf("want a missing-subcommand error, got %v", err)
	}
	if err := app.RunCLI(context.Background(), []string{"grit", "stage", `{"subcommand":"status"}`}, nil); err != nil {
		t.Fatalf("positional tool and JSON args: %v", err)
	}
	if h.stdout.Len() == 0 {
		t.Error("status printed nothing to stdout")
	}
}

// escapeRemoteRepo is isolatedGitRepo plus a bare file remote "origin" that
// already holds main, with the cwd set to the repo.
func escapeRemoteRepo(t *testing.T) (repo, remote string) {
	t.Helper()
	repo = isolatedGitRepo(t)
	remote = filepath.Join(filepath.Dir(repo), "remote.git")
	mustGitIn(t, filepath.Dir(repo), "init", "--bare", "-b", "main", remote)
	mustGitIn(t, repo, "remote", "add", "origin", remote)
	mustGitIn(t, repo, "push", "origin", "main")
	t.Chdir(repo)
	return repo, remote
}

func assertEscapePushRefused(t *testing.T, err error, remote, wantTip string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "the escape hatch never force-pushes, deletes, or rewrites the default branch") {
		t.Fatalf("want the named refusal, got %v", err)
	}
	if got := gitOut(t, remote, "rev-parse", "main"); got != wantTip {
		t.Errorf("remote main moved: %q, want %q", got, wantTip)
	}
	records := readEscapeLog(t)
	last := records[len(records)-1]
	if last["outcome"] != "error" || !strings.Contains(last["error"].(string), "never force-pushes") {
		t.Errorf("the refusal must be logged as an error with its text: %v", last)
	}
}

func TestGritEscapeRefusesForcePushOfDefaultBranchEvenWithYes(t *testing.T) {
	repo, remote := escapeRemoteRepo(t)
	forceEscapeTerminal(t, "y\n")
	tip := gitOut(t, remote, "rev-parse", "main")
	mustGitIn(t, repo, "commit", "--allow-empty", "-m", "second")
	err := runEscape("remote", `{"subcommand":"push","branch":"main","force":true}`, true)
	assertEscapePushRefused(t, err, remote, tip)
}

func TestGritEscapeRefusesForceWithLeasePushOfDefaultBranch(t *testing.T) {
	repo, remote := escapeRemoteRepo(t)
	forceEscapeTerminal(t, "y\n")
	tip := gitOut(t, remote, "rev-parse", "main")
	mustGitIn(t, repo, "commit", "--allow-empty", "-m", "second")
	err := runEscape("remote", `{"subcommand":"push","branch":"main","force_with_lease":true}`, true)
	assertEscapePushRefused(t, err, remote, tip)
}

func TestGritEscapeRefusesDeleteOfDefaultBranch(t *testing.T) {
	_, remote := escapeRemoteRepo(t)
	forceEscapeTerminal(t, "y\n")
	tip := gitOut(t, remote, "rev-parse", "main")
	err := runEscape("remote", `{"subcommand":"push","branch":"main","delete":true}`, true)
	assertEscapePushRefused(t, err, remote, tip)
}

func TestGritEscapeRefusesNonFastForwardPushOfDefaultBranch(t *testing.T) {
	repo, remote := escapeRemoteRepo(t)
	forceEscapeTerminal(t, "y\n")
	// Rewrite local main so the remote tip is no ancestor of it.
	mustGitIn(t, repo, "commit", "--allow-empty", "--amend", "-m", "rewritten")
	tip := gitOut(t, remote, "rev-parse", "main")
	err := runEscape("remote", `{"subcommand":"push","branch":"main"}`, true)
	assertEscapePushRefused(t, err, remote, tip)
}

func TestGritEscapeRefusesPushStackIncludingDefaultBranch(t *testing.T) {
	repo, remote := escapeRemoteRepo(t)
	forceEscapeTerminal(t, "y\n")
	tip := gitOut(t, remote, "rev-parse", "main")
	mustGitIn(t, repo, "branch", "feature")
	err := runEscape("remote", `{"subcommand":"push-stack","branches":["feature","main"]}`, true)
	assertEscapePushRefused(t, err, remote, tip)
}

func TestGritEscapeAllowsFastForwardPushOfDefaultBranchAfterTypedY(t *testing.T) {
	repo, remote := escapeRemoteRepo(t)
	mustGitIn(t, repo, "commit", "--allow-empty", "-m", "second")
	want := gitOut(t, repo, "rev-parse", "main")
	args := `{"subcommand":"push","branch":"main"}`

	forceEscapeTerminal(t, "n\n")
	if err := runEscape("remote", args, false); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("a fast-forward push of main must still ask, got %v", err)
	}
	if gitOut(t, remote, "rev-parse", "main") == want {
		t.Fatal("a declined push still ran")
	}

	forceEscapeTerminal(t, "y\n")
	if err := runEscape("remote", args, false); err != nil {
		t.Fatalf("a confirmed fast-forward push of main must run: %v", err)
	}
	if got := gitOut(t, remote, "rev-parse", "main"); got != want {
		t.Errorf("remote main = %q, want %q", got, want)
	}
}

func TestGritEscapeIsNotAnMCPTool(t *testing.T) {
	app := buildApp()
	cmd, ok := app.GetCommand("grit")
	if !ok {
		t.Fatal("sc grit is not registered")
	}
	if cmd.Run != nil || cmd.RunCLI == nil {
		t.Fatalf("must be RunCLI-only (Run=%v RunCLI=%v)", cmd.Run != nil, cmd.RunCLI != nil)
	}
	registry := server.NewToolRegistryV1()
	app.RegisterMCPToolsV1(registry)
	tools, err := registry.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) == 0 {
		t.Fatal("empty tool list: the assertion would be vacuous")
	}
	for _, tool := range tools {
		if tool.Name == "grit" {
			t.Fatalf("the escape hatch is exposed as MCP tool %q", tool.Name)
		}
	}
}
