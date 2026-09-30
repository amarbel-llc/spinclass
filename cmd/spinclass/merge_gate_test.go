package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/merge"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// deadPID is above any kernel pid_max, so kill(2) reports ESRCH.
func deadPID() int { return 1 << 30 }

// TestDecideMergeGateHonoursClaims: a live claim makes the attestation
// unavailable to the gate; a dead-PID claim is void.
func TestDecideMergeGateHonoursClaims(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pid      int
		wantOK   bool
		wantText string
	}{
		{"live claim", os.Getpid(), false, "in-flight"},
		{"dead claim", deadPID(), true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd, repoPath, branch := gatedWorktreeFixture(t, gateSweat)
			st, err := session.Read(repoPath, branch)
			if err != nil {
				t.Fatal(err)
			}
			st.PreMergeAttestation.Claim = &session.AttestationClaim{ID: "x", PID: tc.pid, ClaimedAt: time.Now().UTC()}
			if err := session.Write(*st); err != nil {
				t.Fatal(err)
			}
			gs, failMsg, ok, gitErr := resolveSession(cwd)
			if !ok || gitErr != nil {
				t.Fatalf("resolveSession: ok=%v msg=%q err=%v", ok, failMsg, gitErr)
			}
			gate, msg, gok := decideMergeGate(gs)
			if gok != tc.wantOK {
				t.Fatalf("ok = %v (msg %q), want %v", gok, msg, tc.wantOK)
			}
			if gok && gate != merge.GateAttested {
				t.Errorf("gate = %v, want GateAttested", gate)
			}
			if !gok && !strings.Contains(msg, tc.wantText) {
				t.Errorf("refusal %q should mention %q", msg, tc.wantText)
			}
		})
	}
}

// decideMergeGate (FDR 0031) never claims or consumes: it only picks how the merge was
// admitted. Without an attestation it admits the merge iff exemptions are
// declared — the predicates themselves are judged at landing from the base.
func TestDecideMergeGate(t *testing.T) {
	const skills = "[[pre-merge-skills]]\nname = \"review\"\nrationale = \"Mandatory.\"\n"
	const exemption = "[[pre-merge-exemptions]]\nname = \"lock-only\"\ncommand = \"true\"\n"

	for _, tc := range []struct {
		name      string
		sweatfile string
		wantOK    bool
		wantGate  merge.AttestationGate
	}{
		{"dormant gate", "", true, merge.GateAttested},
		{"no attestation, no exemptions", skills, false, 0},
		{"no attestation, exemptions declared", skills + exemption, true, merge.GateNeedsExemption},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testgit.RequireGit(t)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			base := t.TempDir()
			t.Setenv("HOME", base) // bound the cascade — see TestResolveSession
			repo := filepath.Join(base, "repo")
			if err := os.MkdirAll(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			testgit.MustInit(t, repo)
			if tc.sweatfile != "" {
				if err := os.WriteFile(filepath.Join(repo, "sweatfile"), []byte(tc.sweatfile), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			wt := filepath.Join(repo, ".worktrees", "feature")
			testgit.MustWorktreeAdd(t, repo, wt, "feature")
			t.Chdir(wt)

			gs, failMsg, ok, gitErr := resolveSession(wt)
			if !ok || gitErr != nil {
				t.Fatalf("resolveSession: ok=%v msg=%q err=%v", ok, failMsg, gitErr)
			}
			gate, msg, gok := decideMergeGate(gs)
			if gok != tc.wantOK {
				t.Fatalf("ok = %v (msg %q), want %v", gok, msg, tc.wantOK)
			}
			if gok && gate != tc.wantGate {
				t.Errorf("gate = %v, want %v", gate, tc.wantGate)
			}
			if !gok && msg == "" {
				t.Error("refusal carried no attestation directive")
			}
		})
	}
}
