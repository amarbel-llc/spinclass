package sweatfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHardcodedDefaultsGitExcludes(t *testing.T) {
	defaults := GetDefault()

	if defaults.Git == nil {
		t.Fatal("expected non-nil Git struct")
	}

	if defaults.Git.Excludes == nil {
		t.Fatal("expected non-nil git excludes slice")
	}

	// Every path spinclass (or a tool it invokes) writes into a worktree
	// must be excluded so it never shows as untracked / gets accidentally
	// staged (#116, #119): .envrc and .direnv/ come from the direnv
	// integration, .tmp/ is the session scratch dir, and
	// .claude/settings.local.json carries the claude-allow rules. The
	// [session-entry].env dotenv file lives inside .spinclass/ (#121).
	want := []string{
		".worktrees/", ".spinclass/", ".mcp.json",
		".envrc", ".direnv/", ".tmp/", ".claude/settings.local.json",
	}
	if len(defaults.Git.Excludes) != len(want) {
		t.Fatalf(
			"expected %d git excludes, got %d: %v",
			len(want),
			len(defaults.Git.Excludes),
			defaults.Git.Excludes,
		)
	}
	for i, w := range want {
		if defaults.Git.Excludes[i] != w {
			t.Errorf("excludes[%d]: expected %q, got %q", i, w, defaults.Git.Excludes[i])
		}
	}
}

func TestHardcodedDefaultsClaudeAllow(t *testing.T) {
	defaults := GetDefault()

	home, _ := os.UserHomeDir()
	if home == "" {
		if defaults.Claude != nil {
			t.Errorf(
				"expected nil Claude when HOME is empty, got %v",
				defaults.Claude,
			)
		}
		return
	}

	if defaults.Claude == nil {
		t.Fatal("expected non-nil Claude struct")
	}

	if len(defaults.Claude.Allow) != 1 {
		t.Fatalf(
			"expected 1 claude allow rule, got %d: %v",
			len(defaults.Claude.Allow),
			defaults.Claude.Allow,
		)
	}

	wantRule := "Read(" + filepath.Join(home, ".claude") + "/*)"
	if defaults.Claude.Allow[0] != wantRule {
		t.Errorf(
			"Claude.Allow[0]: got %q, want %q",
			defaults.Claude.Allow[0],
			wantRule,
		)
	}
}
