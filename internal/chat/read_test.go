package chat

import "testing"

func TestReadFirehoseThenCursorAdvances(t *testing.T) {
	setChatroom(t)
	const me = "repo/me"

	mustSend(t, Message{From: "a/one", To: Broadcast, Body: "first"})
	mustSend(t, Message{From: "b/two", To: me, Body: "second"})

	got, err := Read(me, ReadFilter{}, false)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("firehose: expected 2 messages, got %d: %+v", len(got), got)
	}

	// Second read: cursor advanced, nothing new.
	got2, err := Read(me, ReadFilter{}, false)
	if err != nil {
		t.Fatalf("Read 2: %v", err)
	}
	if len(got2) != 0 {
		t.Fatalf("after cursor advance: expected 0, got %d: %+v", len(got2), got2)
	}
}

func TestReadPeekDoesNotAdvance(t *testing.T) {
	setChatroom(t)
	const me = "repo/me"
	mustSend(t, Message{From: "a/one", To: Broadcast, Body: "x"})

	peeked, err := Read(me, ReadFilter{}, true)
	if err != nil {
		t.Fatalf("peek Read: %v", err)
	}
	if len(peeked) != 1 {
		t.Fatalf("peek: expected 1, got %d", len(peeked))
	}

	// A normal read must still see it — peek did not advance the cursor.
	got, err := Read(me, ReadFilter{}, false)
	if err != nil {
		t.Fatalf("Read after peek: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("after peek: expected 1 (peek must not advance), got %d", len(got))
	}
}

func TestReadFilters(t *testing.T) {
	setChatroom(t)
	const me = "repo/me"

	mustSend(t, Message{From: "alpha/one", To: Broadcast, Body: "bcast"})
	mustSend(t, Message{From: "alpha/two", To: me, Body: "dm-to-me"})
	mustSend(t, Message{From: "beta/three", To: "other/x", Body: "not-mine"})

	cases := []struct {
		name   string
		filter ReadFilter
		want   []string // bodies, in order
	}{
		{"firehose", ReadFilter{}, []string{"bcast", "dm-to-me", "not-mine"}},
		{"to_me", ReadFilter{ToMe: true}, []string{"bcast", "dm-to-me"}},
		{"from", ReadFilter{From: "beta/three"}, []string{"not-mine"}},
		{"repo", ReadFilter{Repo: "alpha"}, []string{"bcast", "dm-to-me"}},
		{"to_me+repo", ReadFilter{ToMe: true, Repo: "alpha"}, []string{"bcast", "dm-to-me"}},
		{"from+repo mismatch", ReadFilter{From: "alpha/one", Repo: "beta"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// peek so each subtest sees the full set independent of cursor.
			got, err := Read(me, c.filter, true)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("expected %d (%v), got %d: %+v", len(c.want), c.want, len(got), got)
			}
			for i, body := range c.want {
				if got[i].Body != body {
					t.Errorf("position %d: got %q want %q", i, got[i].Body, body)
				}
			}
		})
	}
}

func TestReadFilteredAdvancesPastExcluded(t *testing.T) {
	setChatroom(t)
	const me = "repo/me"

	mustSend(t, Message{From: "other/x", To: "other/y", Body: "not-for-me"})
	mustSend(t, Message{From: "p/q", To: me, Body: "for-me"})

	// Filtered read returns only the addressed message...
	got, err := Read(me, ReadFilter{ToMe: true}, false)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got[0].Body != "for-me" {
		t.Fatalf("filtered read: expected [for-me], got %+v", got)
	}

	// ...but the cursor advanced read-through, so a later firehose read shows
	// nothing — the excluded "not-for-me" was acknowledged, per the locked
	// read-through cursor decision.
	rest, err := Read(me, ReadFilter{}, false)
	if err != nil {
		t.Fatalf("Read 2: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("expected 0 after read-through advance, got %+v", rest)
	}
}

func TestRepoOf(t *testing.T) {
	cases := map[string]string{
		"madder/rare-buckeye": "madder",
		"a/b/c":               "a",
		"noslash":             "noslash",
		"":                    "",
	}
	for in, want := range cases {
		if got := repoOf(in); got != want {
			t.Errorf("repoOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadEmptyRoom(t *testing.T) {
	setChatroom(t)
	got, err := Read("repo/me", ReadFilter{}, false)
	if err != nil {
		t.Fatalf("Read on empty room: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 from empty room, got %d", len(got))
	}
}

// TestCursorFileNotReadAsMessage ensures the cursor file Read writes is not
// itself surfaced as a message by entryFilenames.
func TestCursorFileNotReadAsMessage(t *testing.T) {
	setChatroom(t)
	const me = "repo/me"
	mustSend(t, Message{From: "a/one", To: Broadcast, Body: "x"})
	if _, err := Read(me, ReadFilter{}, false); err != nil { // writes a cursor file
		t.Fatalf("Read: %v", err)
	}
	names, err := entryFilenames()
	if err != nil {
		t.Fatalf("entryFilenames: %v", err)
	}
	for _, n := range names {
		if isCursorFile(n) {
			t.Errorf("cursor file %q leaked into entryFilenames", n)
		}
	}
}
