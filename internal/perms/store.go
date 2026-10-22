package perms

import (
	"encoding/json"
	"strings"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/store"
	gitstore "code.linenisgreat.com/spinclass/internal/store/git"
)

const gritToolPrefix = "mcp__plugin_spinclass_spinclass__grit_"

// storeToolsWithoutSubcommand are the grit tools whose verb has no
// `subcommand` argument (store.ClassOf keys them by the empty subcommand).
var storeToolsWithoutSubcommand = map[string]bool{"commit": true, "merge": true, "clone": true}

var storeTools = map[string]bool{
	"stage": true, "branch": true, "tag": true, "worktree": true, "remote": true, "stash": true,
	"commit": true, "merge": true, "clone": true,
}

var storeBackend store.Backend = gitstore.New()

// resolveStoreSession finds the calling session from its principal. It is a
// variable so tests can stand in for the session index.
var resolveStoreSession = func(principal string) (store.Session, bool) {
	key, ok := session.SessionKeyOfPrincipal(principal)
	if !ok {
		return store.Session{}, false
	}
	st, err := session.FindByTarget(key)
	if err != nil || st.Kind == session.KindImplicit || st.WorktreePath == "" || st.Branch == "" {
		return store.Session{}, false
	}
	return store.Session{Dir: st.WorktreePath, Branch: st.Branch}, true
}

// StoreVerdict is the PreToolUse reading of a grit store tool call (FDR 0036
// section 2). isStore is false for any other tool. An unreadable subcommand
// yields Ask.
//
// principal is the caller's principal as the hook sees it (CLOWN_SESSION_ID
// when set, else the hook's session_id; see hooks.principalForHook).
// principalKnown no longer changes the verdict; kept for logging. A principal
// that resolves to a recorded session lets Classify decide. An unresolved
// session (unknown, ambiguous, conflicting sources, or a confined caller whose
// hook runs in the unit, clown#289) yields Allow for a non-force call, deferred
// to the serve, whose classification and body ownership check refuse what the
// caller does not own; force yields Ask.
func StoreVerdict(toolName string, toolInput map[string]any, principal string, principalKnown bool) (verdict store.Verdict, reason string, isStore bool) {
	tool := strings.TrimPrefix(toolName, gritToolPrefix)
	if tool == toolName || !storeTools[tool] {
		return store.Ask, "", false
	}
	subcommand := ""
	if !storeToolsWithoutSubcommand[tool] {
		var ok bool
		if subcommand, ok = toolInput["subcommand"].(string); !ok {
			return store.Ask, "grit " + tool + ": subcommand is missing or not a string", true
		}
	}
	callerSession, ok := resolveStoreSession(principal)
	if !ok {
		if forceRequested(toolInput) {
			return store.Ask, "grit " + tool + ": force always asks, even when the session is unresolved", true
		}
		return store.Allow, "grit " + tool + ": session unresolved here, deferred to the serve: body ownership check applies", true
	}
	args, err := json.Marshal(toolInput)
	if err != nil {
		return store.Ask, "grit " + tool + ": arguments could not be read", true
	}
	op := store.Op{Tool: tool, Subcommand: subcommand, Args: args}
	if storeBackend.Classify(callerSession, op) == store.Allow {
		return store.Allow, "grit " + tool + " " + subcommand + " is a read or touches only session-owned refs", true
	}
	return store.Ask, "grit " + tool + " " + subcommand + " targets something this session does not own, or is a floor operation; confirm", true
}
