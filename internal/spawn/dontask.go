package spawn

import (
	"fmt"
	"slices"
	"strings"
)

// DontAskModeArgs is what a confined worker's provider-args carry so it can
// never block on a permission prompt: claude's dontAsk mode auto-denies every
// tool call that would otherwise ask (the session never waits for input), while
// allow rules and PreToolUse-hook approvals still run. A confined unit has no
// operator at its tty by construction (FDR 0035 F8a), so an asked question is
// never answered; the first real confined worker sat on one for 30+ minutes.
var DontAskModeArgs = []string{"--permission-mode", "dontAsk"}

// SpliceDontAskMode returns argv with claude's dontAsk permission mode set
// among the provider args (just after the first "--" separator), replacing any
// permission mode the entry already carried (AutoModeArgs included) and the
// retired --enable-auto-mode (dontAsk and auto mode are alternatives; auto
// falls back to prompting after repeated classifier blocks). It fails closed: a confined launch that cannot
// be made non-prompting is refused rather than started able to hang. Does not
// mutate argv.
func SpliceDontAskMode(argv []string) ([]string, error) {
	sep := slices.Index(argv, "--")
	if sep == -1 {
		return nil, fmt.Errorf("a confined spawn must not prompt, but this spawn-entry has no \"--\" provider-args separator to place --permission-mode dontAsk after: %v", argv)
	}
	if resolveProvider(argv[:sep]) != "claude" {
		return nil, fmt.Errorf("a confined spawn must not prompt, but this spawn-entry's provider is not claude, so dontAsk mode cannot be set: %v", argv)
	}
	out := make([]string, 0, len(argv)+2)
	out = append(out, argv[:sep+1]...)
	out = append(out, DontAskModeArgs...)
	rest := argv[sep+1:]
	for i := 0; i < len(rest); i++ {
		switch {
		case rest[i] == "--permission-mode":
			i++ // drop the value too
		case strings.HasPrefix(rest[i], "--permission-mode="), rest[i] == retiredAutoModeFlag:
		default:
			out = append(out, rest[i])
		}
	}
	return out, nil
}
