package hooks

import (
	"os"
	"testing"

	"code.linenisgreat.com/spinclass/internal/session"
)

// The hook can only record what its own environment carries: with
// CLOWN_SESSION_ID unset it records nothing, and the spawn hello then still
// rewrites the state without inventing a principal (#392: every real worker's
// hook-trace.log shows clown_session_id_set=false).
func TestSessionStartWithoutClownSessionIDRecordsNoPrincipal(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "")
	_ = os.Unsetenv("CLOWN_SESSION_ID")
	repo, wt := initSpawnedWorktree(t, "driver/key")

	fireSessionStart(t, wt)

	st, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if st.Principal != "" {
		t.Errorf("Principal = %q, want empty without CLOWN_SESSION_ID", st.Principal)
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
