// Package sshsigtest produces OpenSSH signatures (PROTOCOL.sshsig) from a
// throwaway ed25519 key, for tests that need a "card" without ssh-keygen. It is
// test support only: nothing in the production binary imports it.
package sshsigtest

import (
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	armorBegin = "-----BEGIN SSH SIGNATURE-----"
	armorEnd   = "-----END SSH SIGNATURE-----"
	armorWidth = 70 // ssh-keygen wraps the base64 body at 70 columns
)

// Sign returns the armored sshsig that `ssh-keygen -Y sign -n namespace` would
// produce over message with priv: sha512 message hash, empty reserved field.
func Sign(priv ed25519.PrivateKey, namespace string, message []byte) []byte {
	pub := AuthorizedKey(priv.Public().(ed25519.PublicKey))
	digest := sha512.Sum512(message)

	// What is actually signed: raw magic, then the strings below.
	var signed []byte
	signed = append(signed, "SSHSIG"...)
	signed = appendString(signed, []byte(namespace))
	signed = appendString(signed, nil)
	signed = appendString(signed, []byte("sha512"))
	signed = appendString(signed, digest[:])

	var sigBlob []byte
	sigBlob = appendString(sigBlob, []byte(ssh.KeyAlgoED25519))
	sigBlob = appendString(sigBlob, ed25519.Sign(priv, signed))

	var blob []byte
	blob = append(blob, "SSHSIG"...)
	blob = binary.BigEndian.AppendUint32(blob, 1)
	blob = appendString(blob, pub.Marshal())
	blob = appendString(blob, []byte(namespace))
	blob = appendString(blob, nil)
	blob = appendString(blob, []byte("sha512"))
	blob = appendString(blob, sigBlob)

	body := base64.StdEncoding.EncodeToString(blob)
	var b strings.Builder
	b.WriteString(armorBegin + "\n")
	for len(body) > armorWidth {
		b.WriteString(body[:armorWidth] + "\n")
		body = body[armorWidth:]
	}
	b.WriteString(body + "\n")
	b.WriteString(armorEnd + "\n")
	return []byte(b.String())
}

// AuthorizedKey is pub as the ssh.PublicKey a signers list (and
// grantverify.NewRootVerifier) takes.
func AuthorizedKey(pub ed25519.PublicKey) ssh.PublicKey {
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		// Only a wrong-length key can fail, which is a test bug.
		panic("sshsigtest: " + err.Error())
	}
	return k
}

func appendString(b, s []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}
