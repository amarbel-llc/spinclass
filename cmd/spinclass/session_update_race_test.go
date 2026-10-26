package main

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// raceRounds is how many concurrent pairs each lost-update test runs against one
// session record (#348).
const raceRounds = 40

// runRacingPairs runs a(i) and b(i) concurrently for every round, all rounds at
// once, and waits for them.
func runRacingPairs(a, b func(i int)) {
	var wg sync.WaitGroup
	for i := 0; i < raceRounds; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); a(i) }()
		go func() { defer wg.Done(); b(i) }()
	}
	wg.Wait()
}

func grantAll(t *testing.T, repoPath string) {
	t.Helper()
	st, err := session.Read(repoPath, "kid")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < raceRounds; i++ {
		to := fmt.Sprintf("p%d", i)
		if !slices.Contains(st.PendingHandles, to) {
			t.Errorf("handle granted to %s was lost (a concurrent writer overwrote it): PendingHandles has %d of %d", to, len(st.PendingHandles), raceRounds)
			return
		}
	}
}

// A handle grant and a credential update touch different fields of one session;
// neither may drop the other's write.
func TestConcurrentGrantAndCredentialUpdateLoseNothing(t *testing.T) {
	const caller = "caller-principal"
	repoPath, _ := childHandleFixture(t, caller, session.State{
		Holders:      []string{caller},
		HandleRights: map[string]string{caller: "observe,close,grant"},
	})

	runRacingPairs(
		func(i int) {
			if text, err := grantSessionHandle("worker/kid", fmt.Sprintf("p%d", i), ""); err != nil {
				t.Errorf("grant %d: %v (%s)", i, err, text)
			}
		},
		func(i int) {
			c := &session.Credential{MintedAt: time.Now().UTC()}
			if err := session.UpdateCredential(repoPath, "kid", c); err != nil {
				t.Errorf("update credential %d: %v", i, err)
			}
		},
	)

	grantAll(t, repoPath)
	st, err := session.Read(repoPath, "kid")
	if err != nil {
		t.Fatal(err)
	}
	if st.Credential == nil {
		t.Error("credential update was lost")
	}
}

// A restart request and a handle grant touch different fields of one session.
func TestConcurrentRestartAndGrantLoseNothing(t *testing.T) {
	const caller = "caller-principal"
	stubRestartHandoffConcurrent(t)
	repoPath, _ := restartChildFixture(t, caller, session.State{
		Holders:      []string{caller},
		HandleRights: map[string]string{caller: "observe,close,grant,restart"},
	})

	runRacingPairs(
		func(i int) {
			if text, err := grantSessionHandle("worker/kid", fmt.Sprintf("p%d", i), ""); err != nil {
				t.Errorf("grant %d: %v (%s)", i, err, text)
			}
		},
		func(i int) {
			if text, err := runRestartChild(restartChildParams{Child: "worker/kid"}); err != nil {
				t.Errorf("restart %d: %v (%s)", i, err, text)
			}
		},
	)

	grantAll(t, repoPath)
	if readKid(t, repoPath).RestartRequest == nil {
		t.Error("restart request was lost (a concurrent grant overwrote it)")
	}
}

// stubRestartHandoffConcurrent stubs the hand-off with a stub that is safe to
// call from several goroutines (stubRestartHandoff records calls unguarded).
func stubRestartHandoffConcurrent(t *testing.T) {
	t.Helper()
	orig := restartHandoffFn
	restartHandoffFn = func(string, []string) (string, error) { return "", nil }
	t.Cleanup(func() { restartHandoffFn = orig })
}
