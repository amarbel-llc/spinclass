package sysprompt

import (
	"strings"
	"testing"
)

// A failed sweatfile-hierarchy load degrades ASYMMETRICALLY: the design-record
// index falls back to built-in default dirs and keeps rendering, while both
// FDR 0030 indexes take their sources only from the sweatfile and render
// nothing. That combination is indistinguishable from "configured with no
// index sources", which cost a real debugging session — so it must be stated,
// not inferred.
func TestLoadIndexesReportsUnreadableHierarchy(t *testing.T) {
	// os.UserHomeDir is $HOME on unix, so an empty HOME is the reachable way
	// to make the lookup fail — and it is the shape a plugin child process
	// launched without an environment would hit.
	t.Setenv("HOME", "")

	got := loadIndexes(t.TempDir())

	if got.Warning == "" {
		t.Fatal("an unreadable hierarchy must produce a warning, not silently disable the indexes")
	}
	for _, want := range []string{"⚠", "man-index", "repo-index"} {
		if !strings.Contains(got.Warning, want) {
			t.Errorf("warning %q missing %q", got.Warning, want)
		}
	}
}

// The happy path stays quiet: a readable hierarchy that simply selects no
// index sources is not a failure and must not be flagged as one.
func TestLoadIndexesQuietWhenNothingSelected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	got := loadIndexes(t.TempDir())

	if got.Warning != "" {
		t.Errorf("an empty but readable configuration must not warn, got %q", got.Warning)
	}
	if got.Manpages != "" || got.Repositories != "" {
		t.Errorf("no sources selected must render no index sections")
	}
}

// The warning reaches the fragment, after the sections it explains, in both
// render modes.
func TestRenderIncludesIndexWarning(t *testing.T) {
	for _, mode := range []Mode{ModeWorktree, ModeMainCheckout} {
		got, err := Render(Coordinates{
			Mode:          mode,
			SessionKey:    "k",
			DesignRecords: "## Design records\n\n**proposed**\n- FDR 0030 — X",
			IndexWarning:  "⚠ sweatfile-selected system-prompt indexes are unavailable: boom",
		})
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		mustContain(t, got, "⚠ sweatfile-selected system-prompt indexes are unavailable: boom")
		if strings.Index(got, "## Design records") > strings.Index(got, "⚠ sweatfile-selected") {
			t.Errorf("%s: the warning must follow the sections it explains:\n%s", mode, got)
		}

		quiet, err := Render(Coordinates{Mode: mode, SessionKey: "k"})
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if strings.Contains(quiet, "⚠ sweatfile-selected") {
			t.Errorf("%s: an empty warning must add nothing:\n%s", mode, quiet)
		}
	}
}
