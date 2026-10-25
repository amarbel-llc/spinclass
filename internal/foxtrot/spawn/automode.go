package spawn

import (
	"slices"
	"strings"
)

// AutoModeArgs are the claude provider-args spliced into a spawned worker's
// entry by default (see SpliceAutoMode), so an unconfined worker starts in
// auto mode (spinclass#385). Unlike model-flags, this isn't
// provider-configurable via the sweatfile: it's a single claude-only default,
// opt-out only via [session-entry].disable-auto-mode
// (sweatfile.Sweatfile.SessionAutoModeDisabled). A confined spawn replaces it
// with DontAskModeArgs.
var AutoModeArgs = []string{"--permission-mode", "auto"}

// retiredAutoModeFlag was spliced before spinclass#385. Claude Code 2.1.301's
// help does not list it; the splices drop it from entries that still carry it.
const retiredAutoModeFlag = "--enable-auto-mode"

// SpliceAutoMode inserts AutoModeArgs into entry immediately after the
// literal "--" provider-args separator, when the resolved provider is
// "claude" (see resolveProvider), dropping retiredAutoModeFlag. Unlike
// SpliceModelFlag, this is a silent no-op — not a hard error — when entry has
// no "--" separator, the provider isn't "claude", or the provider args already
// name a permission mode: it's an automatic default applied to every spawn,
// not an explicit per-call request, so a fully custom spawn-entry must not be
// broken or overridden by it. Does not mutate entry. Callers gate this on
// !merged.SessionAutoModeDisabled().
func SpliceAutoMode(entry []string) []string {
	idx := slices.Index(entry, "--")
	if idx == -1 || resolveProvider(entry) != "claude" {
		return entry
	}
	rest := entry[idx+1:]
	if slices.ContainsFunc(rest, isPermissionModeArg) {
		return entry
	}
	out := make([]string, 0, len(entry)+len(AutoModeArgs))
	out = append(out, entry[:idx+1]...)
	out = append(out, AutoModeArgs...)
	for _, a := range rest {
		if a != retiredAutoModeFlag {
			out = append(out, a)
		}
	}
	return out
}

func isPermissionModeArg(a string) bool {
	return a == "--permission-mode" || strings.HasPrefix(a, "--permission-mode=")
}
