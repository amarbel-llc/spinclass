package keyholder

// The build's pinned ssh-add and ssh-keygen, beside the systemd pins
// (systempins.go) and under the same rule: never a name looked up on PATH.
var pinSSHAdd, pinSSHKeygen string

// SetSSHPins records the build's pinned absolute paths of ssh-add and
// ssh-keygen. An empty value leaves that tool unpinned.
func SetSSHPins(add, keygen string) {
	pinMu.Lock()
	defer pinMu.Unlock()
	pinSSHAdd, pinSSHKeygen = add, keygen
}

// OverrideSSHPins replaces the pins and returns a function that restores them.
// It exists for tests.
func OverrideSSHPins(add, keygen string) (restore func()) {
	pinMu.Lock()
	prevAdd, prevKeygen := pinSSHAdd, pinSSHKeygen
	pinSSHAdd, pinSSHKeygen = add, keygen
	pinMu.Unlock()
	return func() { SetSSHPins(prevAdd, prevKeygen) }
}

// SSHAddPin returns the pinned ssh-add.
func SSHAddPin() (string, error) {
	pinMu.RLock()
	defer pinMu.RUnlock()
	return resolveTool("ssh-add", "", pinSSHAdd)
}

// SSHKeygenPin returns the pinned ssh-keygen.
func SSHKeygenPin() (string, error) {
	pinMu.RLock()
	defer pinMu.RUnlock()
	return resolveTool("ssh-keygen", "", pinSSHKeygen)
}
