package chat

import (
	"context"
	"os"

	"github.com/amarbel-llc/spinclass/internal/clown"
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

// EmitWake emits m as a clown job-wakeup `message` event addressed to m.To
// (a session key, or the broadcast key "*" — clown's reserved broadcast
// channel). Gated on clown.Enabled() — sender CAPABILITY — not on the chat
// wake mode: SPINCLASS_CHAT_WAKE only selects the RECEIVE path (whether
// chat-watch stands down), and gating the emit on it opened a mixed-window
// delivery hole where a pre-flip legacy-mode sender never woke a clown-mode
// receiver whose chat-watch had already stood down. A legacy-mode RECEIVER
// of a now-always-emitted wake sees at worst a duplicate notification
// (chat-watch + job-watch), the accepted transitional trade-off. The
// chatroom store write must already have happened: an emit failure means a
// lost push, never a lost message, so callers should surface the error
// without failing the send.
func EmitWake(ctx context.Context, m Message) error {
	if !clown.Enabled() {
		return nil
	}
	// The wake line carries only the subject — the harness truncates long
	// notification events (#103); the result_ref names the body's recovery
	// path.
	return clown.SendMessage(ctx, m.To, m.From, clown.Source, m.DisplaySubject(), m.RecoveryHint())
}
