package confinement

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// ClassifyCaller answers "is the caller with this principal a confined worker?"
// for the grit store tools (FDR 0036 section 9). principal is the
// CLOWN_SESSION_ID of the clown hosting the asking process: in the frontend
// `spinclass serve` that is the clown that launched the unit, which is exactly
// the value that clown wrote into the unit record (claude's own environment
// never carries it, clown#136).
//
// It fails closed. The caller counts as confined unless every source positively
// shows it is not: the principal must be resolvable, the frontend principal
// mapping must be absent (not merely unreadable), and the unit records directory
// must either not exist or hold no record naming the principal. An unreadable
// or unparsable record, a launch-failed record naming the principal, and
// several records naming it are all "confined". A process that runs inside a
// clown agent unit is confined whatever the records say: from there the state
// directory is hidden, so an absent record proves nothing.
func ClassifyCaller(sys System, stateDir, principal string) (confined bool, why string) {
	if inside, err := InsideAgentUnit(sys); err == nil && inside {
		return true, "this process runs inside a clown agent unit"
	}
	if err := validPrincipalName(principal); err != nil {
		return true, fmt.Sprintf("caller principal unresolved: %v", err)
	}
	if _, err := LoadPrincipalMapping(sys, stateDir, principal); err == nil {
		return true, fmt.Sprintf("the frontend maps principal %s to a confined session", principal)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return true, fmt.Sprintf("principal mapping unreadable: %v", err)
	}
	return classifyByUnitRecords(sys, stateDir, principal)
}

func classifyByUnitRecords(sys System, stateDir, principal string) (bool, string) {
	dir := filepath.Join(stateDir, "spinclass", "units")
	names, err := sys.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, "no unit records exist"
	}
	if err != nil {
		return true, fmt.Sprintf("unit records unreadable: %v", err)
	}
	var matching []string
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".spec.json") {
			continue
		}
		raw, err := sys.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return true, fmt.Sprintf("unit record %s unreadable: %v", name, err)
		}
		var head struct {
			Principal string `json:"principal"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return true, fmt.Sprintf("unit record %s does not parse: %v", name, err)
		}
		if head.Principal == principal {
			matching = append(matching, name)
		}
	}
	switch len(matching) {
	case 0:
		return false, "no unit record names this principal"
	case 1:
		return true, fmt.Sprintf("unit record %s names principal %s", matching[0], principal)
	}
	return true, fmt.Sprintf("ambiguous: %d unit records name principal %s", len(matching), principal)
}

// WorktreeOfPrincipal is the session worktree the frontend principal mapping
// binds to principal. Only the frontend mapping is consulted: the worktree-local
// session state is agent-writable.
func WorktreeOfPrincipal(sys System, stateDir, principal string) (string, bool) {
	m, err := LoadPrincipalMapping(sys, stateDir, principal)
	if err != nil {
		return "", false
	}
	return m.Worktree, true
}
