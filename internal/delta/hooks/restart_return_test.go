package hooks

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// The SessionStart hook of a relaunched session marks the return of a restart
// and tells the same audience the start wake told (#419, FDR 0037).

type returnWake struct{ target, message string }

func stubReturnWakes(t *testing.T) *[]returnWake {
	t.Helper()
	var calls []returnWake
	orig := notifyPrincipal
	notifyPrincipal = func(_ context.Context, target, _, message string) error {
		calls = append(calls, returnWake{target, message})
		return nil
	}
	t.Cleanup(func() { notifyPrincipal = orig })
	return &calls
}

const returnSelf = "5e5e5e5e-0000-4000-8000-000000000000"

// restartingWorktree stages myrepo/feature mid-restart: restarting since a
// minute ago, asked by "boss", held by "spawner" and the session itself.
func restartingWorktree(t *testing.T) (repo, wt string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", returnSelf)
	repo, wt = initSpawnedWorktree(t, "")
	st, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	since := time.Now().UTC().Add(-time.Minute)
	st.RestartingAt = &since
	st.RestartingBy = "boss"
	st.RestartingReason = "why"
	st.Holders = []string{"spawner", returnSelf}
	if err := session.Write(*st); err != nil {
		t.Fatal(err)
	}
	return repo, wt
}

func targetsOfWakes(calls []returnWake) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.target)
	}
	slices.Sort(out)
	return out
}

func TestSessionStartAfterARestartStampsRestartedAtAndWakes(t *testing.T) {
	wakes := stubReturnWakes(t)
	repo, wt := restartingWorktree(t)

	fireSessionStart(t, wt)

	st, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if st.RestartedAt == nil || st.RestartedAt.Before(*st.RestartingAt) {
		t.Fatalf("RestartedAt = %v, want stamped at or after RestartingAt %v", st.RestartedAt, st.RestartingAt)
	}
	if got, want := targetsOfWakes(*wakes), []string{"boss", "spawner"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("woken = %v, want %v (never the session itself)", got, want)
	}
	for _, w := range *wakes {
		if want := "myrepo/feature is back after its restart"; w.message != want {
			t.Errorf("message = %q, want %q", w.message, want)
		}
	}
}

func TestSessionStartWithoutARestartInFlightDoesNothing(t *testing.T) {
	wakes := stubReturnWakes(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo, wt := initSpawnedWorktree(t, "")

	fireSessionStart(t, wt)

	st, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if st.RestartedAt != nil || len(*wakes) != 0 {
		t.Errorf("RestartedAt = %v, wakes = %+v; want neither", st.RestartedAt, *wakes)
	}
}

func TestSessionStartAfterARestartWakesOnlyOnce(t *testing.T) {
	wakes := stubReturnWakes(t)
	repo, wt := restartingWorktree(t)

	fireSessionStart(t, wt)
	first, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	n := len(*wakes)
	fireSessionStart(t, wt) // resume/clear/compact re-fire

	second, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if len(*wakes) != n || !second.RestartedAt.Equal(*first.RestartedAt) {
		t.Errorf("a second SessionStart re-notified or re-stamped: wakes %d -> %d, RestartedAt %v -> %v",
			n, len(*wakes), first.RestartedAt, second.RestartedAt)
	}
}

// stubHookParent makes the hook's parent pid `parent` and treats only the pids
// in `provable` as provably the session's.
func stubHookParent(t *testing.T, parent int, provable ...int) {
	t.Helper()
	origP, origB := hookParentPID, pidIsSessions
	hookParentPID = func() int { return parent }
	pidIsSessions = func(pid int, _, _ string) bool { return slices.Contains(provable, pid) }
	t.Cleanup(func() { hookParentPID, pidIsSessions = origP, origB })
}

// A parent that is not provably the session's (a transient shell wrapper) is
// never recorded, and a recorded pid that already proves out is kept.
func TestSessionStartKeepsAProvablePIDAndSkipsAnUnprovableParent(t *testing.T) {
	stubReturnWakes(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo, wt := initSpawnedWorktree(t, "")
	st, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	st.PID = 777
	if err := session.Write(*st); err != nil {
		t.Fatal(err)
	}

	stubHookParent(t, 888) // 888 unprovable
	fireSessionStart(t, wt)
	if got, _ := session.Read(repo, "feature"); got.PID == 888 {
		t.Fatalf("recorded an unprovable parent")
	}

	stubHookParent(t, 999, 777, 999) // recorded 777 provable, new parent 999 too
	got0, _ := session.Read(repo, "feature")
	got0.PID = 777
	if err := session.Write(*got0); err != nil {
		t.Fatal(err)
	}
	fireSessionStart(t, wt)
	if got, _ := session.Read(repo, "feature"); got.PID != 777 {
		t.Fatalf("PID = %d, want the already-provable 777 kept", got.PID)
	}
}

// After a restart (or a hand `clown restart`) the recorded pid is the old sc
// launcher, which close cannot prove is the session's. The relaunch's
// SessionStart re-records the agent process so close can stop it.
func TestSessionStartReRecordsTheRelaunchedProcessPID(t *testing.T) {
	stubReturnWakes(t)
	repo, wt := restartingWorktree(t)
	stubHookParent(t, 424242, 424242)

	fireSessionStart(t, wt)

	st, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if st.PID != 424242 {
		t.Fatalf("PID = %d, want the relaunched hook parent 424242", st.PID)
	}
}

// A relaunch with no restart in flight (a hand `clown restart`) re-records too.
func TestSessionStartWithoutARestartRecordInFlightStillReRecordsThePID(t *testing.T) {
	stubReturnWakes(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo, wt := initSpawnedWorktree(t, "")
	stubHookParent(t, 515151, 515151)

	fireSessionStart(t, wt)

	st, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if st.PID != 515151 {
		t.Fatalf("PID = %d, want 515151", st.PID)
	}
}

// Inside a unit the pid is in the unit's namespace and is not authority.
func TestSessionStartInAUnitDoesNotRecordAPID(t *testing.T) {
	stubReturnWakes(t)
	repo, wt := restartingWorktree(t)
	t.Setenv("CLOWN_UNIT", "spinclass-agent-myrepo-feature.service")
	before, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	stubHookParent(t, 424242, 424242)

	fireSessionStart(t, wt)

	st, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if st.PID != before.PID {
		t.Fatalf("PID = %d, want unchanged %d inside a unit", st.PID, before.PID)
	}
}

// A hook inside a confined unit reaches only the display copy, so it must not
// pretend to mark a return the frontend record cannot see.
func TestSessionStartInAUnitAfterARestartDoesNothing(t *testing.T) {
	wakes := stubReturnWakes(t)
	repo, wt := restartingWorktree(t)
	t.Setenv("CLOWN_UNIT", "spinclass-agent-myrepo-feature.service")

	fireSessionStart(t, wt)

	st, err := session.Read(repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if st.RestartedAt != nil || len(*wakes) != 0 {
		t.Errorf("RestartedAt = %v, wakes = %+v; want neither inside a unit", st.RestartedAt, *wakes)
	}
}
