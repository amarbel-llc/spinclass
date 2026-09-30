// Package attestation implements the pre-merge skill attestation gate
// described in docs/features/0007-pre-merge-skill-attestation.md.
//
// The gate is active when the resolved sweatfile hierarchy contains a
// non-empty [[pre-merge-skills]] list. In that mode, the MCP handlers for
// merge-this-session and check-this-session Claim the buffered attestation
// when they commit to an attempt. The attestation is consumed only when that
// merge lands or that check passes (Settle landed=true) and released for the
// retry on every other outcome (Settle landed=false). A claim is owned by a
// PID; a claim whose PID is dead is void, so a crashed serve never burns the
// attestation. There is no sticky once-per-session mode. PIDs and session
// state are host-local: a claim is only judged live or dead on its own host.
//
// Until #219 task 3 rewires the handlers, the deprecated Check/Consume shims
// still consume at commit time, before success is known.
//
// The CLI (`sc merge` / `sc run` / `sc check`) does not claim — the gate
// is MCP-only by design; a terminal merge only records the bypass (FDR 0031's
// pre-merge policy stage, internal/merge/policy.go).
package attestation

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
	tap "code.linenisgreat.com/tap/go/pkgs/writer"
)

// ErrAttestationRequired is returned by Peek and Claim when no available
// attestation is buffered. The returned output text is a self-contained TAP-14
// document that the MCP handler should ship to the agent verbatim.
var ErrAttestationRequired = errors.New("pre-merge skill attestation required")

// Slot is where one session's attestation lives: a worktree session's state
// file or an implicit (main-checkout) session's per-randID file.
//
// Every mutation goes through update, a locked read-modify-write (sidecar
// flock + atomic rename, session.Update); load is the unlocked read for Peek.
type Slot struct {
	load    func() (*session.State, error)
	store   func(session.State) error                 // locked whole-state replace
	update  func(fn func(*session.State) error) error // locked read-modify-write
	missing string                                    // message when the session state cannot be loaded
}

var errNoLiveSession = errors.New("no live session")

// errSuperseded aborts an update that finds the attestation no longer matches
// the ticket (a re-record, or another settle, replaced it): nothing to write.
var errSuperseded = errors.New("attestation superseded")

// WorktreeSlot addresses the attestation in the worktree session state for
// (repoPath, branch). A closed (tombstoned) session reads as missing.
func WorktreeSlot(repoPath, branch string) Slot {
	return Slot{
		load: func() (*session.State, error) {
			st, err := session.Read(repoPath, branch)
			if err != nil {
				return nil, err
			}
			if st.IsTombstone() {
				return nil, fmt.Errorf("session is closed: %w", os.ErrNotExist)
			}
			return st, nil
		},
		store: func(s session.State) error {
			return session.Update(repoPath, branch, func(cur *session.State) error { *cur = s; return nil })
		},
		update: func(fn func(*session.State) error) error {
			return session.Update(repoPath, branch, fn)
		},
		missing: "could not read session state to verify attestation; this MCP tool requires a tracked spinclass session",
	}
}

// ImplicitSlot addresses the attestation in the implicit (main-checkout)
// session live at checkout. The session's randID is resolved ONCE here and
// every later load, store and update is bound to it, so a session that ended
// (or another one that started) is never confused with this one; stores
// refuse rather than recreate a vanished state file.
func ImplicitSlot(checkout string) Slot {
	const missing = "could not find a live implicit session at checkout to verify attestation; the session may have ended"
	_, randID, findErr := session.FindImplicitAtCwd(checkout)
	if findErr == nil && randID == "" {
		findErr = fmt.Errorf("no live implicit session at %s: %w", checkout, errNoLiveSession)
	}
	if findErr != nil {
		return Slot{
			load:    func() (*session.State, error) { return nil, findErr },
			store:   func(session.State) error { return findErr },
			update:  func(func(*session.State) error) error { return findErr },
			missing: missing,
		}
	}
	return Slot{
		load: func() (*session.State, error) {
			st, id, err := session.FindImplicitAtCwd(checkout)
			if err != nil {
				return nil, fmt.Errorf("find implicit session: %w", err)
			}
			if st == nil || id != randID {
				return nil, fmt.Errorf("implicit session %s at %s is gone: %w", randID, checkout, os.ErrNotExist)
			}
			return st, nil
		},
		store: func(s session.State) error {
			return session.UpdateImplicit(checkout, randID, func(cur *session.State) error { *cur = s; return nil })
		},
		update: func(fn func(*session.State) error) error {
			return session.UpdateImplicit(checkout, randID, fn)
		},
		missing: missing,
	}
}

// Ticket identifies one attempt's hold on an attestation. The zero Ticket
// means "nothing held"; settling it is a no-op.
type Ticket struct {
	RecordedAt time.Time
	HeadSha    string
	ClaimID    string
}

// ValidationError describes why a nothing-but-the-truth input failed
// strict-presence checks. An Empty() ValidationError indicates a clean
// attestation.
type ValidationError struct {
	Missing        []string
	Unrecognised   []string
	EmptyReasoning []string
}

func (v ValidationError) Empty() bool {
	return len(v.Missing) == 0 && len(v.Unrecognised) == 0 && len(v.EmptyReasoning) == 0
}

func (v ValidationError) Error() string {
	var parts []string
	if len(v.Missing) > 0 {
		parts = append(parts, "missing entries for required skills: "+strings.Join(v.Missing, ", "))
	}
	if len(v.Unrecognised) > 0 {
		parts = append(parts, "unrecognised skill names: "+strings.Join(v.Unrecognised, ", "))
	}
	if len(v.EmptyReasoning) > 0 {
		parts = append(parts, "empty reasoning for: "+strings.Join(v.EmptyReasoning, ", "))
	}
	if len(parts) == 0 {
		return "attestation valid"
	}
	return "attestation invalid: " + strings.Join(parts, "; ")
}

// Validate checks that input addresses exactly the required skills
// (strict presence) and that every reasoning is non-empty after
// whitespace-trimming (lenient content). Duplicate names are flagged
// under Unrecognised with a "(duplicate)" suffix.
func Validate(required []sweatfile.PreMergeSkill, input []session.AttestedSkill) ValidationError {
	requiredSet := make(map[string]bool, len(required))
	for _, s := range required {
		requiredSet[s.Name] = true
	}
	var verr ValidationError
	seen := make(map[string]bool, len(input))
	for _, a := range input {
		if seen[a.Name] {
			verr.Unrecognised = append(verr.Unrecognised, a.Name+" (duplicate)")
			continue
		}
		seen[a.Name] = true
		if !requiredSet[a.Name] {
			verr.Unrecognised = append(verr.Unrecognised, a.Name)
			continue
		}
		if strings.TrimSpace(a.Reasoning) == "" {
			verr.EmptyReasoning = append(verr.EmptyReasoning, a.Name)
		}
	}
	for _, s := range required {
		if !seen[s.Name] {
			verr.Missing = append(verr.Missing, s.Name)
		}
	}
	return verr
}

// Record writes a validated attestation into the slot's session state,
// overwriting any prior buffered attestation (and dropping any claim on it: the
// newest attestation always wins the single slot). headSha is the session
// worktree HEAD at attest time, "" when unknown (#219).
func Record(slot Slot, skills []session.AttestedSkill, headSha string) error {
	called := false
	err := slot.update(func(st *session.State) error {
		called = true
		st.PreMergeAttestation = &session.PreMergeAttestation{
			RecordedAt: time.Now().UTC(),
			Skills:     skills,
			HeadSha:    headSha,
		}
		return nil
	})
	if err != nil {
		if !called {
			return fmt.Errorf("read session state: %w", err)
		}
		return fmt.Errorf("write session state: %w", err)
	}
	return nil
}

// loadFailure is the refusal for an unreadable session: the slot's message
// plus the underlying cause.
func loadFailure(required []sweatfile.PreMergeSkill, slot Slot, cause error) string {
	return renderFailure(required, fmt.Sprintf("%s (%v)", slot.missing, cause))
}

// evaluate is the shared decision of Peek and Claim: given a loaded state, is
// the attestation available? On refusal it returns the TAP failure doc and
// ErrAttestationRequired.
func evaluate(required []sweatfile.PreMergeSkill, st *session.State) (string, error) {
	a := st.PreMergeAttestation
	if a == nil {
		return renderFailure(required,
			"no fresh attestation buffered; call `nothing-but-the-truth` first, then retry"), ErrAttestationRequired
	}
	if a.Claim != nil && session.IsAlive(a.Claim.PID) {
		return renderFailure(required, fmt.Sprintf(
			"the buffered attestation is held by an in-flight merge or check "+
				"(claimed %s by pid %d); it is consumed if that attempt lands and "+
				"released for a retry if it fails. To merge a further batch now, "+
				"record a fresh attestation with `nothing-but-the-truth`",
			a.Claim.ClaimedAt.Format(time.RFC3339), a.Claim.PID)), ErrAttestationRequired
	}
	return "", nil
}

// Peek reports whether the gate is satisfied for slot WITHOUT claiming the
// buffered attestation, so a refusal never touches it (spinclass#265). It is
// advisory: a later Claim can still be refused after a successful Peek (another
// attempt claimed in between); that TOCTOU is by design, and Claim is the
// authority. Outcomes:
//
//   - Gate dormant (no required skills): (true, "", nil).
//   - Attestation buffered and not held by a live claim (a dead-PID claim is
//     void): (true, "", nil), buffer untouched.
//   - No attestation, unreadable state, or a live claim held by another
//     attempt: (false, <TAP failure doc>, ErrAttestationRequired).
func Peek(merged sweatfile.Sweatfile, slot Slot) (ok bool, output string, err error) {
	required := merged.ActivePreMergeSkills()
	if len(required) == 0 {
		return true, "", nil
	}
	st, err := slot.load()
	if err != nil {
		return false, loadFailure(required, slot, err), ErrAttestationRequired
	}
	if output, err := evaluate(required, st); err != nil {
		return false, output, err
	}
	return true, "", nil
}

// Claim takes the hold on the buffered attestation for one merge or check
// attempt: it refuses exactly as Peek does, otherwise stamps a claim owned by
// this process and returns the Ticket that Settle needs. A dormant gate
// returns the zero Ticket, which Settle treats as a no-op.
func Claim(merged sweatfile.Sweatfile, slot Slot) (Ticket, string, error) {
	required := merged.ActivePreMergeSkills()
	if len(required) == 0 {
		return Ticket{}, "", nil
	}
	var (
		tk      Ticket
		refusal string
		called  bool
	)
	err := slot.update(func(st *session.State) error {
		called = true
		if output, err := evaluate(required, st); err != nil {
			refusal = output
			return err
		}
		now := time.Now().UTC()
		a := st.PreMergeAttestation
		a.Claim = &session.AttestationClaim{
			ID:        fmt.Sprintf("%d-%d", os.Getpid(), now.UnixNano()),
			PID:       os.Getpid(),
			ClaimedAt: now,
		}
		tk = Ticket{RecordedAt: a.RecordedAt, HeadSha: a.HeadSha, ClaimID: a.Claim.ID}
		return nil
	})
	switch {
	case err == nil:
		return tk, "", nil
	case !called: // session unreadable, closed or gone
		return Ticket{}, loadFailure(required, slot, err), ErrAttestationRequired
	case errors.Is(err, ErrAttestationRequired):
		return Ticket{}, refusal, ErrAttestationRequired
	default:
		return Ticket{}, "", fmt.Errorf("claim pre-merge attestation: %w", err)
	}
}

// Settle ends a hold: landed consumes the attestation, anything else releases
// the claim and keeps the attestation for the retry. It is a no-op for a zero
// Ticket, an unloadable session, or when a re-record superseded the held
// attestation (the newest attestation wins). Returns an error only on a store
// failure.
func Settle(slot Slot, t Ticket, landed bool) error {
	if t.ClaimID == "" {
		return nil
	}
	called := false
	err := slot.update(func(st *session.State) error {
		called = true
		a := st.PreMergeAttestation
		if a == nil || !a.RecordedAt.Equal(t.RecordedAt) || a.Claim == nil || a.Claim.ID != t.ClaimID {
			return errSuperseded
		}
		if landed {
			st.PreMergeAttestation = nil
		} else {
			a.Claim = nil
		}
		return nil
	})
	switch {
	case err == nil, errors.Is(err, errSuperseded):
		return nil
	case !called && errors.Is(err, os.ErrNotExist): // session gone or closed
		return nil
	default:
		return fmt.Errorf("settle pre-merge attestation: %w", err)
	}
}

// Check verifies a fresh attestation is buffered and consumes it.
//
// Deprecated: removed in #219 task 3.
func Check(merged sweatfile.Sweatfile, repoPath, branch string) (ok bool, output string, err error) {
	if pok, output, perr := Peek(merged, WorktreeSlot(repoPath, branch)); !pok {
		return false, output, perr
	}
	if cerr := Consume(merged, repoPath, branch); cerr != nil {
		return false, "", cerr
	}
	return true, "", nil
}

// Consume clears any buffered attestation for (repoPath, branch).
//
// Deprecated: removed in #219 task 3.
func Consume(merged sweatfile.Sweatfile, repoPath, branch string) error {
	return consumeSlot(merged, WorktreeSlot(repoPath, branch))
}

func consumeSlot(merged sweatfile.Sweatfile, slot Slot) error {
	if len(merged.ActivePreMergeSkills()) == 0 {
		return nil
	}
	st, readErr := slot.load()
	if readErr != nil || st.PreMergeAttestation == nil {
		return nil
	}
	st.PreMergeAttestation = nil
	if writeErr := slot.store(*st); writeErr != nil {
		return fmt.Errorf("clear pre-merge attestation: %w", writeErr)
	}
	return nil
}

// CheckImplicit is Check for an implicit (main-checkout) session.
//
// Deprecated: removed in #219 task 3.
func CheckImplicit(merged sweatfile.Sweatfile, checkout string) (ok bool, output string, err error) {
	if pok, output, perr := Peek(merged, ImplicitSlot(checkout)); !pok {
		return false, output, perr
	}
	if cerr := ConsumeImplicit(merged, checkout); cerr != nil {
		return false, "", cerr
	}
	return true, "", nil
}

// PeekImplicit is Peek for an implicit (main-checkout) session.
//
// Deprecated: removed in #219 task 3.
func PeekImplicit(merged sweatfile.Sweatfile, checkout string) (ok bool, output string, err error) {
	return Peek(merged, ImplicitSlot(checkout))
}

// ConsumeImplicit is Consume for an implicit (main-checkout) session.
//
// Deprecated: removed in #219 task 3.
func ConsumeImplicit(merged sweatfile.Sweatfile, checkout string) error {
	return consumeSlot(merged, ImplicitSlot(checkout))
}

// renderFailure builds a self-contained TAP-14 document describing the
// gate failure. The doc emits a directive comment, a single `not ok`
// test point with structured YAMLish diagnostics naming the required
// skills and the required tool, then closes with a Plan line.
func renderFailure(required []sweatfile.PreMergeSkill, msg string) string {
	var buf bytes.Buffer
	tw := tap.NewWriter(&buf)
	tw.Comment("directive: this repo requires pre-merge skill attestation; call `nothing-but-the-truth` first")
	tw.NotOk("pre-merge skill attestation missing", map[string]string{
		"severity":        "fail",
		"message":         msg,
		"required_tool":   "nothing-but-the-truth",
		"required_skills": renderRequiredSkills(required),
	})
	tw.Plan()
	return buf.String()
}

// renderRequiredSkills builds a multi-line YAML block listing the
// required skills with their rationales. The TAP writer detects the
// embedded newlines and emits it under a `required_skills: |` heading
// so MCP-aware agents can read each entry inline without an extra
// fetch.
func renderRequiredSkills(required []sweatfile.PreMergeSkill) string {
	var b strings.Builder
	for _, s := range required {
		fmt.Fprintf(&b, "- name: %s\n", s.Name)
		fmt.Fprintf(&b, "  rationale: %s\n", quoteYAMLScalar(s.Rationale))
	}
	return strings.TrimRight(b.String(), "\n")
}

func quoteYAMLScalar(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
