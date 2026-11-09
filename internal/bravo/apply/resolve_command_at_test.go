package apply

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ResolveCommandAt is the freeze's devShell resolution, exported for the
// regenerate phase, which resolves against a session's own HEAD.
func TestResolveCommandAtReturnsTheDevShellsAbsolutePathForTheRev(t *testing.T) {
	var gotRepo, gotRev, gotBin string
	stubResolver(t, func(_ context.Context, repoPath, sha, bin string) (string, error) {
		gotRepo, gotRev, gotBin = repoPath, sha, bin
		return "/nix/store/abc-fmt/bin/fmt", nil
	})
	path, err := ResolveCommandAt(context.Background(), "/repo", "deadbeef", "fmt")
	if err != nil {
		t.Fatalf("ResolveCommandAt: %v", err)
	}
	if path != "/nix/store/abc-fmt/bin/fmt" {
		t.Errorf("path = %q", path)
	}
	if gotRepo != "/repo" || gotRev != "deadbeef" || gotBin != "fmt" {
		t.Errorf("resolver saw (%q, %q, %q), want (/repo, deadbeef, fmt)", gotRepo, gotRev, gotBin)
	}
}

func TestResolveCommandAtLeavesAnAbsoluteBinAloneWithoutEvaluating(t *testing.T) {
	refusingResolver(t)
	path, err := ResolveCommandAt(context.Background(), "/repo", "deadbeef", "/nix/store/x/bin/fmt")
	if err != nil || path != "/nix/store/x/bin/fmt" {
		t.Errorf("= (%q, %v), want the path unchanged and no evaluation", path, err)
	}
}

func TestResolveCommandAtRefusesARelativeResult(t *testing.T) {
	stubResolver(t, func(context.Context, string, string, string) (string, error) {
		return "fmt", nil
	})
	_, err := ResolveCommandAt(context.Background(), "/repo", "deadbeef", "fmt")
	if err == nil || !strings.Contains(err.Error(), "not an absolute path") {
		t.Errorf("err = %v, want a not-absolute refusal", err)
	}
}

func TestResolveCommandAtWrapsAnEvaluationFailure(t *testing.T) {
	boom := errors.New("boom")
	stubResolver(t, func(context.Context, string, string, string) (string, error) {
		return "", boom
	})
	_, err := ResolveCommandAt(context.Background(), "/repo", "deadbeef", "fmt")
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "fmt") {
		t.Errorf("err = %v, want the cause wrapped and the command named", err)
	}
}
