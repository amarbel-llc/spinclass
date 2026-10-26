package hooks

import (
	"os"
	"path/filepath"
	"strings"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// Read-only moxy tools the hook auto-allows on the caller's own worktree and
// the worktrees of sessions it holds an accepted handle on (spinclass#372).
const (
	moxyToolPrefix  = "mcp__plugin_moxy_moxy__"
	moxyFolioRead   = moxyToolPrefix + "folio_read"
	moxyFolioLs     = moxyToolPrefix + "folio_ls"
	moxyFolioGlob   = moxyToolPrefix + "folio_glob"
	moxyFolioTree   = moxyToolPrefix + "folio_tree"
	moxyFolioType   = moxyToolPrefix + "folio_file-type"
	moxyGritBranch  = moxyToolPrefix + "grit_branch"
	moxyGritStage   = moxyToolPrefix + "grit_stage"
	madderURIPrefix = "madder://"
)

// loadSessionStates is the session index the allow reads held children from. A
// variable so tests can stand in for it.
var loadSessionStates = func() []session.State {
	all, err := session.ListAll(nil)
	if err != nil {
		return nil
	}
	return all
}

// moxyReadPathArgs returns the filesystem paths a read-only moxy call names, or
// ok=false when the call is not one of the allowed read-only shapes (any other
// tool, a mutating subcommand, an escaping glob pattern, a non-path value). A
// missing path argument stands for the tool's default, the working directory.
func moxyReadPathArgs(input hookInput) (paths []string, ok bool) {
	args := input.ToolInput
	str := func(key string) (value string, present, valid bool) {
		v, has := args[key]
		if !has {
			return "", false, true
		}
		s, isStr := v.(string)
		return s, true, isStr
	}
	single := func(key string) ([]string, bool) {
		s, present, valid := str(key)
		if !valid || strings.HasPrefix(s, madderURIPrefix) {
			return nil, false
		}
		if !present || s == "" {
			s = input.CWD
		}
		return []string{s}, true
	}
	switch input.ToolName {
	case moxyFolioRead:
		return single("file_path")
	case moxyFolioLs, moxyFolioTree, moxyFolioType:
		return single("path")
	case moxyFolioGlob:
		pattern, _, valid := str("pattern")
		if !valid || strings.HasPrefix(pattern, "/") {
			return nil, false
		}
		for _, seg := range strings.Split(pattern, "/") {
			if seg == ".." {
				return nil, false
			}
		}
		return single("path")
	case moxyGritBranch, moxyGritStage:
		sub, _, valid := str("subcommand")
		if !valid {
			return nil, false
		}
		readOnly := map[string]bool{"log": true, "rev-parse": true, "diff": true}
		if input.ToolName == moxyGritStage {
			readOnly = map[string]bool{"status": true, "diff": true}
		}
		if !readOnly[sub] {
			return nil, false
		}
		repo, ok := single("repo_path")
		if !ok {
			return nil, false
		}
		base := repo[0]
		if !filepath.IsAbs(base) {
			base = filepath.Join(input.CWD, base)
		}
		paths = repo
		if raw, present := args["paths"]; present {
			list, isList := raw.([]any)
			if !isList {
				return nil, false
			}
			for _, e := range list {
				s, isStr := e.(string)
				if !isStr {
					return nil, false
				}
				if !filepath.IsAbs(s) {
					s = filepath.Join(base, s)
				}
				paths = append(paths, s)
			}
		}
		return paths, true
	}
	return nil, false
}

// moxyReadRoot is a directory tree a read-only moxy call may touch.
type moxyReadRoot struct {
	path string // symlink-resolved
	kind string // "own worktree" or "held child <key>"
	own  bool
}

// moxyReadRoots is the caller's own worktree plus the worktree of every live
// session the principal holds an ACCEPTED handle on. A pending grant confers no
// authority (FDR 0032 D12), and an empty principal holds nothing.
func moxyReadRoots(ownWorktree, principal string) []moxyReadRoot {
	var roots []moxyReadRoot
	if ownWorktree != "" {
		roots = append(roots, moxyReadRoot{path: resolvePath(ownWorktree), kind: "own worktree", own: true})
	}
	if principal == "" {
		return roots
	}
	for _, st := range loadSessionStates() {
		if st.IsTombstone() || st.Kind == session.KindImplicit || st.WorktreePath == "" || !st.IsHolder(principal) {
			continue
		}
		roots = append(roots, moxyReadRoot{path: resolvePath(st.WorktreePath), kind: "held child " + st.Key()})
	}
	return roots
}

func pathWithin(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// moxyReadAllowReason returns the allow reason when input is a read-only moxy
// call whose every path resolves inside an allowed root. Otherwise ok is false
// and the hook writes no decision, leaving Claude Code's normal flow.
func moxyReadAllowReason(input hookInput, ownWorktree string) (reason string, ok bool) {
	paths, ok := moxyReadPathArgs(input)
	if !ok {
		return "", false
	}
	envPrincipal := os.Getenv("CLOWN_SESSION_ID")
	principal, _, _ := principalForHook(envPrincipal, input.SessionID)
	roots := moxyReadRoots(ownWorktree, principal)
	var kinds []string
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(input.CWD, p)
		}
		resolved := resolvePath(p)
		kind := ""
		for _, r := range roots {
			if !pathWithin(resolved, r.path) {
				continue
			}
			// Another session's .spinclass/ is never readable, even inside a root.
			if !r.own && pathWithin(resolved, filepath.Join(r.path, ".spinclass")) {
				continue
			}
			kind = r.kind
			break
		}
		if kind == "" {
			return "", false
		}
		seen := false
		for _, k := range kinds {
			seen = seen || k == kind
		}
		if !seen {
			kinds = append(kinds, kind)
		}
	}
	return "read-only " + strings.TrimPrefix(input.ToolName, moxyToolPrefix) + " inside " + strings.Join(kinds, ", ") + " (spinclass#372)", true
}
