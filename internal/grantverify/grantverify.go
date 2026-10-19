// Package grantverify wires the in-tree sshsig verifier to the operator's
// allowed-signers file, producing the root verifier profilegrant.NewVerifier
// takes (FDR 0034, Phase 2). It fails closed: no signers file, or one with no
// operator key, verifies nothing.
package grantverify

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"code.linenisgreat.com/spinclass/internal/operatoract"
	"code.linenisgreat.com/spinclass/internal/sshsig"
	"golang.org/x/crypto/ssh"
)

// Principal is the allowed-signers principal that names the operator's card key.
const Principal = "operator"

// NewRootVerifier returns a function that accepts a root record's signature iff
// it is an sshsig over exactly actText, under piggy-escalation-v1, by one of keys.
func NewRootVerifier(keys []ssh.PublicKey) func(actText string, sig []byte) error {
	return func(actText string, sig []byte) error {
		if len(sig) == 0 {
			return errors.New("root record carries no signature")
		}
		return sshsig.Verify(sig, []byte(actText), operatoract.NamespaceEscalation, keys)
	}
}

// LoadSigners reads an allowed-signers file and returns the keys it grants to
// Principal for the escalation namespace. A missing or unusable file is an
// error naming path, never an empty (and so vacuously failing) key list.
func LoadSigners(path string) ([]ssh.PublicKey, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("profile-grant signers file %s: %w", path, err)
	}
	defer f.Close()
	signers, err := sshsig.ParseAllowedSigners(f)
	if err != nil {
		return nil, fmt.Errorf("profile-grant signers file %s: %w", path, err)
	}
	keys := signers.KeysFor(Principal, operatoract.NamespaceEscalation)
	if len(keys) == 0 {
		return nil, fmt.Errorf("profile-grant signers file %s: no key for principal %q in namespace %s",
			path, Principal, operatoract.NamespaceEscalation)
	}
	return keys, nil
}

// DefaultSignersPath is $XDG_CONFIG_HOME/spinclass/profile-grant-signers,
// falling back to ~/.config/spinclass/profile-grant-signers.
func DefaultSignersPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "spinclass", "profile-grant-signers")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Relative on purpose: it will not exist, and LoadSigners then fails
		// closed naming this path.
		return filepath.Join(".config", "spinclass", "profile-grant-signers")
	}
	return filepath.Join(home, ".config", "spinclass", "profile-grant-signers")
}
