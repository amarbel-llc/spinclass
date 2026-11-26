package confinement

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// Evidence is what the frontend's own records, and the process's own cgroup,
// say about the caller of a call that arrived with no origin (stdio, or clown's
// loopback relay to the older serve socket). It is the classification the three
// session verbs that hold an escalation floor use (restart-this-session,
// cancel-restart, stop-this-session): they refuse a confined caller what an
// unconfined one keeps, so they must not refuse for want of evidence.
//
// It is NOT the classification of the grit and perms gates (ClassifyCaller,
// ClassifyServeCaller): those fail closed on master and still do. The rule here
// is the one the key holder uses for a new failure source (a rule only refuses
// when there is evidence it applies), with one proviso: a record that exists
// but cannot be read or parsed is itself evidence (EvidenceUnreadable refuses).
type Evidence int

const (
	// EvidenceUnconfined: the frontend's principal mapping positively names this
	// caller an unconfined session.
	EvidenceUnconfined Evidence = iota
	// EvidenceAbsent: nothing the frontend wrote names this caller (no mapping,
	// no unit record, no serve record, an unresolved principal or state
	// directory). The call behaves as on master: it is not refused by the
	// classification.
	EvidenceAbsent
	// EvidenceConfined: the process runs inside a clown agent unit, the mapping
	// names a confined session, or a unit record names the principal.
	EvidenceConfined
	// EvidenceUnreadable: a record that could name the caller exists and cannot
	// be read or parsed. Evidence exists, its content is unknown: refuse.
	EvidenceUnreadable
)

// Refuses says the classification refuses the caller the escalations.
func (e Evidence) Refuses() bool { return e == EvidenceConfined || e == EvidenceUnreadable }

func (e Evidence) String() string {
	switch e {
	case EvidenceUnconfined:
		return "unconfined"
	case EvidenceAbsent:
		return "absent"
	case EvidenceConfined:
		return "confined"
	case EvidenceUnreadable:
		return "unreadable"
	}
	return fmt.Sprintf("Evidence(%d)", int(e))
}

// ClassifyServeCallerEvidence classifies the caller of a no-origin call from
// the process's cgroup and the frontend records. Order: a process inside an
// agent unit is confined whatever the records say (from there the state
// directory is hidden, so an absent record proves nothing); then the principal
// mapping; then the unit records; then the serve records, which only add
// evidence when one of them cannot be read.
//
// Two live serves of one principal, or no serve record of its own, are not
// evidence by themselves: master's grit and perms gates fail closed on them,
// these verbs do not.
//
// worktree is the session's worktree as the FRONTEND's own record names it ("" when
// none is known). A confined launch spinclass wrote for it (the unit spec, written
// before clown reports its unit record) counts as confined from that moment: there
// is no window between the launch and clown's record in which a confined caller
// reads as unconfined.
func ClassifyServeCallerEvidence(sys System, stateDir, principal, worktree string) (Evidence, string) {
	if inside, err := InsideAgentUnit(sys); err == nil && inside {
		return EvidenceConfined, "this process runs inside a clown agent unit"
	}
	if worktree != "" {
		if ev, why := launchEvidence(sys, stateDir, worktree); ev != EvidenceAbsent {
			return ev, why
		}
	}
	if err := validPrincipalName(principal); err != nil {
		return EvidenceAbsent, fmt.Sprintf("caller principal unresolved: %v", err)
	}
	mappedUnconfined := false
	if m, err := LoadPrincipalMapping(sys, stateDir, principal); err == nil {
		if m.Unit != UnconfinedUnit {
			return EvidenceConfined, fmt.Sprintf("the frontend maps principal %s to a confined session", principal)
		}
		mappedUnconfined = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return EvidenceUnreadable, fmt.Sprintf("principal mapping unreadable: %v", err)
	}
	if ev, why := evidenceFromUnitRecords(sys, stateDir, principal); ev != EvidenceAbsent {
		return ev, why
	}
	if ev, why := evidenceFromServeRecords(sys, stateDir, principal); ev != EvidenceAbsent {
		return ev, why
	}
	if mappedUnconfined {
		return EvidenceUnconfined, fmt.Sprintf("the frontend maps principal %s to an unconfined session", principal)
	}
	return EvidenceAbsent, "no frontend record names this principal"
}

// launchEvidence reads the confined-launch files of a worktree (clown's unit
// record, and the spec spinclass wrote before it): either existing says confined,
// and a stat that fails for any reason but absence is unreadable evidence.
func launchEvidence(sys System, stateDir, worktree string) (Evidence, string) {
	for _, rec := range []string{UnitRecordPath(stateDir, worktree), unitRecordPathKeyed(stateDir, filepath.Clean(worktree))} {
		for _, f := range []string{rec, specPathFor(rec)} {
			_, err := sys.Lstat(f)
			if err == nil {
				return EvidenceConfined, fmt.Sprintf("spinclass launched this session confined (%s exists)", filepath.Base(f))
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return EvidenceUnreadable, fmt.Sprintf("the launch record %s cannot be read: %v", filepath.Base(f), err)
			}
		}
	}
	return EvidenceAbsent, "spinclass wrote no confined launch for this session"
}

func evidenceFromUnitRecords(sys System, stateDir, principal string) (Evidence, string) {
	dir := filepath.Join(stateDir, "spinclass", "units")
	names, err := sys.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return EvidenceAbsent, "no unit records exist"
	}
	if err != nil {
		return EvidenceUnreadable, fmt.Sprintf("unit records unreadable: %v", err)
	}
	var matching []string
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".spec.json") {
			continue
		}
		raw, err := sys.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return EvidenceUnreadable, fmt.Sprintf("unit record %s unreadable: %v", name, err)
		}
		var head struct {
			Principal string `json:"principal"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return EvidenceUnreadable, fmt.Sprintf("unit record %s does not parse: %v", name, err)
		}
		if head.Principal == principal {
			matching = append(matching, name)
		}
	}
	switch len(matching) {
	case 0:
		return EvidenceAbsent, "no unit record names this principal"
	case 1:
		return EvidenceConfined, fmt.Sprintf("unit record %s names principal %s", matching[0], principal)
	}
	return EvidenceConfined, fmt.Sprintf("ambiguous: %d unit records name principal %s", len(matching), principal)
}

// evidenceFromServeRecords reads only the records that carry the caller's
// principal in their name. A missing directory, or no such record, is absence;
// a record of the principal that cannot be read or parsed is unreadable
// evidence. It never says confined: a serve record does not say what the
// session is.
func evidenceFromServeRecords(sys System, stateDir, principal string) (Evidence, string) {
	dir := servesDir(stateDir)
	names, err := sys.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return EvidenceAbsent, "this serve has no record"
	}
	if err != nil {
		return EvidenceUnreadable, fmt.Sprintf("serve records unreadable: %v", err)
	}
	for _, name := range names {
		if !strings.HasPrefix(name, principal+"-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		raw, err := sys.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return EvidenceUnreadable, fmt.Sprintf("serve record %s unreadable: %v", name, err)
		}
		var rec ServeRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return EvidenceUnreadable, fmt.Sprintf("serve record %s does not parse: %v", name, err)
		}
	}
	return EvidenceAbsent, "no serve record of this principal is unreadable"
}
