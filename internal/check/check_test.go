package check

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/embeds"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupRepoWithWorktree creates an isolated git repo + worktree under
// t.TempDir() and returns (root, repoDir, wtPath). $HOME and other
// git-config env vars are scoped to root for the duration of the test.
func setupRepoWithWorktree(t *testing.T, branch string) (root, repoDir, wtPath string) {
	t.Helper()
	root = t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", root)

	gitConfigDir := filepath.Join(root, "gitconfig")
	if err := os.MkdirAll(gitConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(gitConfigDir, "config"))
	t.Setenv("HOME", root)

	repoDir = filepath.Join(root, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit(t, repoDir, "init", "-b", "main")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "file.txt"), []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "file.txt")
	runGit(t, repoDir, "commit", "-m", "initial")

	wtDir := filepath.Join(repoDir, ".worktrees")
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wtPath = filepath.Join(wtDir, branch)
	runGit(t, repoDir, "worktree", "add", "-b", branch, wtPath)

	return root, repoDir, wtPath
}

func writeSweatfile(t *testing.T, wtPath, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wtPath, "sweatfile"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write sweatfile: %v", err)
	}
}

func TestRunHookSuccessTAP(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-success")
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"true\"\n")

	var buf bytes.Buffer
	links, err := Run(&buf, "tap", wtPath, false)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("expected no blob links without madder pinned, got %v", links)
	}

	got := buf.String()
	if !strings.Contains(got, "ok") {
		t.Errorf("expected TAP 'ok' in output, got: %q", got)
	}
	if strings.Contains(got, "not ok") {
		t.Errorf("did not expect 'not ok' in output, got: %q", got)
	}
	if !strings.Contains(got, "pre-merge hook") {
		t.Errorf("expected 'pre-merge hook' description in output, got: %q", got)
	}
	if !strings.Contains(got, "1..") {
		t.Errorf("expected TAP plan in output, got: %q", got)
	}
}

func TestRunHookFailureTAP(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-failure")
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"false\"\n")

	var buf bytes.Buffer
	_, err := Run(&buf, "tap", wtPath, false)
	if err == nil {
		t.Fatalf("expected error when hook fails, got nil. Output: %s", buf.String())
	}

	got := buf.String()
	if !strings.Contains(got, "not ok") {
		t.Errorf("expected TAP 'not ok' in output, got: %q", got)
	}
	if !strings.Contains(got, "1..") {
		t.Errorf("expected TAP plan in output (so client can detect failure), got: %q", got)
	}
}

// withFakeMadder installs a fake `madder write -format json -` for the
// duration of the test. The fake reads stdin to EOF and emits a known
// JSON envelope; the bytes written to stdin are captured to a file so
// the test can assert on what spinclass piped through.
func withFakeMadder(t *testing.T) (madderBin, stdinCapture string) {
	t.Helper()
	dir := t.TempDir()
	madderBin = filepath.Join(dir, "fake-madder")
	stdinCapture = filepath.Join(dir, "stdin")
	script := `#!/bin/sh
case "$1" in
  init)
    mkdir -p "$PWD/.madder/local/share/blob_stores/default"
    touch "$PWD/.madder/local/share/blob_stores/default/blob_store-config"
    ;;
  write)
    cat >"` + stdinCapture + `"
    printf '{"id":"sha256-fake","size":0,"source":"-"}\n'
    ;;
esac
exit 0
`
	if err := os.WriteFile(madderBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	prevMadder, prevDirenv, prevDodder := embeds.MadderBin(), embeds.DirenvBin(), embeds.DodderBin()
	embeds.Set(madderBin, prevDirenv, prevDodder)
	t.Cleanup(func() { embeds.Set(prevMadder, prevDirenv, prevDodder) })
	return madderBin, stdinCapture
}

func TestRunHookCompactShape(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-compact")
	_, stdinCapture := withFakeMadder(t)
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"echo line-one; echo line-two\"\n")

	var buf bytes.Buffer
	links, err := Run(&buf, "tap", wtPath, false)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(links) != 1 || links[0].URI != "madder://blobs/sha256-fake" {
		t.Fatalf("expected one blob link {URI=madder://blobs/sha256-fake}, got %v", links)
	}
	if links[0].MimeType != "text/plain" {
		t.Errorf("expected MimeType text/plain for format=raw, got %q", links[0].MimeType)
	}

	got := buf.String()

	if !strings.Contains(got, "# directive: if status is ok") {
		t.Errorf("expected directive comment, got:\n%s", got)
	}
	for _, want := range []string{
		"command: echo line-one; echo line-two",
		"format: raw",
		"resource_link: madder://blobs/sha256-fake",
		"exit_code: 0",
		"elapsed:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output, got:\n%s", want, got)
		}
	}
	// On success, the response must omit both visibility fields: the
	// test point being `ok` is itself the liveness signal and the
	// resource_link remains the authoritative full-output surface.
	if strings.Contains(got, "tail:") {
		t.Errorf("did not expect 'tail:' on raw success, got:\n%s", got)
	}
	if strings.Contains(got, "failure:") {
		t.Errorf("did not expect 'failure:' on raw success, got:\n%s", got)
	}
	// Compact shape never opens an OutputBlock — no nested `# Subtest`
	// or indented diagnostic block separator from OutputBlock.
	if strings.Contains(got, "# Output:") {
		t.Errorf("did not expect OutputBlock '# Output:' line, got:\n%s", got)
	}

	stdinBytes, err := os.ReadFile(stdinCapture)
	if err != nil {
		t.Fatalf("reading madder stdin capture: %v", err)
	}
	if !strings.Contains(string(stdinBytes), "line-one") || !strings.Contains(string(stdinBytes), "line-two") {
		t.Errorf("expected hook output piped to madder stdin, got: %q", stdinBytes)
	}
}

func TestRunHookCompactShape_Failure(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-compact-fail")
	withFakeMadder(t)
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"echo about-to-fail; exit 7\"\n")

	var buf bytes.Buffer
	links, err := Run(&buf, "tap", wtPath, false)
	if err == nil {
		t.Fatal("expected hook failure")
	}
	if len(links) != 1 || links[0].URI != "madder://blobs/sha256-fake" {
		t.Fatalf("expected one blob link {URI=madder://blobs/sha256-fake} even on failure, got %v", links)
	}
	if links[0].MimeType != "text/plain" {
		t.Errorf("expected MimeType text/plain for format=raw failure, got %q", links[0].MimeType)
	}

	got := buf.String()
	if !strings.Contains(got, "not ok") {
		t.Errorf("expected 'not ok' for failed hook, got:\n%s", got)
	}
	if !strings.Contains(got, "exit_code: 7") {
		t.Errorf("expected exit_code: 7, got:\n%s", got)
	}
	if !strings.Contains(got, "about-to-fail") {
		t.Errorf("expected hook stdout in tail, got:\n%s", got)
	}
	if !strings.Contains(got, "resource_link: madder://blobs/sha256-fake") {
		t.Errorf("expected resource_link in failure response, got:\n%s", got)
	}
	if !strings.Contains(got, "format: raw") {
		t.Errorf("expected 'format: raw' in failure response, got:\n%s", got)
	}
}

// readNDJSONRecords parses the stdin capture file from withFakeMadder as
// newline-delimited JSON, returning only TestRecords (type=="test"). The
// blob may also carry summary/bailout entries; this helper drops those.
func readNDJSONRecords(t *testing.T, path string) []struct {
	Type        string         `json:"type"`
	N           int            `json:"n"`
	Description string         `json:"description"`
	OK          bool           `json:"ok"`
	Diagnostic  map[string]any `json:"diagnostic"`
} {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading madder stdin capture: %v", err)
	}
	var recs []struct {
		Type        string         `json:"type"`
		N           int            `json:"n"`
		Description string         `json:"description"`
		OK          bool           `json:"ok"`
		Diagnostic  map[string]any `json:"diagnostic"`
	}
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec struct {
			Type        string         `json:"type"`
			N           int            `json:"n"`
			Description string         `json:"description"`
			OK          bool           `json:"ok"`
			Diagnostic  map[string]any `json:"diagnostic"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("unmarshalling blob line %q: %v\nfull blob:\n%s", line, err, raw)
		}
		if rec.Type == "test" {
			recs = append(recs, rec)
		}
	}
	return recs
}

func TestRunHookCompactShape_TapNDJSONSuccess(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-tap-ndjson-success")
	_, stdinCapture := withFakeMadder(t)
	// Hook prints a valid TAP-14 stream with one passing test point.
	writeSweatfile(t, wtPath, "[hooks]\n"+
		"pre-merge = \"printf 'TAP version 14\\n1..1\\nok 1 - synthetic\\n'\"\n"+
		"pre-merge-output-format = \"tap-ndjson\"\n")

	var buf bytes.Buffer
	links, err := Run(&buf, "tap", wtPath, false)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(links) != 1 || links[0].URI != "madder://blobs/sha256-fake" {
		t.Fatalf("expected one blob link {URI=madder://blobs/sha256-fake}, got %v", links)
	}
	if links[0].MimeType != "application/x-ndjson" {
		t.Errorf("expected MimeType application/x-ndjson for format=tap-ndjson, got %q", links[0].MimeType)
	}

	got := buf.String()
	if !strings.Contains(got, "format: tap-ndjson") {
		t.Errorf("expected 'format: tap-ndjson' in output, got:\n%s", got)
	}
	if !strings.Contains(got, "ok") {
		t.Errorf("expected 'ok' in output, got:\n%s", got)
	}
	if strings.Contains(got, "not ok") {
		t.Errorf("did not expect 'not ok' on success, got:\n%s", got)
	}
	if strings.Contains(got, "tail:") {
		t.Errorf("did not expect 'tail:' on tap-ndjson success, got:\n%s", got)
	}
	if strings.Contains(got, "failure:") {
		t.Errorf("did not expect 'failure:' on success, got:\n%s", got)
	}

	// The madder blob must be the PARSED ndjson, not the raw stdout.
	recs := readNDJSONRecords(t, stdinCapture)
	if len(recs) != 1 {
		t.Fatalf("expected 1 TestRecord in blob, got %d: %+v", len(recs), recs)
	}
	if !recs[0].OK {
		t.Errorf("expected OK=true on the parsed record, got %+v", recs[0])
	}
	if recs[0].N != 1 || recs[0].Description != "synthetic" {
		t.Errorf("unexpected record fields: %+v", recs[0])
	}
	// Sanity: the blob must NOT contain the raw 'TAP version 14' header
	// (we wrote parsed ndjson, not the raw stream).
	raw, _ := os.ReadFile(stdinCapture)
	if strings.Contains(string(raw), "TAP version 14") {
		t.Errorf("blob should not contain raw TAP header on tap-ndjson, got:\n%s", raw)
	}
}

func TestRunHookCompactShape_TapNDJSONFailure(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-tap-ndjson-failure")
	_, stdinCapture := withFakeMadder(t)
	// Hook prints a valid TAP-14 stream with one not-ok and a YAML
	// diagnostic, then exits non-zero so RunPreMergeHook returns an
	// ExitError. The shell wraps printf with `exit 1` via `&&` chain.
	writeSweatfile(t, wtPath, "[hooks]\n"+
		"pre-merge = \"printf 'TAP version 14\\n1..1\\nnot ok 1 - synthetic\\n  ---\\n  message: expected 7 got 9\\n  ...\\n'; exit 1\"\n"+
		"pre-merge-output-format = \"tap-ndjson\"\n")

	var buf bytes.Buffer
	_, err := Run(&buf, "tap", wtPath, false)
	if err == nil {
		t.Fatalf("expected hook failure, got nil. Output: %s", buf.String())
	}

	got := buf.String()
	if !strings.Contains(got, "not ok") {
		t.Errorf("expected 'not ok' for failed hook, got:\n%s", got)
	}
	if !strings.Contains(got, "format: tap-ndjson") {
		t.Errorf("expected 'format: tap-ndjson' in output, got:\n%s", got)
	}
	if !strings.Contains(got, "failure:") {
		t.Errorf("expected 'failure:' field with parsed records, got:\n%s", got)
	}
	if !strings.Contains(got, "expected 7 got 9") {
		t.Errorf("expected diagnostic message in failure summary, got:\n%s", got)
	}
	if strings.Contains(got, "tail:") {
		t.Errorf("did not expect 'tail:' when parsed records exist, got:\n%s", got)
	}

	// The madder blob must contain exactly one TestRecord with OK=false.
	recs := readNDJSONRecords(t, stdinCapture)
	if len(recs) != 1 {
		t.Fatalf("expected 1 TestRecord in blob, got %d: %+v", len(recs), recs)
	}
	if recs[0].OK {
		t.Errorf("expected OK=false on the parsed failure record, got %+v", recs[0])
	}
}

func TestRunHookCompactShape_TapNDJSONDegenerateFallback(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-tap-ndjson-degenerate")
	withFakeMadder(t)
	// Hook prints non-TAP garbage (no `TAP version 14` line) and exits
	// non-zero. The parser produces zero records, so the response must
	// fall back to `tail:` carrying the raw output.
	writeSweatfile(t, wtPath, "[hooks]\n"+
		"pre-merge = \"echo this is not tap; exit 3\"\n"+
		"pre-merge-output-format = \"tap-ndjson\"\n")

	var buf bytes.Buffer
	_, err := Run(&buf, "tap", wtPath, false)
	if err == nil {
		t.Fatalf("expected hook failure, got nil. Output: %s", buf.String())
	}

	got := buf.String()
	if !strings.Contains(got, "not ok") {
		t.Errorf("expected 'not ok' for failed hook, got:\n%s", got)
	}
	if !strings.Contains(got, "format: tap-ndjson") {
		t.Errorf("expected 'format: tap-ndjson' in output, got:\n%s", got)
	}
	if !strings.Contains(got, "tail:") {
		t.Errorf("expected 'tail:' fallback on degenerate stream, got:\n%s", got)
	}
	if !strings.Contains(got, "this is not tap") {
		t.Errorf("expected garbage stdout in tail, got:\n%s", got)
	}
	if strings.Contains(got, "failure:") {
		t.Errorf("did not expect 'failure:' on degenerate stream, got:\n%s", got)
	}
}

func TestRunNoHookConfigured(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-no-hook")
	// No sweatfile written.

	var buf bytes.Buffer
	if _, err := Run(&buf, "tap", wtPath, false); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "ok") {
		t.Errorf("expected TAP 'ok' for no-hook case, got: %q", got)
	}
	// Per the design: agents should treat "no hook" as a successful check
	// because there is nothing to run. The TAP message should make that
	// reason explicit so a human reading the output is not confused.
	if !strings.Contains(strings.ToLower(got), "no pre-merge hook") {
		t.Errorf("expected 'no pre-merge hook' message, got: %q", got)
	}
	if !strings.Contains(got, "1..") {
		t.Errorf("expected TAP plan in output, got: %q", got)
	}
}
