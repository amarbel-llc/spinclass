// Package spawn launches detached, harness-booted worker sessions (FDR
// 0006): repo-dirname resolution, sweatfile spawn-entry template
// substitution, worktree + session-state creation with spawned_by lineage,
// and the chat-hello gate that proves the worker came up.
package spawn

import "strings"

// SubstituteEntry renders the spawn-entry argv: {dir}→wtPath, {prompt}→brief.
// Replacement is substring-level within each element (an element may embed
// the placeholder), but the brief always stays within its element — no
// shell joining or re-splitting. {dir} is substituted BEFORE {prompt} so a
// brief that happens to contain the literal text "{dir}" survives verbatim.
func SubstituteEntry(entry []string, brief, wtPath string) []string {
	out := make([]string, len(entry))
	for i, e := range entry {
		e = strings.ReplaceAll(e, "{dir}", wtPath)
		e = strings.ReplaceAll(e, "{prompt}", brief)
		out[i] = e
	}
	return out
}

// SubstituteWindow renders the spawn-window argv template (#149): {id}→id
// and {dir}→wtPath substring-substituted per element. Returns nil for an
// empty template (knob unset). {attach-id} is deliberately NOT resolved here —
// it is filled by substituteAttachID after the hello, since the worker's posh
// session id is unknown at render time. No {entry} splice — the window command
// is a leaf argv; validate rejects {entry}/{prompt} in it.
func SubstituteWindow(template []string, id, wtPath string) []string {
	if len(template) == 0 {
		return nil
	}
	out := make([]string, len(template))
	for i, e := range template {
		e = strings.ReplaceAll(e, "{id}", id)
		e = strings.ReplaceAll(e, "{dir}", wtPath)
		out[i] = e
	}
	return out
}

// substituteAttachID resolves the {attach-id} placeholder in an
// already-rendered spawn-window argv with the worker's posh session id, learned
// from the hello (direction B). Separated from SubstituteWindow because that id
// is only known after the worker boots, whereas {id}/{dir} are known at launch.
func substituteAttachID(argv []string, attachID string) []string {
	out := make([]string, len(argv))
	for i, e := range argv {
		out[i] = strings.ReplaceAll(e, "{attach-id}", attachID)
	}
	return out
}
