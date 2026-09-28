package main

import (
	"os"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/merge"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// decideMergeGate (FDR 0031) never consumes: it only picks how the merge was
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
			t.Setenv("HOME", base) // bound the cascade — see TestResolveGatedSession
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
