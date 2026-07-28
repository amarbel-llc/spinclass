package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// TestAuthorizeChildReapMatrix pins the #249 authorization contract: the ONLY
// authorized combination is a child whose spawned_by equals the caller's own
// session key. Every other row must refuse — a fall-open here would let any
// session close any other session in `sc list`.
func TestAuthorizeChildReapMatrix(t *testing.T) {
	child := func(spawnedBy string) session.State {
		return session.State{
			RepoPath:     "/repos/worker",
			WorktreePath: "/repos/worker/.worktrees/kid",
			Branch:       "kid",
			SessionKey:   "worker/kid",
			SpawnedBy:    spawnedBy,
		}
	}

	cases := []struct {
		name      string
		callerKey string
		child     session.State
		wantErr   bool
		wantIn    []string
	}{
		{
			name:      "spawned by the caller is authorized",
			callerKey: "driver/main-oak",
			child:     child("driver/main-oak"),
		},
		{
			name:      "spawned by another driver is refused, naming both keys",
			callerKey: "driver/main-oak",
			child:     child("other/elm"),
			wantErr:   true,
			wantIn:    []string{"worker/kid", "other/elm", "driver/main-oak"},
		},
		{
			name:      "never-spawned session is refused",
			callerKey: "driver/main-oak",
			child:     child(""),
			wantErr:   true,
			wantIn:    []string{"worker/kid", "driver/main-oak", "spawned_by"},
		},
		{
			name:      "unresolvable caller identity is refused, not defaulted",
			callerKey: "",
			child:     child("driver/main-oak"),
			wantErr:   true,
			wantIn:    []string{"worker/kid"},
		},
		{
			name:      "empty caller must not match an empty lineage",
			callerKey: "",
			child:     child(""),
			wantErr:   true,
			wantIn:    []string{"worker/kid"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := authorizeChildReap(tc.callerKey, tc.child)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("authorizeChildReap: unexpected refusal: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected a refusal, got nil")
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q is missing %q", err, want)
				}
			}
		})
	}
}

// childFixture stands up a repo with one spawned child worktree session and
// returns the repo path, the child's worktree path, and the driver key the
// child records as its spawned_by. HOME/XDG_STATE_HOME are sandboxed and
// SPINCLASS_SESSION_ID supplies the caller identity currentSessionKey reads
// first, so no live driver worktree is needed. Nix gc is disabled via the
// repo sweatfile — a reap is not the place to exercise the store.
func childFixture(t *testing.T, driverKey, spawnedBy string) (repoPath, wtPath string) {
	t.Helper()
	testgit.RequireGit(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPINCLASS_SESSION_ID", driverKey)

	repoPath = filepath.Join(t.TempDir(), "worker")
	testgit.MustInit(t, repoPath)
	sweatfile := "[hooks]\ndisable-nix-gc = true\n"
	if err := os.WriteFile(filepath.Join(repoPath, "sweatfile"), []byte(sweatfile), 0o644); err != nil {
		t.Fatal(err)
	}

	wtPath = filepath.Join(repoPath, ".worktrees", "kid")
	testgit.MustWorktreeAdd(t, repoPath, wtPath, "kid")

	// Mirror production setup: a spinclass-managed worktree gets `.spinclass/`
	// written into `.git/info/exclude` by worktree.Create -> applyGitExcludes
	// (it is in sweatfile.GetDefault's baseline excludes). This fixture stages
	// the worktree with a raw `git worktree add`, bypassing that, so the
	// session state session.Write drops at <wt>/.spinclass/state.json would
	// read as an untracked file — and close.RunResolved's porcelain check
	// would call the child dirty and refuse to reap it without --force,
	// defeating the very case these tests cover.
	//
	// On the host a personal global core.excludesFile masks that; inside the
	// nix sandbox there is none, so omitting this passes locally and fails in
	// the gate. Same trap, same fix, as internal/shop/shop_test.go (#65).
	excludePath := filepath.Join(repoPath, ".git", "info", "exclude")
	if err := os.WriteFile(excludePath, []byte(".spinclass/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := session.Write(session.State{
		SessionState: session.StateInactive,
		RepoPath:     repoPath,
		WorktreePath: wtPath,
		Branch:       "kid",
		SessionKey:   "worker/kid",
		SpawnedBy:    spawnedBy,
		Entrypoint:   []string{"/bin/sh"},
		StartedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return repoPath, wtPath
}

// mustCommitInWorktree adds an unmerged commit to the child's branch so the
// close safety check (commits ahead of the default branch) fires.
func mustCommitInWorktree(t *testing.T, wtPath string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("unmerged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", wtPath, "add", "work.txt"},
		{"-C", wtPath, "commit", "-m", "child work"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func callCloseChild(t *testing.T, args string) (text string, isErr bool) {
	t.Helper()
	res, err := handleCloseChildSession(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("handleCloseChildSession: %v", err)
	}
	return res.Text, res.IsErr
}

// TestCloseChildSessionReapsCleanChild is the motivating case (#249): a child
// this session spawned that never produced a commit — a failed hello handshake
// leaves exactly this shape — reaps without force. Worktree gone, branch gone,
// session state tombstoned.
func TestCloseChildSessionReapsCleanChild(t *testing.T) {
	const driverKey = "driver/main-oak"
	repoPath, wtPath := childFixture(t, driverKey, driverKey)

	text, isErr := callCloseChild(t, `{"child":"worker/kid"}`)
	if isErr {
		t.Fatalf("expected the reap to succeed, got error result: %s", text)
	}

	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree %s still present after reap (stat err = %v)", wtPath, err)
	}
	out, err := exec.Command("git", "-C", repoPath, "branch", "--list", "kid").Output()
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("branch kid still exists after reap: %q", out)
	}
	st, err := session.Read(repoPath, "kid")
	if err != nil {
		t.Fatalf("reading tombstone: %v", err)
	}
	if got := st.ResolveState(); got != session.StateAbandoned {
		t.Errorf("state after reap = %q, want %q (tombstone)", got, session.StateAbandoned)
	}
}

// TestCloseChildSessionRefusesForeignChild covers the authorization refusal on
// the real handler path: the child exists and is perfectly reapable, but it
// belongs to another driver, so nothing is torn down.
func TestCloseChildSessionRefusesForeignChild(t *testing.T) {
	_, wtPath := childFixture(t, "driver/main-oak", "other/elm")

	text, isErr := callCloseChild(t, `{"child":"worker/kid"}`)
	if !isErr {
		t.Fatalf("expected a refusal, got success: %s", text)
	}
	for _, want := range []string{"other/elm", "driver/main-oak"} {
		if !strings.Contains(text, want) {
			t.Errorf("refusal %q is missing session key %q", text, want)
		}
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("refused reap must leave the worktree intact: %v", err)
	}
}

// TestCloseChildSessionUnmergedNeedsForce pins that close.RunResolved's
// existing safety check still governs an owned child: unmerged commits and no
// TTY to confirm on means a legible --force refusal (never a hang), and the
// same call with force reaps.
func TestCloseChildSessionUnmergedNeedsForce(t *testing.T) {
	const driverKey = "driver/main-oak"
	repoPath, wtPath := childFixture(t, driverKey, driverKey)
	mustCommitInWorktree(t, wtPath)

	text, isErr := callCloseChild(t, `{"child":"worker/kid"}`)
	if !isErr {
		t.Fatalf("expected a refusal for an unmerged child, got success: %s", text)
	}
	if !strings.Contains(text, "--force") {
		t.Errorf("refusal %q does not point at --force", text)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("refused reap must leave the worktree intact: %v", err)
	}

	text, isErr = callCloseChild(t, `{"child":"worker/kid","force":true}`)
	if isErr {
		t.Fatalf("expected force to reap the unmerged child, got error result: %s", text)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree %s still present after forced reap (stat err = %v)", wtPath, err)
	}
	if _, err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "refs/heads/kid").Output(); err == nil {
		t.Error("branch kid still exists after forced reap")
	}
}

// TestCloseChildSessionRejectsMissingArgs covers the cheap rejections that
// must fire before any teardown: an empty child, and a target that matches no
// session at all.
func TestCloseChildSessionRejectsMissingArgs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPINCLASS_SESSION_ID", "driver/main-oak")

	cases := []struct{ name, args, want string }{
		{"empty child", `{}`, "child is required"},
		{"unknown child", `{"child":"no-such-session"}`, "no spinclass session"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, isErr := callCloseChild(t, tc.args)
			if !isErr {
				t.Fatalf("expected an error result, got success: %s", text)
			}
			if !strings.Contains(text, tc.want) {
				t.Errorf("error %q does not contain %q", text, tc.want)
			}
		})
	}
}
