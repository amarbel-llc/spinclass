// Package spawn launches detached, harness-booted worker sessions (FDR
// 0006): repo-dirname resolution, sweatfile spawn/spawn-entry template
// substitution, worktree + session-state creation with spawned_by lineage,
// and the chat-hello gate that proves the worker came up.
package spawn

import (
	"fmt"
	"strings"
)

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

// SubstituteSpawn renders the spawn argv: {id}→id and {dir}→wtPath
// substring-substituted per element; the element exactly equal to "{entry}"
// is replaced by splicing in the (already-substituted) entry argv
// element-wise. An embedded {entry} is NOT a splice point — an argv cannot
// be expanded element-wise inside one string. Errors when entry is empty
// (no [session-entry].spawn-entry configured) or spawnTpl has no "{entry}"
// element.
func SubstituteSpawn(spawnTpl []string, id, wtPath string, entry []string) ([]string, error) {
	if len(entry) == 0 {
		return nil, fmt.Errorf(
			"no [session-entry].spawn-entry configured: spawn needs a harness argv to boot the worker into (e.g. [\"clown\", \"{prompt}\"])",
		)
	}

	out := make([]string, 0, len(spawnTpl)+len(entry)-1)
	spliced := false
	for _, e := range spawnTpl {
		if e == "{entry}" {
			out = append(out, entry...)
			spliced = true
			continue
		}
		e = strings.ReplaceAll(e, "{id}", id)
		e = strings.ReplaceAll(e, "{dir}", wtPath)
		out = append(out, e)
	}
	if !spliced {
		return nil, fmt.Errorf(
			"spawn template %q has no \"{entry}\" element to splice the harness argv into", spawnTpl,
		)
	}
	return out, nil
}
