package main

import (
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// restart-preflight --peek is clown's last check before relaunching an agent
// it already stopped (#419): every way a session can be gone must read
// "closed", so the frontend never relaunches into a worktree being removed.
func TestRestartPeekReportsEveryClosedState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, repoPath string, st *session.State)
		want   string
		reason string
	}{
		{name: "live", want: peekStateLive, reason: "is live"},
		{
			name: "marked closing",
			mutate: func(t *testing.T, _ string, st *session.State) {
				now := time.Now().UTC()
				st.ClosingAt = &now
				writeState(t, st)
			},
			want: peekStateClosed, reason: "is being closed",
		},
		{
			name: "abandoned",
			mutate: func(t *testing.T, _ string, st *session.State) {
				st.SessionState = session.StateAbandoned
				writeState(t, st)
			},
			want: peekStateClosed, reason: "is closed",
		},
		{
			name: "tombstoned",
			mutate: func(t *testing.T, repoPath string, st *session.State) {
				if err := session.Tombstone(repoPath, st.Branch, ""); err != nil {
					t.Fatal(err)
				}
			},
			want: peekStateClosed, reason: "closed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoPath, _ := restartSelfFixture(t, true)
			if tc.mutate != nil {
				tc.mutate(t, repoPath, readKid(t, repoPath))
			}
			v, err := runRestartPeek("worker/kid")
			if err != nil {
				t.Fatalf("runRestartPeek: %v", err)
			}
			if v.Verdict != "none" || v.State != tc.want || !strings.Contains(v.Reason, tc.reason) {
				t.Errorf("got %+v, want verdict none, state %q, reason containing %q", v, tc.want, tc.reason)
			}
		})
	}
}

func TestRestartPeekOfAnUnknownSessionIsClosed(t *testing.T) {
	restartSelfFixture(t, true)
	v, err := runRestartPeek("nope/nothing")
	if err != nil || v.State != peekStateClosed {
		t.Fatalf("got %+v, %v; want closed", v, err)
	}
}

// --peek is read-only: a queued request survives it, for the preflight to
// consume.
func TestRestartPeekConsumesNothing(t *testing.T) {
	repoPath, _ := requestRestart(t)
	if v, err := runRestartPeek("worker/kid"); err != nil || v.State != peekStateLive {
		t.Fatalf("got %+v, %v; want live", v, err)
	}
	st := readKid(t, repoPath)
	if st.RestartRequest == nil || st.RestartingAt != nil {
		t.Errorf("peek changed state: request=%v restartingAt=%v", st.RestartRequest, st.RestartingAt)
	}
}

func writeState(t *testing.T, st *session.State) {
	t.Helper()
	if err := session.Write(*st); err != nil {
		t.Fatal(err)
	}
}
