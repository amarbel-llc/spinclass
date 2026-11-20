package perms

import "testing"

// A stop at idle only queues a request the session's turn end and the job
// check gate; a stop now kills the current turn and any running job, so it
// asks the human, failing closed on anything that is not plainly idle.
func TestStopNowAlwaysAsks(t *testing.T) {
	for _, tool := range []string{stopThisSessionTool, stopChildSessionTool} {
		for _, tc := range []struct {
			name  string
			input map[string]any
			ask   bool
		}{
			{"absent", map[string]any{}, false},
			{"idle", map[string]any{"when": "idle"}, false},
			{"empty", map[string]any{"when": ""}, false},
			{"now", map[string]any{"when": "now"}, true},
			{"not a string", map[string]any{"when": true}, true},
			{"unknown", map[string]any{"when": "soon"}, true},
		} {
			t.Run(tool+"/"+tc.name, func(t *testing.T) {
				if _, ask := AlwaysAsk(tool, tc.input, t.TempDir()); ask != tc.ask {
					t.Errorf("AlwaysAsk = %v, want %v", ask, tc.ask)
				}
			})
		}
	}
}
