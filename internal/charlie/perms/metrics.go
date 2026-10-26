package perms

import "code.linenisgreat.com/spinclass/internal/0/statsd"

// Permission event names, the third segment of permission.<repo>.<event>.… (#369).
const (
	// EventPrompt: a real prompt is about to be shown (no tier rule, or the
	// always-ask floor deferred to the human).
	EventPrompt = "prompt"
	// EventTierAllow: RunCheck auto-approved via a tier rule.
	EventTierAllow = "tier_allow"
	// EventUnattendedDeny: the PreToolUse hook turned an ask into a deny
	// because the session is dontAsk.
	EventUnattendedDeny = "unattended_deny"
)

// CountMetric emits one counter name (without the spinclass. prefix) for the
// session at cwd. It is a seam, not a default: resolving the statsd endpoint
// needs the sweatfile hierarchy, which perms must not import, so the CLI layer
// installs it. Nil means "emit nothing".
var CountMetric func(cwd, name string)

// CountPermissionEvent counts permission.<repo>.<event>.<origin>.<tool>. The
// origin is "subagent" when agentID is non-empty, else "main". Fire-and-forget:
// a hook must never fail or slow down because of metrics.
func CountPermissionEvent(cwd, event, agentID, toolName string) {
	if CountMetric == nil {
		return
	}
	origin := "main"
	if agentID != "" {
		origin = "subagent"
	}
	CountMetric(cwd, "permission."+statsd.Segment(repoFromCWD(cwd))+"."+event+"."+origin+"."+statsd.Segment(toolName))
}
