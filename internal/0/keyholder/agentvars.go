package keyholder

import "strings"

// operatorAgentVars are the environment variables that name the operator's
// agent or make a tool prompt through his desktop for it. SSH_AUTH_SOCK is not
// here: a session on its own key has it REPLACED by the holder's socket.
//
//   - PIGGY_AUTH_SOCK and PIVY_AUTH_SOCK: piggy resolves its agent from
//     PIGGY_AUTH_SOCK first, then SSH_AUTH_SOCK, then PIVY_AUTH_SOCK, so one
//     left behind would win over the holder's socket.
//   - SSH_AGENT_PID: the pid of the operator's ssh-agent.
//   - GPG_AGENT_INFO: the (legacy) address of his gpg-agent.
//   - SSH_ASKPASS and SSH_ASKPASS_REQUIRE (any SSH_ASKPASS*): the program that
//     asks him for a PIN or a touch.
var operatorAgentVars = map[string]bool{
	"PIGGY_AUTH_SOCK": true,
	"PIVY_AUTH_SOCK":  true,
	"SSH_AGENT_PID":   true,
	"GPG_AGENT_INFO":  true,
}

// IsOperatorAgentVar reports whether name is an environment variable that
// points a process at the operator's agent (or at his askpass). A session on
// its own key must not inherit it; see the package doc and the [keyholder] section of spinclass-sweatfile(5).
func IsOperatorAgentVar(name string) bool {
	return operatorAgentVars[name] || strings.HasPrefix(name, "SSH_ASKPASS")
}

// OperatorAgentVarsIn returns, in the order given, the names in environ
// (KEY=VALUE entries) that IsOperatorAgentVar selects and that carry a value.
func OperatorAgentVarsIn(environ []string) []string {
	var names []string
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok && v != "" && IsOperatorAgentVar(k) {
			names = append(names, k)
		}
	}
	return names
}
