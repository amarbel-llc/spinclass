package sessionexec

import (
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

func holderWorkerEnviron(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_COUNT": "5",
		"GIT_CONFIG_KEY_0": "gpg.format", "GIT_CONFIG_VALUE_0": "ssh",
		"GIT_CONFIG_KEY_1": "user.signingkey", "GIT_CONFIG_VALUE_1": "/parent/signing_key.pub",
		"GIT_CONFIG_KEY_2": "commit.gpgsign", "GIT_CONFIG_VALUE_2": "true",
		"GIT_CONFIG_KEY_3": "gpg.ssh.program", "GIT_CONFIG_VALUE_3": "/parent/keyholder-git-sign",
		"GIT_CONFIG_KEY_4": "gpg.ssh.allowedSignersFile", "GIT_CONFIG_VALUE_4": "/parent/allowed_signers",
		keyholder.GitSessionEnv: "parent/session", keyholder.GitAgentEnv: "/parent/agent.sock",
	} {
		t.Setenv(k, v)
	}
}

func signs(env []string) bool {
	return strings.Contains(strings.Join(env, "\n"), "/parent/")
}

// `sc exec` and `sc run` from a holder worker: into another session its signing
// stays behind; into its own it keeps it.
func TestExecIntoAnotherSessionDropsTheCallersHolderSigning(t *testing.T) {
	holderWorkerEnviron(t)
	other := &session.State{SessionKey: "other/session", WorktreePath: t.TempDir()}
	if signs(IdentityEnv(other)) {
		t.Error("another session's command signs with the caller's key")
	}
	own := &session.State{SessionKey: "parent/session", WorktreePath: t.TempDir()}
	if !signs(IdentityEnv(own)) {
		t.Error("a command in the worker's own session lost its signing")
	}
}

func TestExecOutsideASessionDropsTheCallersHolderSigning(t *testing.T) {
	holderWorkerEnviron(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	_, env, err := resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if signs(env) {
		t.Error("a command outside any session signs with the caller's key")
	}
}
