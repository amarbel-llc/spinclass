package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// childHandleFixture stands up a repo with one child worktree session whose
// Holders/PendingHandles/HandleRights are set directly (bypassing spawn), for
// exercising the handle tools' flows against a precise starting shape. Mirrors
// childFixture's sandboxing (HOME/XDG_STATE_HOME, git excludes so the
// .spinclass/ state file doesn't read as an untracked dirty file — see
// childFixture's comment) but leaves the handle fields to the caller. st's
// identity fields (RepoPath/WorktreePath/Branch/SessionKey/Entrypoint/
// StartedAt) are overwritten; only the handle-related fields the caller set
// survive.
func childHandleFixture(t *testing.T, callerPrincipal string, st session.State) (repoPath, wtPath string) {
	t.Helper()
	testgit.RequireGit(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", callerPrincipal)
	t.Setenv("SPINCLASS_SESSION_ID", "")

	repoPath = filepath.Join(t.TempDir(), "worker")
	testgit.MustInit(t, repoPath)
	sweatfile := "[hooks]\ndisable-nix-gc = true\n"
	if err := os.WriteFile(filepath.Join(repoPath, "sweatfile"), []byte(sweatfile), 0o644); err != nil {
		t.Fatal(err)
	}
	wtPath = filepath.Join(repoPath, ".worktrees", "kid")
	testgit.MustWorktreeAdd(t, repoPath, wtPath, "kid")
	excludePath := filepath.Join(repoPath, ".git", "info", "exclude")
	if err := os.WriteFile(excludePath, []byte(".spinclass/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st.SessionState = session.StateInactive
	st.RepoPath = repoPath
	st.WorktreePath = wtPath
	st.Branch = "kid"
	st.SessionKey = "worker/kid"
	st.Entrypoint = []string{"/bin/sh"}
	st.StartedAt = time.Now().UTC()
	if err := session.Write(st); err != nil {
		t.Fatal(err)
	}
	return repoPath, wtPath
}

func callGrant(t *testing.T, args string) (text string, isErr bool) {
	t.Helper()
	res, err := handleGrantSessionHandle(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("handleGrantSessionHandle: %v", err)
	}
	return res.Text, res.IsErr
}

func callRelease(t *testing.T, args string) (text string, isErr bool) {
	t.Helper()
	res, err := handleReleaseSessionHandle(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("handleReleaseSessionHandle: %v", err)
	}
	return res.Text, res.IsErr
}

func callListHandles(t *testing.T, args string) (text string, isErr bool) {
	t.Helper()
	res, err := handleListHandles(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("handleListHandles: %v", err)
	}
	return res.Text, res.IsErr
}

func callWhoami(t *testing.T) (text string, isErr bool) {
	t.Helper()
	res, err := handleWhoami(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("handleWhoami: %v", err)
	}
	return res.Text, res.IsErr
}

// TestGrantSessionHandleByHolder is the motivating case: a caller already
// holding a handle (literally in Holders, not just via SpawnedByPrincipal)
// grants to a new principal, landing it in PendingHandles with the default
// rights.
func TestGrantSessionHandleByHolder(t *testing.T) {
	const caller = "caller-principal"
	repoPath, _ := childHandleFixture(t, caller, session.State{Holders: []string{caller}})

	text, isErr := callGrant(t, `{"child":"worker/kid","to":"p9"}`)
	if isErr {
		t.Fatalf("expected grant to succeed, got error: %s", text)
	}

	st, err := session.Read(repoPath, "kid")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(st.PendingHandles, "p9") {
		t.Errorf("PendingHandles = %v, want to contain p9", st.PendingHandles)
	}
	if got := st.HandleRights["p9"]; got != "observe,close" {
		t.Errorf("HandleRights[p9] = %q, want the default", got)
	}
}

// TestGrantSessionHandleByNonHolderRefused: a caller holding no handle at all
// is refused, naming the actual holder, and the child's state is left
// completely untouched.
func TestGrantSessionHandleByNonHolderRefused(t *testing.T) {
	const caller = "caller-principal"
	repoPath, _ := childHandleFixture(t, caller, session.State{
		SpawnedByPrincipal: "other-spawner",
		Holders:            []string{"other-spawner"},
	})

	text, isErr := callGrant(t, `{"child":"worker/kid","to":"p9"}`)
	if !isErr {
		t.Fatalf("expected grant to be refused, got success: %s", text)
	}
	if !strings.Contains(text, "other-spawner") {
		t.Errorf("refusal %q should name the actual holder", text)
	}

	st, err := session.Read(repoPath, "kid")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.PendingHandles) != 0 {
		t.Errorf("PendingHandles = %v, want unchanged (empty) after a refused grant", st.PendingHandles)
	}
}

// TestGrantSessionHandleByPendingHolderAcceptsFirst: a caller that was only
// PENDING exercises the handle by granting onward — first use — so it must be
// accepted (promoted into Holders) before the grant proceeds.
func TestGrantSessionHandleByPendingHolderAcceptsFirst(t *testing.T) {
	const caller = "caller-principal"
	repoPath, _ := childHandleFixture(t, caller, session.State{
		SpawnedByPrincipal: "other-spawner",
		Holders:            []string{"other-spawner"},
		PendingHandles:     []string{caller},
	})

	text, isErr := callGrant(t, `{"child":"worker/kid","to":"p9"}`)
	if isErr {
		t.Fatalf("expected the pending holder's grant to succeed (accept-on-first-use), got error: %s", text)
	}

	st, err := session.Read(repoPath, "kid")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(st.Holders, caller) {
		t.Errorf("Holders = %v, want to contain the accepted caller %q", st.Holders, caller)
	}
	if slices.Contains(st.PendingHandles, caller) {
		t.Errorf("caller should have moved out of PendingHandles after accepting, got %v", st.PendingHandles)
	}
	if !slices.Contains(st.PendingHandles, "p9") {
		t.Errorf("PendingHandles = %v, want to contain the newly-granted p9", st.PendingHandles)
	}
}

// TestReleaseSessionHandleByHolder covers both release outcomes: an orphan
// report when the caller was the last holder, and a "remaining holders"
// report otherwise.
func TestReleaseSessionHandleByHolder(t *testing.T) {
	t.Run("orphan when the last holder releases", func(t *testing.T) {
		const caller = "caller-principal"
		repoPath, _ := childHandleFixture(t, caller, session.State{Holders: []string{caller}})

		text, isErr := callRelease(t, `{"child":"worker/kid"}`)
		if isErr {
			t.Fatalf("expected release to succeed, got error: %s", text)
		}
		if !strings.Contains(text, "orphan") {
			t.Errorf("expected the result to report the orphan, got: %s", text)
		}

		st, err := session.Read(repoPath, "kid")
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Holders) != 0 {
			t.Errorf("Holders = %v, want empty", st.Holders)
		}
	})

	t.Run("remaining holders reported when not the last", func(t *testing.T) {
		const caller = "caller-principal"
		repoPath, _ := childHandleFixture(t, caller, session.State{Holders: []string{caller, "sibling"}})

		text, isErr := callRelease(t, `{"child":"worker/kid"}`)
		if isErr {
			t.Fatalf("expected release to succeed, got error: %s", text)
		}
		if !strings.Contains(text, "sibling") {
			t.Errorf("expected the remaining holder to be named, got: %s", text)
		}

		st, err := session.Read(repoPath, "kid")
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Holders) != 1 || st.Holders[0] != "sibling" {
			t.Errorf("Holders = %v, want [sibling]", st.Holders)
		}
	})
}

// TestReleaseSessionHandleByNonHolderRefused: releasing a handle the caller
// never held at all is a legible refusal, not a silent no-op.
func TestReleaseSessionHandleByNonHolderRefused(t *testing.T) {
	const caller = "caller-principal"
	childHandleFixture(t, caller, session.State{Holders: []string{"someone-else"}})

	text, isErr := callRelease(t, `{"child":"worker/kid"}`)
	if !isErr {
		t.Fatalf("expected release to be refused, got success: %s", text)
	}
	if !strings.Contains(text, "holds no handle") {
		t.Errorf("refusal %q should say the caller holds no handle", text)
	}
}

// TestListHandlesShowsHeldAndPendingAndAcceptPromotes covers list-handles
// across two DIFFERENT sessions — one where the caller is an accepted holder,
// one where it is only pending — and that accept:true promotes the pending
// one before reporting.
func TestListHandlesShowsHeldAndPendingAndAcceptPromotes(t *testing.T) {
	testgit.RequireGit(t)
	const caller = "caller-principal"
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", caller)
	t.Setenv("SPINCLASS_SESSION_ID", "")

	heldRepo := filepath.Join(t.TempDir(), "held-repo")
	testgit.MustInit(t, heldRepo)
	heldWt := filepath.Join(heldRepo, ".worktrees", "kid")
	testgit.MustWorktreeAdd(t, heldRepo, heldWt, "kid")
	if err := session.Write(session.State{
		SessionState: session.StateInactive,
		RepoPath:     heldRepo,
		WorktreePath: heldWt,
		Branch:       "kid",
		SessionKey:   "held-repo/kid",
		Holders:      []string{caller},
		HandleRights: map[string]string{caller: "observe,close"},
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	pendingRepo := filepath.Join(t.TempDir(), "pending-repo")
	testgit.MustInit(t, pendingRepo)
	pendingWt := filepath.Join(pendingRepo, ".worktrees", "kid")
	testgit.MustWorktreeAdd(t, pendingRepo, pendingWt, "kid")
	if err := session.Write(session.State{
		SessionState:   session.StateInactive,
		RepoPath:       pendingRepo,
		WorktreePath:   pendingWt,
		Branch:         "kid",
		SessionKey:     "pending-repo/kid",
		PendingHandles: []string{caller},
		HandleRights:   map[string]string{caller: "observe"},
		Entrypoint:     []string{"/bin/sh"},
		StartedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	text, isErr := callListHandles(t, `{}`)
	if isErr {
		t.Fatalf("list-handles failed: %s", text)
	}
	if !strings.Contains(text, "held    held-repo/kid") {
		t.Errorf("expected a held line for held-repo/kid, got: %s", text)
	}
	if !strings.Contains(text, "pending pending-repo/kid") {
		t.Errorf("expected a pending line for pending-repo/kid, got: %s", text)
	}

	text, isErr = callListHandles(t, `{"accept":true}`)
	if isErr {
		t.Fatalf("list-handles with accept failed: %s", text)
	}
	if strings.Contains(text, "pending pending-repo/kid") {
		t.Errorf("expected pending-repo/kid to be promoted to held after accept, got: %s", text)
	}
	if !strings.Contains(text, "held    pending-repo/kid") {
		t.Errorf("expected pending-repo/kid to show as held after accept, got: %s", text)
	}

	st, err := session.Read(pendingRepo, "kid")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.PendingHandles) != 0 {
		t.Errorf("PendingHandles = %v, want empty after accept", st.PendingHandles)
	}
	if !slices.Contains(st.Holders, caller) {
		t.Errorf("Holders = %v, want to contain %q after accept", st.Holders, caller)
	}
}

// TestListHandlesReportsNoHandles: an empty index (or one with no matching
// entries) reports "no handles" rather than an empty string.
func TestListHandlesReportsNoHandles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "caller-principal")

	text, isErr := callListHandles(t, `{}`)
	if isErr {
		t.Fatalf("list-handles failed: %s", text)
	}
	if text != "no handles" {
		t.Errorf("text = %q, want %q", text, "no handles")
	}
}

// TestSiblingReapsGrantedHandle is FDR 0032 slice 0's promotion-criteria case
// (docs/features/0032-principals-handles-and-provenance.md): principal A
// spawns (and holds a handle on) a child; A grants a handle to sibling B; B —
// a genuinely different principal, switched in via CLOWN_SESSION_ID, never
// the spawner — accepts on first use by reaping, and succeeds.
func TestSiblingReapsGrantedHandle(t *testing.T) {
	const principalA = "principal-a"
	const principalB = "principal-b"
	repoPath, wtPath := childHandleFixture(t, principalA, session.State{
		SpawnedByPrincipal: principalA,
		Holders:            []string{principalA},
	})

	grantText, isErr := callGrant(t, `{"child":"worker/kid","to":"principal-b"}`)
	if isErr {
		t.Fatalf("expected A's grant to succeed, got error: %s", grantText)
	}

	// Switch identity to B — the sibling the handle was granted to, never the
	// spawner A.
	t.Setenv("CLOWN_SESSION_ID", principalB)

	closeText, isErr := callCloseChild(t, `{"child":"worker/kid"}`)
	if isErr {
		t.Fatalf("expected B's reap to succeed (accept-on-first-use), got error: %s", closeText)
	}

	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree %s still present after reap", wtPath)
	}
	st, err := session.Read(repoPath, "kid")
	if err != nil {
		t.Fatalf("reading tombstone: %v", err)
	}
	if !slices.Contains(st.Holders, principalB) {
		t.Errorf("tombstone Holders = %v, want to contain %q", st.Holders, principalB)
	}
}

// TestWhoamiPrintsPrincipalAndSessionKey covers both a session with a known
// session key ($SPINCLASS_SESSION_ID set) and one with none (cwd outside any
// worktree/implicit session).
func TestWhoamiPrintsPrincipalAndSessionKey(t *testing.T) {
	t.Run("with SPINCLASS_SESSION_ID set", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		t.Setenv("CLOWN_SESSION_ID", "principal-a")
		t.Setenv("SPINCLASS_SESSION_ID", "myrepo/mybranch")

		text, isErr := callWhoami(t)
		if isErr {
			t.Fatalf("whoami failed: %s", text)
		}
		if !strings.Contains(text, "principal: principal-a") {
			t.Errorf("expected a principal line, got: %s", text)
		}
		if !strings.Contains(text, "session_key: myrepo/mybranch") {
			t.Errorf("expected the session key, got: %s", text)
		}
	})

	t.Run("with SPINCLASS_SESSION_ID unset and no session at cwd", func(t *testing.T) {
		testgit.RequireGit(t)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		t.Setenv("CLOWN_SESSION_ID", "principal-b")
		t.Setenv("SPINCLASS_SESSION_ID", "")
		t.Chdir(t.TempDir()) // deliberately not a git repository

		text, isErr := callWhoami(t)
		if isErr {
			t.Fatalf("whoami failed: %s", text)
		}
		if !strings.Contains(text, "principal: principal-b") {
			t.Errorf("expected a principal line, got: %s", text)
		}
		if !strings.Contains(text, "session_key: (none)") {
			t.Errorf("expected session_key: (none), got: %s", text)
		}
	})
}
