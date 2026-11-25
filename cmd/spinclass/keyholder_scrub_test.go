package main

import (
	"bytes"
	"strings"
	"testing"
)

// With the switch on, every variable that names the operator's agent that the
// launching process carries is emptied for the session, not only SSH_AUTH_SOCK;
// a variable the launching process does not carry is not invented.
func TestKeyholderOnEmptiesEveryVariableThatNamesTheOperatorsAgent(t *testing.T) {
	t.Setenv("PIGGY_AUTH_SOCK", "/run/user/1000/piggy-agent.sock")
	t.Setenv("PIVY_AUTH_SOCK", "/run/user/1000/pivy.sock")
	t.Setenv("SSH_AGENT_PID", "4242")
	t.Setenv("SSH_ASKPASS", "/usr/lib/ssh/askpass")
	t.Setenv("SSH_ASKPASS_REQUIRE", "force")
	t.Setenv("GPG_AGENT_INFO", "")
	home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n")
	khUseFake(t, newKhFake(t))
	env, err := keyholderSessionEnv(home, repo, "repo/a", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"PIGGY_AUTH_SOCK", "PIVY_AUTH_SOCK", "SSH_AGENT_PID", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE"} {
		if v, ok := env[name]; !ok || v != "" {
			t.Errorf("%s = (%q, %v), want it present and empty", name, v, ok)
		}
	}
	if _, ok := env["GPG_AGENT_INFO"]; ok {
		t.Error("GPG_AGENT_INFO was already empty in the launching process and needs no entry")
	}
	if !strings.HasSuffix(env["SSH_AUTH_SOCK"], "agent.sock") || strings.Contains(env["SSH_AUTH_SOCK"], "/run/user/1000") {
		t.Errorf("SSH_AUTH_SOCK = %q, want the holder's socket", env["SSH_AUTH_SOCK"])
	}
}

// The overlay also empties one a configured entry env set, and leaves the rest.
func TestWithKeyholderEnvEmptiesWhatAConfiguredEntryEnvSetToo(t *testing.T) {
	kh := map[string]string{"SSH_AUTH_SOCK": "/holder/agent.sock"}
	in := map[string]string{"PIGGY_AUTH_SOCK": "/operator/piggy.sock", "SSH_ASKPASS": "/bin/askpass", "KEEP": "1", "SSH_AUTH_SOCK": "/operator/agent.sock"}
	got := withKeyholderEnv(in, kh)
	if got["PIGGY_AUTH_SOCK"] != "" || got["SSH_ASKPASS"] != "" {
		t.Errorf("an entry env that names the operator's agent survived: %v", got)
	}
	if got["KEEP"] != "1" || got["SSH_AUTH_SOCK"] != "/holder/agent.sock" {
		t.Errorf("the overlay lost the rest or the holder socket: %v", got)
	}
	if in["PIGGY_AUTH_SOCK"] != "/operator/piggy.sock" {
		t.Error("the input map was modified")
	}
	// Off: nothing is scrubbed.
	if same := withKeyholderEnv(in, nil); same["PIGGY_AUTH_SOCK"] != "/operator/piggy.sock" {
		t.Errorf("an off switch scrubbed the environment: %v", same)
	}
}
