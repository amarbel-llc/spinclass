package check

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/0/gatecost"
	"code.linenisgreat.com/spinclass/internal/0/statsd"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/bravo/hookrun"
)

// gateHistory points XDG_STATE_HOME at a fresh dir and the nix-daemon cgroup
// file at a missing path, and returns the history file a gate would append to.
func gateHistory(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	// The hermetic test env turns recording off; these tests check it.
	t.Setenv("SPINCLASS_DISABLE_GATE_COST", "")
	fakeDaemonCPUStat(t, filepath.Join(t.TempDir(), "no-nix-daemon"))
	return filepath.Join(state, "spinclass", "host-queue", "history.ndjson")
}

func fakeDaemonCPUStat(t *testing.T, path string) {
	t.Helper()
	prev := daemonCPUStatPath
	daemonCPUStatPath = path
	t.Cleanup(func() { daemonCPUStatPath = prev })
}

// historyLines decodes every history line; a missing file is no lines.
func historyLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("history line %q is not JSON: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestCheckGateAppendsOneHistoryLine(t *testing.T) {
	_, repoDir, wtPath := setupRepoWithWorktree(t, "feature-cost")
	history := gateHistory(t)
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"true\"\n")

	if _, _, err := runCheck(t, wtPath); err != nil {
		t.Fatalf("Run: %v", err)
	}

	lines := historyLines(t, history)
	if len(lines) != 1 {
		t.Fatalf("history has %d lines, want 1: %v", len(lines), lines)
	}
	got := lines[0]
	if got["repo"] != filepath.Base(repoDir) || got["kind"] != gatecost.KindCheck || got["outcome"] != statsd.OutcomeOK {
		t.Errorf("record = %v, want repo %s, kind check, outcome ok", got, filepath.Base(repoDir))
	}
	if _, ok := got["wall_ms"]; !ok {
		t.Errorf("record has no wall_ms: %v", got)
	}
	for _, absent := range []string{"failed_recipe", "daemon_cpu_usec", "cpu_usec", "mem_peak_bytes"} {
		if _, ok := got[absent]; ok {
			t.Errorf("record carries %s, which nothing measured: %v", absent, got)
		}
	}
}

func TestFailedCheckGateRecordsTheFailingRecipe(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-cost-fail")
	history := gateHistory(t)
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"echo 'error: recipe `lint` failed on line 3 with exit code 1' >&2; exit 1\"\n")

	if _, _, err := runCheck(t, wtPath); err == nil {
		t.Fatal("the failing hook passed")
	}

	lines := historyLines(t, history)
	if len(lines) != 1 {
		t.Fatalf("history has %d lines, want 1: %v", len(lines), lines)
	}
	if lines[0]["outcome"] != statsd.OutcomeFailed || lines[0]["failed_recipe"] != "lint" {
		t.Errorf("record = %v, want outcome failed and failed_recipe lint", lines[0])
	}
}

// A passing gate's output is not parsed for a failing recipe.
func TestPassingGateRecordsNoFailingRecipe(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-cost-pass")
	history := gateHistory(t)
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"echo 'error: recipe `flaky` failed on line 1 with exit code 1'\"\n")

	if _, _, err := runCheck(t, wtPath); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if lines := historyLines(t, history); len(lines) != 1 || lines[0]["failed_recipe"] != nil {
		t.Errorf("history = %v, want one line without failed_recipe", lines)
	}
}

func TestRecordCostsFalseAppendsNothing(t *testing.T) {
	root, _, wtPath := setupRepoWithWorktree(t, "feature-cost-off")
	history := gateHistory(t)
	global := filepath.Join(root, ".config", "spinclass", "sweatfile")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte("[host-queue]\nrecord-costs = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"true\"\n")
	drain := statsdSink(t)

	if _, _, err := runCheck(t, wtPath); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if lines := historyLines(t, history); len(lines) != 0 {
		t.Errorf("record-costs = false still appended %v", lines)
	}
	for _, d := range drain() {
		if strings.HasPrefix(d, statsd.Prefix+"gate.") {
			t.Errorf("record-costs = false still emitted %q", d)
		}
	}
}

// SPINCLASS_DISABLE_GATE_COST=1 records nothing, whatever the sweatfiles say.
func TestGateCostKillSwitchRecordsNothing(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-cost-kill")
	history := gateHistory(t)
	t.Setenv("SPINCLASS_DISABLE_GATE_COST", "1")
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"true\"\n")
	drain := statsdSink(t)

	if _, _, err := runCheck(t, wtPath); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if lines := historyLines(t, history); len(lines) != 0 {
		t.Errorf("the kill switch still appended %v", lines)
	}
	for _, d := range drain() {
		if strings.HasPrefix(d, statsd.Prefix+"gate.") {
			t.Errorf("the kill switch still emitted %q", d)
		}
	}
}

// A repo layer cannot turn recording off (trusted layers only).
func TestRepoLayerRecordCostsIsIgnored(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-cost-repo")
	history := gateHistory(t)
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"true\"\n\n[host-queue]\nrecord-costs = false\n")

	if _, _, err := runCheck(t, wtPath); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if lines := historyLines(t, history); len(lines) != 1 {
		t.Errorf("history = %v, want the repo-layer switch ignored and one line", lines)
	}
}

// Recording must never fail or change a gate (the #349 rule): with the state
// dir unwritable the gate's result is exactly what it would have been.
func TestUnwritableHistoryDoesNotChangeTheGate(t *testing.T) {
	for _, tc := range []struct {
		name, hook string
		wantErr    bool
	}{
		{"passing", "true", false},
		{"failing", "exit 4", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, wtPath := setupRepoWithWorktree(t, "feature-cost-ro")
			gateHistory(t)
			blocker := filepath.Join(t.TempDir(), "not-a-dir")
			if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("XDG_STATE_HOME", blocker)
			hook := tc.hook
			hierarchy := sweatfile.Hierarchy{Merged: sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: &hook}}}

			var buf, activity bytes.Buffer
			rep := crap.NewReporter(&buf, crap.ReporterOptions{})
			ts := rep.TestStream(0)
			_, err := RunWithReporterContext(context.Background(), rep, ts, hierarchy, wtPath, "feature-cost-ro", "", &activity)
			ts.Finish()

			if (err != nil) != tc.wantErr {
				t.Fatalf("gate error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "exit status 4") {
				t.Errorf("gate error = %v, want the hook's own exit status 4", err)
			}
			if !strings.Contains(activity.String(), "gate cost not recorded") {
				t.Errorf("no warning in the activity log: %q", activity.String())
			}
		})
	}
}

// A merge's gate and a guarded gate are told apart from a check's; a guarded
// gate's history line names its caller.
func TestGateKindIsMergeOrGuarded(t *testing.T) {
	t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")
	defer hookrun.SetConfinedGateTools("git", "sh")()
	_, repoDir, wtPath := setupRepoWithWorktree(t, "feature-cost-kind")
	history := gateHistory(t)
	hook := "true"
	hierarchy := sweatfile.Hierarchy{Merged: sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMerge: &hook}}}
	mergeCtx := WithGateKind(context.Background(), gatecost.KindMerge)

	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	if _, err := RunWithReporterContext(mergeCtx, rep, ts, hierarchy, wtPath, "feature-cost-kind", "", nil); err != nil {
		t.Fatalf("merge gate: %v", err)
	}
	gate := sweatfileio.ConfinedPreMergeGate{Command: "true", Source: "/trusted/sweatfile"}
	hookSha := runGit(t, wtPath, "rev-parse", "HEAD")
	if _, err := RunGuardedWithReporterContext(mergeCtx, rep, ts, hierarchy, wtPath, "feature-cost-kind", hookSha, gate, nil); err != nil {
		t.Fatalf("guarded gate: %v\n%s", err, buf.String())
	}
	if _, err := RunGuardedWithReporterContext(context.Background(), rep, ts, hierarchy, wtPath, "feature-cost-kind", hookSha, gate, nil); err != nil {
		t.Fatalf("guarded check: %v\n%s", err, buf.String())
	}
	ts.Finish()

	lines := historyLines(t, history)
	if len(lines) != 3 {
		t.Fatalf("history has %d lines, want 3: %v", len(lines), lines)
	}
	for i, want := range [][2]string{
		{gatecost.KindMerge, gatecost.KindMerge},
		{gatecost.KindGuarded, gatecost.KindMerge},
		{gatecost.KindGuarded, gatecost.KindCheck},
	} {
		if lines[i]["kind"] != want[0] || lines[i]["caller"] != want[1] {
			t.Errorf("line %d kind/caller = %v/%v, want %s/%s", i, lines[i]["kind"], lines[i]["caller"], want[0], want[1])
		}
	}
	for _, l := range lines {
		if l["repo"] != filepath.Base(repoDir) {
			t.Errorf("repo = %v, want %s", l["repo"], filepath.Base(repoDir))
		}
	}
}

// The nix-daemon cgroup delta is read from the spinclass process around the
// gate; a missing file is no field (gateHistory's default), a present one a
// value.
func TestGateRecordsTheNixDaemonDelta(t *testing.T) {
	_, _, wtPath := setupRepoWithWorktree(t, "feature-cost-daemon")
	history := gateHistory(t)
	stat := filepath.Join(t.TempDir(), "cpu.stat")
	if err := os.WriteFile(stat, []byte("usage_usec 100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fakeDaemonCPUStat(t, stat)
	// The hook itself bumps the daemon's counter, standing in for a build.
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"echo usage_usec 350 > "+stat+"\"\n")

	if _, _, err := runCheck(t, wtPath); err != nil {
		t.Fatalf("Run: %v", err)
	}
	lines := historyLines(t, history)
	if len(lines) != 1 || lines[0]["daemon_cpu_usec"] != float64(250) {
		t.Errorf("history = %v, want daemon_cpu_usec 250", lines)
	}
}

func TestCheckGateEmitsGateMetrics(t *testing.T) {
	_, repoDir, wtPath := setupRepoWithWorktree(t, "feature-cost-metrics")
	gateHistory(t)
	writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"echo 'error: recipe `test.nix` failed on line 2 with exit code 1'; exit 1\"\n")
	drain := statsdSink(t)

	if _, _, err := runCheck(t, wtPath); err == nil {
		t.Fatal("the failing hook passed")
	}
	prefix := statsd.Prefix + "gate." + statsd.Segment(filepath.Base(repoDir)) + ".check."
	var sawWall, sawRecipe bool
	for _, d := range drain() {
		switch {
		case strings.HasPrefix(d, prefix+"wall.failed:") && strings.HasSuffix(d, "|ms"):
			sawWall = true
		case d == prefix+"failed_recipe.test_nix:1|c":
			sawRecipe = true
		}
	}
	if !sawWall || !sawRecipe {
		t.Errorf("wall timer seen %v, failed_recipe counter seen %v; want both under %s", sawWall, sawRecipe, prefix)
	}
}
