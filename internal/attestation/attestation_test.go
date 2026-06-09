package attestation

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
)

func TestValidateStrictOnPresence(t *testing.T) {
	required := []sweatfile.PreMergeSkill{
		{Name: "eng:code-reviewer", Rationale: "Required."},
		{Name: "simplify", Rationale: "Prune."},
	}

	verr := Validate(required, []session.AttestedSkill{
		{Name: "eng:code-reviewer", Used: true, Reasoning: "Reviewed."},
	})
	if verr.Empty() {
		t.Fatalf("expected missing 'simplify', got clean: %+v", verr)
	}
	if len(verr.Missing) != 1 || verr.Missing[0] != "simplify" {
		t.Errorf("Missing: got %v", verr.Missing)
	}
}

func TestValidateRejectsUnrecognised(t *testing.T) {
	required := []sweatfile.PreMergeSkill{
		{Name: "eng:code-reviewer", Rationale: "Required."},
	}

	verr := Validate(required, []session.AttestedSkill{
		{Name: "eng:code-reviewer", Used: true, Reasoning: "Reviewed."},
		{Name: "stranger", Used: true, Reasoning: "Not on the list."},
	})
	if verr.Empty() {
		t.Fatalf("expected unrecognised entry, got clean")
	}
	if len(verr.Unrecognised) != 1 || verr.Unrecognised[0] != "stranger" {
		t.Errorf("Unrecognised: got %v", verr.Unrecognised)
	}
}

func TestValidateRejectsDuplicate(t *testing.T) {
	required := []sweatfile.PreMergeSkill{
		{Name: "eng:code-reviewer", Rationale: "Required."},
	}

	verr := Validate(required, []session.AttestedSkill{
		{Name: "eng:code-reviewer", Used: true, Reasoning: "First."},
		{Name: "eng:code-reviewer", Used: false, Reasoning: "Second."},
	})
	if verr.Empty() {
		t.Fatalf("expected duplicate flagged, got clean")
	}
	found := false
	for _, u := range verr.Unrecognised {
		if strings.Contains(u, "duplicate") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected (duplicate) marker in Unrecognised %v", verr.Unrecognised)
	}
}

func TestValidateRejectsEmptyReasoning(t *testing.T) {
	required := []sweatfile.PreMergeSkill{
		{Name: "eng:code-reviewer", Rationale: "Required."},
	}

	verr := Validate(required, []session.AttestedSkill{
		{Name: "eng:code-reviewer", Used: true, Reasoning: "   "},
	})
	if verr.Empty() {
		t.Fatalf("expected empty-reasoning flagged, got clean")
	}
	if len(verr.EmptyReasoning) != 1 || verr.EmptyReasoning[0] != "eng:code-reviewer" {
		t.Errorf("EmptyReasoning: got %v", verr.EmptyReasoning)
	}
}

func TestValidateAcceptsCleanInput(t *testing.T) {
	required := []sweatfile.PreMergeSkill{
		{Name: "eng:code-reviewer", Rationale: "Required."},
		{Name: "simplify", Rationale: "Prune."},
	}

	verr := Validate(required, []session.AttestedSkill{
		{Name: "eng:code-reviewer", Used: true, Reasoning: "Reviewed."},
		{Name: "simplify", Used: false, Reasoning: "Trivial diff."},
	})
	if !verr.Empty() {
		t.Errorf("expected clean, got %+v", verr)
	}
}

// setupGateSession creates a tempdir-rooted session that session.Read /
// session.Write can locate. Returns (repoPath, branch).
func setupGateSession(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "xdg-state"))
	repo := filepath.Join(base, "repo")
	branch := "gate-branch"
	wt := filepath.Join(repo, ".worktrees", branch)
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	st := session.State{
		PID:          12345,
		SessionState: session.StateActive,
		RepoPath:     repo,
		WorktreePath: wt,
		Branch:       branch,
		SessionKey:   filepath.Base(repo) + "/" + branch,
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC().Truncate(time.Second),
	}
	if err := session.Write(st); err != nil {
		t.Fatal(err)
	}
	return repo, branch
}

func TestCheckDormantWhenNoSkills(t *testing.T) {
	repo, branch := setupGateSession(t)

	merged := sweatfile.Sweatfile{}
	ok, output, err := Check(merged, repo, branch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Errorf("expected gate dormant, got !ok with output: %s", output)
	}
	if output != "" {
		t.Errorf("expected empty output, got %q", output)
	}
}

func TestCheckFailsWithoutAttestation(t *testing.T) {
	repo, branch := setupGateSession(t)

	merged := sweatfile.Sweatfile{
		PreMergeSkills: []sweatfile.PreMergeSkill{
			{Name: "eng:code-reviewer", Rationale: "Required."},
		},
	}
	ok, output, err := Check(merged, repo, branch)
	if !errors.Is(err, ErrAttestationRequired) {
		t.Fatalf("expected ErrAttestationRequired, got %v", err)
	}
	if ok {
		t.Errorf("expected gate to fail, got ok=true")
	}
	if !strings.Contains(output, "not ok 1 - pre-merge skill attestation missing") {
		t.Errorf("output missing structured TAP failure: %s", output)
	}
	if !strings.Contains(output, "required_tool: nothing-but-the-truth") {
		t.Errorf("output missing required_tool: %s", output)
	}
	if !strings.Contains(output, "eng:code-reviewer") {
		t.Errorf("output missing required skill name: %s", output)
	}
	if !strings.Contains(output, `rationale: "Required."`) {
		t.Errorf("output missing rationale quoting: %s", output)
	}
}

func TestCheckConsumesBufferedAttestation(t *testing.T) {
	repo, branch := setupGateSession(t)

	required := []sweatfile.PreMergeSkill{
		{Name: "eng:code-reviewer", Rationale: "Required."},
	}
	if err := Record(repo, branch, []session.AttestedSkill{
		{Name: "eng:code-reviewer", Used: true, Reasoning: "Done."},
	}); err != nil {
		t.Fatal(err)
	}

	merged := sweatfile.Sweatfile{PreMergeSkills: required}

	// First Check consumes the attestation.
	ok, output, err := Check(merged, repo, branch)
	if err != nil {
		t.Fatalf("first Check returned error: %v", err)
	}
	if !ok {
		t.Fatalf("first Check expected to pass, got !ok with output: %s", output)
	}

	// Verify state was cleared.
	st, err := session.Read(repo, branch)
	if err != nil {
		t.Fatal(err)
	}
	if st.PreMergeAttestation != nil {
		t.Errorf("expected attestation cleared after consume, got %+v", st.PreMergeAttestation)
	}

	// Second Check (no fresh attestation) must fail.
	ok2, _, err2 := Check(merged, repo, branch)
	if !errors.Is(err2, ErrAttestationRequired) {
		t.Errorf("second Check: expected ErrAttestationRequired, got %v", err2)
	}
	if ok2 {
		t.Error("second Check expected to fail, got ok=true")
	}
}

func TestRecordRoundTrip(t *testing.T) {
	repo, branch := setupGateSession(t)

	skills := []session.AttestedSkill{
		{Name: "eng:code-reviewer", Used: true, Reasoning: "Reviewed."},
		{Name: "simplify", Used: false, Reasoning: "Trivial."},
	}
	if err := Record(repo, branch, skills); err != nil {
		t.Fatal(err)
	}

	st, err := session.Read(repo, branch)
	if err != nil {
		t.Fatal(err)
	}
	if st.PreMergeAttestation == nil {
		t.Fatal("expected attestation, got nil")
	}
	if len(st.PreMergeAttestation.Skills) != 2 {
		t.Errorf("Skills: got %d, want 2", len(st.PreMergeAttestation.Skills))
	}
}

// setupImplicitSession materializes a live implicit (main-checkout) session
// under an XDG-isolated temp checkout. Returns the checkout root.
func setupImplicitSession(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "xdg-state"))
	checkout := filepath.Join(base, "checkout")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	st := session.State{
		Kind:         session.KindImplicit,
		PID:          os.Getpid(),
		SessionState: session.StateActive,
		WorktreePath: checkout,
		Branch:       "master",
		SessionKey:   "r/master-deadbeef",
	}
	if err := session.WriteImplicit(st, "deadbeef"); err != nil {
		t.Fatal(err)
	}
	return checkout
}

func TestRecordImplicitAndCheckImplicitRoundTrip(t *testing.T) {
	checkout := setupImplicitSession(t)

	required := []sweatfile.PreMergeSkill{
		{Name: "eng:code-reviewer", Rationale: "Required."},
	}
	merged := sweatfile.Sweatfile{PreMergeSkills: required}

	if err := RecordImplicit(checkout, []session.AttestedSkill{
		{Name: "eng:code-reviewer", Used: true, Reasoning: "x"},
	}); err != nil {
		t.Fatalf("RecordImplicit: %v", err)
	}

	// First CheckImplicit consumes the buffered attestation.
	ok, output, err := CheckImplicit(merged, checkout)
	if err != nil {
		t.Fatalf("first CheckImplicit returned error: %v", err)
	}
	if !ok {
		t.Fatalf("first CheckImplicit expected to pass, got !ok with output: %s", output)
	}
	if output != "" {
		t.Errorf("first CheckImplicit expected empty output, got %q", output)
	}

	// Second CheckImplicit must fail (single-use consumed).
	ok2, output2, err2 := CheckImplicit(merged, checkout)
	if !errors.Is(err2, ErrAttestationRequired) {
		t.Fatalf("second CheckImplicit: expected ErrAttestationRequired, got %v", err2)
	}
	if ok2 {
		t.Error("second CheckImplicit expected to fail, got ok=true")
	}
	if !strings.Contains(output2, "not ok 1 - pre-merge skill attestation missing") {
		t.Errorf("second CheckImplicit output missing structured TAP failure: %s", output2)
	}
	if !strings.Contains(output2, "required_tool: nothing-but-the-truth") {
		t.Errorf("second CheckImplicit output missing required_tool: %s", output2)
	}
	if !strings.Contains(output2, "eng:code-reviewer") {
		t.Errorf("second CheckImplicit output missing required skill name: %s", output2)
	}
}

func TestCheckImplicitDormantWhenNoSkills(t *testing.T) {
	checkout := setupImplicitSession(t)

	merged := sweatfile.Sweatfile{}
	ok, output, err := CheckImplicit(merged, checkout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Errorf("expected gate dormant, got !ok with output: %s", output)
	}
	if output != "" {
		t.Errorf("expected empty output, got %q", output)
	}
}

func TestCheckImplicitFailsWhenNoAttestation(t *testing.T) {
	checkout := setupImplicitSession(t)

	merged := sweatfile.Sweatfile{
		PreMergeSkills: []sweatfile.PreMergeSkill{
			{Name: "eng:code-reviewer", Rationale: "Required."},
		},
	}
	ok, output, err := CheckImplicit(merged, checkout)
	if !errors.Is(err, ErrAttestationRequired) {
		t.Fatalf("expected ErrAttestationRequired, got %v", err)
	}
	if ok {
		t.Errorf("expected gate to fail, got ok=true")
	}
	if !strings.Contains(output, "not ok 1 - pre-merge skill attestation missing") {
		t.Errorf("output missing structured TAP failure: %s", output)
	}
	if !strings.Contains(output, "required_tool: nothing-but-the-truth") {
		t.Errorf("output missing required_tool: %s", output)
	}
	if !strings.Contains(output, "eng:code-reviewer") {
		t.Errorf("output missing required skill name: %s", output)
	}
	if !strings.Contains(output, `rationale: "Required."`) {
		t.Errorf("output missing rationale quoting: %s", output)
	}
}

func TestRenderRequiredSkillsQuotesScalar(t *testing.T) {
	required := []sweatfile.PreMergeSkill{
		{Name: "with-quote", Rationale: `Has a "quote" and \backslash.`},
	}
	out := renderRequiredSkills(required)
	if !strings.Contains(out, `\"`) {
		t.Errorf("expected escaped double-quote in output, got %s", out)
	}
	if !strings.Contains(out, `\\`) {
		t.Errorf("expected escaped backslash in output, got %s", out)
	}
}
