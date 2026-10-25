package mergelock

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// QueueDirName is the directory inside the git common dir holding one marker
// file per merge currently in the repo's merge queue (spinclass#363).
const QueueDirName = "spinclass-merge.queue"

// markerSettle is how long a marker is presumed live without probing it: its
// owner creates the file and only then locks it, so a marker younger than
// this may simply not be locked yet.
const markerSettle = 5 * time.Second

// Join registers the caller in the repo's merge queue and reports how many
// OTHER merges are in it — waiting for the lock or holding it — so the caller
// can report the depth it found on arrival. Call it before Acquire and call
// leave (idempotent) once the merge is over, after Release.
//
// The flock on the merge lock has no waiter list, so membership is a marker
// file per merge, each held under its owner's own flock. A marker whose lock
// can be taken belongs to a process that died without leaving; it is removed
// rather than counted, so a crash never inflates the depth.
//
// It is telemetry only and carries no locking semantics: every failure
// degrades to (0, no-op) and never affects the merge.
func Join(gitCommonDir string) (ahead int, leave func()) {
	noop := func() {}
	dir := filepath.Join(gitCommonDir, QueueDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, noop
	}
	own, err := os.CreateTemp(dir, "merge-*")
	if err != nil {
		return 0, noop
	}
	_, _ = tryFlock(own)

	var once sync.Once
	leave = func() {
		once.Do(func() {
			_ = os.Remove(own.Name())
			_ = syscall.Flock(int(own.Fd()), syscall.LOCK_UN)
			_ = own.Close()
		})
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, leave
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if path == own.Name() {
			continue
		}
		if markerIsLive(path) {
			ahead++
		}
	}
	return ahead, leave
}

// markerIsLive reports whether path is another merge's live queue marker,
// removing it when its owner is gone.
func markerIsLive(path string) bool {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false // already left
	}
	defer file.Close()
	if info, statErr := file.Stat(); statErr == nil && time.Since(info.ModTime()) < markerSettle {
		return true
	}
	acquired, err := tryFlock(file)
	if err != nil || !acquired {
		return err == nil
	}
	_ = os.Remove(path)
	return false
}
