package check

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
	"code.linenisgreat.com/crap/go-crap/v2/ndjsoncrap"
	"code.linenisgreat.com/spinclass/internal/embeds"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
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

// setupRepo creates an isolated git repo (a plain main checkout, NO worktree)
// under t.TempDir() and returns (root, repoDir). It mirrors the repo-init half
// of setupRepoWithWorktree and is used to exercise the implicit (main-checkout)
// session case where wtPath == repoDir.
func setupRepo(t *testing.T) (root, repoDir string) {
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

	return root, repoDir
}

func writeSweatfile(t *testing.T, wtPath, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wtPath, "sweatfile"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write sweatfile: %v", err)
	}
}

// decodeRecords parses a buffered ndjson-crap stream into its records.
func decodeRecords(t *testing.T, raw []byte) []ndjsoncrap.Record {
	t.Helper()
	rd := ndjsoncrap.NewReader(bytes.NewReader(raw))
	var recs []ndjsoncrap.Record
	for {
		rec, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decoding ndjson-crap stream: %v\nstream:\n%s", err, raw)
		}
		recs = append(recs, rec)
	}
	return recs
}

// runCheck drives Run with a Reporter over a bytes.Buffer and returns the
// blob links, the decoded record stream, and Run's error.
func runCheck(t *testing.T, wtPath string) ([]BlobLink, []ndjsoncrap.Record, error) {
	t.Helper()
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	links, err := Run(rep, wtPath)
	return links, decodeRecords(t, buf.Bytes()), err
}

// singleTest asserts the stream carries exactly one result-family test
// record and returns it.
func singleTest(t *testing.T, recs []ndjsoncrap.Record) ndjsoncrap.Test {
	t.Helper()
	var tests []ndjsoncrap.Test
	for _, rec := range recs {
		if tr, ok := rec.(ndjsoncrap.Test); ok {
			tests = append(tests, tr)
		}
	}
	if len(tests) != 1 {
		t.Fatalf("expected exactly 1 test record, got %d: %+v", len(tests), tests)
	}
	return tests[0]
}

// outputText concatenates the Data of every execution-family Output record
// — the hook's live lines as carried on the wire.
func outputText(recs []ndjsoncrap.Record) string {
	var b strings.Builder
	for _, rec := range recs {
		if out, ok := rec.(ndjsoncrap.Output); ok {
			b.WriteString(out.Data)
		}
	}
	return b.String()
}

// nodeEnds returns the execution-family NodeEnd records in the stream —
// the phase verdicts (Phase.Done writes exit_code 0, Phase.Fail writes 1).
func nodeEnds(recs []ndjsoncrap.Record) []ndjsoncrap.NodeEnd {
	var ends []ndjsoncrap.NodeEnd
	for _, rec := range recs {
		if ne, ok := rec.(ndjsoncrap.NodeEnd); ok {
			ends = append(ends, ne)
		}
	}
	return ends
}

// assertNodeEndExit asserts the stream carries exactly one NodeEnd and that
// its exit_code is non-nil and equal to want.
func assertNodeEndExit(t *testing.T, recs []ndjsoncrap.Record, want int) {
	t.Helper()
	ends := nodeEnds(recs)
	if len(ends) != 1 {
		t.Fatalf("expected exactly 1 node_end record, got %d: %+v", len(ends), ends)
	}
	if ends[0].ExitCode == nil {
		t.Fatalf("expected non-nil exit_code on node_end, got %+v", ends[0])
	}
	if *ends[0].ExitCode != want {
		t.Errorf("expected node_end exit_code %d, got %d", want, *ends[0].ExitCode)
	}
}

// hasPlanAndSummary reports whether the stream carries the result-family
// plan and summary framing (the ndjson-crap analogue of TAP's "1..N").
func hasPlanAndSummary(recs []ndjsoncrap.Record) (plan, summary bool) {
	for _, rec := range recs {
		switch rec.(type) {
		case ndjsoncrap.Plan:
			plan = true
		case ndjsoncrap.Summary:
			summary = true
		}
	}
	return plan, summary
}

// assertNoTestRecords fails if the stream carries any result-family test
// record: since go-crap v2.2.1 the hook stage is a self-sufficient execution
// Phase (node_end carries the verdict diagnostic), and pairing it with a Test
// record double-renders the verdict (crap#22).
func assertNoTestRecords(t *testing.T, recs []ndjsoncrap.Record) {
	t.Helper()
	for _, rec := range recs {
		if tr, ok := rec.(ndjsoncrap.Test); ok {
			t.Errorf("unexpected result-family test record %q: the hook stage must be phase-only", tr.Description)
		}
	}
}

// singleNodeEnd returns the stream's only node_end record, failing unless
// exactly one exists.
func singleNodeEnd(t *testing.T, recs []ndjsoncrap.Record) ndjsoncrap.NodeEnd {
	t.Helper()
	ends := nodeEnds(recs)
	if len(ends) != 1 {
		t.Fatalf("expected exactly one node_end record, got %d", len(ends))
	}
	return ends[0]
}

// hookNodeName returns the node_start name for the hook phase, failing
// unless exactly one node_start exists.
func hookNodeName(t *testing.T, recs []ndjsoncrap.Record) string {
	t.Helper()
	var names []string
	for _, rec := range recs {
		if ns, ok := rec.(ndjsoncrap.NodeStart); ok {
			names = append(names, ns.Name)
		}
	}
	if len(names) != 1 {
		t.Fatalf("expected exactly one node_start record, got %d", len(names))
	}
	return names[0]
}

func TestRunHookSuccess(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-success")
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"true\"\n")

	links, recs, err := runCheck(t, wtPath)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("expected no blob links without madder pinned, got %v", links)
	}

	assertNoTestRecords(t, recs)
	ne := singleNodeEnd(t, recs)
	if ne.ExitCode == nil || *ne.ExitCode != 0 {
		t.Errorf("expected passing node_end (exit 0), got %+v", ne)
	}
	if ne.Diagnostic != nil {
		t.Errorf("expected nil diagnostic on success, got %+v", ne.Diagnostic)
	}
	if name := hookNodeName(t, recs); !strings.Contains(name, "pre-merge hook") {
		t.Errorf("expected 'pre-merge hook' phase name, got %q", name)
	}
	if plan, summary := hasPlanAndSummary(recs); !plan || !summary {
		t.Errorf("expected plan+summary framing, got plan=%v summary=%v", plan, summary)
	}
}

func TestRunHookFailure(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-failure")
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"false\"\n")

	_, recs, err := runCheck(t, wtPath)
	if err == nil {
		t.Fatalf("expected error when hook fails, got nil")
	}

	assertNoTestRecords(t, recs)
	ne := singleNodeEnd(t, recs)
	if ne.ExitCode == nil || *ne.ExitCode == 0 {
		t.Errorf("expected failing node_end, got %+v", ne)
	}
	if got := fmt.Sprintf("%v", ne.Diagnostic["exit_code"]); got != "1" {
		t.Errorf("expected exit_code 1 in node_end diagnostic, got %v", ne.Diagnostic["exit_code"])
	}
	// Summary must still be emitted on failure so a client can detect the
	// stream finished (the analogue of the TAP plan).
	if plan, summary := hasPlanAndSummary(recs); !plan || !summary {
		t.Errorf("expected plan+summary framing on failure, got plan=%v summary=%v", plan, summary)
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

func TestRunHookPhaseShape(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-compact")
	_, stdinCapture := withFakeMadder(t)
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"echo line-one; echo line-two\"\n")

	links, recs, err := runCheck(t, wtPath)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(links) != 1 || links[0].URI != "madder://blobs/sha256-fake" {
		t.Fatalf("expected one blob link {URI=madder://blobs/sha256-fake}, got %v", links)
	}
	if links[0].MimeType != "text/plain" {
		t.Errorf("expected MimeType text/plain for format=raw, got %q", links[0].MimeType)
	}

	// The Phase IS the verdict unit (crap#22): a success verdict on the
	// node_end, no diagnostic, and no paired result-family test record.
	assertNoTestRecords(t, recs)
	assertNodeEndExit(t, recs, 0)
	if ne := singleNodeEnd(t, recs); ne.Diagnostic != nil {
		t.Errorf("did not expect diagnostic on raw success, got %+v", ne.Diagnostic)
	}

	// The Phase carries the hook's live lines as Output records (the
	// viewport's rolling tail) plus the blob-link line for the wire.
	out := outputText(recs)
	for _, want := range []string{"line-one", "line-two", "resource_link: madder://blobs/sha256-fake"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in phase output records, got:\n%s", want, out)
		}
	}

	stdinBytes, err := os.ReadFile(stdinCapture)
	if err != nil {
		t.Fatalf("reading madder stdin capture: %v", err)
	}
	if !strings.Contains(string(stdinBytes), "line-one") || !strings.Contains(string(stdinBytes), "line-two") {
		t.Errorf("expected hook output piped to madder stdin, got: %q", stdinBytes)
	}
}

func TestRunHookPhaseShape_Failure(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-compact-fail")
	withFakeMadder(t)
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"echo about-to-fail; exit 7\"\n")

	links, recs, err := runCheck(t, wtPath)
	if err == nil {
		t.Fatal("expected hook failure")
	}
	if len(links) != 1 || links[0].URI != "madder://blobs/sha256-fake" {
		t.Fatalf("expected one blob link {URI=madder://blobs/sha256-fake} even on failure, got %v", links)
	}
	if links[0].MimeType != "text/plain" {
		t.Errorf("expected MimeType text/plain for format=raw failure, got %q", links[0].MimeType)
	}

	// The Phase IS the verdict unit (crap#22): FailDiag rides the
	// diagnostic on the node_end, and no result-family test record is
	// paired with it. The node_end's own exit_code stays 1 (Phase.FailDiag
	// hardcodes it); the hook's real 7 lives in the diagnostic, whose keys
	// win when the renderers merge.
	assertNoTestRecords(t, recs)
	assertNodeEndExit(t, recs, 1)
	ne := singleNodeEnd(t, recs)
	if got := fmt.Sprintf("%v", ne.Diagnostic["exit_code"]); got != "7" {
		t.Errorf("expected exit_code 7, got %v", ne.Diagnostic["exit_code"])
	}
	if got, _ := ne.Diagnostic["output"].(string); !strings.Contains(got, "about-to-fail") {
		t.Errorf("expected hook stdout in diagnostic output, got %v", ne.Diagnostic["output"])
	}
	if ne.Diagnostic["resource_link"] != "madder://blobs/sha256-fake" {
		t.Errorf("expected resource_link in failure diagnostic, got %v", ne.Diagnostic["resource_link"])
	}
	if ne.Diagnostic["format"] != "raw" {
		t.Errorf("expected format raw in failure diagnostic, got %v", ne.Diagnostic["format"])
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

func TestRunHookPhaseShape_TapNDJSONSuccess(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-tap-ndjson-success")
	_, stdinCapture := withFakeMadder(t)
	// Hook prints a valid TAP-14 stream with one passing test point.
	writeSweatfile(t, wtPath, "[hooks]\n"+
		"pre-merge = \"printf 'TAP version 14\\n1..1\\nok 1 - synthetic\\n'\"\n"+
		"pre-merge-output-format = \"tap-ndjson\"\n")

	links, recs, err := runCheck(t, wtPath)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(links) != 1 || links[0].URI != "madder://blobs/sha256-fake" {
		t.Fatalf("expected one blob link {URI=madder://blobs/sha256-fake}, got %v", links)
	}
	if links[0].MimeType != "application/x-ndjson" {
		t.Errorf("expected MimeType application/x-ndjson for format=tap-ndjson, got %q", links[0].MimeType)
	}

	assertNoTestRecords(t, recs)
	assertNodeEndExit(t, recs, 0)
	if ne := singleNodeEnd(t, recs); ne.Diagnostic != nil {
		t.Errorf("did not expect diagnostic on tap-ndjson success, got %+v", ne.Diagnostic)
	}

	// Even on the buffered (structured) path the hook's raw lines stream
	// live as Output records on the phase.
	if out := outputText(recs); !strings.Contains(out, "ok 1 - synthetic") {
		t.Errorf("expected raw hook TAP lines in phase output records, got:\n%s", out)
	}

	// The madder blob must be the PARSED ndjson, not the raw stdout.
	blobRecs := readNDJSONRecords(t, stdinCapture)
	if len(blobRecs) != 1 {
		t.Fatalf("expected 1 TestRecord in blob, got %d: %+v", len(blobRecs), blobRecs)
	}
	if !blobRecs[0].OK {
		t.Errorf("expected OK=true on the parsed record, got %+v", blobRecs[0])
	}
	if blobRecs[0].N != 1 || blobRecs[0].Description != "synthetic" {
		t.Errorf("unexpected record fields: %+v", blobRecs[0])
	}
	// Sanity: the blob must NOT contain the raw 'TAP version 14' header
	// (we wrote parsed ndjson, not the raw stream).
	raw, _ := os.ReadFile(stdinCapture)
	if strings.Contains(string(raw), "TAP version 14") {
		t.Errorf("blob should not contain raw TAP header on tap-ndjson, got:\n%s", raw)
	}
}

func TestRunHookPhaseShape_TapNDJSONFailure(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-tap-ndjson-failure")
	_, stdinCapture := withFakeMadder(t)
	// Hook prints a valid TAP-14 stream with one not-ok and a YAML
	// diagnostic, then exits non-zero so RunPreMergeHook returns an
	// ExitError.
	writeSweatfile(t, wtPath, "[hooks]\n"+
		"pre-merge = \"printf 'TAP version 14\\n1..1\\nnot ok 1 - synthetic\\n  ---\\n  message: expected 7 got 9\\n  ...\\n'; exit 1\"\n"+
		"pre-merge-output-format = \"tap-ndjson\"\n")

	_, recs, err := runCheck(t, wtPath)
	if err == nil {
		t.Fatalf("expected hook failure, got nil")
	}

	assertNoTestRecords(t, recs)
	assertNodeEndExit(t, recs, 1)
	ne := singleNodeEnd(t, recs)
	if ne.Diagnostic["format"] != "tap-ndjson" {
		t.Errorf("expected format tap-ndjson in diagnostic, got %v", ne.Diagnostic["format"])
	}
	out, _ := ne.Diagnostic["output"].(string)
	if !strings.Contains(out, "expected 7 got 9") {
		t.Errorf("expected failure summary with diagnostic message in output, got %q", out)
	}
	if !strings.Contains(out, "#1 synthetic") {
		t.Errorf("expected failing record reference in failure summary, got %q", out)
	}

	// The madder blob must contain exactly one TestRecord with OK=false.
	blobRecs := readNDJSONRecords(t, stdinCapture)
	if len(blobRecs) != 1 {
		t.Fatalf("expected 1 TestRecord in blob, got %d: %+v", len(blobRecs), blobRecs)
	}
	if blobRecs[0].OK {
		t.Errorf("expected OK=false on the parsed failure record, got %+v", blobRecs[0])
	}
}

func TestRunHookPhaseShape_NdjsonCrapSuccess(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-ndjson-crap-success")
	_, stdinCapture := withFakeMadder(t)
	// Hook emits canonical ndjson-crap directly (one passing test record).
	writeSweatfile(t, wtPath, "[hooks]\n"+
		"pre-merge = \"echo '{\\\"type\\\":\\\"test\\\",\\\"n\\\":1,\\\"description\\\":\\\"synthetic\\\",\\\"ok\\\":true}'\"\n"+
		"pre-merge-output-format = \"ndjson-crap\"\n")

	links, recs, err := runCheck(t, wtPath)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(links) != 1 || links[0].MimeType != "application/x-ndjson" {
		t.Fatalf("expected one application/x-ndjson blob link, got %v", links)
	}

	assertNoTestRecords(t, recs)
	assertNodeEndExit(t, recs, 0)
	if ne := singleNodeEnd(t, recs); ne.Diagnostic != nil {
		t.Errorf("did not expect diagnostic on success, got %+v", ne.Diagnostic)
	}

	// The blob stores the ndjson-crap stream verbatim (already canonical).
	raw, _ := os.ReadFile(stdinCapture)
	if !strings.Contains(string(raw), `"type":"test"`) || !strings.Contains(string(raw), "synthetic") {
		t.Errorf("blob should contain the verbatim ndjson-crap record, got:\n%s", raw)
	}
}

func TestRunHookPhaseShape_NdjsonCrapFailure(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-ndjson-crap-failure")
	withFakeMadder(t)
	// Hook emits an ndjson-crap failing test record with a diagnostic, then
	// exits non-zero.
	writeSweatfile(t, wtPath, "[hooks]\n"+
		"pre-merge = \"echo '{\\\"type\\\":\\\"test\\\",\\\"n\\\":1,\\\"description\\\":\\\"synthetic\\\",\\\"ok\\\":false,\\\"diagnostic\\\":{\\\"message\\\":\\\"expected 7 got 9\\\"}}'; exit 1\"\n"+
		"pre-merge-output-format = \"ndjson-crap\"\n")

	_, recs, err := runCheck(t, wtPath)
	if err == nil {
		t.Fatalf("expected hook failure, got nil")
	}

	assertNoTestRecords(t, recs)
	assertNodeEndExit(t, recs, 1)
	ne := singleNodeEnd(t, recs)
	if ne.Diagnostic["format"] != "ndjson-crap" {
		t.Errorf("expected format ndjson-crap in diagnostic, got %v", ne.Diagnostic["format"])
	}
	out, _ := ne.Diagnostic["output"].(string)
	if !strings.Contains(out, "expected 7 got 9") {
		t.Errorf("expected failure summary built from parsed crap records, got %q", out)
	}
}

func TestRunHookPhaseShape_TapNDJSONDegenerateFallback(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-tap-ndjson-degenerate")
	withFakeMadder(t)
	// Hook prints non-TAP garbage (no `TAP version 14` line) and exits
	// non-zero. The parser produces zero records, so the diagnostic's
	// output must fall back to the raw ring tail.
	writeSweatfile(t, wtPath, "[hooks]\n"+
		"pre-merge = \"echo this is not tap; exit 3\"\n"+
		"pre-merge-output-format = \"tap-ndjson\"\n")

	_, recs, err := runCheck(t, wtPath)
	if err == nil {
		t.Fatalf("expected hook failure, got nil")
	}

	assertNoTestRecords(t, recs)
	assertNodeEndExit(t, recs, 1)
	ne := singleNodeEnd(t, recs)
	if ne.Diagnostic["format"] != "tap-ndjson" {
		t.Errorf("expected format tap-ndjson in diagnostic, got %v", ne.Diagnostic["format"])
	}
	out, _ := ne.Diagnostic["output"].(string)
	if !strings.Contains(out, "this is not tap") {
		t.Errorf("expected raw tail fallback in output on degenerate stream, got %q", out)
	}
}

func TestRunHookPhaseShape_TapNDJSONAllSkipFallsBackToTail(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-tap-ndjson-all-skip")
	withFakeMadder(t)
	// Hook emits valid TAP whose only records are SKIP/TODO, then exits
	// non-zero. buildFailureSummary filters directive-bearing records, so the
	// summary is empty; the diagnostic's output must fall back to the raw
	// ring tail instead of going silent (#86, ported from PR #127).
	writeSweatfile(t, wtPath, "[hooks]\n"+
		"pre-merge = \"printf 'TAP version 14\\n1..2\\nok 1 - flaky # SKIP quarantined\\nnot ok 2 - later # TODO known gap\\n'; exit 1\"\n"+
		"pre-merge-output-format = \"tap-ndjson\"\n")

	_, recs, err := runCheck(t, wtPath)
	if err == nil {
		t.Fatalf("expected hook failure, got nil")
	}

	assertNoTestRecords(t, recs)
	assertNodeEndExit(t, recs, 1)
	ne := singleNodeEnd(t, recs)
	out, _ := ne.Diagnostic["output"].(string)
	if out == "" {
		t.Fatalf("diagnostic output empty: all-skip summary must fall back to tail")
	}
	if !strings.Contains(out, "quarantined") {
		t.Errorf("expected raw tail fallback in output on all-skip stream, got %q", out)
	}
}

func TestRunHookPhaseShape_NdjsonCrapAllSkipFallsBackToTail(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-ndjson-crap-all-skip")
	withFakeMadder(t)
	// Same bug class for the ndjson-crap format: the only record carries a
	// skip directive, buildFailureSummaryCrap returns "", and the output
	// must fall back to the ring tail.
	writeSweatfile(t, wtPath, "[hooks]\n"+
		"pre-merge = \"echo '{\\\"type\\\":\\\"test\\\",\\\"n\\\":1,\\\"description\\\":\\\"flaky\\\",\\\"ok\\\":true,\\\"directive\\\":{\\\"kind\\\":\\\"skip\\\",\\\"reason\\\":\\\"quarantined\\\"}}'; exit 1\"\n"+
		"pre-merge-output-format = \"ndjson-crap\"\n")

	_, recs, err := runCheck(t, wtPath)
	if err == nil {
		t.Fatalf("expected hook failure, got nil")
	}

	assertNoTestRecords(t, recs)
	assertNodeEndExit(t, recs, 1)
	ne := singleNodeEnd(t, recs)
	out, _ := ne.Diagnostic["output"].(string)
	if out == "" {
		t.Fatalf("diagnostic output empty: all-skip summary must fall back to tail")
	}
	if !strings.Contains(out, "quarantined") {
		t.Errorf("expected raw tail fallback in output on all-skip stream, got %q", out)
	}
}

// mergeWorktreeDirs returns the names of transient build worktrees (.merge-*)
// currently present under <repoDir>/.worktrees.
func mergeWorktreeDirs(t *testing.T, repoDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repoDir, ".worktrees"))
	if err != nil {
		t.Fatalf("read .worktrees: %v", err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".merge-") {
			names = append(names, e.Name())
		}
	}
	return names
}

// By default the pre-merge hook runs in an isolated detached build worktree
// (a .merge-* sibling), not in the session worktree, and the build worktree is
// cleaned up afterward.
func TestRunHookInBuildWorktree(t *testing.T) {
	_, repoDir, wtPath := setupRepoWithWorktree(t, "feature-build-wt")
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"pwd\"\n")

	_, recs, err := runCheck(t, wtPath)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// The hook's pwd output streams as Output records on the phase.
	if out := outputText(recs); !strings.Contains(out, ".merge-feature-build-wt-") {
		t.Errorf("expected hook to run in a .merge-* build worktree, got pwd output:\n%s", out)
	}
	if leftovers := mergeWorktreeDirs(t, repoDir); len(leftovers) != 0 {
		t.Errorf("expected build worktree cleaned up, found leftovers: %v", leftovers)
	}
}

// With [hooks].disable-merge-build-worktree the hook runs in place in the
// session worktree (legacy behavior) and no build worktree is created.
func TestRunHookInPlaceWhenDisabled(t *testing.T) {
	_, repoDir, wtPath := setupRepoWithWorktree(t, "feature-inplace")
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"pwd\"\ndisable-merge-build-worktree = true\n")

	_, recs, err := runCheck(t, wtPath)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	out := outputText(recs)
	if strings.Contains(out, ".merge-") {
		t.Errorf("expected in-place run with build worktree disabled, got:\n%s", out)
	}
	// pwd should resolve to the session worktree itself.
	if !strings.Contains(out, filepath.Base(wtPath)) {
		t.Errorf("expected hook pwd to be the session worktree %q, got:\n%s", wtPath, out)
	}
	if leftovers := mergeWorktreeDirs(t, repoDir); len(leftovers) != 0 {
		t.Errorf("expected no build worktree, found: %v", leftovers)
	}
}

// The build worktree is a clean checkout of the committed sha, so uncommitted
// changes in the session worktree are invisible to the hook (the gate verifies
// what will merge, not the dirty tree). With the feature disabled the same hook
// would see the uncommitted file — asserted in the second half.
func TestBuildWorktreeVerifiesCommittedTree(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-committed")
	// An uncommitted scratch file in the session worktree.
	if err := os.WriteFile(filepath.Join(wtPath, "scratch.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Default (isolated): hook succeeds because scratch.txt is absent in the
	// committed-sha checkout.
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"test ! -f scratch.txt\"\n")
	if _, _, err := runCheck(t, wtPath); err != nil {
		t.Fatalf("isolated hook should not see uncommitted scratch.txt: %v", err)
	}

	// Disabled (in place): the same hook now fails because scratch.txt IS
	// present in the session worktree.
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"test ! -f scratch.txt\"\ndisable-merge-build-worktree = true\n")
	if _, _, err := runCheck(t, wtPath); err == nil {
		t.Fatalf("in-place hook should see uncommitted scratch.txt and fail")
	}
}

func TestRunNoHookConfigured(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-no-hook")
	// No sweatfile written.

	_, recs, err := runCheck(t, wtPath)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	tr := singleTest(t, recs)
	if !tr.OK {
		t.Errorf("expected passing test record for no-hook case, got %+v", tr)
	}
	// Per the design: agents should treat "no hook" as a successful check
	// because there is nothing to run. The description should make that
	// reason explicit so a human reading the output is not confused.
	if !strings.Contains(strings.ToLower(tr.Description), "no pre-merge hook") {
		t.Errorf("expected 'no pre-merge hook' message, got %q", tr.Description)
	}
	if plan, summary := hasPlanAndSummary(recs); !plan || !summary {
		t.Errorf("expected plan+summary framing, got plan=%v summary=%v", plan, summary)
	}
}

// #129 regression: an interrupted prior merge can leave the physical build
// worktree directory behind with no git admin entry. `git worktree prune`
// (run by WorktreeAddDetached) is a no-op on such a dir, so the subsequent
// `git worktree add` would fail "already exists" and wedge all future merges.
// resolveHookDir must force-remove the exact buildPath before the add.
//
// Deterministic because buildPath embeds os.Getpid() (stable within the test
// process) plus the known sha/branch — calling resolveHookDir twice with the
// same args computes the same buildPath, so simulating the leftover dir at the
// first call's hookDir reproduces the collision exactly.
func TestResolveHookDirClearsStaleDir(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-stale")
	hookSha := runGit(t, wtPath, "rev-parse", "HEAD")

	// First call: create the build worktree, then clean it up.
	buildPath, cleanup, err := resolveHookDir(sweatfile.Hierarchy{}, wtPath, "feature-stale", hookSha)
	if err != nil {
		t.Fatalf("first resolveHookDir: %v", err)
	}
	cleanup()

	// Simulate an interrupted run: a leftover NON-EMPTY physical dir at the same
	// path, with no git admin entry (cleanup already removed the registration).
	// git refuses to `worktree add` into a non-empty dir, so the leftover wedges
	// the add unless resolveHookDir force-removes it first. (An empty dir would
	// not reproduce the bug — git tolerates adding into an empty target.)
	if err := os.MkdirAll(buildPath, 0o755); err != nil {
		t.Fatalf("simulate leftover dir: %v", err)
	}
	// DO NOT remove this WriteFile: git happily adds a worktree into an EMPTY
	// target dir, so without a file inside, the second resolveHookDir would
	// succeed even WITHOUT the os.RemoveAll fix — making this regression guard
	// silently useless. The non-empty dir is what actually reproduces the #129
	// "already exists" wedge.
	if err := os.WriteFile(filepath.Join(buildPath, "leftover.txt"), []byte("interrupted"), 0o644); err != nil {
		t.Fatalf("simulate leftover file: %v", err)
	}

	// Second call with identical args computes the same buildPath. Without the
	// os.RemoveAll fix this fails with "already exists".
	buildPath2, cleanup2, err := resolveHookDir(sweatfile.Hierarchy{}, wtPath, "feature-stale", hookSha)
	if err != nil {
		t.Fatalf("second resolveHookDir must not be wedged by stale dir: %v", err)
	}
	defer cleanup2()

	if buildPath2 != buildPath {
		t.Fatalf("expected identical buildPath across calls, got %q then %q", buildPath, buildPath2)
	}
	// The returned dir must be a usable worktree (has a .git file).
	if _, err := os.Stat(filepath.Join(buildPath2, ".git")); err != nil {
		t.Errorf("expected usable build worktree at %q: %v", buildPath2, err)
	}
}

// #130 regression: for an implicit (main-checkout) session wtPath is the repo
// root, so the old filepath.Dir(wtPath) put the build worktree in the repo's
// PARENT dir — outside the repo. resolveHookDir must derive the parent from the
// repo root's .worktrees/ via git.CommonDir, so even a main checkout lands
// under <repo>/.worktrees/.
func TestResolveHookDirMainCheckoutPlacement(t *testing.T) {
	_, repoDir := setupRepo(t)
	hookSha := runGit(t, repoDir, "rev-parse", "HEAD")

	// Implicit session: wtPath == repoDir.
	buildPath, cleanup, err := resolveHookDir(sweatfile.Hierarchy{}, repoDir, "main", hookSha)
	if err != nil {
		t.Fatalf("resolveHookDir for main checkout: %v", err)
	}
	defer cleanup()

	wantPrefix := filepath.Join(repoDir, ".worktrees") + string(filepath.Separator)
	if !strings.HasPrefix(buildPath, wantPrefix) {
		t.Errorf("expected build worktree under %q, got %q", wantPrefix, buildPath)
	}
	// And NOT a sibling of the repo (the old buggy location).
	if filepath.Dir(buildPath) == filepath.Dir(repoDir) {
		t.Errorf("build worktree landed in repo's parent dir (the #130 bug): %q", buildPath)
	}

	// Sanity: CommonDir on a main checkout returns the repo root.
	root, err := git.CommonDir(repoDir)
	if err != nil {
		t.Fatalf("CommonDir(repoDir): %v", err)
	}
	if root != repoDir {
		t.Errorf("CommonDir(main checkout) = %q, want repo root %q", root, repoDir)
	}
}
