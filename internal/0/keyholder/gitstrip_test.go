package keyholder

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// replaceHolderKey makes the rig's agent hold a new key instead, as a lost pair
// replaced by a new one does: same socket, same pair directory, another key.
func (r *signingRig) replaceHolderKey() string {
	r.t.Helper()
	path, pub := newKey(r.t, r.keygen, r.home, "replacement")
	for _, args := range [][]string{{"-D"}, {path}} {
		cmd := exec.Command(tool(r.t, "ssh-add"), args...)
		cmd.Env = append(slices.Clone(r.operator), "SSH_AUTH_SOCK="+r.pair.AgentSock)
		if out, err := cmd.CombinedOutput(); err != nil {
			r.t.Fatalf("ssh-add %v: %v: %s", args, err, out)
		}
	}
	if err := os.Remove(path); err != nil {
		r.t.Fatal(err)
	}
	r.pair.PublicKey = pub
	return pub
}

// A restart keeps the worker's environment while a lost pair comes back with a
// new key (H1): the same environment must sign with the new one.
func TestAWorkerKeepsSigningAfterItsPairIsReplaced(t *testing.T) {
	r := newSigningRig(t)
	worker := r.worker(nil)
	oldPub := r.pair.PublicKey
	r.mustGit(worker, r.wt, "commit", "-q", "--allow-empty", "-m", "before")

	newPub := r.replaceHolderKey()
	// What the re-mint path runs (keyholderSessionEnvUndo): the writer, whose
	// returned environment is thrown away.
	if _, err := GitSigningEnv(r.base, "repo/w", r.keygen, r.pair, r.operator, nil); err != nil {
		t.Fatal(err)
	}

	if out, err := r.git(worker, r.wt, "commit", "-q", "--allow-empty", "-m", "after"); err != nil {
		t.Fatalf("the same environment cannot commit after the pair was replaced: %v: %s", err, out)
	}
	if !r.verifiesAgainst(newPub) || r.verifiesAgainst(oldPub) {
		t.Error("the commit is not signed by the new key")
	}
	if out, err := r.git(worker, r.wt, "verify-commit", "HEAD"); err != nil {
		t.Errorf("verify-commit in the worker after the replacement: %v: %s", err, out)
	}
}

func envValue(env []string, name string) (string, bool) {
	value, found := "", false
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); k == name {
			value, found = v, true
		}
	}
	return value, found
}

// A launch made from inside a holder worker must not carry its signing (M1),
// and must keep the user's own settings, before and after the holder's.
func TestStripGitSigningRemovesOnlyTheHoldersSettings(t *testing.T) {
	r := newSigningRig(t)
	user := []string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=gpg.format", "GIT_CONFIG_VALUE_0=openpgp", "GIT_CONFIG_KEY_1=core.abbrev", "GIT_CONFIG_VALUE_1=12"}
	worker := r.worker(nil, user...)
	// Something in the worker appends a setting of its own after the holder's.
	worker = append(worker, "GIT_CONFIG_COUNT=8", "GIT_CONFIG_KEY_7=core.autocrlf", "GIT_CONFIG_VALUE_7=input")

	stripped := StripGitSigning(worker)
	for name, want := range map[string]string{
		"GIT_CONFIG_COUNT": "3", "GIT_CONFIG_KEY_0": "gpg.format", "GIT_CONFIG_VALUE_0": "openpgp",
		"GIT_CONFIG_KEY_1": "core.abbrev", "GIT_CONFIG_KEY_2": "core.autocrlf", "GIT_CONFIG_VALUE_2": "input",
	} {
		if got, _ := envValue(stripped, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{GitSessionEnv, GitAgentEnv, "GIT_CONFIG_KEY_3", "GIT_CONFIG_VALUE_7"} {
		if _, ok := envValue(stripped, name); ok {
			t.Errorf("%s survived the strip", name)
		}
	}
	r.mustGit(stripped, r.wt, "commit", "-q", "--allow-empty", "-m", "child")
	if r.signed(stripped, "HEAD") {
		t.Error("a child without a holder still signs")
	}

	// A holder with nothing inherited leaves no GIT_CONFIG at all.
	bare := StripGitSigning(r.worker(nil))
	if _, ok := envValue(bare, "GIT_CONFIG_COUNT"); ok {
		t.Error("GIT_CONFIG_COUNT survived with nothing under it")
	}

	// Nobody's holder: the same settings by name are the user's and stay.
	own := []string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=gpg.format", "GIT_CONFIG_VALUE_0=ssh", "GIT_CONFIG_KEY_1=gpg.ssh.program", "GIT_CONFIG_VALUE_1=" + filepath.Join(r.home, "my-signer")}
	if got := StripGitSigning(own); !slices.Equal(got, own) {
		t.Errorf("a user's own settings were changed: %d entries, want %d", len(got), len(own))
	}
}

func TestConfiguredGitConfigDoesNotTurnSigningOff(t *testing.T) {
	configured := map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "a.b", "GIT_CONFIG_VALUE_0": "c", "GIT_CONFIG_GLOBAL": "/x", "OTHER": "y"}
	if notice := DropConfiguredGitConfig(configured, map[string]string{"GIT_CONFIG_COUNT": "1"}); notice != "" || len(configured) != 5 {
		t.Errorf("without a holder's signing nothing may be dropped (%d left, %q)", len(configured), notice)
	}
	notice := DropConfiguredGitConfig(configured, map[string]string{GitSessionEnv: "repo/w"})
	if !strings.Contains(notice, "[direnv.dotenv]") || len(configured) != 2 || configured["GIT_CONFIG_GLOBAL"] != "/x" {
		t.Errorf("left %d entries, notice %q", len(configured), notice)
	}
}
