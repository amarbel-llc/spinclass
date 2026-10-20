package git

import (
	"fmt"

	"code.linenisgreat.com/spinclass/internal/store"
)

// refuseUnownedForConfined is the body-side ownership check for confined
// callers (FDR 0036 section 9.4). A confined worker cannot have been shown a
// permission prompt, so the tool body refuses whatever Owned rejects. It is the
// same check Classify applies to a write verb, minus the force flag, which only
// decides whether a human is asked. Unconfined callers are never checked here:
// for them the PreToolUse hook is the sole gate (section 2).
func (b Backend) refuseUnownedForConfined(s store.Session, op store.Op) error {
	if !s.Confinement.Confined() || store.ClassOf(op.Tool, op.Subcommand) != store.ClassWrite {
		return nil
	}
	args, ok := decodeClassifyArgs(op.Args)
	if !ok || !args.optionFree() || !b.writeAllowed(s, op, args) {
		return fmt.Errorf("refusing %s: a confined caller may only touch refs and the worktree its session owns (the session branch and %s%s*)", verbLabel(op), s.Branch, ownershipSeparator)
	}
	return nil
}

func verbLabel(op store.Op) string {
	if op.Subcommand == "" {
		return op.Tool
	}
	return op.Tool + " " + op.Subcommand
}

// noHooksConfig points git at a hooks directory that cannot exist, which
// disables every hook, including the per-commit hook installed through the
// worktree's core.hooksPath (FDR 0019).
var noHooksConfig = []string{"-c", "core.hooksPath=/dev/null"}

// withoutHooksForConfined disables git hooks for a confined caller's mutating
// verb. The hooks live in the worker-writable worktree, so running them in the
// frontend would execute worker-authored code outside the unit (FDR 0036
// section 9.3); the merge gate is the check that remains. commit also gets an
// explicit --no-verify so the intent survives a hooks path override.
func withoutHooksForConfined(s store.Session, op store.Op, argv []string) []string {
	if !s.Confinement.Confined() || store.ClassOf(op.Tool, op.Subcommand) != store.ClassWrite {
		return argv
	}
	if op.Tool == "commit" && len(argv) > 0 && argv[0] == "commit" {
		argv = append([]string{"commit", "--no-verify"}, argv[1:]...)
	}
	return append(append([]string{}, noHooksConfig...), argv...)
}

var commitCreatingVerbs = map[string]bool{
	"commit ":            true,
	"branch cherry-pick": true,
	"branch revert":      true,
	"branch rebase":      true,
	"branch restack":     true,
}

// identityEnv stamps the configured identity onto the verbs that create
// commits. Unset fields add nothing, so git resolves the identity itself.
func identityEnv(s store.Session, op store.Op) []string {
	if !commitCreatingVerbs[op.Tool+" "+op.Subcommand] {
		return nil
	}
	return s.Identity.Env()
}
