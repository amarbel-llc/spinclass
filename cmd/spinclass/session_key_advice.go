package main

import "fmt"

// sessionKeyNewPairAdvice is the remedy `sc session-key` names when a session's
// pair is dead or its key cannot be vouched for: stop the pair, then resume. The
// resume starts a new pair and mints a new key ONLY with [keyholder] mode on, and
// the new key reaches only the process that launch starts: a still-running agent
// of the session keeps the SSH_AUTH_SOCK it was launched with, which points at the
// stopped pair, so that agent has to be restarted as well (issue #523).
func sessionKeyNewPairAdvice(key, why string) string {
	return fmt.Sprintf("run `sc keyholder-stop %s` %s, then, with [keyholder] mode on, `sc resume %s` starts a new pair with a new key (announced); the session's RUNNING agent keeps its old SSH_AUTH_SOCK, which points at the stopped pair, so restart that agent too: the new key reaches only the process a launch starts", key, why, key)
}
