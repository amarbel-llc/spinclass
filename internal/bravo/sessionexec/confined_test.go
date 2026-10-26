package sessionexec

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// spinclass#400: an operator shell in a once-confined worktree would run direnv
// and project tooling the worker could have planted.

func writeSession(t *testing.T) (wt string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	wt = filepath.Join(home, "repo", ".worktrees", "feat")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := session.Write(session.State{
		RepoPath: filepath.Join(home, "repo"), WorktreePath: wt, Branch: "feat", SessionKey: "repo/feat",
		SessionState: session.StateInactive, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return wt
}

func TestExecIntoOnceConfinedWorktreeRefuses(t *testing.T) {
	wt := writeSession(t)
	rec := confinement.UnitRecordPath(os.Getenv("XDG_STATE_HOME"), wt)
	if err := os.MkdirAll(filepath.Dir(rec), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := resolve("repo/feat")
	wantRefusal(t, err)
	t.Chdir(wt)
	_, _, err = resolve("")
	wantRefusal(t, err)
}

func TestExecIntoNeverConfinedWorktreeIsUnchanged(t *testing.T) {
	wt := writeSession(t)
	dir, env, err := resolve("repo/feat")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if dir != wt || len(env) == 0 {
		t.Errorf("dir = %q, env len %d; want the worktree and identity env", dir, len(env))
	}
}

func TestRefuseOnceConfinedFailsClosedWhenStateDirIsUnknowable(t *testing.T) {
	st := &session.State{SessionKey: "repo/feat", WorktreePath: t.TempDir()}
	wantRefusal(t, refuseOnceConfined("", errors.New("no home"), st))
}

func wantRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("no refusal")
	}
	for _, w := range []string{"repo/feat", "#400", "sc resume repo/feat", "sc close repo/feat"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("refusal %q does not contain %q", err, w)
		}
	}
}
