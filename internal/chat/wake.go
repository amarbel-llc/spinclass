package chat

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// WakeMode selects the push path for chat messages. See
// docs/features/0009-cross-session-chat-monitor.md (clown-wake migration) and
// clown RFC-0009 (the job-wakeup channel contract).
type WakeMode string

const (
	// WakeModeLegacy pushes via the spinclass chat-watch plugin monitor
	// polling the chatroom file store (the pre-migration behavior).
	WakeModeLegacy WakeMode = "legacy"
	// WakeModeClown additionally emits each message as a clown job-wakeup
	// `message` event, delivered by clown's job-watch monitor. The chatroom
	// file store remains the system of record in both modes; the mode only
	// selects the push path.
	WakeModeClown WakeMode = "clown"
)

// ResolveWakeMode reads the chat wake mode from SPINCLASS_CHAT_WAKE. Unset or
// unrecognized values resolve to legacy, so the rollout default is the
// pre-migration behavior and a typo can never silently disable the store.
func ResolveWakeMode() WakeMode {
	if WakeMode(os.Getenv("SPINCLASS_CHAT_WAKE")) == WakeModeClown {
		return WakeModeClown
	}
	return WakeModeLegacy
}

// clownBin resolves the clown binary for wake emits: $CLOWN_BIN (exported by
// clown into every plugin MCP server, RFC-0009 §2) with a PATH-lookup
// fallback.
func clownBin() string {
	if v := os.Getenv("CLOWN_BIN"); v != "" {
		return v
	}
	return "clown"
}

// emitWakeTimeout bounds the clown CLI call so a wedged binary cannot hang
// chat-send. The emit is a local journal append + optional datagram; seconds
// is generous.
const emitWakeTimeout = 10 * time.Second

// EmitWake emits m as a clown job-wakeup `message` event addressed to m.To
// (a session key, or the broadcast key "*" — clown's reserved broadcast
// channel). No-op in legacy mode. The chatroom store write must already have
// happened: an emit failure means a lost push, never a lost message, so
// callers should surface the error without failing the send.
func EmitWake(ctx context.Context, m Message) error {
	if ResolveWakeMode() != WakeModeClown {
		return nil
	}

	// Detach from the caller's cancellation: the store write has already
	// happened, so the recipient should still be woken even if the sender's
	// MCP request is cancelled mid-emit. The timeout is the only bound.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), emitWakeTimeout)
	defer cancel()

	resultRef := fmt.Sprintf("chat-read from=%s peek=true", m.From)
	cmd := exec.CommandContext(
		ctx, clownBin(),
		"job", "message",
		"--target", m.To,
		"--from", m.From,
		"--source", "spinclass",
		"--message", m.Body,
		"--result-ref", resultRef,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := bytes.TrimSpace(stderr.Bytes())
		if len(detail) > 0 {
			return fmt.Errorf("clown wake emit: %w: %s", err, detail)
		}
		return fmt.Errorf("clown wake emit: %w", err)
	}
	return nil
}
