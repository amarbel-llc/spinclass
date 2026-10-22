package spawn

import (
	"os"
	"path/filepath"
	"testing"
)

// spawn.log and spawn-window.log live in the worker-writable .spinclass
// directory. A symlink planted there must not be followed: the log open would
// otherwise append to (or create) whatever file the link names.
func TestStartDetachedDoesNotFollowAPlantedLogSymlink(t *testing.T) {
	for _, name := range []string{"spawn.log", "spawn-window.log"} {
		t.Run(name, func(t *testing.T) {
			wt := t.TempDir()
			dir := filepath.Join(wt, ".spinclass")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(t.TempDir(), "victim")
			if err := os.WriteFile(victim, []byte("precious"), 0o644); err != nil {
				t.Fatal(err)
			}
			dangling := filepath.Join(t.TempDir(), "created-through-link")
			link := filepath.Join(dir, name)
			if err := os.Symlink(victim, link); err != nil {
				t.Fatal(err)
			}

			err := startDetached([]string{"/bin/sh", "-c", "echo appended-by-entry"}, wt, os.Environ(), link)
			if err == nil {
				t.Error("startDetached opened the log through the planted symlink")
			}
			if b, _ := os.ReadFile(victim); string(b) != "precious" {
				t.Errorf("the planted link's target was written: %q", b)
			}

			_ = os.Remove(link)
			if err := os.Symlink(dangling, link); err != nil {
				t.Fatal(err)
			}
			_ = startDetached([]string{"/bin/sh", "-c", "true"}, wt, os.Environ(), link)
			if _, err := os.Lstat(dangling); err == nil {
				t.Error("startDetached created a file through the planted dangling symlink")
			}
		})
	}
}
