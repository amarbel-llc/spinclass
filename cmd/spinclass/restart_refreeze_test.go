package main

import (
	"io"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
)

// stubRefreezeHook replaces the frozen-hook refresh the preflight runs, records
// the worktree it was asked about, and returns rec.
func stubRefreezeHook(t *testing.T, rec *session.FrozenHook) *string {
	t.Helper()
	var asked string
	prev := refreezeFrozenHook
	refreezeFrozenHook = func(_ sweatfile.Sweatfile, rp worktree.ResolvedPath, _ *session.FrozenHook, _ io.Writer) *session.FrozenHook {
		asked = rp.AbsPath
		return rec
	}
	t.Cleanup(func() { refreezeFrozenHook = prev })
	return &asked
}

// A restart re-freezes the per-commit hook from the landed default branch, as
// `sc resume` does (spinclass#475): clown relaunches the session without
// `sc resume`, so the preflight that approves the restart is where it happens.
func TestRestartPreflightReFreezesThePreCommitHook(t *testing.T) {
	stubNotifyPrincipal(t)
	repoPath, wtPath := requestRestart(t)
	fresh := &session.FrozenHook{Frozen: true, BaseSha: "freshbase", BaseLabel: "origin/main"}
	asked := stubRefreezeHook(t, fresh)

	v, err := runRestartPreflight("worker/kid")
	if err != nil || v.Verdict != "restart" {
		t.Fatalf("got %+v, %v; want restart", v, err)
	}
	if *asked != wtPath {
		t.Errorf("re-freeze asked about %q, want the session worktree %q", *asked, wtPath)
	}
	st := readKid(t, repoPath)
	if st.FrozenHook == nil || st.FrozenHook.BaseSha != "freshbase" {
		t.Errorf("FrozenHook = %+v, want the re-frozen record recorded", st.FrozenHook)
	}
}

// A refused restart does not touch the hook: nothing is relaunched.
func TestRestartPreflightRefusedLeavesTheHookAlone(t *testing.T) {
	stubNotifyPrincipal(t)
	repoPath, _ := requestRestart(t)
	asked := stubRefreezeHook(t, &session.FrozenHook{Frozen: true, BaseSha: "freshbase"})
	st := readKid(t, repoPath)
	st.Kind = session.KindImplicit
	if err := session.Write(*st); err != nil {
		t.Fatal(err)
	}

	if v, err := runRestartPreflight("worker/kid"); err != nil || v.Verdict == "restart" {
		t.Fatalf("got %+v, %v; want no restart", v, err)
	}
	if *asked != "" {
		t.Errorf("re-freeze ran for a session that is not restarted (asked about %q)", *asked)
	}
}
