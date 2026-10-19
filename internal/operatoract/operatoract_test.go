package operatoract

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/sshsig"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestEncodeRequestGolden(t *testing.T) {
	// Built by hand from RFC 0012 2.1: one outer string around
	// version, key, namespace, hash_algorithm, text, flags.
	want := "00000026" + // outer string length (38)
		"00000001" + // version
		"000000034b4559" + // key "KEY"
		"000000026e73" + // namespace "ns"
		"00000006736861323536" + // hash "sha256"
		"0000000368690a" + // text "hi\n"
		"00000000" // flags
	got := hex.EncodeToString(encodeRequest([]byte("KEY"), "ns", "sha256", []byte("hi\n")))
	if got != want {
		t.Fatalf("request payload\n got %s\nwant %s", got, want)
	}
}

func TestLocalPrechecks(t *testing.T) {
	cases := []struct {
		name, namespace, text string
		want                  any
	}{
		{"empty", NamespaceEscalation, "", new(*RefusedTextError)},
		{"oversize", NamespaceEscalation, strings.Repeat("a", MaxTextBytes+1), new(*RefusedTextError)},
		{"not utf8", NamespaceEscalation, "a\xffb", new(*RefusedTextError)},
		{"escape", NamespaceEscalation, "a\x1b[2Jb", new(*RefusedTextError)},
		{"carriage return", NamespaceEscalation, "a\r\n", new(*RefusedTextError)},
		{"c1 control", NamespaceEscalation, "a\u0085b", new(*RefusedTextError)},
		{"del", NamespaceEscalation, "a\x7fb", new(*RefusedTextError)},
		{"bad namespace", "git", "ok\n", new(*RefusedNamespaceError)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dialed := false
			s := &AgentSigner{dial: func(context.Context) (net.Conn, error) { dialed = true; return nil, errors.New("must not dial") }}
			_, err := s.Sign(context.Background(), c.namespace, c.text)
			if err == nil || !errors.As(err, c.want) {
				t.Fatalf("got %v, want %T", err, c.want)
			}
			if dialed {
				t.Fatal("a locally refused request must not reach the agent")
			}
		})
	}
	if err := checkLocally(NamespaceEscalation, strings.Repeat("a", MaxTextBytes)+""); err != nil {
		t.Fatalf("a text of exactly the limit is allowed: %v", err)
	}
	if err := checkLocally(NamespaceEscalation, "two\nlines\tand tab ✓"); err != nil {
		t.Fatalf("newline, tab and non-ASCII are allowed: %v", err)
	}
}

// fakeAgent is a keyring holding one PIV_slot_9C identity whose extension
// handler is supplied by the test.
type fakeAgent struct {
	agent.ExtendedAgent
	ext func(name string, contents []byte) ([]byte, error)
}

func (f *fakeAgent) Extension(name string, contents []byte) ([]byte, error) {
	return f.ext(name, contents)
}

func keyringWith(t *testing.T, comments ...string) (agent.ExtendedAgent, [][]byte) {
	t.Helper()
	kr := agent.NewKeyring().(agent.ExtendedAgent)
	var blobs [][]byte
	for _, c := range comments {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := kr.Add(agent.AddedKey{PrivateKey: priv, Comment: c}); err != nil {
			t.Fatal(err)
		}
		signer, _ := ssh.NewSignerFromKey(priv)
		blobs = append(blobs, signer.PublicKey().Marshal())
	}
	return kr, blobs
}

// serve runs ag on a unix socket in a temp dir and returns a signer that dials it.
func serve(t *testing.T, ag agent.Agent) *AgentSigner {
	t.Helper()
	// A short path: unix socket paths are limited to ~100 bytes.
	dir, err := os.MkdirTemp("", "oa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _ = agent.ServeAgent(ag, c) }()
		}
	}()
	return &AgentSigner{
		Slot: DefaultSlot,
		dial: func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}
}

func str(b []byte) []byte { return appendString(nil, b) }

func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

// reply builds the extension response of RFC 0012 2.2 as a whole message body.
func reply(status uint32, sshsigBlob []byte, detail string) []byte {
	contents := append(u32(1), u32(status)...)
	contents = append(contents, str(sshsigBlob)...)
	contents = append(contents, str([]byte(detail))...)
	out := []byte{extensionReplyID}
	out = append(out, str([]byte(extensionName))...)
	return append(out, str(contents)...)
}

func fixtureBlob(t *testing.T) (blob []byte, armored []byte) {
	t.Helper()
	armored, err := os.ReadFile("testdata/ed25519.sig")
	if err != nil {
		t.Fatal(err)
	}
	var b64 strings.Builder
	for _, l := range strings.Split(string(armored), "\n") {
		if l != "" && !strings.HasPrefix(l, "-----") {
			b64.WriteString(l)
		}
	}
	blob, err = base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		t.Fatal(err)
	}
	return blob, armored
}

func TestSignSuccessRoundTripsThroughArmor(t *testing.T) {
	blob, _ := fixtureBlob(t)
	kr, blobs := keyringWith(t, "PIV_slot_9A 0A1B2C3D", "PIV_slot_9C 0A1B2C3D")
	var seen []byte
	ag := &fakeAgent{ExtendedAgent: kr, ext: func(name string, contents []byte) ([]byte, error) {
		if name != extensionName {
			t.Errorf("extension name %q", name)
		}
		seen = contents
		return reply(0, blob, ""), nil
	}}
	s := serve(t, ag)

	message, err := os.ReadFile("testdata/message.txt")
	if err != nil {
		t.Fatal(err)
	}
	armored, err := s.Sign(context.Background(), NamespaceEscalation, string(message))
	if err != nil {
		t.Fatal(err)
	}

	// The request named the 9C key (not 9A) and carried the exact text.
	want := encodeRequest(blobs[1], NamespaceEscalation, requestHash, message)
	if !bytes.Equal(seen, want) {
		t.Fatalf("request contents\n got %x\nwant %x", seen, want)
	}
	// The armored output is what the in-tree verifier accepts.
	pub, _, _, _, err := ssh.ParseAuthorizedKey(mustRead(t, "testdata/ed25519.pub"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sshsig.Verify(armored, message, NamespaceEscalation, []ssh.PublicKey{pub}); err != nil {
		t.Fatalf("armored output does not verify: %v", err)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSignMapsEveryStatusToItsOwnType(t *testing.T) {
	cases := []struct {
		status uint32
		want   any
	}{
		{1, new(*NoSuchKeyError)},
		{2, new(*RefusedPolicyError)},
		{3, new(*RefusedNamespaceError)},
		{4, new(*RefusedTextError)},
		{5, new(*CancelledError)},
		{6, new(*CardAbsentError)},
		{7, new(*BadRequestError)},
		{8, new(*AgentError)},
		{99, new(*UnknownStatusError)},
	}
	for _, c := range cases {
		kr, _ := keyringWith(t, "PIV_slot_9C 0A1B2C3D")
		ag := &fakeAgent{ExtendedAgent: kr, ext: func(string, []byte) ([]byte, error) {
			return reply(c.status, nil, "slot 9C: PIN once, touch cached"), nil
		}}
		_, err := serve(t, ag).Sign(context.Background(), NamespaceEscalation, "ok\n")
		if err == nil || !errors.As(err, c.want) {
			t.Fatalf("status %d: got %T %v, want %T", c.status, err, err, c.want)
		}
		if !strings.Contains(err.Error(), "touch cached") {
			t.Fatalf("status %d: detail lost: %v", c.status, err)
		}
		var ns *NotSupportedError
		if errors.As(err, &ns) {
			t.Fatalf("status %d must not look like an unsupported agent", c.status)
		}
	}
}

func TestSignRejectsMalformedReplies(t *testing.T) {
	good, _ := fixtureBlob(t)
	cases := map[string][]byte{
		"wrong message type": append([]byte{6}, reply(0, good, "")[1:]...),
		"wrong extension":    append(append([]byte{extensionReplyID}, str([]byte("other@x"))...), str(nil)...),
		"truncated contents": append(append([]byte{extensionReplyID}, str([]byte(extensionName))...), str([]byte{0, 0, 0, 1})...),
		"signed but empty":   reply(0, nil, ""),
		"wrong version": func() []byte {
			r := reply(0, good, "")
			// version is the first u32 of contents: after type, name string, contents length.
			r[1+4+len(extensionName)+4+3] = 2
			return r
		}(),
	}
	for name, body := range cases {
		kr, _ := keyringWith(t, "PIV_slot_9C 0A1B2C3D")
		ag := &fakeAgent{ExtendedAgent: kr, ext: func(string, []byte) ([]byte, error) { return body, nil }}
		_, err := serve(t, ag).Sign(context.Background(), NamespaceEscalation, "ok\n")
		var pe *ProtocolError
		if !errors.As(err, &pe) {
			t.Errorf("%s: got %T %v, want *ProtocolError", name, err, err)
		}
	}
}

func TestUnsupportedExtensionHasItsOwnError(t *testing.T) {
	// A bare keyring answers SSH_AGENT_FAILURE to every extension, as an agent
	// older than operator-act@piggy does.
	kr, _ := keyringWith(t, "PIV_slot_9C 0A1B2C3D")
	_, err := serve(t, kr).Sign(context.Background(), NamespaceEscalation, "ok\n")
	var ns *NotSupportedError
	if !errors.As(err, &ns) {
		t.Fatalf("got %T %v, want *NotSupportedError", err, err)
	}

	// SSH_AGENT_EXTENSION_FAILURE is the same condition (RFC 0012 2.2).
	ag := &fakeAgent{ExtendedAgent: kr, ext: func(string, []byte) ([]byte, error) { return nil, errors.New("boom") }}
	_, err = serve(t, ag).Sign(context.Background(), NamespaceEscalation, "ok\n")
	if !errors.As(err, &ns) {
		t.Fatalf("extension failure: got %T %v, want *NotSupportedError", err, err)
	}
}

func TestKeySelection(t *testing.T) {
	kr, _ := keyringWith(t, "PIV_slot_9A 0A1B2C3D")
	ag := &fakeAgent{ExtendedAgent: kr, ext: func(string, []byte) ([]byte, error) { t.Error("must not call"); return nil, nil }}
	_, err := serve(t, ag).Sign(context.Background(), NamespaceEscalation, "ok\n")
	var nk *NoSuchKeyError
	if !errors.As(err, &nk) || !strings.Contains(err.Error(), "PIV_slot_9A") {
		t.Fatalf("no 9C key: got %v", err)
	}

	two, _ := keyringWith(t, "PIV_slot_9C AAAAAAAA", "PIV_slot_9C BBBBBBBB")
	s := serve(t, &fakeAgent{ExtendedAgent: two, ext: func(string, []byte) ([]byte, error) { return nil, nil }})
	if _, err := s.Sign(context.Background(), NamespaceEscalation, "ok\n"); !errors.As(err, &nk) {
		t.Fatalf("ambiguous: got %v", err)
	}
	blob, _ := fixtureBlob(t)
	s = serve(t, &fakeAgent{ExtendedAgent: two, ext: func(string, []byte) ([]byte, error) { return reply(0, blob, ""), nil }})
	s.GUID = "bbbb"
	if _, err := s.Sign(context.Background(), NamespaceEscalation, "ok\n"); err != nil {
		t.Fatalf("guid narrows to one key: %v", err)
	}
}

func TestSignHonoursContextCancellation(t *testing.T) {
	kr, _ := keyringWith(t, "PIV_slot_9C 0A1B2C3D")
	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ag := &fakeAgent{ExtendedAgent: kr, ext: func(string, []byte) ([]byte, error) {
		close(entered)
		<-release // the operator is still at the PIN prompt
		return nil, nil
	}}
	s := serve(t, ag)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Sign(ctx, NamespaceEscalation, "ok\n"); done <- err }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Sign did not return after cancel")
	}
}

func TestNewAgentSignerNamesTheMissingSocketVariable(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	_, err := NewAgentSigner()
	if err == nil || !strings.Contains(err.Error(), "SSH_AUTH_SOCK") {
		t.Fatalf("got %v", err)
	}
	t.Setenv("SSH_AUTH_SOCK", "/nonexistent/sock")
	s, err := NewAgentSigner()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sign(context.Background(), NamespaceEscalation, "ok\n"); err == nil || !strings.Contains(err.Error(), "connect") {
		t.Fatalf("unreachable socket: got %v", err)
	}
}

func TestArmorMatchesSSHKeygenShape(t *testing.T) {
	out := string(armor(bytes.Repeat([]byte{7}, 100)))
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if lines[0] != "-----BEGIN SSH SIGNATURE-----" || lines[len(lines)-1] != "-----END SSH SIGNATURE-----" {
		t.Fatalf("markers: %q", out)
	}
	if len(lines[1]) != 70 {
		t.Fatalf("first body line is %d columns, want 70", len(lines[1]))
	}
}
