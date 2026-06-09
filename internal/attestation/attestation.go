// Package attestation implements the pre-merge skill attestation gate
// described in docs/features/0007-pre-merge-skill-attestation.md.
//
// The gate is active when the resolved sweatfile hierarchy contains a
// non-empty [[pre-merge-skills]] list. In that mode, the MCP handlers
// for merge-this-session and check-this-session call Check before
// invoking the inner merge/check machinery: if no fresh attestation is
// buffered in the session state, Check returns a structured TAP
// failure that the handler ships back verbatim. Each successful Check
// consumes one buffered attestation; there is no sticky once-per-
// session mode.
//
// The CLI (`sc merge` / `sc check`) does not call Check — the gate is
// MCP-only by design.
package attestation

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amarbel-llc/spinclass/internal/session"
	"github.com/amarbel-llc/spinclass/internal/sweatfile"
	tap "github.com/amarbel-llc/tap/go/pkgs/writer"
)

// ErrAttestationRequired is returned by Check when no fresh attestation
// is buffered. The returned output text is a self-contained TAP-14
// document that the MCP handler should ship to the agent verbatim.
var ErrAttestationRequired = errors.New("pre-merge skill attestation required")

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

// Record writes a validated attestation into the session state for
// (repoPath, branch). Overwrites any prior buffered attestation.
func Record(repoPath, branch string, skills []session.AttestedSkill) error {
	st, err := session.Read(repoPath, branch)
	if err != nil {
		return fmt.Errorf("read session state: %w", err)
	}
	st.PreMergeAttestation = &session.PreMergeAttestation{
		RecordedAt: time.Now().UTC(),
		Skills:     skills,
	}
	if err := session.Write(*st); err != nil {
		return fmt.Errorf("write session state: %w", err)
	}
	return nil
}

// RecordImplicit writes a validated attestation into an implicit (main-checkout)
// session's per-randID state file at checkout. Mirrors Record but loads via
// FindImplicitAtCwd and persists via WriteImplicit. Overwrites any prior
// buffered attestation.
func RecordImplicit(checkout string, skills []session.AttestedSkill) error {
	st, randID, err := session.FindImplicitAtCwd(checkout)
	if err != nil {
		return fmt.Errorf("find implicit session: %w", err)
	}
	if st == nil {
		return fmt.Errorf("no live implicit session at %s", checkout)
	}
	st.PreMergeAttestation = &session.PreMergeAttestation{
		RecordedAt: time.Now().UTC(),
		Skills:     skills,
	}
	if err := session.WriteImplicit(*st, randID); err != nil {
		return fmt.Errorf("write implicit session state: %w", err)
	}
	return nil
}

// CheckImplicit is Check for an implicit (main-checkout) session: it verifies a
// fresh attestation is buffered in the per-randID state file and consumes it.
// Same three outcomes as Check (dormant → (true,"",nil); satisfied → consume +
// (true,"",nil); failed → (false, TAP doc, ErrAttestationRequired)).
func CheckImplicit(merged sweatfile.Sweatfile, checkout string) (ok bool, output string, err error) {
	required := merged.ActivePreMergeSkills()
	if len(required) == 0 {
		return true, "", nil
	}

	st, randID, readErr := session.FindImplicitAtCwd(checkout)
	if readErr != nil || st == nil {
		return false, renderFailure(required,
			"could not read implicit session state to verify attestation; this MCP tool requires a tracked spinclass session"), ErrAttestationRequired
	}
	if st.PreMergeAttestation == nil {
		return false, renderFailure(required,
			"no fresh attestation buffered; call `nothing-but-the-truth` first, then retry"), ErrAttestationRequired
	}

	st.PreMergeAttestation = nil
	if writeErr := session.WriteImplicit(*st, randID); writeErr != nil {
		return false, "", fmt.Errorf("clear pre-merge attestation: %w", writeErr)
	}
	return true, "", nil
}

// Check verifies whether a fresh attestation is buffered and applies
// to the required skills declared in the merged sweatfile. Three
// outcomes:
//
//   - Gate dormant (no required skills): returns (true, "", nil).
//   - Gate satisfied: consumes the buffered attestation by clearing
//     the field and re-writing session state, then returns (true, "",
//     nil).
//   - Gate failed: returns (false, <self-contained TAP failure doc>,
//     ErrAttestationRequired). Callers ship the TAP doc to the agent
//     unchanged.
//
// Any internal error (session state read/write failure outside the
// "no buffered attestation" path) is returned as the third tuple
// element with output empty.
func Check(merged sweatfile.Sweatfile, repoPath, branch string) (ok bool, output string, err error) {
	required := merged.ActivePreMergeSkills()
	if len(required) == 0 {
		return true, "", nil
	}

	st, readErr := session.Read(repoPath, branch)
	if readErr != nil {
		return false, renderFailure(required,
			"could not read session state to verify attestation; this MCP tool requires a tracked spinclass session"), ErrAttestationRequired
	}
	if st.PreMergeAttestation == nil {
		return false, renderFailure(required,
			"no fresh attestation buffered; call `nothing-but-the-truth` first, then retry"), ErrAttestationRequired
	}

	st.PreMergeAttestation = nil
	if writeErr := session.Write(*st); writeErr != nil {
		return false, "", fmt.Errorf("clear pre-merge attestation: %w", writeErr)
	}
	return true, "", nil
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
