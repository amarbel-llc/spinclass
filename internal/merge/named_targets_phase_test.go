package merge

import (
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/ndjsoncrap"

	"code.linenisgreat.com/spinclass/internal/git"
)

// landed reports whether the feature merge reached main (a.txt is present).
func landed(t *testing.T, repoDir string) bool {
	t.Helper()
	out, err := git.Run(repoDir, "show", "main:a.txt")
	return err == nil && strings.TrimSpace(out) == "a"
}

// pmNode is a decoded view of one post-merge execution node (FDR 0026 /
// spinclass#276): its label, its terminal verdict (node_end exit code), the
// producer diagnostic riding on the node_end, the output streamed as Output
// records under it, and the stream index of its node_start (for asserting
// declaration order).
type pmNode struct {
	name     string
	exitOK   bool
	diag     map[string]any
	output   string
	startIdx int
}

// postMergeNodes joins the NodeStart/Output/NodeEnd records by node id and
// returns, in node_start order, every execution node whose label names a
// post-merge target. The named [[post-merge]] path emits these instead of
// result-family Test points, so its output attribution and verdict are carried
// structurally by node id (crap's muxing model), not by a text prefix.
func postMergeNodes(recs []ndjsoncrap.Record) []pmNode {
	type acc struct {
		name     string
		startIdx int
		out      strings.Builder
		end      *ndjsoncrap.NodeEnd
	}
	byTP := map[int]*acc{}
	var order []int
	for i, rec := range recs {
		switch r := rec.(type) {
		case ndjsoncrap.NodeStart:
			byTP[r.TP] = &acc{name: r.Name, startIdx: i}
			order = append(order, r.TP)
		case ndjsoncrap.Output:
			if a := byTP[r.TP]; a != nil {
				a.out.WriteString(r.Data)
			}
		case ndjsoncrap.NodeEnd:
			if a := byTP[r.TP]; a != nil {
				end := r
				a.end = &end
			}
		}
	}
	var nodes []pmNode
	for _, tp := range order {
		a := byTP[tp]
		if !strings.HasPrefix(a.name, "post-merge ") {
			continue
		}
		n := pmNode{name: a.name, output: a.out.String(), startIdx: a.startIdx}
		if a.end != nil {
			n.exitOK = a.end.ExitCode != nil && *a.end.ExitCode == 0
			n.diag = a.end.Diagnostic
		}
		nodes = append(nodes, n)
	}
	return nodes
}

// findNode returns the first post-merge node whose label has the given prefix.
func findNode(recs []ndjsoncrap.Record, prefix string) (pmNode, bool) {
	for _, n := range postMergeNodes(recs) {
		if strings.HasPrefix(n.name, prefix) {
			return n, true
		}
	}
	return pmNode{}, false
}

// nodeNames lists post-merge node labels, for failure messages.
func nodeNames(recs []ndjsoncrap.Record) []string {
	var names []string
	for _, n := range postMergeNodes(recs) {
		names = append(names, n.name)
	}
	return names
}

// diagString reads a string-valued diagnostic field (verdict/severity/message),
// returning "" when absent or non-string.
func diagString(diag map[string]any, key string) string {
	if diag == nil {
		return ""
	}
	s, _ := diag[key].(string)
	return s
}

// A named target with a verify that passes is a node closed with exit 0, and the
// merge lands.
func TestNamedPostMergeTargetVerifyOK(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[[post-merge]]
name = "krone"
command = "echo triggered"
verify = "echo healthy"
`)
	recs, err := runFinishTargets(t, repoDir, wtPath, "feature", false, nil)
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	if !landed(t, repoDir) {
		t.Fatal("merge did not land")
	}
	n, ok := findNode(recs, "post-merge krone")
	if !ok || !n.exitOK {
		t.Fatalf("expected ok post-merge krone node, got %+v (all: %v)", n, nodeNames(recs))
	}
}

// A verify that fails is verdict=verify-failed — distinct from a command
// failure — and is non-fatal: the node closes with a failing verdict but the
// merge still lands.
func TestNamedPostMergeTargetVerifyFailed(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[[post-merge]]
name = "krone"
command = "echo triggered"
verify = "echo unhealthy >&2; exit 1"
`)
	recs, err := runFinishTargets(t, repoDir, wtPath, "feature", false, nil)
	if err != nil {
		t.Fatalf("a verify failure must not fail the merge, got %v", err)
	}
	if !landed(t, repoDir) {
		t.Fatal("merge did not land")
	}
	n, ok := findNode(recs, "post-merge krone")
	if !ok || n.exitOK {
		t.Fatalf("expected a failing post-merge krone node, got %+v", n)
	}
	if v := diagString(n.diag, "verdict"); v != "verify-failed" {
		t.Errorf("verdict = %q, want verify-failed", v)
	}
	if sev := diagString(n.diag, "severity"); sev != "warn" {
		t.Errorf("severity = %q, want warn", sev)
	}
	// The verify's output streamed as the node's own output records.
	if !strings.Contains(n.output, "unhealthy") {
		t.Errorf("verify output not surfaced on the node: %q", n.output)
	}
}

// A command failure is verdict=command-failed, and the verify is not reached.
func TestNamedPostMergeTargetCommandFailed(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[[post-merge]]
name = "krone"
command = "echo trigger-broke >&2; exit 2"
verify = "echo should-not-run"
`)
	recs, err := runFinishTargets(t, repoDir, wtPath, "feature", false, nil)
	if err != nil {
		t.Fatalf("a command failure must not fail the merge, got %v", err)
	}
	if !landed(t, repoDir) {
		t.Fatal("merge did not land")
	}
	n, ok := findNode(recs, "post-merge krone")
	if !ok || n.exitOK {
		t.Fatalf("expected a failing post-merge krone node, got %+v", n)
	}
	if v := diagString(n.diag, "verdict"); v != "command-failed" {
		t.Errorf("verdict = %q, want command-failed", v)
	}
	if strings.Contains(n.output, "should-not-run") {
		t.Errorf("verify must not run after a command failure: %q", n.output)
	}
}

// Named targets supersede the legacy [hooks].post-merge string: with both
// configured, only the named target runs (FDR 0026) — as a node, and the legacy
// string emits neither a node nor a result-family point.
func TestNamedTargetsSupersedeLegacyString(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[hooks]
post-merge = "echo LEGACY-RAN"

[[post-merge]]
name = "krone"
command = "echo krone-deployed"
`)
	recs, err := runFinishTargets(t, repoDir, wtPath, "feature", false, nil)
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	if _, ok := findNode(recs, "post-merge krone"); !ok {
		t.Fatalf("expected the named target node to run: %v", nodeNames(recs))
	}
	// The legacy string's point would be labeled "post-merge feature" — it must
	// appear as neither a node nor a result-family test point.
	if n, ok := findNode(recs, "post-merge feature"); ok {
		t.Errorf("legacy [hooks].post-merge must be superseded, got node %+v", n)
	}
	if tr, ok := findTest(testRecords(recs), "post-merge feature"); ok {
		t.Errorf("legacy [hooks].post-merge must be superseded, got point %+v", tr)
	}
}

// A targets selection runs only the named subset; unselected targets do not run.
func TestPostMergeSelectionSubset(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[[post-merge]]
name = "krone"
command = "echo krone"

[[post-merge]]
name = "nikulin"
command = "echo nikulin"
`)
	recs, err := runFinishTargets(t, repoDir, wtPath, "feature", false, []string{"krone"})
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	if _, ok := findNode(recs, "post-merge krone"); !ok {
		t.Errorf("krone should have run: %v", nodeNames(recs))
	}
	if n, ok := findNode(recs, "post-merge nikulin"); ok {
		t.Errorf("nikulin should NOT have run under targets=[krone], got %+v", n)
	}
}

// An empty selection deploys nothing (a docs-only merge), yet still lands.
func TestPostMergeSelectionNoneSkipsAll(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[[post-merge]]
name = "krone"
command = "echo krone"
`)
	recs, err := runFinishTargets(t, repoDir, wtPath, "feature", false, []string{})
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	if !landed(t, repoDir) {
		t.Fatal("an empty selection must still land the merge")
	}
	if n, ok := findNode(recs, "post-merge "); ok {
		t.Errorf("empty selection must run no post-merge target, got %+v", n)
	}
}

// Selecting a name no target declares fails the merge BEFORE it lands — a typo
// must not silently skip the intended deploy and land anyway (FDR 0026). The
// pre-landing validation lives in PrepareMerge, so it surfaces as a failing
// result-family "post-merge selection" point, not a post-merge node.
func TestPostMergeSelectionUnknownFailsPreLanding(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[[post-merge]]
name = "krone"
command = "echo krone"
`)
	mainBefore := runGit(t, repoDir, "rev-parse", "main")
	recs, err := runFinishTargets(t, repoDir, wtPath, "feature", false, []string{"kron"})
	if err == nil {
		t.Fatal("an unknown post-merge target must fail the merge")
	}
	if mainAfter := runGit(t, repoDir, "rev-parse", "main"); mainAfter != mainBefore {
		t.Errorf("merge must not land on an unknown-target selection: main moved %s -> %s", mainBefore, mainAfter)
	}
	tests := testRecords(recs)
	tr, ok := findTest(tests, "post-merge selection")
	if !ok || tr.OK {
		t.Fatalf("expected a failing 'post-merge selection' point, got %+v (%v)", tr, testDescs(tests))
	}
	if msg := diagString(tr.Diagnostic, "message"); !strings.Contains(msg, "kron") {
		t.Errorf("selection error should name the unknown target: %v", tr.Diagnostic)
	}
}

// Multiple targets run and a half-failure (one target fails) is non-fatal: the
// merge lands and every target reports its own verdict, with node_starts in
// declaration order (krone before nikulin).
func TestNamedTargetsHalfFailureIsNonFatal(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[[post-merge]]
name = "krone"
command = "echo krone-ok"

[[post-merge]]
name = "nikulin"
command = "echo nikulin-broke >&2; exit 1"
`)
	recs, err := runFinishTargets(t, repoDir, wtPath, "feature", false, nil)
	if err != nil {
		t.Fatalf("a half-failing multi-target phase must not fail the merge, got %v", err)
	}
	if !landed(t, repoDir) {
		t.Fatal("merge did not land")
	}
	krone, kok := findNode(recs, "post-merge krone")
	nikulin, nok := findNode(recs, "post-merge nikulin")
	if !kok || !krone.exitOK {
		t.Errorf("krone should be ok: %+v", krone)
	}
	if !nok || nikulin.exitOK {
		t.Errorf("nikulin should be not-ok: %+v", nikulin)
	}
	// Declaration order: krone's node_start precedes nikulin's in the stream.
	if krone.startIdx > nikulin.startIdx {
		t.Errorf("targets should appear in declaration order (krone before nikulin): krone@%d nikulin@%d", krone.startIdx, nikulin.startIdx)
	}
}

// Selected targets run CONCURRENTLY (spinclass#276), proved deterministically
// with a rendezvous barrier: each target touches its own marker, then waits
// (bounded) for the sibling's. If the phase ran sequentially, the first target
// would wait out its whole bound for a marker the second has not yet had a
// chance to write, and self-report exit 1 — so "both ok" is a parallel-only
// outcome, with no reliance on wall-clock timing assertions.
func TestNamedTargetsRunConcurrently(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	dir := t.TempDir()
	barrier := func(self, other string) string {
		// Up to ~5s (100 * 0.05s) waiting for the sibling; exit 1 if it never
		// shows, which is exactly the sequential-regression signature.
		return "touch " + dir + "/" + self + ".started; " +
			"for i in $(seq 1 100); do [ -f " + dir + "/" + other + ".started ] && exit 0; sleep 0.05; done; " +
			"echo '" + self + ": " + other + " never started (ran sequentially?)' >&2; exit 1"
	}
	writeRepoSweatfile(t, repoDir, "\n"+
		"[[post-merge]]\n"+
		"name = \"krone\"\n"+
		"command = "+quoteTOML(barrier("krone", "nikulin"))+"\n"+
		"\n"+
		"[[post-merge]]\n"+
		"name = \"nikulin\"\n"+
		"command = "+quoteTOML(barrier("nikulin", "krone"))+"\n")

	recs, err := runFinishTargets(t, repoDir, wtPath, "feature", false, nil)
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	if !landed(t, repoDir) {
		t.Fatal("merge did not land")
	}
	for _, name := range []string{"krone", "nikulin"} {
		n, ok := findNode(recs, "post-merge "+name)
		if !ok || !n.exitOK {
			t.Fatalf("%s should be ok — targets must run concurrently, not sequentially: %+v (all: %v)", name, n, nodeNames(recs))
		}
	}
}

// Node verdicts stay in DECLARATION order even when a later-declared target
// finishes first (spinclass#276): node_starts are allocated up front in
// declaration order, so the ladder is deterministic regardless of scheduling.
func TestNamedTargetsEmitInDeclarationOrderRegardlessOfFinish(t *testing.T) {
	repoDir, wtPath := setupPostMergeRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, `
[[post-merge]]
name = "krone"
command = "sleep 0.4; echo krone-ok"

[[post-merge]]
name = "nikulin"
command = "echo nikulin-ok"
`)
	recs, err := runFinishTargets(t, repoDir, wtPath, "feature", false, nil)
	if err != nil {
		t.Fatalf("FinishMerge: %v", err)
	}
	krone, kok := findNode(recs, "post-merge krone")
	nikulin, nok := findNode(recs, "post-merge nikulin")
	if !kok || !krone.exitOK {
		t.Errorf("krone should be ok: %+v", krone)
	}
	if !nok || !nikulin.exitOK {
		t.Errorf("nikulin should be ok: %+v", nikulin)
	}
	// nikulin finishes first (krone sleeps), but krone is declared first, so its
	// node_start must still precede nikulin's in the stream.
	if krone.startIdx > nikulin.startIdx {
		t.Errorf("node order must be declaration order (krone before nikulin) despite finish order: krone@%d nikulin@%d", krone.startIdx, nikulin.startIdx)
	}
}

// quoteTOML renders s as a TOML basic string (double-quoted, backslash/quote
// escaped) so a multi-command shell string embeds cleanly in a sweatfile.
func quoteTOML(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
