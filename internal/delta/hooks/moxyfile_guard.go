package hooks

import (
	"os"
	"path/filepath"
	"strings"
)

// The moxyfile-edit guard (spinclass#438, slice 0). The moxyfile decides
// which tools a session has, so a session must not widen it on its own: in an
// unconfined session every tool call that creates, changes, replaces, moves
// or removes a moxyfile on the hierarchy moxy merges for the session asks,
// whatever any allow list says (a deny under dontAsk, by the usual
// conversion). Inside a confined unit the hook writes no decision and the
// unit's own permission rules decide, as for any other write there: the
// in-unit moxyfile is the profile's and the unit bounds the worker, so a
// forced prompt would only strand an unattended one.
//
// Like the sweatfile guard it is hook-only and small, and does not see Bash:
// a shell redirect onto a moxyfile is not caught here.

// moxyfileWriteKeys names, per tool, the arguments whose path the call would
// create, change, replace, move or remove. Reads are not listed.
var moxyfileWriteKeys = map[string][]string{
	"Write":                              {"file_path"},
	"Edit":                               {"file_path"},
	"MultiEdit":                          {"file_path"},
	"mcp__plugin_moxy_moxy__folio_write": {"file_path"},
	"mcp__plugin_moxy_moxy__folio_cp":    {"destination"},
	"mcp__plugin_moxy_moxy__folio_mv":    {"source", "destination"},
	"mcp__plugin_moxy_moxy__folio_link":  {"target"},
	"mcp__plugin_moxy_moxy__folio_rm":    {"paths"},
}

// moxyfileWriteTarget returns the first moxyfile on the session's hierarchy
// that the call would alter. Relative values are skipped: they resolve
// against the tool's cwd, which this hook cannot see.
func moxyfileWriteTarget(input hookInput, sessionWorktree string) (string, bool) {
	keys, ok := moxyfileWriteKeys[input.ToolName]
	if !ok {
		return "", false
	}
	cwd := sessionWorktree
	if cwd == "" {
		cwd = input.CWD
	}
	home, _ := os.UserHomeDir()
	for _, key := range keys {
		v, _ := input.ToolInput[key].(string)
		values := []string{v}
		if key == "paths" {
			values = strings.Fields(v)
		}
		for _, p := range values {
			if p != "" && filepath.IsAbs(p) && isMoxyfile(p, home, cwd) {
				return p, true
			}
		}
	}
	return "", false
}

// isMoxyfile reports whether path, symlinks resolved, is one of the moxyfiles
// moxy merges for a session at cwd (moxyfile(5), FILE HIERARCHY): the global
// ~/.config/moxy/moxyfile, and <dir>/moxyfile for every directory from HOME
// down to cwd. A cwd outside HOME is walked to the root instead. Files a
// moxyfile pulls in with `include` are not known without parsing it and are
// not guarded.
func isMoxyfile(path, home, cwd string) bool {
	resolved := resolvePath(path)
	if filepath.Base(resolved) != "moxyfile" {
		return false
	}
	dir := filepath.Dir(resolved)
	resolvedHome := ""
	if home != "" {
		resolvedHome = resolvePath(home)
		if dir == resolvedHome || dir == filepath.Join(resolvedHome, ".config", "moxy") {
			return true
		}
	}
	if cwd == "" {
		return false
	}
	for d := resolvePath(cwd); ; d = filepath.Dir(d) {
		if d == dir {
			return true
		}
		if d == resolvedHome || filepath.Dir(d) == d {
			return false
		}
	}
}
