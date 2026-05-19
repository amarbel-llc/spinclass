package clean

import (
	"bytes"
	"strings"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/nixgc"
	tap "github.com/amarbel-llc/tap/go"
)

func TestParsePorcelainEmpty(t *testing.T) {
	changes := ParsePorcelain("")
	if len(changes) != 0 {
		t.Errorf("expected 0 changes, got %d", len(changes))
	}
}

func TestParsePorcelainModified(t *testing.T) {
	changes := ParsePorcelain(" M file.go")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Code != " M" {
		t.Errorf("expected code ' M', got %q", changes[0].Code)
	}
	if changes[0].Path != "file.go" {
		t.Errorf("expected path 'file.go', got %q", changes[0].Path)
	}
	if changes[0].Description() != "modified" {
		t.Errorf("expected description 'modified', got %q", changes[0].Description())
	}
}

func TestParsePorcelainUntracked(t *testing.T) {
	changes := ParsePorcelain("?? newfile.txt")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Code != "??" {
		t.Errorf("expected code '??', got %q", changes[0].Code)
	}
	if changes[0].Description() != "untracked" {
		t.Errorf("expected description 'untracked', got %q", changes[0].Description())
	}
}

func TestParsePorcelainDeleted(t *testing.T) {
	changes := ParsePorcelain(" D removed.go")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Description() != "deleted" {
		t.Errorf("expected description 'deleted', got %q", changes[0].Description())
	}
}

func TestParsePorcelainAdded(t *testing.T) {
	changes := ParsePorcelain("A  staged.go")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Description() != "added" {
		t.Errorf("expected description 'added', got %q", changes[0].Description())
	}
}

func TestParsePorcelainRenamed(t *testing.T) {
	changes := ParsePorcelain("R  old.go -> new.go")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Code != "R " {
		t.Errorf("expected code 'R ', got %q", changes[0].Code)
	}
	if changes[0].Path != "new.go" {
		t.Errorf("expected path 'new.go', got %q", changes[0].Path)
	}
	if changes[0].Description() != "renamed" {
		t.Errorf("expected description 'renamed', got %q", changes[0].Description())
	}
}

func TestParsePorcelainMultiple(t *testing.T) {
	input := " M file1.go\n?? file2.txt\nA  file3.go\n D file4.go"
	changes := ParsePorcelain(input)
	if len(changes) != 4 {
		t.Fatalf("expected 4 changes, got %d", len(changes))
	}

	expected := []struct {
		code string
		path string
		desc string
	}{
		{" M", "file1.go", "modified"},
		{"??", "file2.txt", "untracked"},
		{"A ", "file3.go", "added"},
		{" D", "file4.go", "deleted"},
	}

	for i, exp := range expected {
		if changes[i].Code != exp.code {
			t.Errorf("change %d: expected code %q, got %q", i, exp.code, changes[i].Code)
		}
		if changes[i].Path != exp.path {
			t.Errorf("change %d: expected path %q, got %q", i, exp.path, changes[i].Path)
		}
		if changes[i].Description() != exp.desc {
			t.Errorf("change %d: expected desc %q, got %q", i, exp.desc, changes[i].Description())
		}
	}
}

func TestParsePorcelainStagedModified(t *testing.T) {
	changes := ParsePorcelain("MM both.go")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Code != "MM" {
		t.Errorf("expected code 'MM', got %q", changes[0].Code)
	}
	if changes[0].Description() != "modified" {
		t.Errorf("expected description 'modified', got %q", changes[0].Description())
	}
}

func TestParsePorcelainStagedDeleted(t *testing.T) {
	changes := ParsePorcelain("D  gone.go")
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Description() != "deleted" {
		t.Errorf("expected description 'deleted', got %q", changes[0].Description())
	}
}

// TestRunReapEmptyClosureEmitsOk is the regression guard for #76:
// nixgc.Reap with an empty Closure is a no-op success, and the
// surrounding OutputBlock must render "ok" — not "not ok". Pre-fix,
// the success path returned a non-nil *tap.Diagnostics carrying only
// summary extras, which tap-go interprets as "not ok". Mirrors the
// twin test in internal/close.
func TestRunReapEmptyClosureEmitsOk(t *testing.T) {
	var buf bytes.Buffer
	tw := tap.NewWriter(&buf)
	plan := nixgc.Plan{WorktreePath: "/tmp/empty-wt"}
	runReap(tw, plan, "brave-myrtle")
	tw.Plan()

	out := buf.String()
	if strings.Contains(out, "not ok") {
		t.Errorf("TAP output unexpectedly contains 'not ok' for empty-closure reap:\n%s", out)
	}
	if !strings.Contains(out, "ok 1 - nix-gc reap brave-myrtle") {
		t.Errorf("TAP output missing 'ok 1 - nix-gc reap brave-myrtle':\n%s", out)
	}
	for _, want := range []string{"reclaimed: 0", "bytes_freed: 0", "closure: 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("TAP output missing summary line %q:\n%s", want, out)
		}
	}
}
