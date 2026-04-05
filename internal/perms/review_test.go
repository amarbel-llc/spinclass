package perms

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRouteDecisions(t *testing.T) {
	tmpDir := t.TempDir()
	tiersDir := filepath.Join(tmpDir, "tiers")
	os.MkdirAll(filepath.Join(tiersDir, "repos"), 0o755)

	// Seed global tier with ["Read"]
	globalPath := filepath.Join(tiersDir, "global.json")
	globalTier := Tier{Allow: []string{"Read"}}
	globalData, _ := json.MarshalIndent(globalTier, "", "  ")
	os.WriteFile(globalPath, globalData, 0o644)

	// Seed repo tier with []
	repoPath := filepath.Join(tiersDir, "repos", "myrepo.json")
	repoTier := Tier{Allow: []string{}}
	repoData, _ := json.MarshalIndent(repoTier, "", "  ")
	os.WriteFile(repoPath, repoData, 0o644)

	decisions := []ReviewDecision{
		{Rule: "Edit", Action: ReviewPromoteGlobal},
		{Rule: "Bash(go test:*)", Action: ReviewPromoteRepo},
	}

	err := RouteDecisions(tiersDir, "myrepo", decisions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify global tier now contains "Edit"
	global, err := LoadTierFile(globalPath)
	if err != nil {
		t.Fatalf("failed to load global tier: %v", err)
	}
	globalFound := map[string]bool{}
	for _, r := range global.Allow {
		globalFound[r] = true
	}
	if !globalFound["Read"] {
		t.Error("expected Read to remain in global tier")
	}
	if !globalFound["Edit"] {
		t.Error("expected Edit to be promoted to global tier")
	}

	// Verify repo tier now contains "Bash(go test:*)"
	repo, err := LoadTierFile(repoPath)
	if err != nil {
		t.Fatalf("failed to load repo tier: %v", err)
	}
	repoFound := map[string]bool{}
	for _, r := range repo.Allow {
		repoFound[r] = true
	}
	if !repoFound["Bash(go test:*)"] {
		t.Error("expected Bash(go test:*) to be promoted to repo tier")
	}
}

func TestRouteDecisionsDiscard(t *testing.T) {
	tmpDir := t.TempDir()
	tiersDir := filepath.Join(tmpDir, "tiers")
	os.MkdirAll(filepath.Join(tiersDir, "repos"), 0o755)

	decisions := []ReviewDecision{
		{Rule: "Bash(rm -rf:*)", Action: ReviewDiscard},
	}

	err := RouteDecisions(tiersDir, "myrepo", decisions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Discard is a no-op — rule just isn't promoted.
}

func TestRouteDecisionsKeep(t *testing.T) {
	tmpDir := t.TempDir()
	tiersDir := filepath.Join(tmpDir, "tiers")
	os.MkdirAll(filepath.Join(tiersDir, "repos"), 0o755)

	decisions := []ReviewDecision{
		{Rule: "Edit", Action: ReviewKeep},
	}

	err := RouteDecisions(tiersDir, "myrepo", decisions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Keep is a no-op — rule stays only in the log.
}

func TestDryRunDecisions(t *testing.T) {
	var buf bytes.Buffer
	decisions := []ReviewDecision{
		{Rule: "Bash(go test:*)", Action: ReviewPromoteGlobal},
		{Rule: "Edit", Action: ReviewPromoteRepo},
		{Rule: "Bash(rm -rf:*)", Action: ReviewDiscard},
		{Rule: "Read", Action: ReviewKeep},
	}

	DryRunDecisions(&buf, "/tmp/tiers", "myrepo", decisions)
	out := buf.String()

	if !strings.Contains(out, "would promote to global tier") {
		t.Error("expected global tier output")
	}
	if !strings.Contains(out, "Bash(go test:*)") {
		t.Error("expected promoted rule in output")
	}
	if !strings.Contains(out, "would discard (not promoted)") {
		t.Error("expected discard output")
	}
	if !strings.Contains(out, "would keep (not promoted)") {
		t.Error("expected keep output")
	}
}
