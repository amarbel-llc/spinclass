package sweatfile_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

// A top-level [[post-merge]] array decodes to named targets with an optional
// verify, distinct from the legacy [hooks].post-merge scalar (FDR 0026).
func TestParsePostMergeTargets(t *testing.T) {
	input := []byte(`
[hooks]
post-merge = "legacy-blob"

[[post-merge]]
name    = "krone"
command = "trigger-krone"
verify  = "check-krone"

[[post-merge]]
name    = "nikulin"
command = "trigger-nikulin"
`)
	doc, err := sweatfileio.Parse(input)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if u := doc.Undecoded(); len(u) != 0 {
		t.Errorf("unexpected undecoded keys: %v", u)
	}
	sf := *doc.Data()

	// Legacy scalar and named targets coexist in config.
	if got := sf.PostMergeHookCommand(); got == nil || *got != "legacy-blob" {
		t.Errorf("legacy [hooks].post-merge: got %v", got)
	}
	if len(sf.PostMerge) != 2 {
		t.Fatalf("PostMerge: expected 2, got %d (%+v)", len(sf.PostMerge), sf.PostMerge)
	}
	if sf.PostMerge[0].Name != "krone" || sf.PostMerge[0].Command != "trigger-krone" {
		t.Errorf("PostMerge[0]: got %+v", sf.PostMerge[0])
	}
	if !sf.PostMerge[0].HasVerify() || *sf.PostMerge[0].Verify != "check-krone" {
		t.Errorf("PostMerge[0].Verify: got %v", sf.PostMerge[0].Verify)
	}
	if sf.PostMerge[1].Name != "nikulin" || sf.PostMerge[1].HasVerify() {
		t.Errorf("PostMerge[1]: got %+v (verify should be unset)", sf.PostMerge[1])
	}
}

// An empty top-level array is a distinct, non-nil value — the "clear inherited"
// signal, parallel to allowed-mcps = [].
func TestParsePostMergeEmptyArray(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte(`post-merge = []`))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	sf := *doc.Data()
	if sf.PostMerge == nil {
		t.Error("expected non-nil empty slice for an explicit empty array")
	}
	if len(sf.PostMerge) != 0 {
		t.Errorf("expected empty, got %v", sf.PostMerge)
	}
}

// A name-only target (no command) is a removal sentinel: retained by the merge
// (so it can override an inherited target) but filtered by
// ActivePostMergeTargets, mirroring [[mcps]] / ActiveMCPs.
func TestActivePostMergeTargetsFiltersSentinels(t *testing.T) {
	sf := Sweatfile{PostMerge: []PostMergeTarget{
		{Name: "krone", Command: "deploy"},
		{Name: "stale"},                     // removal sentinel
		{Name: "blank", Command: "   \n  "}, // whitespace-only == unset
	}}
	active := sf.ActivePostMergeTargets()
	if len(active) != 1 || active[0].Name != "krone" {
		t.Errorf("ActivePostMergeTargets: got %+v, want only krone", active)
	}
}

func TestPostMergePhaseActive(t *testing.T) {
	tt := true
	cases := []struct {
		name string
		sf   Sweatfile
		want bool
	}{
		{"nothing", Sweatfile{}, false},
		{"legacy string only", Sweatfile{Hooks: &Hooks{PostMerge: sptr("deploy")}}, true},
		{"named target only", Sweatfile{PostMerge: []PostMergeTarget{{Name: "k", Command: "deploy"}}}, true},
		{"only a sentinel", Sweatfile{PostMerge: []PostMergeTarget{{Name: "k"}}}, false},
		{
			"disabled suppresses targets",
			Sweatfile{
				Hooks:     &Hooks{DisablePostMerge: &tt},
				PostMerge: []PostMergeTarget{{Name: "k", Command: "deploy"}},
			},
			false,
		},
		{
			"disabled suppresses legacy string",
			Sweatfile{Hooks: &Hooks{PostMerge: sptr("deploy"), DisablePostMerge: &tt}},
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.sf.PostMergePhaseActive(); got != c.want {
				t.Errorf("PostMergePhaseActive() = %v, want %v", got, c.want)
			}
		})
	}
}

// Merge is dedup-by-name (same pattern as [[mcps]]): a child target overrides
// the inherited one of the same name, new names append, and a name-only child
// entry becomes a removal sentinel at consumption.
func TestMergePostMergeTargetsDedupByName(t *testing.T) {
	parent := Sweatfile{PostMerge: []PostMergeTarget{
		{Name: "krone", Command: "old-krone"},
		{Name: "nikulin", Command: "deploy-nikulin"},
	}}
	child := Sweatfile{PostMerge: []PostMergeTarget{
		{Name: "krone", Command: "new-krone", Verify: sptr("check")}, // override
		{Name: "vlad", Command: "deploy-vlad"},                       // append
		{Name: "nikulin"},                                            // remove
	}}
	merged := parent.MergeWith(child)

	byName := map[string]PostMergeTarget{}
	for _, tgt := range merged.PostMerge {
		byName[tgt.Name] = tgt
	}
	if got := byName["krone"]; got.Command != "new-krone" || !got.HasVerify() {
		t.Errorf("krone should be overridden with verify: got %+v", got)
	}
	if got := byName["vlad"]; got.Command != "deploy-vlad" {
		t.Errorf("vlad should be appended: got %+v", got)
	}
	// nikulin retained in the raw list (as a sentinel) but filtered by Active.
	active := map[string]bool{}
	for _, tgt := range merged.ActivePostMergeTargets() {
		active[tgt.Name] = true
	}
	if active["nikulin"] {
		t.Error("nikulin name-only child entry should remove the inherited target")
	}
	if !active["krone"] || !active["vlad"] {
		t.Errorf("krone and vlad should be active: %v", active)
	}
}

// A nil child PostMerge inherits the parent's targets unchanged.
func TestMergePostMergeInheritWhenNil(t *testing.T) {
	parent := Sweatfile{PostMerge: []PostMergeTarget{{Name: "krone", Command: "deploy"}}}
	merged := parent.MergeWith(Sweatfile{})
	if len(merged.PostMerge) != 1 || merged.PostMerge[0].Name != "krone" {
		t.Errorf("nil child should inherit: got %+v", merged.PostMerge)
	}
}

// Run: command succeeds, no verify => ok (opaque-command / graceful degradation).
func TestPostMergeTargetRunCommandOnlyOK(t *testing.T) {
	dir := t.TempDir()
	tgt := PostMergeTarget{Name: "k", Command: "echo deployed"}
	var buf bytes.Buffer
	verdict, err := tgt.Run(context.Background(), dir, nil, &buf)
	if err != nil || verdict != PostMergeOK {
		t.Fatalf("got verdict=%q err=%v out=%q", verdict, err, buf.String())
	}
	if !strings.Contains(buf.String(), "deployed") {
		t.Errorf("command output not streamed: %q", buf.String())
	}
}

// Run: command fails => command-failed, and verify is NOT run.
func TestPostMergeTargetRunCommandFailedSkipsVerify(t *testing.T) {
	dir := t.TempDir()
	verifyMarker := filepath.Join(dir, "verify-ran")
	tgt := PostMergeTarget{
		Name:    "k",
		Command: "echo boom >&2; exit 3",
		Verify:  sptr("touch " + verifyMarker),
	}
	var buf bytes.Buffer
	verdict, err := tgt.Run(context.Background(), dir, nil, &buf)
	if err == nil || verdict != PostMergeCommandFailed {
		t.Fatalf("got verdict=%q err=%v", verdict, err)
	}
	if _, statErr := os.Stat(verifyMarker); statErr == nil {
		t.Error("verify must NOT run when the command failed")
	}
}

// Run: command succeeds, verify succeeds => ok, verify observed the env.
func TestPostMergeTargetRunVerifyOK(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "verify-out")
	tgt := PostMergeTarget{
		Name:    "k",
		Command: "echo triggered",
		Verify:  sptr("echo sha=$SPINCLASS_MERGED_SHA > " + out),
	}
	var buf bytes.Buffer
	verdict, err := tgt.Run(context.Background(), dir, []string{"SPINCLASS_MERGED_SHA=abc123"}, &buf)
	if err != nil || verdict != PostMergeOK {
		t.Fatalf("got verdict=%q err=%v out=%q", verdict, err, buf.String())
	}
	raw, readErr := os.ReadFile(out)
	if readErr != nil {
		t.Fatalf("verify did not run: %v", readErr)
	}
	if got := strings.TrimSpace(string(raw)); got != "sha=abc123" {
		t.Errorf("verify env: got %q, want sha=abc123", got)
	}
}

// Run: command succeeds but verify fails => verify-failed (the split that tells
// a human "investigate the probe/ack path" rather than "fix the change").
func TestPostMergeTargetRunVerifyFailed(t *testing.T) {
	dir := t.TempDir()
	tgt := PostMergeTarget{
		Name:    "k",
		Command: "echo triggered",
		Verify:  sptr("echo unhealthy >&2; exit 1"),
	}
	var buf bytes.Buffer
	verdict, err := tgt.Run(context.Background(), dir, nil, &buf)
	if err == nil || verdict != PostMergeVerifyFailed {
		t.Fatalf("got verdict=%q err=%v", verdict, err)
	}
	if !strings.Contains(buf.String(), "unhealthy") {
		t.Errorf("verify output not streamed: %q", buf.String())
	}
}
