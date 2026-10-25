package job

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The worktree's .spinclass directory is writable by the worker it hosts. A
// symlink planted at job.log must not be followed by the frontend: through a
// live link the create would truncate the target, and through a dangling one it
// would make it.
func TestStartDoesNotFollowAPlantedJobLogSymlink(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, target string)
	}{
		{"live target", func(t *testing.T, target string) {
			if err := os.WriteFile(target, []byte("precious"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"dangling target", func(t *testing.T, target string) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wt := t.TempDir()
			dir := filepath.Join(wt, ".spinclass")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "sentinel")
			tc.setup(t, target)
			if err := os.Symlink(target, filepath.Join(dir, "job.log")); err != nil {
				t.Fatal(err)
			}

			_, err := Start(wt, KindCheck, false, "planted", func(ctx context.Context, w io.Writer) (string, bool) {
				_, _ = io.WriteString(w, "output\n")
				return "ok", false
			})
			if err == nil {
				select {
				case <-WaitDone(wt):
				case <-time.After(10 * time.Second):
					t.Fatal("job did not finish")
				}
			}

			b, rerr := os.ReadFile(target)
			switch tc.name {
			case "live target":
				if string(b) != "precious" {
					t.Errorf("the planted link's target was rewritten: %q (%v)", b, rerr)
				}
			default:
				if rerr == nil {
					t.Errorf("the frontend created the sentinel through the planted link: %q", b)
				}
			}
		})
	}
}

// A symlinked .spinclass directory redirects every write under it.
func TestWriteRefusesASymlinkedSpinclassDir(t *testing.T) {
	wt := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(wt, ".spinclass")); err != nil {
		t.Fatal(err)
	}
	if err := Write(wt, &Job{ID: "x", Kind: KindCheck, Status: StatusRunning, ServePID: os.Getpid(), StartedAt: time.Now()}); err == nil {
		entries, _ := os.ReadDir(elsewhere)
		t.Errorf("Write followed the symlinked .spinclass; %d entries landed in the redirect target", len(entries))
	}
}
