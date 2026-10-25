package hooks

import (
	"fmt"
	"io"
	"os"

	"code.linenisgreat.com/spinclass/internal/alfa/store"
	"code.linenisgreat.com/spinclass/internal/perms"
)

// storeDecision classifies a grit store tool call. isStore is false for any
// other tool. The returned writer emits allow (with the worktree path rewrite
// when it changed the input) or ask.
func storeDecision(input hookInput, mainRepoRoot, sessionWorktree string, rewriteEnabled bool) (decision func(io.Writer) error, isStore bool) {
	toolInput := input.ToolInput
	rewritten, summary := map[string]any(nil), ""
	if rewriteEnabled && mainRepoRoot != "" && sessionWorktree != "" {
		if updated, changedKeys, changed := rewriteWorktreePaths(input, resolvePath(mainRepoRoot), resolvePath(sessionWorktree)); changed {
			toolInput, rewritten, summary = updated, updated, changedKeys
		}
	}
	envPrincipal := os.Getenv("CLOWN_SESSION_ID")
	logPrincipalMismatch(input.CWD, envPrincipal, input.SessionID)
	principal, _, _ := principalForHook(envPrincipal, input.SessionID)
	// CLOWN_UNIT is a hint, not a boundary: an unconfined caller that sets it
	// only gets the serve's body refusal instead of a prompt (FDR 0035).
	inUnit := os.Getenv("CLOWN_UNIT") != ""
	verdict, reason, isStore := perms.StoreVerdict(input.ToolName, toolInput, principal, input.CWD, inUnit)
	if !isStore {
		return nil, false
	}
	if verdict != store.Allow {
		return func(w io.Writer) error { return writeAsk(w, reason) }, true
	}
	if rewritten == nil {
		return func(w io.Writer) error { return writeAllow(w, reason) }, true
	}
	rewriteReason := fmt.Sprintf("redirected into the session worktree (%s): %s", resolvePath(sessionWorktree), summary)
	return func(w io.Writer) error {
		return writeRewrite(w, rewritten, reason+"; "+rewriteReason, "[spinclass] "+rewriteReason)
	}, true
}
