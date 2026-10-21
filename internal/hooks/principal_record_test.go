package hooks

import (
	"os"
	"testing"

	"code.linenisgreat.com/spinclass/internal/session"
)

func TestPrincipalForHook(t *testing.T) {
	const a = "5e5e5e5e-0000-4000-8000-000000000000"
	const b = "6f6f6f6f-0000-4000-8000-000000000000"
	tests := []struct {
		name, env, hook string
		want            string
		wantSource      string
		wantOK          bool
	}{
		{"env empty, hook set", "", a, a, "hook-session-id", true},
		{"env set, hook empty", a, "", a, "env", true},
		{"both equal", a, a, a, "env", true},
		{"both differ, env wins", a, b, a, "env", true},
		{"both differ swapped, env wins", b, a, b, "env", true},
		{"both empty", "", "", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, source, ok := principalForHook(tc.env, tc.hook)
			if got != tc.want || source != tc.wantSource || ok != tc.wantOK {
				t.Errorf("principalForHook(%q, %q) = (%q, %q, %t), want (%q, %q, %t)",
					tc.env, tc.hook, got, source, ok, tc.want, tc.wantSource, tc.wantOK)
			}
		})
	}
}

// clown does not export CLOWN_SESSION_ID to the hook (#392), so with it unset
// the principal is the hook's session_id.
func TestSessionStartWithoutClownSessionIDRecordsHookSessionID(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "")
	_ = os.Unsetenv("CLOWN_SESSION_ID")
	repo, wt := initSpawnedWorktree(t, "driver/key")
	const hookID = "5e5e5e5e-0000-4000-8000-000000000000"

	fireSessionStartWithID(t, wt, hookID)

	st, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if st.Principal != hookID {
		t.Errorf("Principal = %q, want the hook session_id %q", st.Principal, hookID)
	}
	if st.HelloSentAt == nil {
		t.Error("the hello must still go out and adopt the state")
	}
}

// When CLOWN_SESSION_ID and the hook session_id disagree CLOWN_SESSION_ID is
// still THE principal and is recorded (the mismatch is only logged), and the
// spawn hello still goes out.
func TestSessionStartDisagreeingPrincipalSourcesRecordTheEnv(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const envID = "6f6f6f6f-0000-4000-8000-000000000000"
	t.Setenv("CLOWN_SESSION_ID", envID)
	repo, wt := initSpawnedWorktree(t, "driver/key")

	fireSessionStartWithID(t, wt, "5e5e5e5e-0000-4000-8000-000000000000")

	st, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if st.Principal != envID {
		t.Errorf("Principal = %q, want the env principal %q when the sources disagree", st.Principal, envID)
	}
	if st.HelloSentAt == nil {
		t.Error("the hello must still go out and adopt the state")
	}
}

// A spawn-path state (SpawnedBy and Holders set) records the principal and the
// hello's later rewrite of the same state does not lose it.
func TestSessionStartOnSpawnedStateKeepsPrincipalThroughHello(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const self = "5e5e5e5e-0000-4000-8000-000000000000"
	t.Setenv("CLOWN_SESSION_ID", self)
	repo, wt := initSpawnedWorktree(t, "driver/key")

	fireSessionStart(t, wt)

	st, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if st.Principal != self {
		t.Errorf("Principal = %q, want %q after record + hello", st.Principal, self)
	}
	if st.HelloSentAt == nil {
		t.Error("HelloSentAt not set: the hello did not run")
	}

	fireSessionStart(t, wt) // resume/clear/compact re-fire
	if st, _ = session.Read(repo, "feature"); st.Principal != self {
		t.Errorf("Principal = %q after re-fire, want %q kept", st.Principal, self)
	}
}
