package spawn

import (
	"maps"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// KeyholderEnv, when set (cmd/spinclass sets it), returns the environment that
// points a new worker at its own key holder in place of the operator's agent,
// starting the pair if the [keyholder] switch is on; nil when it is off. An
// error refuses the spawn: a worker that was meant to run on its own key must
// not launch on the operator's. undo is non-nil only when THIS call created the
// pair; the spawn calls it if the launch then fails, since no session will exist
// to close the pair.
var KeyholderEnv func(home, repoPath, sessionKey string) (env map[string]string, undo func(), err error)

// withKeyholderEnv returns sessionEnv with kh laid over it and every other
// variable that names the operator's agent emptied, including one a configured
// entry env set; sessionEnv itself is never modified.
func withKeyholderEnv(sessionEnv, kh map[string]string) map[string]string {
	if len(kh) == 0 {
		return sessionEnv
	}
	out := make(map[string]string, len(sessionEnv)+len(kh))
	maps.Copy(out, sessionEnv)
	for k := range out {
		if keyholder.IsOperatorAgentVar(k) {
			out[k] = ""
		}
	}
	maps.Copy(out, kh)
	return out
}
