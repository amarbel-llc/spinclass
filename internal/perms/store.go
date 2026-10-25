package perms

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/alfa/store"
	gitstore "code.linenisgreat.com/spinclass/internal/alfa/store/git"
	"code.linenisgreat.com/spinclass/internal/confinement"
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

// resolveStoreSession finds the calling session from its principal through the
// frontend principal mapping only (never a session record's own principal
// field). The error says why a session could not be found. It is a variable so
// tests can stand in for the session index.
var resolveStoreSession = func(principal string) (store.Session, error) {
	key, err := confinement.SessionKeyOfPrincipal(principal)
	if err != nil {
		return store.Session{}, err
	}
	st, err := session.FindByTarget(key)
	if err != nil {
		return store.Session{}, err
	}
	if st.Kind == session.KindImplicit || st.WorktreePath == "" || st.Branch == "" {
		return store.Session{}, fmt.Errorf("session %s is not a worktree session", key)
	}
	return ScopedStoreSession(st.WorktreePath, st.Branch, principal), nil
}

// resolveStoreSessionAtCwd finds the calling session from the directory the
// call is made in, with no principal (#415). It is how an UNCONFINED caller is
// resolved: the principal mapping is a confinement concept, written only for
// confined workers, and outside a unit there is no boundary for it to defend.
// A variable so tests can stand in for the session index.
var resolveStoreSessionAtCwd = func(cwd, principal string) (store.Session, error) {
	if cwd == "" {
		return store.Session{}, errors.New("no working directory to resolve the session from")
	}
	st, err := session.FindByWorktreePath(cwd)
	if err != nil {
		return store.Session{}, fmt.Errorf("no session has %s in its worktree: %w", cwd, err)
	}
	if st.IsTombstone() || st.Kind == session.KindImplicit || st.WorktreePath == "" || st.Branch == "" {
		return store.Session{}, fmt.Errorf("%s is not inside a live worktree session", cwd)
	}
	return ScopedStoreSession(st.WorktreePath, st.Branch, principal), nil
}

// ScopedStoreSession is the session a permission reading classifies against:
// the worktree and branch, plus the cross-session scoping (store.Session
// SessionAt and Held), so a call aimed at another session's worktree asks
// before any git runs there.
func ScopedStoreSession(dir, branch, principal string) store.Session {
	return store.Session{Dir: dir, Branch: branch, SessionAt: session.KeyAtWorktree, Held: session.HeldKeys(principal)}
}

// StoreVerdict is the PreToolUse reading of a grit store tool call (FDR 0036
// section 2). isStore is false for any other tool. An unreadable subcommand
// yields Ask.
//
// principal is the caller's principal as the hook sees it (CLOWN_SESSION_ID
// when set, else the hook's session_id; see hooks.principalForHook). A
// principal that resolves to a recorded session lets Classify decide.
//
// inUnit is true when the hook runs inside a confined unit (clown sets
// CLOWN_UNIT there and nowhere else, FDR 0035 "CLOWN_UNIT"). There the hook
// cannot resolve the session, so an unresolved non-force call yields Allow,
// deferred to the serve, whose classification and body ownership check refuse
// what the caller does not own. Outside a unit a principal with no mapping is
// the normal case (only confined workers have one), so the session is resolved
// from cwd, the hook payload's working directory, and Classify decides as for
// any session (#415). Only when neither resolves does the call ask. Force asks
// whenever the session is unresolved.
func StoreVerdict(toolName string, toolInput map[string]any, principal, cwd string, inUnit bool) (verdict store.Verdict, reason string, isStore bool) {
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
	callerSession, resolveErr := resolveStoreSession(principal)
	if resolveErr != nil && !inUnit {
		if atCwd, cwdErr := resolveStoreSessionAtCwd(cwd, principal); cwdErr == nil {
			callerSession, resolveErr = atCwd, nil
		} else {
			resolveErr = fmt.Errorf("%v; and from the working directory: %v", resolveErr, cwdErr)
		}
	}
	if resolveErr != nil {
		if forceRequested(toolInput) {
			return store.Ask, "grit " + tool + ": force always asks, even when the session is unresolved", true
		}
		if inUnit {
			return store.Allow, "grit " + tool + ": confined unit, session unresolved here, deferred to the serve: body ownership check applies", true
		}
		return store.Ask, "grit " + tool + ": session unresolved (" + resolveErr.Error() + ") and the caller is not in a confined unit; confirm", true
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
