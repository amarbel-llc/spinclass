package check

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
	"code.linenisgreat.com/crap/go-crap/v2/ndjsoncrap"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	"code.linenisgreat.com/spinclass/internal/bravo/hookrun"
)

func testPoints(recs []ndjsoncrap.Record) []ndjsoncrap.Test {
	var tests []ndjsoncrap.Test
	for _, rec := range recs {
		if tr, ok := rec.(ndjsoncrap.Test); ok {
			tests = append(tests, tr)
		}
	}
	return tests
}

func findPoint(tests []ndjsoncrap.Test, prefix string) (ndjsoncrap.Test, bool) {
	for _, tr := range tests {
		if strings.HasPrefix(tr.Description, prefix) {
			return tr, true
		}
	}
	return ndjsoncrap.Test{}, false
}

// The guarded gate (spinclass#449) runs the trusted command in the detached
// build worktree and never the repo's own pre-merge command, and says so in
// the record.
func TestGuardedGateRunsTheTrustedCommandInTheBuildWorktree(t *testing.T) {
	t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")
	defer hookrun.SetConfinedGateTools("git", "sh")()
	_, repoDir, wtPath := setupRepoWithWorktree(t, "feature")
	markers := t.TempDir()
	repoHook := filepath.Join(markers, "repo-hook")
	pwdOut := filepath.Join(markers, "pwd")
	repoCmd := ": > " + repoHook
	hierarchy := sweatfile.Hierarchy{Merged: sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: &repoCmd}}}
	hookSha := runGit(t, wtPath, "rev-parse", "HEAD")

	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	gate := sweatfileio.ConfinedPreMergeGate{Command: "pwd > " + pwdOut, Source: "/trusted/sweatfile"}
	_, err := RunGuardedWithReporterContext(context.Background(), rep, ts, hierarchy, wtPath, "feature", hookSha, gate, nil)
	ts.Finish()
	if err != nil {
		t.Fatalf("guarded gate: %v\n%s", err, buf.String())
	}
	if _, serr := os.Stat(repoHook); serr == nil {
		t.Error("the repo's pre-merge command ran")
	}
	pwd, rerr := os.ReadFile(pwdOut)
	if rerr != nil {
		t.Fatalf("the gate did not run: %v", rerr)
	}
	wantPrefix := filepath.Join(repoDir, ".worktrees", BuildWorktreePrefix)
	if got := strings.TrimSpace(string(pwd)); !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("the gate ran in %q, want a build worktree under %q", got, wantPrefix)
	}
	tr, ok := findPoint(testPoints(decodeRecords(t, buf.Bytes())), "guarded gate feature")
	if !ok || !tr.OK || !strings.Contains(tr.Description, "/trusted/sweatfile") {
		t.Errorf("audit point = %+v, want an ok point naming the command's source", tr)
	}
}

// Without a pinned sha or with the build worktree turned off the gate would
// run in the session worktree, which the worker controls: refused, nothing run.
func TestGuardedGateRefusesWithoutTheBuildWorktree(t *testing.T) {
	defer hookrun.SetConfinedGateTools("git", "sh")()
	_, _, wtPath := setupRepoWithWorktree(t, "feature")
	hookSha := runGit(t, wtPath, "rev-parse", "HEAD")
	disabled := true
	for _, tc := range []struct {
		name      string
		hookSha   string
		hierarchy sweatfile.Hierarchy
	}{
		{"no sha", "", sweatfile.Hierarchy{}},
		{"build worktree disabled", hookSha, sweatfile.Hierarchy{Merged: sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{DisableMergeBuildWorktree: &disabled}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "ran")
			var buf bytes.Buffer
			rep := crap.NewReporter(&buf, crap.ReporterOptions{})
			ts := rep.TestStream(0)
			gate := sweatfileio.ConfinedPreMergeGate{Command: ": > " + marker}
			_, err := RunGuardedWithReporterContext(context.Background(), rep, ts, tc.hierarchy, wtPath, "feature", tc.hookSha, gate, nil)
			ts.Finish()
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if _, serr := os.Stat(marker); serr == nil {
				t.Error("the gate ran although it was refused")
			}
			tr, ok := findPoint(testPoints(decodeRecords(t, buf.Bytes())), "guarded gate feature")
			if !ok || tr.OK {
				t.Errorf("refusal point = %+v, want a failing guarded gate point", tr)
			}
		})
	}
}

// `sc check` of a session the frontend launched confined runs the guarded
// gate from the trusted layer, not the repo's pre-merge.
func TestCheckOfAConfinedSessionRunsTheGuardedGate(t *testing.T) {
	t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")
	defer hookrun.SetConfinedGateTools("git", "sh")()
	root, _, wtPath := setupRepoWithWorktree(t, "feature")
	stateDir := filepath.Join(root, "state")
	t.Setenv("XDG_STATE_HOME", stateDir)
	record := confinement.UnitRecordPath(stateDir, wtPath)
	if err := os.MkdirAll(filepath.Dir(record), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	markers := t.TempDir()
	repoHook := filepath.Join(markers, "repo-hook")
	gateMarker := filepath.Join(markers, "gate")
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \": > "+repoHook+"\"\n")
	global := filepath.Join(root, ".config", "spinclass", "sweatfile")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte("[hooks]\nconfined-pre-merge = \": > "+gateMarker+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, recs, err := runCheck(t, wtPath)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if _, serr := os.Stat(gateMarker); serr != nil {
		t.Error("the trusted guarded gate did not run")
	}
	if _, serr := os.Stat(repoHook); serr == nil {
		t.Error("the repo's pre-merge command ran for a confined session")
	}
	if _, ok := findPoint(testPoints(recs), "guarded gate feature"); !ok {
		t.Error("no guarded gate audit point in the check record")
	}
}
