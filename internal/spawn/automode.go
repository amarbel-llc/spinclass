package spawn

// AutoModeFlag is the claude CLI flag spliced into a spawned worker's
// provider-args by default (see SpliceAutoModeFlag). Unlike model-flags,
// this isn't provider-configurable via the sweatfile: it's a single
// claude-only default, opt-out only via [session-entry].disable-auto-mode
// (sweatfile.Sweatfile.SessionAutoModeDisabled).
const AutoModeFlag = "--enable-auto-mode"

// SpliceAutoModeFlag inserts AutoModeFlag into entry immediately after the
// literal "--" provider-args separator, when the resolved provider is
// "claude" (see resolveProvider). Unlike SpliceModelFlag, this is a silent
// no-op — not a hard error — when entry has no "--" separator or the
// provider isn't "claude": it's an automatic default enhancement applied to
// every spawn, not an explicit per-call request, so a fully custom
// spawn-entry (a different harness entirely, or a non-claude provider) must
// not be broken by it. Does not mutate entry. Callers gate this on
// !merged.SessionAutoModeDisabled().
func SpliceAutoModeFlag(entry []string) []string {
	idx := -1
	for i, e := range entry {
		if e == "--" {
			idx = i
			break
		}
	}
	if idx == -1 {
		return entry
	}
	if resolveProvider(entry) != "claude" {
		return entry
	}
	out := make([]string, 0, len(entry)+1)
	out = append(out, entry[:idx+1]...)
	out = append(out, AutoModeFlag)
	out = append(out, entry[idx+1:]...)
	return out
}
