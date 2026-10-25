package mergelock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ageMarkers backdates every marker past the settle window, so liveness is
// decided by the flock probe rather than by freshness.
func ageMarkers(t *testing.T, gitCommonDir string) {
	t.Helper()
	queueDir := filepath.Join(gitCommonDir, QueueDirName)
	entries, err := os.ReadDir(queueDir)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	for _, entry := range entries {
		if err := os.Chtimes(filepath.Join(queueDir, entry.Name()), old, old); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJoinCountsOtherLiveMerges(t *testing.T) {
	dir := t.TempDir()

	ahead, leaveFirst := Join(dir)
	if ahead != 0 {
		t.Fatalf("first joiner: ahead = %d, want 0", ahead)
	}
	ageMarkers(t, dir)
	ahead, leaveSecond := Join(dir)
	if ahead != 1 {
		t.Fatalf("second joiner: ahead = %d, want 1", ahead)
	}
	ageMarkers(t, dir)
	ahead, leaveThird := Join(dir)
	if ahead != 2 {
		t.Fatalf("third joiner: ahead = %d, want 2", ahead)
	}

	leaveFirst()
	leaveFirst() // idempotent
	leaveThird()
	ageMarkers(t, dir)
	ahead, leaveFourth := Join(dir)
	defer leaveFourth()
	defer leaveSecond()
	if ahead != 1 {
		t.Fatalf("after two left: ahead = %d, want 1", ahead)
	}
}

// A marker whose owner died (nothing holds its flock) is reaped, not counted,
// once it is past the settle window.
func TestJoinReapsDeadMarker(t *testing.T) {
	dir := t.TempDir()
	queueDir := filepath.Join(dir, QueueDirName)
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(queueDir, "merge-dead")
	if err := os.WriteFile(dead, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ageMarkers(t, dir)

	ahead, leave := Join(dir)
	defer leave()
	if ahead != 0 {
		t.Errorf("dead marker counted: ahead = %d, want 0", ahead)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("dead marker not removed: %v", err)
	}
}

// A marker younger than the settle window may not be locked yet, so it counts.
func TestJoinCountsFreshUnlockedMarker(t *testing.T) {
	dir := t.TempDir()
	queueDir := filepath.Join(dir, QueueDirName)
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queueDir, "merge-fresh"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	ahead, leave := Join(dir)
	defer leave()
	if ahead != 1 {
		t.Errorf("fresh marker: ahead = %d, want 1", ahead)
	}
}

func TestJoinDegradesWhenQueueDirUnusable(t *testing.T) {
	dir := t.TempDir()
	// A regular file where the queue directory should be.
	if err := os.WriteFile(filepath.Join(dir, QueueDirName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ahead, leave := Join(dir)
	leave()
	if ahead != 0 {
		t.Errorf("ahead = %d, want 0", ahead)
	}
}
