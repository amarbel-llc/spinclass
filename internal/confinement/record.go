package confinement

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// UnitRecordVersion is the unit-record version this package understands.
const UnitRecordVersion = 1

// AgentSlice is the systemd slice clown places every agent unit in (clown
// FDR 0020). A record whose cgroup is not a unit under it is refused, so a
// forged record cannot name an ordinary login scope as "the unit" and excuse
// an unconfined agent (F11).
const AgentSlice = "clown-agent.slice"

// UnitRecord is what clown writes, atomically and from the frontend, once a
// session's agent unit is active (FDR 0035, "Interface with clown"). The
// reported Unit is authoritative; spinclass's name hint is only a hint.
type UnitRecord struct {
	Version      int    `json:"version"`
	Unit         string `json:"unit"`
	Cgroup       string `json:"cgroup"`
	InvocationID string `json:"invocation_id"`
	UID          int    `json:"uid"`
	// Pty is the st_rdev of the pty clown gave the agent, 0 if none. That
	// pty lives on the host's devpts (a passed fd).
	Pty uint64 `json:"pty"`
	// ServeSocket is the per-session unix socket the bridge serves this
	// unit's MCP on (F12).
	ServeSocket string `json:"serve_socket"`
	// MainPID is the unit's main process in the frontend's pid namespace.
	MainPID int `json:"main_pid"`
}

// UnitRecordPath is where clown writes a session's unit record: in
// spinclass's state directory, keyed by the worktree. Never inside the
// worktree, which is bound read-write into the unit, so the unit could
// forge its own record (FDR 0035, "Interface with clown").
func UnitRecordPath(stateDir, worktree string) string {
	h := sha256.Sum256([]byte(filepath.Clean(worktree)))
	return filepath.Join(stateDir, "spinclass", "units", fmt.Sprintf("%x.json", h[:8]))
}

// LoadUnitRecord reads and validates a session's unit record. Every defect is
// an error: the check fails closed on a record it cannot trust the shape of.
func LoadUnitRecord(sys System, stateDir, worktree string) (UnitRecord, error) {
	path := UnitRecordPath(stateDir, worktree)
	b, err := sys.ReadFile(path)
	if err != nil {
		return UnitRecord{}, fmt.Errorf("no agent unit record (%s): the session was not launched confined: %w", path, err)
	}
	return ParseUnitRecord(b)
}

// ParseUnitRecord decodes and validates a unit record.
func ParseUnitRecord(b []byte) (UnitRecord, error) {
	// Unknown fields are tolerated: clown may report more than the check
	// reads, and the version field is what gates a change of meaning.
	var r UnitRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return UnitRecord{}, fmt.Errorf("agent unit record does not parse: %w", err)
	}
	switch {
	case r.Version != UnitRecordVersion:
		return UnitRecord{}, fmt.Errorf("agent unit record version %d, want %d", r.Version, UnitRecordVersion)
	case r.Unit == "":
		return UnitRecord{}, errors.New("agent unit record names no unit")
	case !isAgentUnitCgroup(r.Cgroup):
		return UnitRecord{}, fmt.Errorf("agent unit record cgroup %q is not a unit under %s", r.Cgroup, AgentSlice)
	case r.ServeSocket != "" && !filepath.IsAbs(r.ServeSocket):
		return UnitRecord{}, fmt.Errorf("agent unit record serve_socket %q is not absolute", r.ServeSocket)
	}
	return r, nil
}

// isAgentUnitCgroup accepts an absolute, clean cgroup path whose parent
// components include AgentSlice and whose last is a .service unit.
func isAgentUnitCgroup(cg string) bool {
	if !strings.HasPrefix(cg, "/") || filepath.Clean(cg) != cg {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(cg, "/"), "/")
	if len(parts) < 2 || !strings.HasSuffix(parts[len(parts)-1], ".service") {
		return false
	}
	return slices.Contains(parts[:len(parts)-1], AgentSlice)
}

// cgroupWithin reports whether cg equals root or sits beneath it, by path
// component (so /a/b is not within /a/bc).
func cgroupWithin(cg, root string) bool {
	cg, root = filepath.Clean(cg), filepath.Clean(root)
	return cg == root || strings.HasPrefix(cg, root+"/")
}

// parseCgroupV2 returns the unified-hierarchy path from a /proc/<pid>/cgroup
// file. A host without a "0::" line is not cgroup v2 and fails.
func parseCgroupV2(b []byte) (string, error) {
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			if !strings.HasPrefix(rest, "/") {
				return "", fmt.Errorf("cgroup v2 path %q is not absolute", rest)
			}
			return filepath.Clean(rest), nil
		}
	}
	return "", errors.New("no cgroup v2 (0::) line")
}
