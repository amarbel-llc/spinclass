package clean

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/bravo/keyholdersweep"
)

// stopUnits is the systemd the key holder stop talks to.
type stopUnits struct {
	stopped []string
	stuck   bool
}

func (f *stopUnits) Start(context.Context, string, []string, []string, ...string) error { return nil }

func (f *stopUnits) Active(context.Context, string) (bool, error) { return false, nil }

func (f *stopUnits) Output(context.Context, []string, []string) ([]byte, error) { return nil, nil }

func (f *stopUnits) Stop(_ context.Context, unit string) error {
	if f.stuck {
		return errors.New("the unit would not stop")
	}
	f.stopped = append(f.stopped, unit)
	return nil
}

// A worktree `sc clean` removes takes its session's key holder pair with it; a
// pair that will not stop keeps the worktree and names the way out.
func TestRemoveWorktreeStopsTheSessionsKeyHolder(t *testing.T) {
	for _, stuck := range []bool{false, true} {
		t.Run(map[bool]string{false: "stops", true: "will-not-stop"}[stuck], func(t *testing.T) {
			_, repoDir := setupRepo(t)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			units := &stopUnits{stuck: stuck}
			prev := keyholdersweep.NewLauncher
			t.Cleanup(func() { keyholdersweep.NewLauncher = prev })
			keyholdersweep.NewLauncher = func() *keyholder.Launcher {
				return &keyholder.Launcher{Base: keyholder.StateBase(), Run: units}
			}
			wtPath := filepath.Join(repoDir, ".worktrees", "merged-k")
			runGit(t, repoDir, "worktree", "add", "-b", "merged-k", wtPath)
			key := filepath.Base(repoDir) + "/merged-k"
			pairDir := keyholder.SessionDir(keyholder.StateBase(), key)
			if err := os.MkdirAll(pairDir, 0o700); err != nil {
				t.Fatal(err)
			}

			err := removeWorktree(worktreeInfo{repo: filepath.Base(repoDir), branch: "merged-k", repoPath: repoDir, worktreePath: wtPath, merged: true}, nil)

			_, wtErr := os.Stat(wtPath)
			_, pairErr := os.Stat(pairDir)
			if stuck {
				if err == nil || !strings.Contains(err.Error(), "sc keyholder-stop "+key) {
					t.Fatalf("removeWorktree = %v; want a refusal naming sc keyholder-stop %s", err, key)
				}
				if wtErr != nil || pairErr != nil {
					t.Errorf("worktree: %v, pair directory: %v; want both kept", wtErr, pairErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("removeWorktree: %v", err)
			}
			if wtErr == nil || pairErr == nil || len(units.stopped) != 2 {
				t.Errorf("worktree left: %v, pair directory left: %v, stopped %v; want all gone", wtErr == nil, pairErr == nil, units.stopped)
			}
		})
	}
}
