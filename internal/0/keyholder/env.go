package keyholder

import "os"

// minimalEnv is the only environment a command the launcher runs inherits:
// PATH and HOME. SSH_AUTH_SOCK, PIGGY_AUTH_SOCK and the display variables are
// never carried, so the operator's agent is not reachable from a mint.
func minimalEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "HOME"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}
