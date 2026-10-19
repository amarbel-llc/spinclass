package sshsigtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"code.linenisgreat.com/spinclass/internal/sshsig"
	"golang.org/x/crypto/ssh"
)

const ns = "piggy-escalation-v1"

func TestSignVerifiesWithTheProductionVerifier(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("allow x to spawn\n")
	armored := Sign(priv, ns, msg)
	keys := []ssh.PublicKey{AuthorizedKey(pub)}

	if err := sshsig.Verify(armored, msg, ns, keys); err != nil {
		t.Fatalf("own signature rejected: %v", err)
	}
	parsed, err := sshsig.Parse(armored)
	if err != nil || parsed.HashAlgorithm != "sha512" {
		t.Fatalf("parse: %v, %+v", err, parsed)
	}
	if err := sshsig.Verify(armored, append(msg, 'x'), ns, keys); err == nil {
		t.Error("tampered message accepted")
	}
	if err := sshsig.Verify(armored, msg, "other-ns", keys); err == nil {
		t.Error("wrong namespace accepted")
	}
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := sshsig.Verify(armored, msg, ns, []ssh.PublicKey{AuthorizedKey(otherPub)}); err == nil {
		t.Error("foreign key accepted")
	}
}
