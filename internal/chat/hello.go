package chat

import (
	"fmt"
	"strings"
	"time"
)

// HelloSubject is the spawn handshake subject prefix; WaitForHello keys on it
// so unrelated worker chatter cannot satisfy the gate.
const HelloSubject = "hello from spawned session"

// helloPollInterval is how often WaitForHello re-peeks the chatroom between
// the immediate first check and the deadline.
const helloPollInterval = 250 * time.Millisecond

// SendHello posts the spawn handshake from the worker session (from) to the
// driver session (to). Fired by the worker's SessionStart hook when its
// state carries spawned_by (FDR 0006). Send handles the clown wake emit.
func SendHello(from, to string) error {
	return Send(Message{
		From: from, To: to,
		Subject: HelloSubject + " " + from,
		Body:    "spawn handshake (FDR 0006): session " + from + " is up.",
	})
}

// WaitForHello blocks until a hello from `from` addressed to reader, newer
// than since, appears — or deadline elapses (the error names the deadline
// and the worker key). Polls every 250ms with peek reads, so the reader's
// cursor never advances: the hello stays visible to the reader's own
// chat-read after the wait returns.
func WaitForHello(reader, from string, since time.Time, deadline time.Duration) error {
	helloArrived := func() (bool, error) {
		msgs, err := Read(reader, ReadFilter{ToMe: true, From: from}, true)
		if err != nil {
			return false, err
		}
		for _, m := range msgs {
			if strings.HasPrefix(m.Subject, HelloSubject) && m.Timestamp.After(since) {
				return true, nil
			}
		}
		return false, nil
	}

	// Check once before the first tick so an already-arrived hello does not
	// pay the poll interval.
	if ok, err := helloArrived(); err != nil {
		return err
	} else if ok {
		return nil
	}

	ticker := time.NewTicker(helloPollInterval)
	defer ticker.Stop()
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return fmt.Errorf("no hello from spawned session %s within %s (spawn handshake deadline, FDR 0006)", from, deadline)
		case <-ticker.C:
			if ok, err := helloArrived(); err != nil {
				return err
			} else if ok {
				return nil
			}
		}
	}
}
