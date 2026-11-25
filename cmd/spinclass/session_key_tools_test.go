package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// sc session-key runs ssh-add and ssh-keygen from the build's pins and from
// nowhere else: an absolute store path when the build pinned one, a refusal
// naming the pin when it did not. The key holder never looks a tool up on PATH.
func TestSessionKeyToolsUseTheBuildsPinsAndRefuseWhenUnpinned(t *testing.T) {
	prevAdd, prevKeygen := sshAddBin, sshKeygenBin
	t.Cleanup(func() { sshAddBin, sshKeygenBin = prevAdd, prevKeygen })

	sshAddBin, sshKeygenBin = "", ""
	if got, err := sessionKeySSHAdd(); !errors.Is(err, errSessionKeyToolUnpinned) || got != "" {
		t.Errorf("unpinned ssh-add = %q, %v, want a refusal naming the pin", got, err)
	}
	if got, err := sessionKeySSHKeygen(); !errors.Is(err, errSessionKeyToolUnpinned) || got != "" {
		t.Errorf("unpinned ssh-keygen = %q, %v, want a refusal naming the pin", got, err)
	}

	sshAddBin, sshKeygenBin = "/nix/store/x-openssh/bin/ssh-add", "/nix/store/x-openssh/bin/ssh-keygen"
	if got, err := sessionKeySSHAdd(); err != nil || got != sshAddBin {
		t.Errorf("pinned ssh-add = %q, %v, want the pin", got, err)
	}
	if got, err := sessionKeySSHKeygen(); err != nil || got != sshKeygenBin {
		t.Errorf("pinned ssh-keygen = %q, %v, want the pin", got, err)
	}
}

// An unpinned build must not run a program of that name found on PATH, through
// any of the three call sites that shell out.
func TestSessionKeyNeverRunsAToolFoundOnPATHWhenUnpinned(t *testing.T) {
	prevAdd, prevKeygen := sshAddBin, sshKeygenBin
	t.Cleanup(func() { sshAddBin, sshKeygenBin = prevAdd, prevKeygen })
	sshAddBin, sshKeygenBin = "", ""

	poisoned := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	for _, name := range []string{"ssh-add", "ssh-keygen"} {
		script := "#!/bin/sh\necho " + name + " >> '" + marker + "'\nexit 0\n"
		if err := os.WriteFile(filepath.Join(poisoned, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", poisoned+":"+os.Getenv("PATH"))

	ctx := context.Background()
	if _, err := sessionKeyAgentList(ctx, "/nonexistent/agent.sock"); !errors.Is(err, errSessionKeyToolUnpinned) {
		t.Errorf("sessionKeyAgentList unpinned = %v, want the unpinned refusal", err)
	}
	if _, err := sessionKeyFingerprint(ctx, "ssh-ed25519 AAAA"); !errors.Is(err, errSessionKeyToolUnpinned) {
		t.Errorf("sessionKeyFingerprint unpinned = %v, want the unpinned refusal", err)
	}
	if _, err := sessionKeyCertList(ctx, []byte("x")); !errors.Is(err, errSessionKeyToolUnpinned) {
		t.Errorf("sessionKeyCertList unpinned = %v, want the unpinned refusal", err)
	}
	if _, err := os.Stat(marker); err == nil {
		b, _ := os.ReadFile(marker)
		t.Errorf("a program found on PATH ran: %q", string(b))
	}
}
