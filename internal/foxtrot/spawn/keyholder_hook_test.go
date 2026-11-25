package spawn

import "testing"

func TestWithKeyholderEnvReplacesTheOperatorsAgentAndNeverMutatesItsInput(t *testing.T) {
	in := map[string]string{"SSH_AUTH_SOCK": "/operator/agent.sock", "KEEP": "1"}
	got := withKeyholderEnv(in, map[string]string{"SSH_AUTH_SOCK": "/holder/agent.sock"})
	if got["SSH_AUTH_SOCK"] != "/holder/agent.sock" || got["KEEP"] != "1" {
		t.Errorf("got %v, want the holder's socket and the rest kept", got)
	}
	if in["SSH_AUTH_SOCK"] != "/operator/agent.sock" {
		t.Error("the input map was modified")
	}
	if same := withKeyholderEnv(in, nil); same["SSH_AUTH_SOCK"] != "/operator/agent.sock" {
		t.Errorf("an empty key holder env changed the environment: %v", same)
	}
}
