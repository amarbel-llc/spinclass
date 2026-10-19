package sshsig

import (
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// The fixtures in testdata/ are produced by a real `ssh-keygen -Y sign` (see
// the debug-sshsig-fixtures recipe); nothing here signs with our own encoder.
const fixtureNamespace = "piggy-escalation-v1"

type fixture struct {
	name    string
	sigFile string
	pubFile string
	keyType string
	hash    string
}

var fixtures = []fixture{
	{"ed25519", "ed25519.sig", "ed25519.pub", ssh.KeyAlgoED25519, "sha512"},
	{"ed25519-sha256", "ed25519-sha256.sig", "ed25519.pub", ssh.KeyAlgoED25519, "sha256"},
	{"ecdsa-p256", "ecdsa.sig", "ecdsa.pub", ssh.KeyAlgoECDSA256, "sha512"},
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fixtureKey(t *testing.T, pubFile string) ssh.PublicKey {
	t.Helper()
	k, _, _, _, err := ssh.ParseAuthorizedKey(readFixture(t, pubFile))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// parts is the decoded sshsig blob, re-encodable with mutations.
type parts struct {
	magic    string
	version  uint32
	pk, ns   []byte
	reserved []byte
	hash     []byte
	sig      []byte
	trailing []byte
}

func splitFixture(t *testing.T, sigFile string) parts {
	t.Helper()
	raw, err := dearmor(string(readFixture(t, sigFile)))
	if err != nil {
		t.Fatal(err)
	}
	p := parts{magic: string(raw[:6]), version: binary.BigEndian.Uint32(raw[6:])}
	b := raw[10:]
	for _, dst := range []*[]byte{&p.pk, &p.ns, &p.reserved, &p.hash, &p.sig} {
		*dst, b, err = readString(b)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(b) != 0 {
		t.Fatal("fixture has trailing bytes")
	}
	return p
}

func (p parts) armor() []byte {
	b := []byte(p.magic)
	b = binary.BigEndian.AppendUint32(b, p.version)
	for _, f := range [][]byte{p.pk, p.ns, p.reserved, p.hash, p.sig} {
		b = appendString(b, f)
	}
	b = append(b, p.trailing...)
	enc := base64.StdEncoding.EncodeToString(b)
	var sb strings.Builder
	sb.WriteString(armorBegin + "\n")
	for len(enc) > 70 {
		sb.WriteString(enc[:70] + "\n")
		enc = enc[70:]
	}
	sb.WriteString(enc + "\n" + armorEnd + "\n")
	return []byte(sb.String())
}

func TestRealFixturesVerify(t *testing.T) {
	msg := readFixture(t, "message.txt")
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			key := fixtureKey(t, f.pubFile)
			if err := Verify(readFixture(t, f.sigFile), msg, fixtureNamespace, []ssh.PublicKey{key}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestParseRealFixtures(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			sig, err := Parse(readFixture(t, f.sigFile))
			if err != nil {
				t.Fatal(err)
			}
			if sig.Namespace != fixtureNamespace || sig.HashAlgorithm != f.hash {
				t.Errorf("got namespace %q hash %q", sig.Namespace, sig.HashAlgorithm)
			}
			if sig.PublicKey.Type() != f.keyType || sig.Sig.Format != f.keyType {
				t.Errorf("got key type %q format %q", sig.PublicKey.Type(), sig.Sig.Format)
			}
			if string(sig.PublicKey.Marshal()) != string(fixtureKey(t, f.pubFile).Marshal()) {
				t.Error("parsed key differs from the fixture public key")
			}
		})
	}
}

// Round trip of our parser on the real bytes: splitting and re-encoding must
// reproduce a signature that still verifies.
func TestRealFixtureReencodesAndVerifies(t *testing.T) {
	msg := readFixture(t, "message.txt")
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			again := splitFixture(t, f.sigFile).armor()
			if err := Verify(again, msg, fixtureNamespace, []ssh.PublicKey{fixtureKey(t, f.pubFile)}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVerifyRejectsTamperedMessage(t *testing.T) {
	msg := append(readFixture(t, "message.txt"), '!')
	err := Verify(readFixture(t, "ed25519.sig"), msg, fixtureNamespace, []ssh.PublicKey{fixtureKey(t, "ed25519.pub")})
	wantErr(t, err, "does not verify")
}

func TestVerifyRejectsPinnedNamespaceMismatch(t *testing.T) {
	err := Verify(readFixture(t, "ed25519.sig"), readFixture(t, "message.txt"), "piggy-certificate-v1", []ssh.PublicKey{fixtureKey(t, "ed25519.pub")})
	wantErr(t, err, "namespace mismatch")
	// Exact comparison: a prefix is not a match.
	err = Verify(readFixture(t, "ed25519.sig"), readFixture(t, "message.txt"), "piggy-escalation", []ssh.PublicKey{fixtureKey(t, "ed25519.pub")})
	wantErr(t, err, "namespace mismatch")
}

func TestVerifyRejectsTamperedEmbeddedNamespace(t *testing.T) {
	p := splitFixture(t, "ed25519.sig")
	p.ns = []byte("piggy-certificate-v1")
	// Caller pins the tampered namespace, so only the cryptography can object.
	err := Verify(p.armor(), readFixture(t, "message.txt"), "piggy-certificate-v1", []ssh.PublicKey{fixtureKey(t, "ed25519.pub")})
	wantErr(t, err, "does not verify")
}

func TestVerifyRejectsEmptyPinnedNamespace(t *testing.T) {
	err := Verify(readFixture(t, "ed25519.sig"), readFixture(t, "message.txt"), "", []ssh.PublicKey{fixtureKey(t, "ed25519.pub")})
	wantErr(t, err, "no namespace pinned")
}

func TestVerifyRejectsWrongAllowedKey(t *testing.T) {
	err := Verify(readFixture(t, "ed25519.sig"), readFixture(t, "message.txt"), fixtureNamespace, []ssh.PublicKey{fixtureKey(t, "ecdsa.pub")})
	wantErr(t, err, "not among the allowed keys")
}

func TestVerifyRejectsEmptyAllowed(t *testing.T) {
	err := Verify(readFixture(t, "ed25519.sig"), readFixture(t, "message.txt"), fixtureNamespace, nil)
	wantErr(t, err, "no allowed keys")
	err = Verify(readFixture(t, "ed25519.sig"), readFixture(t, "message.txt"), fixtureNamespace, []ssh.PublicKey{nil})
	wantErr(t, err, "not among the allowed keys")
}

func TestVerifyRejectsFlippedSignatureByte(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			p := splitFixture(t, f.sigFile)
			p.sig = append([]byte(nil), p.sig...)
			p.sig[len(p.sig)-1] ^= 0x01
			err := Verify(p.armor(), readFixture(t, "message.txt"), fixtureNamespace, []ssh.PublicKey{fixtureKey(t, f.pubFile)})
			wantErr(t, err, "does not verify")
		})
	}
}

func TestVerifyDoesNotLeakMessage(t *testing.T) {
	msg := []byte("SECRET-MESSAGE-CONTENT")
	err := Verify(readFixture(t, "ed25519.sig"), msg, fixtureNamespace, []ssh.PublicKey{fixtureKey(t, "ed25519.pub")})
	wantErr(t, err, "does not verify")
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error leaks the message: %v", err)
	}
}

func TestParseRejectsBadArmor(t *testing.T) {
	good := string(readFixture(t, "ed25519.sig"))
	lines := strings.Split(strings.TrimSpace(good), "\n")
	body := strings.Join(lines[1:len(lines)-1], "\n")
	cases := map[string]struct{ in, want string }{
		"empty":          {"", "armor too short"},
		"missing header": {body + "\n" + armorEnd + "\n", "header"},
		"wrong header":   {"-----BEGIN SSH SIGNATURES-----\n" + body + "\n" + armorEnd + "\n", "header"},
		"missing footer": {armorBegin + "\n" + body + "\n", "footer"},
		"wrong footer":   {armorBegin + "\n" + body + "\n-----END SSH SIGNATURES-----\n", "footer"},
		"leading junk":   {"junk\n" + good, "header"},
		"not base64":     {armorBegin + "\n!!!not base64!!!\n" + armorEnd + "\n", "base64"},
		"inner space":    {armorBegin + "\n" + strings.Replace(body, "\n", " \n", 1) + "\n" + armorEnd + "\n", "base64"},
		"empty body":     {armorBegin + "\n\n" + armorEnd + "\n", "magic"},
		"oversize":       {armorBegin + "\n" + strings.Repeat("A", MaxArmoredSize) + "\n" + armorEnd + "\n", "exceeds"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(c.in))
			wantErr(t, err, c.want)
		})
	}
}

func TestParseAcceptsCRLFAndNoTrailingNewline(t *testing.T) {
	good := string(readFixture(t, "ed25519.sig"))
	for name, in := range map[string]string{
		"crlf":       strings.ReplaceAll(good, "\n", "\r\n"),
		"no newline": strings.TrimRight(good, "\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(in)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestParseRejectsBadStructure(t *testing.T) {
	cases := map[string]struct {
		mutate func(*parts)
		want   string
	}{
		"wrong magic":         {func(p *parts) { p.magic = "SSHSIX" }, "bad magic"},
		"version 0":           {func(p *parts) { p.version = 0 }, "version"},
		"version 2":           {func(p *parts) { p.version = 2 }, "version"},
		"trailing bytes":      {func(p *parts) { p.trailing = []byte{0} }, "trailing"},
		"empty namespace":     {func(p *parts) { p.ns = nil }, "empty namespace"},
		"reserved not empty":  {func(p *parts) { p.reserved = []byte("x") }, "reserved"},
		"hash sha1":           {func(p *parts) { p.hash = []byte("sha1") }, "hash algorithm"},
		"hash uppercase":      {func(p *parts) { p.hash = []byte("SHA512") }, "hash algorithm"},
		"empty hash":          {func(p *parts) { p.hash = nil }, "hash algorithm"},
		"garbage public key":  {func(p *parts) { p.pk = []byte{1, 2, 3} }, "public key"},
		"key trailing bytes":  {func(p *parts) { p.pk = append(append([]byte(nil), p.pk...), 0) }, "public key"},
		"truncated signature": {func(p *parts) { p.sig = p.sig[:len(p.sig)-1] }, "signature"},
		"signature trailing":  {func(p *parts) { p.sig = append(append([]byte(nil), p.sig...), 0) }, "trailing"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := splitFixture(t, "ed25519.sig")
			c.mutate(&p)
			_, err := Parse(p.armor())
			wantErr(t, err, c.want)
		})
	}
}

func TestParseRejectsTruncatedBlob(t *testing.T) {
	p := splitFixture(t, "ed25519.sig")
	full := p.armor()
	raw, err := dearmor(string(full))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 3, 6, 9, 10, 13, 20, len(raw) - 1} {
		enc := base64.StdEncoding.EncodeToString(raw[:n])
		if _, err := Parse([]byte(armorBegin + "\n" + enc + "\n" + armorEnd + "\n")); err == nil {
			t.Errorf("truncation to %d bytes was accepted", n)
		}
	}
}

func TestParseRejectsSignatureFormatKeyTypeMismatch(t *testing.T) {
	ed := splitFixture(t, "ed25519.sig")
	ec := splitFixture(t, "ecdsa.sig")
	// ed25519 key carrying an ecdsa-labelled signature, and vice versa.
	ed.sig = ec.sig
	_, err := Parse(ed.armor())
	wantErr(t, err, "does not match key type")

	ec2 := splitFixture(t, "ecdsa.sig")
	ec2.sig = splitFixture(t, "ed25519.sig").sig
	_, err = Parse(ec2.armor())
	wantErr(t, err, "does not match key type")
}

func TestParseRejectsUnsupportedKeyType(t *testing.T) {
	// A well-formed RSA public key (exponent 3, 8-bit modulus: structure only,
	// it is never used for crypto) must be refused on type alone.
	var pk []byte
	pk = appendString(pk, []byte("ssh-rsa"))
	pk = appendString(pk, []byte{3})
	pk = appendString(pk, []byte{0, 0xc5})
	p := splitFixture(t, "ed25519.sig")
	p.pk = pk
	var sig []byte
	sig = appendString(sig, []byte("ssh-rsa"))
	sig = appendString(sig, []byte{1})
	p.sig = sig
	_, err := Parse(p.armor())
	wantErr(t, err, "unsupported key type")
}

func wantErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error containing %q, got nil", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("error %q does not contain %q", err, substr)
	}
}
