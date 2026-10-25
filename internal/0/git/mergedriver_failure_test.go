package git

import (
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

// spinclass#334: a rebase whose bound driver is off PATH stops on a
// "conflict" that is not one. The detector must find the shell's
// not-found line in git's real output, whatever the shell's phrasing.
func TestDriverNotFoundLinesFromRealRebase(t *testing.T) {
	repo := driverRepo(t)
	testgit.MustGit(t, repo, "config", "merge.gen.driver", "spinclass-test-absent-driver %O %A %B")

	out, err := RunEnv(repo, nil, "rebase", "main") // combined output, as merge's rebase runs
	if err == nil {
		t.Fatalf("rebase with an absent driver succeeded:\n%s", out)
	}
	t.Cleanup(func() { _, _ = Run(repo, "rebase", "--abort") })

	lines := DriverNotFoundLines(out)
	if len(lines) == 0 {
		t.Fatalf("no driver-not-found line detected in rebase output:\n%s", out)
	}
	if !strings.Contains(lines[0], "spinclass-test-absent-driver") {
		t.Errorf("detected line %q does not name the driver", lines[0])
	}
}

func TestDriverNotFoundLinesIgnoresContentConflicts(t *testing.T) {
	out := "Auto-merging plain.txt\nCONFLICT (content): Merge conflict in plain.txt\nerror: could not apply 1234567... feature\n"
	if lines := DriverNotFoundLines(out); len(lines) != 0 {
		t.Errorf("content conflict flagged as a missing driver: %q", lines)
	}
}

func TestDriverNotFoundLinesShellPhrasings(t *testing.T) {
	for _, line := range []string{
		"sh: 1: codegen-merge: not found",
		"/bin/sh: codegen-merge: command not found",
		"sh: line 1: codegen-merge: command not found",
	} {
		if got := DriverNotFoundLines("Auto-merging gen.txt\n" + line + "\nCONFLICT (content): Merge conflict in gen.txt\n"); len(got) != 1 || got[0] != line {
			t.Errorf("DriverNotFoundLines(%q) = %q, want that line", line, got)
		}
	}
}
