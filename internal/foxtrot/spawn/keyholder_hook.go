package spawn

import "maps"

// KeyholderEnv, when set (cmd/spinclass sets it), returns the environment that
// points a new worker at its own key holder in place of the operator's agent,
// starting the pair if the [keyholder] switch is on; nil when it is off. An
// error refuses the spawn: a worker that was meant to run on its own key must
// not launch on the operator's.
var KeyholderEnv func(home, repoPath, sessionKey string) (map[string]string, error)

// withKeyholderEnv returns sessionEnv with kh laid over it; sessionEnv itself
// is never modified.
func withKeyholderEnv(sessionEnv, kh map[string]string) map[string]string {
	if len(kh) == 0 {
		return sessionEnv
	}
	out := make(map[string]string, len(sessionEnv)+len(kh))
	maps.Copy(out, sessionEnv)
	maps.Copy(out, kh)
	return out
}
