// Package sshsig verifies OpenSSH signatures (the armored "SSH SIGNATURE"
// blobs of PROTOCOL.sshsig) in-tree, on top of golang.org/x/crypto/ssh's
// signature verification. FDR 0034 uses it to check card-signed grant records.
//
// The parser is deliberately strict and small: every deviation from the one
// shape the card produces is an error, and every error names the check that
// failed. Errors never include the message that was verified.
package sshsig

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	armorBegin = "-----BEGIN SSH SIGNATURE-----"
	armorEnd   = "-----END SSH SIGNATURE-----"

	// magic is the raw (not length-prefixed) preamble of both the signature
	// blob and the data that is actually signed.
	magic = "SSHSIG"

	sigVersion = 1

	// MaxArmoredSize caps the armored input; a real signature is well under 1 KiB.
	MaxArmoredSize = 16 << 10

	hashSHA256 = "sha256"
	hashSHA512 = "sha512"
)

// Signature is the structural content of an sshsig; Parse performs no crypto.
type Signature struct {
	PublicKey     ssh.PublicKey
	Namespace     string
	HashAlgorithm string // "sha256" or "sha512"
	Sig           *ssh.Signature
}

// supportedKeyType restricts verification to the key types we have reason to
// trust. ssh-rsa/ssh-dss (SHA-1) and the sk- types (extra signature fields
// that Signature cannot carry) are rejected rather than half-supported.
func supportedKeyType(t string) bool {
	switch t {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521:
		return true
	}
	return false
}

// Verify checks that armored is a valid sshsig over message, made under exactly
// the given namespace by one of the allowed public keys.
func Verify(armored []byte, message []byte, namespace string, allowed []ssh.PublicKey) error {
	if namespace == "" {
		return errors.New("sshsig: no namespace pinned by caller")
	}
	if len(allowed) == 0 {
		return errors.New("sshsig: no allowed keys")
	}

	sig, err := Parse(armored)
	if err != nil {
		return err
	}

	// Exact comparison: namespaces separate signing purposes, so no prefix,
	// case-folding or glob semantics.
	if sig.Namespace != namespace {
		return fmt.Errorf("sshsig: namespace mismatch: signature has %q, expected %q", sig.Namespace, namespace)
	}

	if !keyAllowed(sig.PublicKey, allowed) {
		return errors.New("sshsig: signing key is not among the allowed keys")
	}

	digest, err := hashMessage(sig.HashAlgorithm, message)
	if err != nil {
		return err
	}
	if err := sig.PublicKey.Verify(signedData(sig.Namespace, sig.HashAlgorithm, digest), sig.Sig); err != nil {
		return fmt.Errorf("sshsig: signature does not verify: %w", err)
	}
	return nil
}

// keyAllowed compares wire encodings, never key comments or Go identity.
func keyAllowed(key ssh.PublicKey, allowed []ssh.PublicKey) bool {
	want := key.Marshal()
	for _, a := range allowed {
		if a != nil && bytes.Equal(a.Marshal(), want) {
			return true
		}
	}
	return false
}

func hashMessage(alg string, message []byte) ([]byte, error) {
	switch alg {
	case hashSHA256:
		d := sha256.Sum256(message)
		return d[:], nil
	case hashSHA512:
		d := sha512.Sum512(message)
		return d[:], nil
	}
	return nil, fmt.Errorf("sshsig: unsupported hash algorithm %q", alg)
}

// signedData is the byte string the signer actually signed (PROTOCOL.sshsig):
// the raw magic, then namespace, reserved (always empty), hash algorithm and
// the message digest as SSH strings.
func signedData(namespace, hashAlg string, digest []byte) []byte {
	var b []byte
	b = append(b, magic...)
	b = appendString(b, []byte(namespace))
	b = appendString(b, nil)
	b = appendString(b, []byte(hashAlg))
	b = appendString(b, digest)
	return b
}

func appendString(b, s []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}

// Parse decodes the armor and the binary structure. It checks everything that
// can be checked without a message: armor, magic, version, namespace shape,
// reserved, hash algorithm, key type, and that the signature format matches
// the key type. It does not verify the signature.
func Parse(armored []byte) (*Signature, error) {
	if len(armored) > MaxArmoredSize {
		return nil, fmt.Errorf("sshsig: armored signature exceeds %d bytes", MaxArmoredSize)
	}
	raw, err := dearmor(string(armored))
	if err != nil {
		return nil, err
	}
	return parseBlob(raw)
}

func dearmor(s string) ([]byte, error) {
	lines := strings.Split(strings.TrimRight(s, "\r\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	if len(lines) < 3 {
		return nil, errors.New("sshsig: armor too short")
	}
	if lines[0] != armorBegin {
		return nil, errors.New("sshsig: missing or wrong armor header line")
	}
	if lines[len(lines)-1] != armorEnd {
		return nil, errors.New("sshsig: missing or wrong armor footer line")
	}
	// Any stray whitespace or non-alphabet byte in the body fails the strict
	// padded decoder.
	raw, err := base64.StdEncoding.Strict().DecodeString(strings.Join(lines[1:len(lines)-1], ""))
	if err != nil {
		return nil, fmt.Errorf("sshsig: armor body is not valid base64: %w", err)
	}
	return raw, nil
}

func parseBlob(b []byte) (*Signature, error) {
	if len(b) < len(magic) || string(b[:len(magic)]) != magic {
		return nil, errors.New("sshsig: bad magic")
	}
	b = b[len(magic):]
	if len(b) < 4 {
		return nil, errors.New("sshsig: truncated before version")
	}
	if v := binary.BigEndian.Uint32(b); v != sigVersion {
		return nil, fmt.Errorf("sshsig: unsupported version %d", v)
	}
	b = b[4:]

	var pkBlob, ns, reserved, hashAlg, sigBlob []byte
	var err error
	for _, f := range []struct {
		name string
		dst  *[]byte
	}{{"public key", &pkBlob}, {"namespace", &ns}, {"reserved", &reserved}, {"hash algorithm", &hashAlg}, {"signature", &sigBlob}} {
		if *f.dst, b, err = readString(b); err != nil {
			return nil, fmt.Errorf("sshsig: reading %s: %w", f.name, err)
		}
	}
	if len(b) != 0 {
		return nil, fmt.Errorf("sshsig: %d trailing bytes after signature", len(b))
	}

	if len(ns) == 0 {
		return nil, errors.New("sshsig: empty namespace")
	}
	if len(reserved) != 0 {
		return nil, errors.New("sshsig: reserved field is not empty")
	}
	if h := string(hashAlg); h != hashSHA256 && h != hashSHA512 {
		return nil, fmt.Errorf("sshsig: unsupported hash algorithm %q", h)
	}

	key, err := ssh.ParsePublicKey(pkBlob)
	if err != nil {
		return nil, fmt.Errorf("sshsig: parsing public key: %w", err)
	}
	// ParsePublicKey ignores trailing bytes; insist on the canonical encoding.
	if !bytes.Equal(key.Marshal(), pkBlob) {
		return nil, errors.New("sshsig: public key has trailing or non-canonical bytes")
	}
	if !supportedKeyType(key.Type()) {
		return nil, fmt.Errorf("sshsig: unsupported key type %q", key.Type())
	}

	format, rest, err := readString(sigBlob)
	if err != nil {
		return nil, fmt.Errorf("sshsig: reading signature format: %w", err)
	}
	blob, rest, err := readString(rest)
	if err != nil {
		return nil, fmt.Errorf("sshsig: reading signature blob: %w", err)
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("sshsig: %d trailing bytes inside signature", len(rest))
	}
	// Binding format to key type also pins the hash inside the signature
	// scheme (ECDSA curve hash, ed25519), so a signature cannot be re-labelled.
	if string(format) != key.Type() {
		return nil, fmt.Errorf("sshsig: signature format %q does not match key type %q", format, key.Type())
	}

	return &Signature{
		PublicKey:     key,
		Namespace:     string(ns),
		HashAlgorithm: string(hashAlg),
		Sig:           &ssh.Signature{Format: string(format), Blob: blob},
	}, nil
}

// readString consumes one SSH string (uint32 length, then bytes).
func readString(b []byte) (s, rest []byte, err error) {
	if len(b) < 4 {
		return nil, nil, errors.New("truncated length")
	}
	n := binary.BigEndian.Uint32(b)
	b = b[4:]
	if uint64(n) > uint64(len(b)) {
		return nil, nil, errors.New("length exceeds remaining data")
	}
	return b[:n], b[n:], nil
}
