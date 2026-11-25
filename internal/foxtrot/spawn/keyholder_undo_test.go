package spawn

import "testing"

func withKeyholderHook(t *testing.T, fn func(home, repoPath, sessionKey string) (map[string]string, func(), error)) {
	t.Helper()
	prev := KeyholderEnv
	t.Cleanup(func() { KeyholderEnv = prev })
	KeyholderEnv = fn
}

// A spawn whose entry cannot start fails after the worker's key holder pair was
// launched; no session will exist to close it, so the spawn stops it.
func TestASpawnThatFailsAfterThePairStartedStopsThePair(t *testing.T) {
	home, repoPath := newWorkerFixture(t, "[session-entry]\nspawn-entry = [\"/nonexistent/spawn-entry\", \"--\", \"{prompt}\"]\n")
	started, undone := 0, 0
	withKeyholderHook(t, func(_, _, key string) (map[string]string, func(), error) {
		started++
		return map[string]string{"SSH_AUTH_SOCK": "/holder/agent.sock"}, func() { undone++ }, nil
	})
	if _, err := LaunchDetachedWith(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "", ConfineDefault); err == nil {
		t.Fatal("the spawn of a missing entry succeeded")
	}
	if started != 1 {
		t.Fatalf("the key holder hook ran %d times, want 1", started)
	}
	if undone != 1 {
		t.Errorf("the pair's undo ran %d times after the failed spawn, want 1", undone)
	}
}

// A pair the spawn only found already running has no undo to call.
func TestASpawnThatFailsWithoutAnUndoCallsNothing(t *testing.T) {
	home, repoPath := newWorkerFixture(t, "[session-entry]\nspawn-entry = [\"/nonexistent/spawn-entry\", \"--\", \"{prompt}\"]\n")
	withKeyholderHook(t, func(_, _, _ string) (map[string]string, func(), error) {
		return map[string]string{"SSH_AUTH_SOCK": "/holder/agent.sock"}, nil, nil
	})
	if _, err := LaunchDetachedWith(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "", ConfineDefault); err == nil {
		t.Fatal("the spawn of a missing entry succeeded")
	}
}
