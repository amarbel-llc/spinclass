package spawn

import (
	"bytes"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
)

func lastValue(env []string, name string) string {
	value := ""
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); k == name {
			value = v
		}
	}
	return value
}

// A worker without a holder of its own, spawned from a holder worker, must not
// sign with the parent's key.
func TestASpawnFromAHolderWorkerDropsItsSigning(t *testing.T) {
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
	env := workerEnvScoped(worktree.ResolvedPath{SessionKey: "w/k", AbsPath: t.TempDir()}, "d", nil, nil, true, "")
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); strings.Contains(kv, "/parent/") || keyholder.IsGitConfigPair(k) {
			t.Errorf("the child carries %s", k)
		}
	}
}

// A dotenv is appended after the holder's settings, where its GIT_CONFIG_COUNT
// would replace them.
func TestADotenvCannotTurnAHolderWorkersSigningOff(t *testing.T) {
	var said bytes.Buffer
	prev := holderNotice
	holderNotice = &said
	t.Cleanup(func() { holderNotice = prev })
	kh := map[string]string{"GIT_CONFIG_COUNT": "5", keyholder.GitSessionEnv: "w/k"}
	dotenv := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=a.b", "GIT_CONFIG_VALUE_0=c", "OTHER=kept"}
	rp := worktree.ResolvedPath{SessionKey: "w/k", AbsPath: t.TempDir()}

	env := workerEnvScoped(rp, "d", withKeyholderEnv(nil, kh), dotenv, true, "")
	if lastValue(env, "GIT_CONFIG_COUNT") != "5" || lastValue(env, "OTHER") != "kept" {
		t.Errorf("count %q, OTHER %q", lastValue(env, "GIT_CONFIG_COUNT"), lastValue(env, "OTHER"))
	}
	if !strings.Contains(said.String(), "[direnv.dotenv]") {
		t.Errorf("nothing said about the ignored dotenv: %q", said.String())
	}

	// A [session-entry].env pair beside the holder's is ignored and said too.
	said.Reset()
	got := withKeyholderEnv(map[string]string{"GIT_CONFIG_KEY_7": "a.b"}, kh)
	if _, kept := got["GIT_CONFIG_KEY_7"]; kept || !strings.Contains(said.String(), "[session-entry].env") {
		t.Errorf("entry env: kept=%v, said %q", kept, said.String())
	}

	said.Reset()
	plain := workerEnvScoped(rp, "d", nil, dotenv, true, "")
	if lastValue(plain, "GIT_CONFIG_COUNT") != "1" || said.Len() != 0 {
		t.Error("without a holder the dotenv must be left alone")
	}
}
