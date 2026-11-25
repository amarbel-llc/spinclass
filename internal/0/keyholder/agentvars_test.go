package keyholder

import (
	"reflect"
	"testing"
)

func TestIsOperatorAgentVarSelectsTheNamesThatPointAtHisAgent(t *testing.T) {
	for _, name := range []string{"PIGGY_AUTH_SOCK", "PIVY_AUTH_SOCK", "SSH_AGENT_PID", "GPG_AGENT_INFO", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "SSH_ASKPASS_FUTURE"} {
		if !IsOperatorAgentVar(name) {
			t.Errorf("%s is not selected, want it scrubbed", name)
		}
	}
	// SSH_AUTH_SOCK is replaced by the holder's socket, not scrubbed; the rest
	// are not agent variables.
	for _, name := range []string{"SSH_AUTH_SOCK", "PATH", "HOME", "SSH_CONNECTION", "SSH_TTY", "PIGGY_STORE_DIR", "GPG_TTY", "XDG_RUNTIME_DIR"} {
		if IsOperatorAgentVar(name) {
			t.Errorf("%s is selected, want it left alone", name)
		}
	}
}

func TestOperatorAgentVarsInReportsOnlyVariablesThatCarryAValue(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin", "PIGGY_AUTH_SOCK=/run/user/1000/piggy.sock", "SSH_AUTH_SOCK=/run/user/1000/agent",
		"SSH_ASKPASS=", "SSH_ASKPASS_REQUIRE=force", "SSH_AGENT_PID=42", "noequals",
	}
	got := OperatorAgentVarsIn(environ)
	want := []string{"PIGGY_AUTH_SOCK", "SSH_ASKPASS_REQUIRE", "SSH_AGENT_PID"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OperatorAgentVarsIn = %v, want %v (an already-empty variable needs no scrub)", got, want)
	}
}
