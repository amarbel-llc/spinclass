// Package confinement is the session-confinement check of FDR 0035: the facts
// that must hold before FDR 0034's profile-rights gate may treat any grant as
// card-rooted. It verifies outcomes (what the driver's agent unit can see and
// reach), never how clown built the unit. It runs in the frontend (spinclass
// serve) and looks into the unit through cgroup.procs and /proc/<pid>/root.
//
// Every probe fails closed: an error reading anything is a failed fact.
package confinement

import (
	"errors"
	"fmt"
	"strings"
)

// Fact is one line of the check's table.
type Fact struct {
	ID     string
	Name   string
	OK     bool
	Detail string
}

// Report is the result of running every probe.
type Report struct {
	Facts []Fact
}

// OK reports whether every fact holds. An empty report never passes.
func (r Report) OK() bool {
	if len(r.Facts) == 0 {
		return false
	}
	for _, f := range r.Facts {
		if !f.OK {
			return false
		}
	}
	return true
}

// Err is nil when every fact holds, otherwise one error naming each failure.
func (r Report) Err() error {
	if r.OK() {
		return nil
	}
	if len(r.Facts) == 0 {
		return errors.New("session confinement: no facts were probed")
	}
	var failed []string
	for _, f := range r.Facts {
		if !f.OK {
			failed = append(failed, fmt.Sprintf("%s %s: %s", f.ID, f.Name, f.Detail))
		}
	}
	return fmt.Errorf("session confinement does not hold (FDR 0035); %d of %d facts fail: %s",
		len(failed), len(r.Facts), strings.Join(failed, "; "))
}

// Session is what the check needs to know about one live session.
type Session struct {
	Key      string
	Worktree string
	// PID is the session's recorded process (0 when unknown). For a spawned
	// worker it is claude; for an attached session it is the `sc` process,
	// which runs outside the unit, so F1 does not bind on it.
	PID int
	// Principal is the session's CLOWN_SESSION_ID (FDR 0032 D1), as the
	// frontend knows it.
	Principal string
	// RecordedPrincipal is what the worktree-local session state says: a
	// display cache the agent can rewrite, compared but never trusted (F15).
	RecordedPrincipal string
}

// Inputs is everything the probes consult besides the System.
type Inputs struct {
	// Driver is the spawning session whose own agent unit is the subject.
	Driver Session
	// Live is every live session on the host, the driver included (F11, F12):
	// those whose recorded PID is alive.
	Live []Session
	// Recorded is every other session that is not closed. Its recorded PID is
	// dead or unknown, but it is still live when its agent unit is running: a
	// confined worker records no PID (FDR 0035 "Session PIDs are untrusted").
	Recorded []Session
	// OperatorHome is the operator's real home directory.
	OperatorHome string
	// AgentSocket is the piggy-agent socket (SSH_AUTH_SOCK) as the frontend
	// sees it.
	AgentSocket string
	// XAuthority is the operator's X authority file as the frontend sees it.
	XAuthority string
	// GitCommonDir and HooksPath are the driver repo's (F9). HooksPath is
	// the resolved core.hooksPath, empty when unset.
	GitCommonDir string
	HooksPath    string
	// AgentComms are the process names F11 searches for outside every agent
	// unit, besides the harnesses the live unit records report. Deprecated
	// fallback: names are spoofable; it goes once clown records every agent it
	// launches (docs/plans/2026-10-20-agent-census-by-unit.md).
	AgentComms []string
	// StateDir is the XDG state base unit records live under
	// (UnitRecordPath), outside every worktree.
	StateDir string
	// RuntimeAllow names the entries of the operator's runtime dir the unit
	// may see: the sockets the spec binds there (F14).
	RuntimeAllow []string
}

// DefaultAgentComms identifies agent runtimes by /proc/<pid>/comm (15 bytes
// at most). nixpkgs' claude-code runs as .claude-wrapped behind its wrapper
// script, so the bare name alone finds nothing on a nix host. juggler is clown
// FDR 0021's headless loop.
var DefaultAgentComms = []string{"claude", ".claude-wrapped", "juggler"}

// Run probes every fact in order. Facts that need the unit fail when there
// is no unit to look into, so a missing unit fails every such line rather
// than passing it vacuously.
func Run(sys System, in Inputs) Report {
	p := &prober{sys: sys, in: in}
	p.loadSubject()
	return Report{Facts: []Fact{
		p.f1Unit(),
		p.f2AgentOutside(),
		p.f3NoInheritedTTY(),
		p.f4NoDisplayEnv(),
		p.f5NoSessionBus(),
		p.f6NoDisplay(),
		p.f7HomeNotWritable(),
		p.f8aNoPoshSocket(),
		p.f8bNoOperatorPTY(),
		p.f9HooksNotWritable(),
		p.f10PrivatePIDs(),
		p.f11EveryAgentConfined(),
		p.f12OnlyOwnServe(),
		p.f13PrivateNetwork(),
		p.f14RuntimeDirBoundOnly(),
		p.f15PrincipalMapping(),
	}}
}
