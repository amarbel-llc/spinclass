package hooks

import (
	"fmt"
	"io"

	"code.linenisgreat.com/spinclass/internal/perms"
	"code.linenisgreat.com/spinclass/internal/store"
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
	verdict, reason, isStore := perms.StoreVerdict(input.ToolName, toolInput)
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
