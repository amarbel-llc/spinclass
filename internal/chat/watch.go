package chat

import (
	"context"
	"time"
)

// pollInterval is how often Watch rescans the chatroom directory. A poll
// loop (rather than inotify/fsnotify) keeps the prototype dependency-free
// and portable across macOS and Linux; see the watch-strategy Tuning Lever
// in docs/features/0009-cross-session-chat-monitor.md.
const pollInterval = 1 * time.Second

// Watch streams messages addressed to sessionKey (broadcasts plus direct
// messages to that key) to the emit callback, in chronological order, until
// ctx is cancelled. It starts from the current end of the chatroom: messages
// already present when Watch begins are treated as seen, so only messages
// that arrive after start are delivered. This matches the monitor semantics
// — a freshly-started session reacts to new chatter, not the backlog.
//
// emit is called once per new message. A returned error from emit stops the
// watch and is returned to the caller.
func Watch(ctx context.Context, sessionKey string, emit func(Message) error) error {
	seen, err := entryFilenames()
	if err != nil {
		return err
	}
	seenSet := make(map[string]struct{}, len(seen))
	for _, n := range seen {
		seenSet[n] = struct{}{}
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			names, err := entryFilenames()
			if err != nil {
				return err
			}
			for _, name := range names {
				if _, ok := seenSet[name]; ok {
					continue
				}
				seenSet[name] = struct{}{}
				m, err := readMessage(name)
				if err != nil {
					// A message that fails to read (e.g. a partial write we
					// raced, or a malformed file) is skipped, not fatal: the
					// watch must survive one bad entry. It stays marked seen
					// so we don't retry it every tick.
					continue
				}
				if !m.addressedTo(sessionKey) {
					continue
				}
				if err := emit(m); err != nil {
					return err
				}
			}
		}
	}
}
