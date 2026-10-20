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
	// Pty is the host path of the pty slave clown allocated for the agent
	// ("/dev/pts/N"), empty if none (clown FDR 0020). The check stats it from
	// the frontend for its device number and devpts instance.
	Pty string `json:"pty"`
	// Capabilities are what the clown that launched the unit reports it
	// enforces (AgentScopeCapabilities).
	Capabilities []string `json:"capabilities"`
	// ServeSocket is the per-session unix socket the bridge serves this
	// unit's MCP on (F12).
	ServeSocket string `json:"serve_socket"`
	// MainPID is the unit's main process in the frontend's pid namespace.
	MainPID int `json:"main_pid"`
	// Principal is the CLOWN_SESSION_ID of the clown that launched the unit,
	// written by that (frontend) clown. claude's own environment never carries
	// it (clown#136), so this record is where a unit's principal is read.
	Principal string `json:"principal"`
	// Error is set by clown when the launch failed after it began writing the
	// record: the other fields may then be empty, and this says why.
	Error string `json:"error"`
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
	case r.Error != "":
		return UnitRecord{}, fmt.Errorf("clown reported the agent unit launch failed: %s", r.Error)
	case r.Version != UnitRecordVersion:
		return UnitRecord{}, fmt.Errorf("agent unit record version %d, want %d", r.Version, UnitRecordVersion)
	case r.Unit == "":
		return UnitRecord{}, errors.New("agent unit record names no unit")
	case !isAgentUnitCgroup(r.Cgroup):
		return UnitRecord{}, fmt.Errorf("agent unit record cgroup %q is not a unit under %s", r.Cgroup, AgentSlice)
	case r.ServeSocket != "" && !filepath.IsAbs(r.ServeSocket):
		return UnitRecord{}, fmt.Errorf("agent unit record serve_socket %q is not absolute", r.ServeSocket)
	case r.Pty != "" && (filepath.Clean(r.Pty) != r.Pty || !strings.HasPrefix(r.Pty, "/dev/pts/")):
		return UnitRecord{}, fmt.Errorf("agent unit record pty %q is not a /dev/pts path", r.Pty)
	}
	return r, nil
}

// UnitSpecPath is where spinclass writes the launch spec it hands clown
// (CLOWN_AGENT_SCOPE), beside the result and likewise outside the worktree:
// clown re-reads it across its [attach] re-exec, so the unit must not be able
// to edit it.
func UnitSpecPath(stateDir, worktree string) string {
	return strings.TrimSuffix(UnitRecordPath(stateDir, worktree), ".json") + ".spec.json"
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
