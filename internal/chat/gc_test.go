package chat

import (
	"os"
	"testing"
	"time"
)

func TestGCMessagesRemovesOldKeepsNew(t *testing.T) {
	setChatroom(t)

	old := time.Now().Add(-48 * time.Hour)
	fresh := time.Now().Add(-1 * time.Minute)
	mustSend(t, Message{From: "a/one", To: Broadcast, Body: "old", Timestamp: old})
	mustSend(t, Message{From: "a/two", To: Broadcast, Body: "fresh", Timestamp: fresh})

	retention := 24 * time.Hour

	if got := CountStaleMessages(retention); got != 1 {
		t.Fatalf("CountStaleMessages: expected 1, got %d", got)
	}

	removed, err := GCMessages(retention)
	if err != nil {
		t.Fatalf("GCMessages: %v", err)
	}
	if removed != 1 {
		t.Fatalf("GCMessages: expected 1 removed, got %d", removed)
	}

	// The fresh message survives and is still readable.
	got, err := Read("repo/me", ReadFilter{}, true)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got[0].Body != "fresh" {
		t.Fatalf("after GC: expected [fresh], got %+v", got)
	}
}

func TestGCMessagesRetentionZeroIsNoop(t *testing.T) {
	setChatroom(t)
	mustSend(t, Message{From: "a/one", To: Broadcast, Body: "x", Timestamp: time.Now().Add(-100 * time.Hour)})

	if got := CountStaleMessages(0); got != 0 {
		t.Errorf("CountStaleMessages(0): expected 0, got %d", got)
	}
	removed, err := GCMessages(0)
	if err != nil {
		t.Fatalf("GCMessages(0): %v", err)
	}
	if removed != 0 {
		t.Errorf("GCMessages(0): expected 0 removed, got %d", removed)
	}
	names, _ := entryFilenames()
	if len(names) != 1 {
		t.Errorf("retention<=0 should keep all messages, got %d", len(names))
	}
}

func TestGCReapsStaleCursorByMtime(t *testing.T) {
	setChatroom(t)
	const me = "repo/me"

	// Create a cursor by reading.
	mustSend(t, Message{From: "a/one", To: Broadcast, Body: "x"})
	if _, err := Read(me, ReadFilter{}, false); err != nil {
		t.Fatalf("Read: %v", err)
	}

	cp := cursorPath(me)
	if _, err := os.Stat(cp); err != nil {
		t.Fatalf("expected cursor file at %s: %v", cp, err)
	}

	// Backdate the cursor's mtime well past the retention window.
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(cp, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	removed, err := GCMessages(24 * time.Hour)
	if err != nil {
		t.Fatalf("GCMessages: %v", err)
	}
	// The message is fresh (kept); only the stale cursor is reaped.
	if removed != 1 {
		t.Fatalf("expected 1 removed (stale cursor), got %d", removed)
	}
	if _, err := os.Stat(cp); !os.IsNotExist(err) {
		t.Errorf("stale cursor should have been removed, stat err = %v", err)
	}
}

func TestGCKeepsFreshCursor(t *testing.T) {
	setChatroom(t)
	const me = "repo/me"
	mustSend(t, Message{From: "a/one", To: Broadcast, Body: "x"})
	if _, err := Read(me, ReadFilter{}, false); err != nil {
		t.Fatalf("Read: %v", err)
	}
	// Fresh cursor (just written) must survive GC.
	if _, err := GCMessages(24 * time.Hour); err != nil {
		t.Fatalf("GCMessages: %v", err)
	}
	if _, err := os.Stat(cursorPath(me)); err != nil {
		t.Errorf("fresh cursor should survive GC: %v", err)
	}
}
