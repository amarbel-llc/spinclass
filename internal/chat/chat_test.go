package chat

import (
	"context"
	"sync"
	"testing"
	"time"
)

// setChatroom points the chatroom at a temp dir for the duration of a test
// by overriding XDG_STATE_HOME (the base xdgStateBase reads).
func setChatroom(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func TestSendThenReadRoundTrips(t *testing.T) {
	setChatroom(t)

	want := Message{From: "repo-a/feat-x", To: Broadcast, Body: "hello world"}
	if err := Send(want); err != nil {
		t.Fatalf("Send: %v", err)
	}

	names, err := entryFilenames()
	if err != nil {
		t.Fatalf("entryFilenames: %v", err)
	}
	if len(names) != 1 {
		t.Fatalf("expected 1 message file, got %d: %v", len(names), names)
	}
	got, err := readMessage(names[0])
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	if got.From != want.From || got.To != want.To || got.Body != want.Body {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, want)
	}
	if got.Timestamp.IsZero() {
		t.Error("Send should have stamped a timestamp")
	}
}

func TestSendDefaultsEmptyToBroadcast(t *testing.T) {
	setChatroom(t)
	if err := Send(Message{From: "a/b", Body: "x"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	names, _ := entryFilenames()
	got, _ := readMessage(names[0])
	if got.To != Broadcast {
		t.Errorf("empty To should default to %q, got %q", Broadcast, got.To)
	}
}

func TestAddressedTo(t *testing.T) {
	key := "repo/branch"
	cases := []struct {
		to   string
		want bool
	}{
		{Broadcast, true},
		{key, true},
		{"other/branch", false},
	}
	for _, c := range cases {
		if got := (Message{To: c.to}).addressedTo(key); got != c.want {
			t.Errorf("addressedTo(%q) with to=%q: got %v want %v", key, c.to, got, c.want)
		}
	}
}

func TestEntryFilenamesSortedAndFiltered(t *testing.T) {
	setChatroom(t)
	// Send three messages with distinct timestamps; filenames must come
	// back in chronological order regardless of send order.
	base := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	for i, off := range []int{2, 0, 1} {
		m := Message{From: "a/b", To: Broadcast, Body: "m", Timestamp: base.Add(time.Duration(off) * time.Second)}
		if err := Send(m); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}
	names, err := entryFilenames()
	if err != nil {
		t.Fatalf("entryFilenames: %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("expected 3, got %d", len(names))
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Errorf("not sorted: %q !< %q", names[i-1], names[i])
		}
	}
}

func TestWatchDeliversOnlyNewAddressedMessages(t *testing.T) {
	setChatroom(t)

	// A pre-existing message must NOT be delivered: Watch starts from the
	// current end of the room.
	if err := Send(Message{From: "old/sender", To: Broadcast, Body: "backlog"}); err != nil {
		t.Fatalf("seed Send: %v", err)
	}

	const me = "repo/me"
	var (
		mu  sync.Mutex
		got []Message
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, me, func(m Message) error {
			mu.Lock()
			got = append(got, m)
			mu.Unlock()
			return nil
		})
	}()

	// Give Watch a tick to snapshot the backlog as "seen".
	time.Sleep(50 * time.Millisecond)

	// Now send: one broadcast, one DM to me, one DM to someone else.
	mustSend(t, Message{From: "x/y", To: Broadcast, Body: "to-all"})
	mustSend(t, Message{From: "x/y", To: me, Body: "to-me"})
	mustSend(t, Message{From: "x/y", To: "other/one", Body: "not-for-me"})

	// Poll for the two we expect, up to a generous deadline.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected 2 delivered messages, got %d: %+v", len(got), got)
	}
	bodies := map[string]bool{}
	for _, m := range got {
		bodies[m.Body] = true
	}
	if !bodies["to-all"] || !bodies["to-me"] {
		t.Errorf("expected to-all and to-me delivered, got %+v", got)
	}
	if bodies["backlog"] {
		t.Error("backlog message should not have been delivered")
	}
	if bodies["not-for-me"] {
		t.Error("message addressed to another session should not have been delivered")
	}
}

func mustSend(t *testing.T, m Message) {
	t.Helper()
	if err := Send(m); err != nil {
		t.Fatalf("Send: %v", err)
	}
}
