package testgit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// DisableAutoMaintenance makes every git this process starts, and the
// children those start, skip auto maintenance and auto gc. Left on, a commit,
// fetch, merge or receive-pack spawns `git maintenance run --auto --detach`,
// which daemonizes and goes on writing into the fixture's TempDir after the
// test has returned, failing its RemoveAll with "directory not empty" (#421,
// #423). It is set through git's environment-config channel rather than a
// per-repo `git config` so it also covers repos a test creates with a bare
// `git init`, clones, and the production code under test.
//
// SetHermeticEnv calls it; call it directly from the TestMain of a package
// that runs git but does not need the rest of the hermetic environment.
func DisableAutoMaintenance() {
	_ = os.Setenv("GIT_CONFIG_COUNT", "2")
	_ = os.Setenv("GIT_CONFIG_KEY_0", "maintenance.auto")
	_ = os.Setenv("GIT_CONFIG_VALUE_0", "false")
	_ = os.Setenv("GIT_CONFIG_KEY_1", "gc.auto")
	_ = os.Setenv("GIT_CONFIG_VALUE_1", "0")
}

// RequireNoDetachedMaintenance fails the test if any git it runs from here on
// detaches background maintenance. git's trace2 events record the detach from
// the process that does it, before the command that spawned it returns, so
// the check does not race the background process. Call it before building the
// fixture so the fixture's own commits and fetches are covered.
func RequireNoDetachedMaintenance(t *testing.T) {
	t.Helper()
	trace := filepath.Join(t.TempDir(), "trace2.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	t.Cleanup(func() {
		raw, err := os.ReadFile(trace)
		if err != nil {
			t.Errorf("git wrote no trace2 events: %v", err)
			return
		}
		for _, line := range strings.Split(string(raw), "\n") {
			detachedChild := strings.Contains(line, `"event":"child_start"`) && strings.Contains(line, `"maintenance"`) && strings.Contains(line, `"--detach"`)
			if detachedChild || strings.Contains(line, `"label":"detach"`) {
				t.Errorf("git detached background maintenance: %s", line)
			}
		}
	})
}
