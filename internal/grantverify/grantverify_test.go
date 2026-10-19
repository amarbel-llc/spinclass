package grantverify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Copies of internal/sshsig/testdata (a real ssh-keygen -Y sign over
// message.txt under piggy-escalation-v1); the test sandbox sees only a
// package's own testdata.
const fixtures = "testdata/"

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(fixtures + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func key(t *testing.T, name string) ssh.PublicKey {
	t.Helper()
	k, _, _, _, err := ssh.ParseAuthorizedKey(read(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestRootVerifierAcceptsGenuineRejectsTamperedAndForeign(t *testing.T) {
	message := string(read(t, "message.txt"))
	sig := read(t, "ed25519.sig")
	verify := NewRootVerifier([]ssh.PublicKey{key(t, "ed25519.pub")})

	if err := verify(message, sig); err != nil {
		t.Fatalf("genuine signature rejected: %v", err)
	}
	if err := verify(message+"extra\n", sig); err == nil {
		t.Fatal("tampered act text accepted")
	}
	if err := verify(message, nil); err == nil {
		t.Fatal("unsigned record accepted")
	}
	other := NewRootVerifier([]ssh.PublicKey{key(t, "ecdsa.pub")})
	if err := other(message, sig); err == nil {
		t.Fatal("signature by a different key accepted")
	}
	if err := NewRootVerifier(nil)(message, sig); err == nil {
		t.Fatal("empty key list accepted")
	}
}

func writeSigners(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "signers")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadSigners(t *testing.T) {
	pub := strings.TrimSpace(string(read(t, "ed25519.pub")))
	path := writeSigners(t, "# card\noperator namespaces=\"piggy-escalation-v1\" "+pub+"\n")
	keys, err := LoadSigners(path)
	if err != nil || len(keys) != 1 {
		t.Fatalf("got %v, %v", keys, err)
	}
	if string(keys[0].Marshal()) != string(key(t, "ed25519.pub").Marshal()) {
		t.Fatal("wrong key loaded")
	}
}

func TestLoadSignersFailsClosed(t *testing.T) {
	pub := strings.TrimSpace(string(read(t, "ed25519.pub")))
	missing := filepath.Join(t.TempDir(), "absent")
	cases := map[string]string{
		missing:                              "",
		writeSigners(t, ""):                  "no key for principal",
		writeSigners(t, "someone "+pub+"\n"): "no key for principal",
		writeSigners(t, "operator namespaces=\"git\" "+pub+"\n"): "no key for principal",
		writeSigners(t, "operator cert-authority "+pub+"\n"):     "unsupported option",
		writeSigners(t, "operator not-a-key\n"):                  "parsing key",
	}
	for path, want := range cases {
		keys, err := LoadSigners(path)
		if err == nil || keys != nil {
			t.Fatalf("%s: got %v, %v; want error", path, keys, err)
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("error does not name the path: %v", err)
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %q lacks %q", path, err, want)
		}
	}
}

func TestDefaultSignersPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got := DefaultSignersPath(); got != "/xdg/spinclass/profile-grant-signers" {
		t.Fatalf("got %s", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/op")
	if got := DefaultSignersPath(); got != "/home/op/.config/spinclass/profile-grant-signers" {
		t.Fatalf("got %s", got)
	}
}
