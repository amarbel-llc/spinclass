package confinement

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"code.linenisgreat.com/spinclass/internal/session"
)

// PrincipalMapping binds a principal to its session in frontend-owned state:
// $XDG_STATE_HOME/spinclass/principals/<principal>.json, beside the unit
// records and like them hidden from every unit by ProtectHome. The grant walk
// joins a principal to its session through THIS, never through the
// worktree-local session state, which the confined agent can rewrite (FDR
// 0035, "The principal-to-session mapping"; amends FDR 0001). The frontend
// writes it from the unit record the launching clown wrote, so the agent never
// names itself.
type PrincipalMapping struct {
	Version    int    `json:"version"`
	Principal  string `json:"principal"`
	SessionKey string `json:"session_key"`
	Worktree   string `json:"worktree"`
	Unit       string `json:"unit"`
}

const principalMappingVersion = 1

// PrincipalMappingPath is where a principal's mapping lives.
func PrincipalMappingPath(stateDir, principal string) string {
	return filepath.Join(stateDir, "spinclass", "principals", principal+".json")
}

func validPrincipalName(p string) error {
	if p == "" || p == "." || p == ".." || strings.ContainsAny(p, "/\\\x00\n") {
		return fmt.Errorf("principal %q cannot name a mapping file", p)
	}
	return nil
}

// LoadPrincipalMapping reads and validates a principal's mapping.
func LoadPrincipalMapping(sys System, stateDir, principal string) (PrincipalMapping, error) {
	if err := validPrincipalName(principal); err != nil {
		return PrincipalMapping{}, err
	}
	b, err := sys.ReadFile(PrincipalMappingPath(stateDir, principal))
	if err != nil {
		return PrincipalMapping{}, fmt.Errorf("no frontend principal mapping for %s: %w", principal, err)
	}
	var m PrincipalMapping
	if err := json.Unmarshal(b, &m); err != nil {
		return PrincipalMapping{}, fmt.Errorf("principal mapping for %s does not parse: %w", principal, err)
	}
	switch {
	case m.Version != principalMappingVersion:
		return PrincipalMapping{}, fmt.Errorf("principal mapping for %s has version %d", principal, m.Version)
	case m.Principal != principal:
		return PrincipalMapping{}, fmt.Errorf("principal mapping file for %s names %s", principal, m.Principal)
	case m.SessionKey == "" || m.Worktree == "" || m.Unit == "":
		return PrincipalMapping{}, fmt.Errorf("principal mapping for %s is incomplete", principal)
	}
	return m, nil
}

// RecordPrincipalFromUnit writes the mapping for the session whose unit
// record exists, taking the principal from that record (written by the
// launching frontend clown; claude's environment never carries it, clown#136),
// not from anything the session wrote. Run in the frontend once the unit is up
// (the spawn hello).
// A session without a unit gets no mapping: its grants cannot be card-rooted
// anyway, since the confinement check fails.
func RecordPrincipalFromUnit(sys System, stateDir, sessionKey, worktree string) (string, error) {
	rec, err := LoadUnitRecord(sys, stateDir, worktree)
	if err != nil {
		return "", err
	}
	principal := rec.Principal
	if principal == "" {
		return "", fmt.Errorf("unit %s's record names no principal", rec.Unit)
	}
	if err := validPrincipalName(principal); err != nil {
		return "", err
	}
	m := PrincipalMapping{Version: principalMappingVersion, Principal: principal, SessionKey: sessionKey, Worktree: worktree, Unit: rec.Unit}
	return principal, writeJSONAtomic(PrincipalMappingPath(stateDir, principal), m)
}

// RemovePrincipalMappingsForSession deletes every mapping that names
// sessionKey, found by reading the mappings themselves. It never trusts the
// session's worktree-cached principal: an agent that rewrote that could
// otherwise have another session's mapping removed when its own ends.
func RemovePrincipalMappingsForSession(stateDir, sessionKey string) error {
	dir := filepath.Join(stateDir, "spinclass", "principals")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		principal, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() {
			continue
		}
		m, err := LoadPrincipalMapping(Host{}, stateDir, principal)
		if err != nil || m.SessionKey != sessionKey {
			continue
		}
		if err := RemovePrincipalMapping(stateDir, principal); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SessionKeyOfPrincipal resolves a caller's principal to its session key,
// preferring the frontend principal mapping, which the session's agent cannot
// write, over the worktree state's recorded principal, which it can (audit row
// 0035-159). Only when no mapping exists (a session never launched confined)
// does it fall back to session.SessionKeyOfPrincipal. A mapping that exists but
// cannot be read or validated, or names a session that is no longer live,
// resolves nothing: the caller fails closed rather than fall back.
func SessionKeyOfPrincipal(principal string) (string, bool) {
	stateDir, err := DefaultStateDir()
	if err != nil {
		return "", false
	}
	m, err := LoadPrincipalMapping(Host{}, stateDir, principal)
	switch {
	case err == nil:
		if !LiveSession(m.SessionKey) {
			return "", false
		}
		return m.SessionKey, true
	case errors.Is(err, os.ErrNotExist):
		return session.SessionKeyOfPrincipal(principal)
	default:
		return "", false
	}
}

// PrincipalOfSession is the principal the frontend mappings bind to sessionKey
// at worktree, found by reading the mappings themselves (the reverse of
// LoadPrincipalMapping). found is false when no mapping names the session. More
// than one mapping naming it, or a mapping directory that cannot be read, is an
// error: the caller must fail closed rather than pick one.
func PrincipalOfSession(sys System, stateDir, sessionKey, worktree string) (principal string, found bool, err error) {
	dir := filepath.Join(stateDir, "spinclass", "principals")
	names, err := sys.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("principal mappings unreadable: %w", err)
	}
	var matches []string
	for _, name := range names {
		p, ok := strings.CutSuffix(name, ".json")
		if !ok {
			continue
		}
		m, err := LoadPrincipalMapping(sys, stateDir, p)
		if err != nil || m.SessionKey != sessionKey || filepath.Clean(m.Worktree) != filepath.Clean(worktree) {
			continue
		}
		matches = append(matches, p)
	}
	switch len(matches) {
	case 0:
		return "", false, nil
	case 1:
		return matches[0], true, nil
	}
	return "", false, fmt.Errorf("ambiguous: %d principal mappings name session %s", len(matches), sessionKey)
}

// LaunchedConfined reports whether worktree has (or had) an agent unit: a
// frontend-owned unit record or spec exists for it.
func LaunchedConfined(stateDir, worktree string) bool {
	for _, f := range []string{UnitRecordPath(stateDir, worktree), UnitSpecPath(stateDir, worktree)} {
		if _, err := os.Lstat(f); err == nil {
			return true
		}
	}
	return false
}

// RemovePrincipalMapping deletes a principal's mapping when its session ends.
func RemovePrincipalMapping(stateDir, principal string) error {
	if err := validPrincipalName(principal); err != nil {
		return err
	}
	if err := os.Remove(PrincipalMappingPath(stateDir, principal)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// SessionOfPrincipal is the grant walk's join (profilegrant.WalkOptions):
// the session the frontend mapping names for principal, if that session is
// still live. Only the frontend mapping is consulted.
func SessionOfPrincipal(stateDir string, live func(sessionKey string) bool) func(string) (string, bool) {
	return func(principal string) (string, bool) {
		m, err := LoadPrincipalMapping(Host{}, stateDir, principal)
		if err != nil || !live(m.SessionKey) {
			return "", false
		}
		return m.SessionKey, true
	}
}

func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// WriteUnitSpec writes the launch spec for clown (CLOWN_AGENT_SCOPE) to
// UnitSpecPath, atomically and mode 0600.
func WriteUnitSpec(stateDir, worktree string, spec any) (string, error) {
	path := UnitSpecPath(stateDir, worktree)
	return path, writeJSONAtomic(path, spec)
}

// RequiredCapabilities are what clown must report before spinclass launches
// a worker confined (coordinator decision, 2026-10-19): without a private
// network the unit reaches every loopback MCP bridge; without frontend MCP
// sockets serve is on loopback; without moxy inside the unit the worker has
// no file tools, and a forwarded moxy is an escape.
var RequiredCapabilities = []string{"agent-unit", "private-network", "frontend-mcp-sockets", "in-unit-moxy"}

// MissingCapabilities returns the required capabilities absent from have.
func MissingCapabilities(have []string) []string {
	var missing []string
	for _, c := range RequiredCapabilities {
		found := false
		for _, h := range have {
			if h == c {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, c)
		}
	}
	return missing
}

// InsideAgentUnit reports whether this process runs inside a clown agent
// unit. A spawn from there would start the child's frontend inside the
// parent's agent unit (FDR 0032 D7), so `sc spawn` refuses.
func InsideAgentUnit(sys System) (bool, error) {
	b, err := sys.ReadFile("/proc/self/cgroup")
	if err != nil {
		return false, err
	}
	cg, err := parseCgroupV2(b)
	if err != nil {
		return false, err
	}
	return strings.Contains(cg+"/", "/"+AgentSlice+"/"), nil
}
