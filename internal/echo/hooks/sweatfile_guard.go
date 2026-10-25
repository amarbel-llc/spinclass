package hooks

import (
	"fmt"
	"io"

	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

// The sweatfile-edit guard: a Write/Edit of a sweatfile asks by default, and
// denies outright when an operator sets [permissions] deny-sweatfile-edits in a
// trusted (global or strictly-ancestor) sweatfile.
//
// It is deliberately small and self-contained. The intended end state is that
// every session IO call is served by a spinclass plugin that enforces its
// permissions at the junction to the plugin; when that exists this file, its
// RunOption and the setting go away together.

// WithDenySweatfileEdits makes the hook deny, rather than ask, on a Write/Edit
// of a sweatfile. Handle derives it from the trusted sweatfile layers.
func WithDenySweatfileEdits(deny bool) RunOption {
	return func(o *runOptions) { o.denySweatfileEdits = deny }
}

// denySweatfileEdits reads the switch for the session's repo from the trusted
// layers only. The trust boundary is the MAIN repo root, never the worktree:
// the worktree's own sweatfile is not a layer at all (sweatfileio.LoadHierarchy
// never reads it), so nothing the agent itself can write is honoured. An
// unreadable hierarchy is off, which leaves the ask in place.
func denySweatfileEdits(home, mainRepoRoot string) bool {
	if home == "" || mainRepoRoot == "" {
		return false
	}
	h, err := sweatfileio.LoadHierarchy(home, mainRepoRoot)
	if err != nil {
		return false
	}
	deny, _ := sweatfileio.TrustedDenySweatfileEdits(h, home, mainRepoRoot)
	return deny
}

// sweatfileEditDecision is the verdict for a Write/Edit of the sweatfile at fp.
func sweatfileEditDecision(w io.Writer, fp string, deny bool) error {
	if deny {
		return writeDeny(w, fmt.Sprintf(
			"This modifies the sweatfile at %s; sweatfile edits are disabled by [permissions] deny-sweatfile-edits in a trusted sweatfile.", fp,
		))
	}
	return writeAsk(w, fmt.Sprintf(
		"This modifies the sweatfile at %s, which controls session configuration.", fp,
	))
}
