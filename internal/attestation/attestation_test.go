package attestation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
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
	if err := Record(WorktreeSlot(repo, branch), []session.AttestedSkill{
		{Name: "eng:code-reviewer", Used: true, Reasoning: "Done."},
	}, ""); err != nil {
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
	if err := Record(WorktreeSlot(repo, branch), skills, ""); err != nil {
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
		SessionKey:   "r/deadbeef",
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

	if err := Record(ImplicitSlot(checkout), []session.AttestedSkill{
		{Name: "eng:code-reviewer", Used: true, Reasoning: "x"},
	}, ""); err != nil {
		t.Fatalf("Record implicit: %v", err)
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

var gateRequired = []sweatfile.PreMergeSkill{
	{Name: "eng:code-reviewer", Rationale: "Required."},
}

var gateSkills = []session.AttestedSkill{
	{Name: "eng:code-reviewer", Used: true, Reasoning: "Done."},
}

func liveGate() sweatfile.Sweatfile {
	return sweatfile.Sweatfile{PreMergeSkills: gateRequired}
}

// deadPID is above any kernel pid_max, so kill(2) reports ESRCH.
func deadPID(t *testing.T) int {
	t.Helper()
	return 1 << 30
}

func readAttestation(t *testing.T, slot Slot) *session.PreMergeAttestation {
	t.Helper()
	st, err := slot.load()
	if err != nil {
		t.Fatal(err)
	}
	return st.PreMergeAttestation
}

func TestPeekAndClaimFailWithoutAttestation(t *testing.T) {
	repo, branch := setupGateSession(t)
	slot := WorktreeSlot(repo, branch)
	merged := liveGate()

	check := func(name, output string, err error) {
		t.Helper()
		if !errors.Is(err, ErrAttestationRequired) {
			t.Fatalf("%s: expected ErrAttestationRequired, got %v", name, err)
		}
		for _, want := range []string{
			"not ok 1 - pre-merge skill attestation missing",
			"required_tool: nothing-but-the-truth",
			"eng:code-reviewer",
			`rationale: "Required."`,
		} {
			if !strings.Contains(output, want) {
				t.Errorf("%s: output missing %q: %s", name, want, output)
			}
		}
	}

	ok, output, err := Peek(merged, slot)
	if ok {
		t.Error("Peek: expected !ok")
	}
	check("Peek", output, err)

	_, output, err = Claim(merged, slot)
	check("Claim", output, err)
}

func TestClaimDormantReturnsZeroTicket(t *testing.T) {
	repo, branch := setupGateSession(t)
	slot := WorktreeSlot(repo, branch)

	tk, output, err := Claim(sweatfile.Sweatfile{}, slot)
	if err != nil || output != "" {
		t.Fatalf("dormant Claim: output=%q err=%v", output, err)
	}
	if tk != (Ticket{}) {
		t.Errorf("expected zero Ticket, got %+v", tk)
	}
	if err := Settle(slot, tk, true); err != nil {
		t.Errorf("Settle on zero ticket: %v", err)
	}
}

func TestRecordStoresHeadSha(t *testing.T) {
	repo, branch := setupGateSession(t)
	slot := WorktreeSlot(repo, branch)
	if err := Record(slot, gateSkills, "abc123"); err != nil {
		t.Fatal(err)
	}
	a := readAttestation(t, slot)
	if a == nil || a.HeadSha != "abc123" || a.Claim != nil {
		t.Fatalf("got %+v", a)
	}
}

func TestClaimThenSettleLandedConsumes(t *testing.T) {
	repo, branch := setupGateSession(t)
	slot := WorktreeSlot(repo, branch)
	merged := liveGate()
	if err := Record(slot, gateSkills, "abc123"); err != nil {
		t.Fatal(err)
	}

	tk, output, err := Claim(merged, slot)
	if err != nil {
		t.Fatalf("Claim: %v (%s)", err, output)
	}
	a := readAttestation(t, slot)
	if a == nil || a.Claim == nil || a.Claim.PID != os.Getpid() {
		t.Fatalf("expected claim by this pid, got %+v", a)
	}
	if !tk.RecordedAt.Equal(a.RecordedAt) || tk.HeadSha != "abc123" || tk.ClaimID == "" || tk.ClaimID != a.Claim.ID {
		t.Errorf("ticket %+v does not match attestation %+v", tk, a)
	}

	if err := Settle(slot, tk, true); err != nil {
		t.Fatal(err)
	}
	if a := readAttestation(t, slot); a != nil {
		t.Errorf("expected attestation consumed, got %+v", a)
	}
	if _, _, err := Peek(merged, slot); !errors.Is(err, ErrAttestationRequired) {
		t.Errorf("Peek after consume: expected ErrAttestationRequired, got %v", err)
	}
}

func TestClaimThenSettleReleasedKeeps(t *testing.T) {
	repo, branch := setupGateSession(t)
	slot := WorktreeSlot(repo, branch)
	merged := liveGate()
	if err := Record(slot, gateSkills, ""); err != nil {
		t.Fatal(err)
	}
	before := readAttestation(t, slot).RecordedAt

	tk, _, err := Claim(merged, slot)
	if err != nil {
		t.Fatal(err)
	}
	if err := Settle(slot, tk, false); err != nil {
		t.Fatal(err)
	}
	a := readAttestation(t, slot)
	if a == nil || a.Claim != nil || !a.RecordedAt.Equal(before) {
		t.Fatalf("expected kept unclaimed attestation, got %+v", a)
	}
	if _, output, err := Claim(merged, slot); err != nil {
		t.Errorf("second Claim: %v (%s)", err, output)
	}
}

func TestLiveClaimBlocksPeekAndClaim(t *testing.T) {
	repo, branch := setupGateSession(t)
	slot := WorktreeSlot(repo, branch)
	merged := liveGate()
	if err := Record(slot, gateSkills, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Claim(merged, slot); err != nil {
		t.Fatal(err)
	}

	_, peekOut, peekErr := Peek(merged, slot)
	_, claimOut, claimErr := Claim(merged, slot)
	for name, c := range map[string]struct {
		out string
		err error
	}{"Peek": {peekOut, peekErr}, "Claim": {claimOut, claimErr}} {
		if !errors.Is(c.err, ErrAttestationRequired) {
			t.Errorf("%s: expected ErrAttestationRequired, got %v", name, c.err)
		}
		if !strings.Contains(c.out, "in-flight") || !strings.Contains(c.out, "nothing-but-the-truth") {
			t.Errorf("%s: output missing in-flight/nothing-but-the-truth: %s", name, c.out)
		}
	}
}

func TestDeadClaimIsVoid(t *testing.T) {
	repo, branch := setupGateSession(t)
	slot := WorktreeSlot(repo, branch)
	merged := liveGate()

	st, err := slot.load()
	if err != nil {
		t.Fatal(err)
	}
	st.PreMergeAttestation = &session.PreMergeAttestation{
		RecordedAt: time.Now().UTC(),
		Skills:     gateSkills,
		Claim:      &session.AttestationClaim{ID: "x", PID: deadPID(t), ClaimedAt: time.Now().UTC()},
	}
	if err := slot.store(*st); err != nil {
		t.Fatal(err)
	}

	if ok, output, err := Peek(merged, slot); !ok || err != nil {
		t.Fatalf("Peek with dead claim: ok=%v err=%v %s", ok, err, output)
	}
	if _, output, err := Claim(merged, slot); err != nil {
		t.Fatalf("Claim with dead claim: %v %s", err, output)
	}
}

func TestSettleAfterReRecordIsNoOp(t *testing.T) {
	for _, landed := range []bool{true, false} {
		repo, branch := setupGateSession(t)
		slot := WorktreeSlot(repo, branch)
		merged := liveGate()
		if err := Record(slot, gateSkills, "old"); err != nil {
			t.Fatal(err)
		}
		t1, _, err := Claim(merged, slot)
		if err != nil {
			t.Fatal(err)
		}
		if err := Record(slot, gateSkills, "new"); err != nil {
			t.Fatal(err)
		}
		if err := Settle(slot, t1, landed); err != nil {
			t.Fatal(err)
		}
		a := readAttestation(t, slot)
		if a == nil || a.Claim != nil || a.HeadSha != "new" {
			t.Errorf("landed=%v: expected new unclaimed attestation, got %+v", landed, a)
		}
	}
}

func TestStaleTicketCannotConsume(t *testing.T) {
	repo, branch := setupGateSession(t)
	slot := WorktreeSlot(repo, branch)
	merged := liveGate()
	if err := Record(slot, gateSkills, ""); err != nil {
		t.Fatal(err)
	}
	t1, _, err := Claim(merged, slot)
	if err != nil {
		t.Fatal(err)
	}
	if err := Settle(slot, t1, false); err != nil {
		t.Fatal(err)
	}
	t2, _, err := Claim(merged, slot)
	if err != nil {
		t.Fatal(err)
	}
	if err := Settle(slot, t1, true); err != nil {
		t.Fatal(err)
	}
	a := readAttestation(t, slot)
	if a == nil || a.Claim == nil || a.Claim.ID != t2.ClaimID {
		t.Fatalf("stale ticket must not touch t2's hold, got %+v", a)
	}
	if err := Settle(slot, t2, true); err != nil {
		t.Fatal(err)
	}
	if a := readAttestation(t, slot); a != nil {
		t.Errorf("expected consumed by t2, got %+v", a)
	}
}

func TestConcurrentClaimsOneWins(t *testing.T) {
	repo, branch := setupGateSession(t)
	slot := WorktreeSlot(repo, branch)
	merged := liveGate()
	if err := Record(slot, gateSkills, ""); err != nil {
		t.Fatal(err)
	}

	const n = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tk, output, err := Claim(merged, slot)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && tk.ClaimID != "":
				wins++
			case errors.Is(err, ErrAttestationRequired) && strings.Contains(output, "in-flight"):
			default:
				t.Errorf("unexpected claim result: tk=%+v err=%v output=%s", tk, err, output)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Errorf("exactly one Claim must win, got %d", wins)
	}
}

func TestImplicitSlotStoreRefusesWhenSessionGone(t *testing.T) {
	checkout := setupImplicitSession(t)
	slot := ImplicitSlot(checkout)
	st, err := slot.load()
	if err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(checkout, ".spinclass", "state-deadbeef.json")
	if err := os.Remove(stateFile); err != nil {
		t.Fatal(err)
	}
	if err := slot.store(*st); err == nil {
		t.Fatal("expected store to refuse a vanished session")
	}
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Errorf("state file must not be recreated, stat err=%v", err)
	}
}

func TestSettleReturnsRealLoadErrorsButNotMissing(t *testing.T) {
	boom := errors.New("boom")
	broken := Slot{
		load:   func() (*session.State, error) { return nil, boom },
		update: func(func(*session.State) error) error { return boom },
	}
	if err := Settle(broken, Ticket{ClaimID: "x"}, true); !errors.Is(err, boom) {
		t.Errorf("expected the real error, got %v", err)
	}
	gone := Slot{
		update: func(func(*session.State) error) error { return fmt.Errorf("gone: %w", os.ErrNotExist) },
	}
	if err := Settle(gone, Ticket{ClaimID: "x"}, true); err != nil {
		t.Errorf("missing session must be a no-op, got %v", err)
	}
}

func TestImplicitSlotClaimSettleRoundTrip(t *testing.T) {
	checkout := setupImplicitSession(t)
	slot := ImplicitSlot(checkout)
	merged := liveGate()
	if err := Record(slot, gateSkills, ""); err != nil {
		t.Fatal(err)
	}

	tk, output, err := Claim(merged, slot)
	if err != nil {
		t.Fatalf("Claim: %v %s", err, output)
	}
	if err := Settle(slot, tk, false); err != nil {
		t.Fatal(err)
	}
	if a := readAttestation(t, slot); a == nil || a.Claim != nil {
		t.Fatalf("expected kept unclaimed, got %+v", a)
	}

	tk, _, err = Claim(merged, slot)
	if err != nil {
		t.Fatal(err)
	}
	if err := Settle(slot, tk, true); err != nil {
		t.Fatal(err)
	}
	if a := readAttestation(t, slot); a != nil {
		t.Errorf("expected consumed, got %+v", a)
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
