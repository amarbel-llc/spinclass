package chat

import (
	"context"

	"github.com/amarbel-llc/spinclass/internal/clown"
)

// EmitWake emits m as a clown job-wakeup `message` event addressed to m.To
// (a session key, or the broadcast key "*" — clown's reserved broadcast
// channel), delivered by clown's job-watch monitor. Gated on clown.Enabled():
// the emit fires whenever the session runs under clown and is dormant
// otherwise. The chatroom store write must already have happened: an emit
// failure means a lost push, never a lost message, so callers should surface
// the error without failing the send.
func EmitWake(ctx context.Context, m Message) error {
	if !clown.Enabled() {
		return nil
	}
	// The wake line carries only the subject — the harness truncates long
	// notification events (#103); the result_ref names the body's recovery
	// path.
	return clown.SendMessage(ctx, m.To, m.From, clown.Source, m.DisplaySubject(), m.RecoveryHint())
}
