// Package operatoract is the client of piggy's operator-act@piggy SSH-agent
// extension (piggy RFC 0012). The card signs only through that extension: the
// caller sends the text and an sshsig namespace, and the agent shows exactly
// that text on the PIN prompt, asks for PIN and touch, and builds the sshsig
// itself. The client therefore never sees a PIN and cannot sign locally.
//
// This code speaks the wire format as the RFC and piggy's own client define
// it; it has not been exercised against a real card.
package operatoract

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"code.linenisgreat.com/spinclass/internal/sshsig"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const (
	// NamespaceEscalation is the sshsig namespace of an escalation grant, the
	// display class a profile grant is signed under (RFC 0012 section 3).
	NamespaceEscalation = "piggy-escalation-v1"

	// MaxTextBytes is the longest act text the agent accepts (RFC 0012 2.5).
	MaxTextBytes = 4096

	// DefaultSlot is the PIV slot piggy provisions PIN-always, touch-always.
	DefaultSlot = "9C"

	extensionName    = "operator-act@piggy"
	requestVersion   = 1
	requestHash      = "sha512" // what piggy operator-sign defaults to; the verifier reads either
	extensionReplyID = 29       // SSH_AGENT_EXTENSION_RESPONSE; RFC 0012 2.2 does not number it, piggy's agent and probe do
)

// registeredNamespaces are the namespaces the agent serves (RFC 0012 section 3).
var registeredNamespaces = []string{"piggy-quote-v1", NamespaceEscalation, "piggy-certificate-v1"}

// Signer asks the operator's card to sign an act text.
type Signer interface {
	// Sign returns the armored sshsig over text under namespace.
	Sign(ctx context.Context, namespace, text string) (armoredSig []byte, err error)
}

// AgentSigner signs through the SSH agent that holds the card.
type AgentSigner struct {
	// Slot is the PIV slot to sign with, matched against the "PIV_slot_<slot>"
	// tag in the agent identity comment, as piggy operator-sign does.
	Slot string
	// GUID optionally narrows Slot to one card, by prefix of the comment's
	// short GUID.
	GUID string
	// Keys, when non-empty, is the allow-list of the card's public keys (from
	// the signers file). Only an agent identity whose key is in it is used: the
	// "PIV_slot_" comment is attacker-settable by anything that can add a key
	// to the agent, so with a list the comment is not trusted for selection.
	// Empty (no signers, --no-verify) falls back to the comment rule.
	Keys []ssh.PublicKey

	dial func(ctx context.Context) (net.Conn, error)
}

// NewAgentSigner returns a signer that dials $SSH_AUTH_SOCK.
func NewAgentSigner() (*AgentSigner, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("operator act: SSH_AUTH_SOCK is not set; operator acts are signed by the agent that holds the card")
	}
	return &AgentSigner{
		Slot: DefaultSlot,
		dial: func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}, nil
}

// Sign implements Signer. It blocks while the operator reads the prompt, types
// the PIN and touches the card, so there is no timeout beyond ctx.
func (s *AgentSigner) Sign(ctx context.Context, namespace, text string) ([]byte, error) {
	if err := checkLocally(namespace, text); err != nil {
		return nil, err
	}
	conn, err := s.dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("operator act: connect to agent: %w", err)
	}
	defer conn.Close()

	// Closing the connection is the only way to interrupt a blocked agent call.
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-finished:
		}
	}()

	sshsig, err := s.request(conn, namespace, text)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("operator act: %w", ctxErr)
		}
		return nil, err
	}
	return armor(sshsig), nil
}

func (s *AgentSigner) request(conn net.Conn, namespace, text string) ([]byte, error) {
	ext := agent.NewClient(conn)
	keys, err := ext.List()
	if err != nil {
		return nil, fmt.Errorf("operator act: list agent identities: %w", err)
	}
	key, err := s.selectKey(keys)
	if err != nil {
		return nil, err
	}
	reply, err := ext.Extension(extensionName, encodeRequest(key, namespace, requestHash, []byte(text)))
	if err != nil {
		return nil, classifyExtensionError(err)
	}
	blob, err := decodeReply(reply)
	if err != nil {
		return nil, err
	}
	// The agent is not trusted to have signed what we asked: a rogue agent
	// could return a valid signature by another key or under another namespace.
	if err := checkReturnedSig(blob, key, namespace); err != nil {
		return nil, err
	}
	return blob, nil
}

// checkReturnedSig requires the sshsig to be under the requested namespace, by
// exactly the key that was selected and sent, with a hash the verifier reads.
func checkReturnedSig(blob, key []byte, namespace string) error {
	sig, err := sshsig.Parse(armor(blob))
	if err != nil {
		return &ProtocolError{Reason: "returned signature does not parse: " + sanitizeDetail(err.Error())}
	}
	if sig.Namespace != namespace {
		return &ProtocolError{Reason: fmt.Sprintf("returned signature is under namespace %q, not the requested %q", sanitizeDetail(sig.Namespace), namespace)}
	}
	if !bytes.Equal(sig.PublicKey.Marshal(), key) {
		return &ProtocolError{Reason: "returned signature is by a different key than the one selected"}
	}
	if sig.HashAlgorithm != "sha256" && sig.HashAlgorithm != "sha512" {
		return &ProtocolError{Reason: fmt.Sprintf("returned signature uses hash %q, want sha256 or sha512", sanitizeDetail(sig.HashAlgorithm))}
	}
	return nil
}

// maxDetailBytes caps agent-supplied text that reaches an error message.
const maxDetailBytes = 200

// sanitizeDetail makes agent-supplied text safe to print: anything but
// printable ASCII and plain space (escape sequences, bidi controls, invalid
// UTF-8) becomes '?', and the result is capped.
func sanitizeDetail(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= maxDetailBytes {
			break
		}
		if r == ' ' || (r >= 0x21 && r <= 0x7e) {
			b.WriteRune(r)
		} else {
			b.WriteByte('?')
		}
	}
	return b.String()
}

// selectKey picks the identity to sign with. With an allow-list it is the
// identity whose key is in the list; otherwise the one whose comment is
// "PIV_slot_<slot> [<guid>...]".
func (s *AgentSigner) selectKey(keys []*agent.Key) ([]byte, error) {
	slot := s.Slot
	if slot == "" {
		slot = DefaultSlot
	}
	tag := "PIV_slot_" + strings.ToUpper(slot)
	var matches []*agent.Key
	var comments []string
	for _, k := range keys {
		comments = append(comments, sanitizeDetail(k.Comment))
		fields := strings.Fields(k.Comment)
		if len(s.Keys) > 0 {
			if !s.keyAllowed(k.Blob) {
				continue
			}
		} else if len(fields) == 0 || !strings.EqualFold(fields[0], tag) {
			continue
		}
		if s.GUID != "" && (len(fields) < 2 || !guidPrefixMatch(fields[1], s.GUID)) {
			continue
		}
		matches = append(matches, k)
	}
	want := tag
	if len(s.Keys) > 0 {
		want = "a key from the signers file"
	}
	switch len(matches) {
	case 1:
		return matches[0].Blob, nil
	case 0:
		return nil, &NoSuchKeyError{Detail: fmt.Sprintf("agent serves no key for %s (served: [%s])", want, sanitizeDetail(strings.Join(comments, ", ")))}
	default:
		return nil, &NoSuchKeyError{Detail: fmt.Sprintf("agent serves %d keys for %s; set a GUID to disambiguate (served: [%s])", len(matches), want, sanitizeDetail(strings.Join(comments, ", ")))}
	}
}

func (s *AgentSigner) keyAllowed(blob []byte) bool {
	for _, k := range s.Keys {
		if k != nil && bytes.Equal(k.Marshal(), blob) {
			return true
		}
	}
	return false
}

// guidPrefixMatch: the configured hint must be a prefix of the comment's GUID.
// The reverse direction would let a short (or empty-ish) comment GUID match any
// longer hint.
func guidPrefixMatch(commentGUID, hint string) bool {
	a, b := strings.ToLower(commentGUID), strings.ToLower(hint)
	return a != "" && b != "" && strings.HasPrefix(a, b)
}

// genericExtensionFailure is x/crypto's text for SSH_AGENT_EXTENSION_FAILURE.
// x/crypto exposes no typed error for it (only agent.ErrExtensionUnsupported
// for a plain SSH_AGENT_FAILURE), so a string match is the only option.
const genericExtensionFailure = "agent: generic extension failure"

// classifyExtensionError separates "this agent does not serve the extension"
// (RFC 0012 2.2: SSH_AGENT_FAILURE or SSH_AGENT_EXTENSION_FAILURE) from
// transport trouble.
func classifyExtensionError(err error) error {
	if errors.Is(err, agent.ErrExtensionUnsupported) || err.Error() == genericExtensionFailure {
		return &NotSupportedError{Cause: err}
	}
	return fmt.Errorf("operator act: agent call: %w", err)
}

// checkLocally mirrors the agent's pre-prompt refusals (RFC 0012 2.3, 2.5) so a
// bad request fails here, fast, instead of costing a round trip.
func checkLocally(namespace, text string) error {
	registered := false
	for _, n := range registeredNamespaces {
		registered = registered || n == namespace
	}
	if !registered {
		return &RefusedNamespaceError{Detail: fmt.Sprintf("checked locally: %q is not an operator-act namespace", namespace)}
	}
	switch {
	case text == "":
		return &RefusedTextError{Detail: "checked locally: text is empty"}
	case len(text) > MaxTextBytes:
		return &RefusedTextError{Detail: fmt.Sprintf("checked locally: text is %d bytes, limit %d", len(text), MaxTextBytes)}
	case !utf8.ValidString(text):
		return &RefusedTextError{Detail: "checked locally: text is not valid UTF-8"}
	}
	for _, r := range text {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return &RefusedTextError{Detail: fmt.Sprintf("checked locally: control character %U is not displayable", r)}
		}
	}
	return nil
}

func appendString(b, s []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}

// encodeRequest builds the extension contents of RFC 0012 2.1, framed as the
// single outer "string contents" that follows the extension name:
//
//	string { uint32 version=1, string key, string namespace,
//	         string hash_algorithm, string text, uint32 flags=0 }
func encodeRequest(key []byte, namespace, hash string, text []byte) []byte {
	var inner []byte
	inner = binary.BigEndian.AppendUint32(inner, requestVersion)
	inner = appendString(inner, key)
	inner = appendString(inner, []byte(namespace))
	inner = appendString(inner, []byte(hash))
	inner = appendString(inner, text)
	inner = binary.BigEndian.AppendUint32(inner, 0)
	return appendString(nil, inner)
}

type reader struct{ rest []byte }

func (r *reader) u32() (uint32, bool) {
	if len(r.rest) < 4 {
		return 0, false
	}
	v := binary.BigEndian.Uint32(r.rest)
	r.rest = r.rest[4:]
	return v, true
}

func (r *reader) string() ([]byte, bool) {
	n, ok := r.u32()
	if !ok || uint64(n) > uint64(len(r.rest)) {
		return nil, false
	}
	s := r.rest[:n]
	r.rest = r.rest[n:]
	return s, true
}

// decodeReply parses a whole extension reply (type byte first) per RFC 0012
// 2.2: byte 29, string extension name, string contents, where contents is
// uint32 version=1, uint32 status, string sshsig, string detail.
func decodeReply(reply []byte) ([]byte, error) {
	if len(reply) == 0 || reply[0] != extensionReplyID {
		kind := -1
		if len(reply) > 0 {
			kind = int(reply[0])
		}
		return nil, &ProtocolError{Reason: fmt.Sprintf("reply has message type %d, want %d (extension response)", kind, extensionReplyID)}
	}
	outer := reader{rest: reply[1:]}
	name, ok := outer.string()
	if !ok || string(name) != extensionName {
		return nil, &ProtocolError{Reason: fmt.Sprintf("reply is for extension %q, not %q", name, extensionName)}
	}
	contents, ok := outer.string()
	if !ok || len(outer.rest) != 0 {
		return nil, &ProtocolError{Reason: "malformed extension response framing"}
	}
	in := reader{rest: contents}
	version, vok := in.u32()
	code, cok := in.u32()
	sshsig, sok := in.string()
	detailBytes, dok := in.string()
	if !vok || !cok || !sok || !dok {
		return nil, &ProtocolError{Reason: "truncated operator-act response"}
	}
	if version != requestVersion {
		return nil, &ProtocolError{Reason: fmt.Sprintf("operator-act response version %d, want %d", version, requestVersion)}
	}
	detail := sanitizeDetail(string(detailBytes))
	if err := statusError(code, detail); err != nil {
		return nil, err
	}
	if len(sshsig) == 0 {
		return nil, &ProtocolError{Reason: "status signed, with no signature"}
	}
	return sshsig, nil
}

// armor wraps an sshsig blob the way ssh-keygen -Y sign does (PROTOCOL.sshsig
// section 1): base64 in 70-column lines between the markers.
func armor(blob []byte) []byte {
	encoded := base64.StdEncoding.EncodeToString(blob)
	var b strings.Builder
	b.WriteString("-----BEGIN SSH SIGNATURE-----\n")
	for len(encoded) > 70 {
		b.WriteString(encoded[:70] + "\n")
		encoded = encoded[70:]
	}
	b.WriteString(encoded + "\n")
	b.WriteString("-----END SSH SIGNATURE-----\n")
	return []byte(b.String())
}
