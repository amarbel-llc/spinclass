package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// gitSettings reads the git settings a launch environment carries.
func gitSettings(t *testing.T, env map[string]string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for i := 0; ; i++ {
		k, ok := env["GIT_CONFIG_KEY_"+string(rune('0'+i))]
		if !ok || i > 8 {
			return out
		}
		out[k] = env["GIT_CONFIG_VALUE_"+string(rune('0'+i))]
	}
}

// Every holder launch (spawn, start, resume, start hand-off) takes its
// environment from here, so this is where a worker gets its signing settings.
func TestAHolderLaunchEnvironmentMakesAPlainCommitSignWithTheHolder(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "")
	home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n")
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	signers := filepath.Join(home, "existing_signers")
	if err := os.WriteFile(signers, []byte("them@example.invalid ssh-ed25519 AAAAEXISTING\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "config", "gpg.ssh.allowedSignersFile", signers).CombinedOutput(); err != nil {
		t.Fatalf("git config: %v: %s", err, out)
	}
	f := newKhFake(t)
	khUseFake(t, f)

	env, err := keyholderSessionEnv(home, repo, "repo/signer", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	got := gitSettings(t, env)
	for k, want := range map[string]string{"gpg.format": "ssh", "commit.gpgsign": "true"} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	// A file, not the key itself: a re-mint rewrites it under a running worker.
	signingKey, err := os.ReadFile(got["user.signingkey"])
	if err != nil || string(signingKey) != "ssh-ed25519 AAAAC3NzaFAKEAAA\n" {
		t.Errorf("user.signingkey = %q holds %q (%v)", got["user.signingkey"], signingKey, err)
	}
	script, err := os.ReadFile(got["gpg.ssh.program"])
	if err != nil || !bytes.Contains(script, []byte("'/pin/ssh-keygen'")) {
		t.Errorf("the signing program does not run the pinned ssh-keygen (%d bytes, %v)", len(script), err)
	}
	allowed, err := os.ReadFile(got["gpg.ssh.allowedSignersFile"])
	if err != nil || !bytes.Contains(allowed, []byte("AAAAC3NzaFAKEAAA")) || !bytes.Contains(allowed, []byte("AAAAEXISTING")) {
		t.Errorf("allowed signers lack the holder's key or the configured file's (%d bytes, %v)", len(allowed), err)
	}
	if env[keyholder.GitSessionEnv] != "repo/signer" || env[keyholder.GitAgentEnv] != env["SSH_AUTH_SOCK"] {
		t.Errorf("session %q, agent %q, SSH_AUTH_SOCK %q", env[keyholder.GitSessionEnv], env[keyholder.GitAgentEnv], env["SSH_AUTH_SOCK"])
	}
}

// A launch whose signing cannot be set up is refused, and stops the pair it
// had just started.
func TestAHolderLaunchThatCannotSetUpSigningStopsItsPair(t *testing.T) {
	home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n")
	f := newKhFake(t)
	khUseFake(t, f)
	t.Cleanup(keyholder.OverrideSSHPins("/pin/ssh-add", ""))

	_, err := keyholderSessionEnv(home, repo, "repo/unsigned", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "setting up commit signing") || !strings.Contains(err.Error(), "next: run `sc keyholder-selftest`") {
		t.Fatalf("err = %v", err)
	}
	if len(f.started) != 2 {
		t.Fatalf("started %v: the refusal came before the pair, the test proves nothing about its stop", f.started)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for unit, on := range f.active {
		if on {
			t.Errorf("%s was left running", unit)
		}
	}
}
