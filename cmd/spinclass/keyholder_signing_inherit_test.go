package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// selftestAgent is a real ssh-agent holding one throwaway key, standing in for a
// pair's holder, and the pair that names it.
func selftestAgent(t *testing.T) (*keyholder.Launcher, keyholder.Pair) {
	t.Helper()
	tools := map[string]string{}
	for _, name := range []string{"ssh-agent", "ssh-add", "ssh-keygen", "git"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("%s is not on PATH: %v", name, err)
		}
		tools[name] = path
	}
	t.Cleanup(keyholder.OverrideSSHPins(tools["ssh-add"], tools["ssh-keygen"]))
	// Short: an agent socket path has a 107-byte limit.
	root, err := os.MkdirTemp("", "khc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	run := func(env []string, name string, args ...string) {
		cmd := exec.Command(tools[name], args...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", name, err, out)
		}
	}
	key := filepath.Join(root, "holder")
	run(nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key)
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(root, "agent.sock")
	agent := exec.Command(tools["ssh-agent"], "-D", "-a", sock)
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Process.Kill(); _, _ = agent.Process.Wait() })
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(sock); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatal("ssh-agent never made its socket")
		}
	}
	run([]string{"SSH_AUTH_SOCK=" + sock}, "ssh-add", key)
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	pairDir := filepath.Join(root, "state", "keyholder", "h")
	if err := os.MkdirAll(pairDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return &keyholder.Launcher{Base: filepath.Dir(pairDir)}, keyholder.Pair{
		Session: "selftest/x", Dir: pairDir, AgentSock: sock, PublicKey: strings.TrimSpace(string(pub)),
	}
}

func TestTheSelftestCommitStepSignsAndVerifiesForReal(t *testing.T) {
	l, pair := selftestAgent(t)
	if err := signedCommitWithHolder(context.Background(), l, pair); err != nil {
		t.Fatalf("a working holder: %v", err)
	}

	// A holder that lacks the pair's key cannot make the commit.
	_, other := selftestAgent(t)
	wrong := pair
	wrong.PublicKey = other.PublicKey
	if err := signedCommitWithHolder(context.Background(), l, wrong); err == nil {
		t.Error("the step passed though the holder does not hold the pair's key")
	}
}

// A commit that is made but not signed is a failure: only the verification sees
// it.
func TestTheSelftestCommitStepRefusesAnUnsignedCommit(t *testing.T) {
	l, pair := selftestAgent(t)
	prev := selftestGitSigningEnv
	t.Cleanup(func() { selftestGitSigningEnv = prev })
	selftestGitSigningEnv = func(base, session, keygen string, p keyholder.Pair, environ []string, signers []byte) (map[string]string, error) {
		env, err := prev(base, session, keygen, p, environ, signers)
		for i := range 8 {
			n := string(rune('0' + i))
			if env["GIT_CONFIG_KEY_"+n] == "commit.gpgsign" {
				env["GIT_CONFIG_VALUE_"+n] = "false"
			}
		}
		return env, err
	}
	err := signedCommitWithHolder(context.Background(), l, pair)
	if err == nil || !strings.Contains(err.Error(), "verify-commit") {
		t.Errorf("an unsigned commit passed: %v", err)
	}
}

// parentHolderEnv makes this process look like a key holder worker of
// parent/session, with a git setting of the user's own beside the holder's.
func parentHolderEnv(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_COUNT": "6",
		"GIT_CONFIG_KEY_0": "core.abbrev", "GIT_CONFIG_VALUE_0": "12",
		"GIT_CONFIG_KEY_1": "gpg.format", "GIT_CONFIG_VALUE_1": "ssh",
		"GIT_CONFIG_KEY_2": "user.signingkey", "GIT_CONFIG_VALUE_2": "/parent/signing_key.pub",
		"GIT_CONFIG_KEY_3": "commit.gpgsign", "GIT_CONFIG_VALUE_3": "true",
		"GIT_CONFIG_KEY_4": "gpg.ssh.program", "GIT_CONFIG_VALUE_4": "/parent/keyholder-git-sign",
		"GIT_CONFIG_KEY_5": "gpg.ssh.allowedSignersFile", "GIT_CONFIG_VALUE_5": "/parent/allowed_signers",
		keyholder.GitSessionEnv: "parent/session", keyholder.GitAgentEnv: "/parent/agent.sock",
	} {
		t.Setenv(k, v)
	}
}

// The restart hand-off, and the start hand-off with the switch off, inherit the
// caller's environment.
func TestAnInheritingHandoffDropsTheCallersHolderSigning(t *testing.T) {
	parentHolderEnv(t)
	env, err := exec.LookPath("env")
	if err != nil {
		t.Fatal(err)
	}
	out, err := runRestartHandoff(t.TempDir(), []string{env})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "/parent/") {
		t.Error("the hand-off ran with the caller's signing settings")
	}
	if !strings.Contains(out, "GIT_CONFIG_COUNT=1\n") && !strings.HasSuffix(out, "GIT_CONFIG_COUNT=1") {
		t.Error("the caller's own setting was not kept alone")
	}
}

func TestTheOperatorsSignersAreNotReadThroughAHolderWorkersSettings(t *testing.T) {
	repo := t.TempDir()
	operators := filepath.Join(repo, "operator_signers")
	generated := filepath.Join(repo, "generated_signers")
	for path, content := range map[string]string{operators: "operator\n", generated: "generated\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "config", "gpg.ssh.allowedSignersFile", operators}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	parentHolderEnv(t)
	t.Setenv("GIT_CONFIG_VALUE_5", generated)
	if got := string(configuredAllowedSigners(repo)); got != "operator\n" {
		t.Errorf("read %q, want the operator's file", got)
	}
}
