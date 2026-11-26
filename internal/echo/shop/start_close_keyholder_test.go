package shop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tap "code.linenisgreat.com/tap/go/pkgs/writer"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
	"code.linenisgreat.com/spinclass/internal/bravo/keyholdersweep"
)

// fakeUnits is the systemd the key holder stop talks to: it records the stopped
// units, and refuses to stop any when stuck.
type fakeUnits struct {
	stopped []string
	stuck   bool
}

func (f *fakeUnits) Start(context.Context, string, []string, []string, ...string) error { return nil }

func (f *fakeUnits) Active(context.Context, string) (bool, error) { return false, nil }

func (f *fakeUnits) Output(context.Context, []string, []string) ([]byte, error) { return nil, nil }

func (f *fakeUnits) Stop(_ context.Context, unit string) error {
	if f.stuck {
		return errors.New("the unit would not stop")
	}
	f.stopped = append(f.stopped, unit)
	return nil
}

// workerEntry is a session entry that runs and exits, optionally leaving one
// commit behind in a clean tree.
type workerEntry struct {
	mockExecutor
	t      *testing.T
	commit bool
}

func (w *workerEntry) Attach(dir, _ string, _ []string, _ bool, tp *tap.TestPoint) error {
	tp.Ok = true
	if w.commit {
		if err := os.WriteFile(filepath.Join(dir, "work"), []byte("w\n"), 0o644); err != nil {
			w.t.Fatal(err)
		}
		mustGit(w.t, dir, "add", "work")
		mustGit(w.t, dir, "commit", "-m", "work")
	}
	return nil
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// startWorkerWithPair sets up a repo and a running pair for the session sc start
// is about to create in it.
func startWorkerWithPair(t *testing.T, units *fakeUnits) (worktree.ResolvedPath, string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	prev := keyholdersweep.NewLauncher
	t.Cleanup(func() { keyholdersweep.NewLauncher = prev })
	keyholdersweep.NewLauncher = func() *keyholder.Launcher {
		return &keyholder.Launcher{Base: keyholder.StateBase(), Run: units}
	}
	repoDir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repoDir, "init", "-b", "master")
	mustGit(t, repoDir, "config", "user.email", "t@example.invalid")
	mustGit(t, repoDir, "config", "user.name", "T")
	mustGit(t, repoDir, "commit", "--allow-empty", "-m", "initial")
	rp := worktree.ResolvedPath{
		AbsPath:    filepath.Join(repoDir, worktree.WorktreesDir, "worker"),
		RepoPath:   repoDir,
		Branch:     "worker",
		SessionKey: "repo/worker",
	}
	pairDir := keyholder.SessionDir(keyholder.StateBase(), rp.SessionKey)
	if err := os.MkdirAll(pairDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return rp, pairDir
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// sc start's own closes (the auto-close of a merged, clean session, and
// --merge-on-close) end the session's key holder pair with the worktree.
func TestStartsOwnClosesStopTheKeyHolder(t *testing.T) {
	for _, tc := range []struct {
		name         string
		mergeOnClose bool
		assume       string
	}{
		{name: "auto-close", assume: "yes"},
		{name: "merge-on-close", mergeOnClose: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			units := &fakeUnits{}
			rp, pairDir := startWorkerWithPair(t, units)
			if tc.assume != "" {
				t.Setenv(EnvAutocloseAssume, tc.assume)
			}
			entry := &workerEntry{t: t, commit: tc.mergeOnClose}
			var buf bytes.Buffer
			if err := Attach(&buf, entry, rp, sweatfile.Sweatfile{}, "tap", tc.mergeOnClose, false, false, false, false); err != nil {
				t.Fatalf("Attach: %v\n%s", err, buf.String())
			}
			if exists(rp.AbsPath) {
				t.Fatalf("the session was not closed, the test proves nothing:\n%s", buf.String())
			}
			if len(units.stopped) != 2 || exists(pairDir) {
				t.Errorf("stopped units %v, pair directory left: %v; want both units stopped and no directory", units.stopped, exists(pairDir))
			}
		})
	}
}

// A pair that will not stop keeps the worktree, and the recovery command is
// printed.
func TestMergeOnCloseKeepsTheWorktreeOfAPairThatWillNotStop(t *testing.T) {
	rp, pairDir := startWorkerWithPair(t, &fakeUnits{stuck: true})
	stdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	var out bytes.Buffer
	done := make(chan struct{})
	go func() { _, _ = out.ReadFrom(r); close(done) }()
	var buf bytes.Buffer
	aerr := Attach(&buf, &workerEntry{t: t, commit: true}, rp, sweatfile.Sweatfile{}, "tap", true, false, false, false, false)
	os.Stdout = stdout
	_ = w.Close()
	<-done
	if aerr != nil {
		t.Fatalf("Attach: %v", aerr)
	}
	if !exists(rp.AbsPath) || !exists(pairDir) {
		t.Fatalf("worktree left: %v, pair directory left: %v; want both kept", exists(rp.AbsPath), exists(pairDir))
	}
	if !strings.Contains(out.String(), "sc keyholder-stop repo/worker") {
		t.Errorf("the recovery command is not printed (%d bytes of output)", out.Len())
	}
}
