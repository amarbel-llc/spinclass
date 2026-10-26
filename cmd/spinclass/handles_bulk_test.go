package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// addSiblingChild stages a second child worktree session, "kid2", in the repo
// childHandleFixture created, carrying st's handle fields.
func addSiblingChild(t *testing.T, repoPath string, st session.State) {
	t.Helper()
	wtPath := filepath.Join(repoPath, ".worktrees", "kid2")
	testgit.MustWorktreeAdd(t, repoPath, wtPath, "kid2")
	st.SessionState = session.StateInactive
	st.RepoPath = repoPath
	st.WorktreePath = wtPath
	st.Branch = "kid2"
	st.SessionKey = "worker/kid2"
	st.Entrypoint = []string{"/bin/sh"}
	st.StartedAt = time.Now().UTC()
	if err := session.Write(st); err != nil {
		t.Fatal(err)
	}
}

// TestGrantSessionHandleBulk pins #342: `child` takes several sessions, every
// one is granted, and the result is one TAP document with a subtest per child.
func TestGrantSessionHandleBulk(t *testing.T) {
	const caller = "caller-principal"
	held := session.State{
		Holders:      []string{caller},
		HandleRights: map[string]string{caller: "observe,close,grant"},
	}
	repoPath, _ := childHandleFixture(t, caller, held)
	addSiblingChild(t, repoPath, held)

	text, isErr := callGrant(t, `{"child":["worker/kid","worker/kid2"],"to":"p9"}`)
	if isErr {
		t.Fatalf("expected the bulk grant to succeed, got error: %s", text)
	}
	for _, branch := range []string{"kid", "kid2"} {
		st, err := session.Read(repoPath, branch)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(st.PendingHandles, "p9") {
			t.Errorf("%s: PendingHandles = %v, want to contain p9", branch, st.PendingHandles)
		}
	}
	for _, want := range []string{"TAP version 14", "# Subtest: worker/kid\n", "# Subtest: worker/kid2", "ok 1 - worker/kid", "ok 2 - worker/kid2", "1..2"} {
		if !strings.Contains(text, want) {
			t.Errorf("result is missing %q:\n%s", want, text)
		}
	}
}

// TestGrantSessionHandleBulkIsIndependent: one child refusing does not abandon
// the rest, and the call still reports failure, naming the child that failed.
func TestGrantSessionHandleBulkIsIndependent(t *testing.T) {
	const caller = "caller-principal"
	repoPath, _ := childHandleFixture(t, caller, session.State{
		Holders:      []string{caller},
		HandleRights: map[string]string{caller: "observe,close,grant"},
	})

	text, isErr := callGrant(t, `{"child":["worker/nope","worker/kid"],"to":"p9"}`)
	if !isErr {
		t.Fatalf("expected an error when a child fails, got: %s", text)
	}
	if !strings.Contains(text, "not ok 1 - worker/nope") || !strings.Contains(text, "ok 2 - worker/kid") {
		t.Errorf("expected per-child verdicts, got:\n%s", text)
	}
	st, err := session.Read(repoPath, "kid")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(st.PendingHandles, "p9") {
		t.Errorf("the good child was not granted: PendingHandles = %v", st.PendingHandles)
	}
}

// TestReleaseSessionHandleBulk: release takes several children too.
func TestReleaseSessionHandleBulk(t *testing.T) {
	const caller = "caller-principal"
	held := session.State{Holders: []string{caller, "sibling"}}
	repoPath, _ := childHandleFixture(t, caller, held)
	addSiblingChild(t, repoPath, held)

	text, isErr := callRelease(t, `{"child":["worker/kid","worker/kid2"]}`)
	if isErr {
		t.Fatalf("expected the bulk release to succeed, got error: %s", text)
	}
	for _, branch := range []string{"kid", "kid2"} {
		st, err := session.Read(repoPath, branch)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(st.Holders, caller) {
			t.Errorf("%s: Holders = %v, want the caller released", branch, st.Holders)
		}
	}
	if !strings.Contains(text, "# Subtest: worker/kid2") {
		t.Errorf("expected a subtest per child, got:\n%s", text)
	}
}

// TestHandleChildParamsAreVariadic: the declared `child` param is variadic, so
// the MCP schema offers an array and `to`/`rights` become flag-only on the CLI.
func TestHandleChildParamsAreVariadic(t *testing.T) {
	for name, params := range map[string]func() []command.Param{
		"grant":   grantSessionHandleParamList,
		"release": releaseSessionHandleParamList,
	} {
		ps := params()
		if len(ps) == 0 || ps[0].Name != "child" || !ps[0].Variadic {
			t.Errorf("%s: first param = %+v, want a variadic child", name, ps)
		}
	}
}
