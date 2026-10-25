package attestation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// forgeDisplayCopy rewrites the session's worktree-local state.json the way a
// confined worker (which binds its worktree read-write) can.
func forgeDisplayCopy(t *testing.T, repo, branch string, mutate func(*session.State)) {
	t.Helper()
	st, err := session.Read(repo, branch)
	if err != nil {
		t.Fatal(err)
	}
	mutate(st)
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(repo, ".worktrees", branch, ".spinclass")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A worker writes a fresh attestation into its own worktree copy of its session
// state. The gate reads the attestation from the frontend record, so a copy the
// worker wrote must not satisfy Peek or Claim, and must not leak into the record.
func TestForgedWorktreeCopyCannotFabricateAnAttestation(t *testing.T) {
	t.Cleanup(session.SetReportSink(func(string) {}))
	repo, branch := setupGateSession(t)
	forgeDisplayCopy(t, repo, branch, func(st *session.State) {
		st.PreMergeAttestation = &session.PreMergeAttestation{
			RecordedAt: time.Now().UTC(),
			Skills:     gateSkills,
			HeadSha:    "deadbeef",
		}
	})
	slot := WorktreeSlot(repo, branch)

	if ok, _, err := Peek(liveGate(), slot); ok || !errors.Is(err, ErrAttestationRequired) {
		t.Errorf("Peek = ok:%v err:%v; want a refusal: the attestation exists only in the worker's copy", ok, err)
	}
	tk, _, err := Claim(liveGate(), slot)
	if !errors.Is(err, ErrAttestationRequired) || tk != (Ticket{}) {
		t.Errorf("Claim = %+v, %v; want ErrAttestationRequired and no ticket", tk, err)
	}
	if st, err := session.Read(repo, branch); err != nil || st.PreMergeAttestation != nil {
		t.Errorf("the forged attestation reached the frontend record: %+v, %v", st, err)
	}
}

// A worker erases the in-flight claim from its copy to get a second merge past a
// buffered attestation that a live merge holds. The claim lives in the frontend
// record, so the gate must still refuse.
func TestForgedWorktreeCopyCannotReleaseALiveClaim(t *testing.T) {
	t.Cleanup(session.SetReportSink(func(string) {}))
	repo, branch := setupGateSession(t)
	slot := WorktreeSlot(repo, branch)
	if err := Record(slot, gateSkills, "abc123"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Claim(liveGate(), slot); err != nil {
		t.Fatalf("the first claim should succeed: %v", err)
	}
	forgeDisplayCopy(t, repo, branch, func(st *session.State) {
		st.PreMergeAttestation.Claim = nil
	})

	if ok, _, err := Peek(liveGate(), slot); ok || !errors.Is(err, ErrAttestationRequired) {
		t.Errorf("Peek = ok:%v err:%v; want a refusal while the live claim stands", ok, err)
	}
	if tk, _, err := Claim(liveGate(), slot); !errors.Is(err, ErrAttestationRequired) || tk != (Ticket{}) {
		t.Errorf("second Claim = %+v, %v; want ErrAttestationRequired", tk, err)
	}
}

// A worker restores a consumed attestation in its copy after a merge landed. The
// frontend consumed it from the record, so the gate must keep refusing.
func TestForgedWorktreeCopyCannotResurrectAConsumedAttestation(t *testing.T) {
	t.Cleanup(session.SetReportSink(func(string) {}))
	repo, branch := setupGateSession(t)
	slot := WorktreeSlot(repo, branch)
	if err := Record(slot, gateSkills, "abc123"); err != nil {
		t.Fatal(err)
	}
	tk, _, err := Claim(liveGate(), slot)
	if err != nil {
		t.Fatal(err)
	}
	if err := Settle(slot, tk, true); err != nil {
		t.Fatal(err)
	}
	forgeDisplayCopy(t, repo, branch, func(st *session.State) {
		st.PreMergeAttestation = &session.PreMergeAttestation{
			RecordedAt: time.Now().UTC(),
			Skills:     gateSkills,
			HeadSha:    "abc123",
		}
	})

	if ok, _, err := Peek(liveGate(), slot); ok || !errors.Is(err, ErrAttestationRequired) {
		t.Errorf("Peek = ok:%v err:%v; want a refusal: the attestation was consumed", ok, err)
	}
}
