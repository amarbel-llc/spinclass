package main

import "testing"

// sc session-key runs ssh-add and ssh-keygen from the build's pins like every
// other tool spinclass shells out to: an absolute store path when the build
// pinned one, the bare name (PATH) only when it did not.
func TestSessionKeyToolsUseTheBuildsPinsAndFallBackToThePathOnlyUnpinned(t *testing.T) {
	prevAdd, prevKeygen := sshAddBin, sshKeygenBin
	t.Cleanup(func() { sshAddBin, sshKeygenBin = prevAdd, prevKeygen })

	sshAddBin, sshKeygenBin = "", ""
	if got := sessionKeySSHAdd(); got != "ssh-add" {
		t.Errorf("unpinned ssh-add = %q, want the bare name", got)
	}
	if got := sessionKeySSHKeygen(); got != "ssh-keygen" {
		t.Errorf("unpinned ssh-keygen = %q, want the bare name", got)
	}

	sshAddBin, sshKeygenBin = "/nix/store/x-openssh/bin/ssh-add", "/nix/store/x-openssh/bin/ssh-keygen"
	if got := sessionKeySSHAdd(); got != sshAddBin {
		t.Errorf("pinned ssh-add = %q, want the pin", got)
	}
	if got := sessionKeySSHKeygen(); got != sshKeygenBin {
		t.Errorf("pinned ssh-keygen = %q, want the pin", got)
	}
}
