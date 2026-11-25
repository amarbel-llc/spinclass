package spawn

import "testing"

func TestSpawnKeyholderOverlayEmptiesEveryVariableThatNamesTheOperatorsAgent(t *testing.T) {
	in := map[string]string{"PIGGY_AUTH_SOCK": "/operator/piggy.sock", "SSH_ASKPASS_REQUIRE": "force", "SSH_AGENT_PID": "7", "KEEP": "1"}
	got := withKeyholderEnv(in, map[string]string{"SSH_AUTH_SOCK": "/holder/agent.sock", "GPG_AGENT_INFO": ""})
	for _, name := range []string{"PIGGY_AUTH_SOCK", "SSH_ASKPASS_REQUIRE", "SSH_AGENT_PID"} {
		if got[name] != "" {
			t.Errorf("%s = %q, want it emptied", name, got[name])
		}
	}
	if got["KEEP"] != "1" || got["SSH_AUTH_SOCK"] != "/holder/agent.sock" {
		t.Errorf("the overlay lost the rest or the holder socket: %v", got)
	}
	if in["PIGGY_AUTH_SOCK"] != "/operator/piggy.sock" {
		t.Error("the input map was modified")
	}
}
