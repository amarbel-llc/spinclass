// Package bridge is the wire format of bridge contract v1: the hello line a
// shim sends, the one-line reply a frontend answers, and the limits both
// sides share. It depends on the standard library only, so clown's copy of
// testdata/vectors.json can drive the same cases.
//
// A connection opens with "BRIDGE/<major> <service>\n". The frontend answers
// with exactly one line, "OK\n" or "ERR <code> <text>\n", and after "OK\n" the
// connection is the service's byte stream in both directions. Both readers
// here read one byte at a time and stop at the newline or at their bound, so
// no service byte is ever consumed by the handshake.
package bridge

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// SocketName is the socket file inside the bridge directory.
	SocketName = "bridge.sock"
	// EnvDir and EnvUnit are the environment variables clown sets for the shim.
	EnvDir  = "CLOWN_BRIDGE_DIR"
	EnvUnit = "CLOWN_BRIDGE_UNIT"

	// MaxHelloBytes bounds the hello line, newline included.
	MaxHelloBytes = 64
	// MaxReplyTextBytes bounds the text of an ERR reply.
	MaxReplyTextBytes = 512
	// MaxStreamsPerSession bounds the open streams of one session.
	MaxStreamsPerSession = 64
	// MaxSocketPath is the longest unix socket path the bridge uses.
	MaxSocketPath = 107
	// HelloTimeout is how long a frontend waits for the hello line.
	HelloTimeout = 5 * time.Second

	// ServerMajor is the one major version this contract serves.
	ServerMajor = 1
	// VersionHint is the fixed hint that ends an "ERR version" text.
	VersionHint = "rebuild or restart the session so the shim and the frontend match"

	maxServiceBytes = 32
	maxMajorDigits  = 3
	maxEchoBytes    = 32
	maxReplyBytes   = MaxHelloBytes + MaxReplyTextBytes
)

// Reply codes of an "ERR <code> <text>" line.
const (
	CodeVersion        = "version"
	CodeUnknownService = "unknown-service"
	CodeRefused        = "refused"
	CodeBusy           = "busy"
	CodeError          = "error"
)

// ReservedServices are the labels a hello may name; the server decides which
// of them it serves.
var ReservedServices = []string{"egress", "control", "session", "hook", "chat", "model", "keys", "status"}

var (
	// ErrMalformed marks input that breaks the grammar.
	ErrMalformed = errors.New("bridge: malformed")
	// ErrTooLong marks a line that did not end within its byte bound.
	ErrTooLong = errors.New("bridge: line too long")
)

// Hello is a parsed hello line.
type Hello struct {
	Major   int
	Service string
}

// ParseHello parses one hello line including its newline. Errors satisfy
// errors.Is with ErrMalformed or ErrTooLong; their text echoes at most 32
// bytes of the input, escaped.
func ParseHello(line []byte) (Hello, error) {
	if len(line) > MaxHelloBytes {
		return Hello{}, fmt.Errorf("%w: hello %s exceeds %d bytes", ErrTooLong, echo(line), MaxHelloBytes)
	}
	if len(line) == 0 || line[len(line)-1] != '\n' {
		if len(line) >= MaxHelloBytes {
			return Hello{}, fmt.Errorf("%w: no newline within %d bytes of %s", ErrTooLong, MaxHelloBytes, echo(line))
		}
		return Hello{}, fmt.Errorf("%w: hello %s has no newline", ErrMalformed, echo(line))
	}
	body := string(line[:len(line)-1])
	rest, ok := strings.CutPrefix(body, "BRIDGE/")
	if !ok {
		return Hello{}, fmt.Errorf("%w: hello %s does not start with BRIDGE/", ErrMalformed, echo(line))
	}
	digits, service, ok := strings.Cut(rest, " ")
	if !ok {
		return Hello{}, fmt.Errorf("%w: hello %s has no service", ErrMalformed, echo(line))
	}
	major, ok := parseMajor(digits)
	if !ok {
		return Hello{}, fmt.Errorf("%w: hello %s has a bad major version", ErrMalformed, echo(line))
	}
	if !validService(service) {
		return Hello{}, fmt.Errorf("%w: hello %s has a bad service label", ErrMalformed, echo(line))
	}
	return Hello{Major: major, Service: service}, nil
}

// FormatHello builds the hello line for a major version and service label,
// refusing what ParseHello would refuse.
func FormatHello(major int, service string) ([]byte, error) {
	line := fmt.Sprintf("BRIDGE/%d %s\n", major, service)
	if _, err := ParseHello([]byte(line)); err != nil {
		return nil, err
	}
	return []byte(line), nil
}

// ReadHello reads one hello line byte by byte. It reads at most MaxHelloBytes
// and never past the newline; a line with no newline within the bound is
// ErrTooLong.
func ReadHello(r io.Reader) (Hello, error) {
	line, err := readLine(r, MaxHelloBytes)
	if err != nil {
		return Hello{}, err
	}
	return ParseHello(line)
}

// OK is the reply that opens the service's byte stream.
func OK() []byte { return []byte("OK\n") }

// MalformedReply is the reply to a hello that does not parse.
func MalformedReply() []byte { return FormatErr(CodeError, "malformed hello") }

// FormatErr builds an "ERR <code> <text>" line. An unknown code becomes
// "error"; CR and LF in text become spaces; text is cut at 512 bytes on a
// rune boundary.
func FormatErr(code, text string) []byte {
	if !validCode(code) {
		code = CodeError
	}
	text = strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
	if len(text) > MaxReplyTextBytes {
		cut := MaxReplyTextBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	return []byte("ERR " + code + " " + text + "\n")
}

// ReadReply reads one reply line byte by byte, bounded at 64+512 bytes, and
// never consumes a byte past the newline: after "OK\n" the rest of the
// connection is the service's.
func ReadReply(r io.Reader) (ok bool, code, text string, err error) {
	line, err := readLine(r, maxReplyBytes)
	if err != nil {
		return false, "", "", err
	}
	body := string(line[:len(line)-1])
	if body == "OK" {
		return true, "", "", nil
	}
	rest, isErr := strings.CutPrefix(body, "ERR ")
	if !isErr {
		return false, "", "", fmt.Errorf("%w: reply %s is neither OK nor ERR", ErrMalformed, echo(line))
	}
	code, text, _ = strings.Cut(rest, " ")
	if !validCode(code) {
		return false, "", "", fmt.Errorf("%w: reply %s has an unknown code", ErrMalformed, echo(line))
	}
	if len(text) > MaxReplyTextBytes || strings.ContainsRune(text, '\r') {
		return false, "", "", fmt.Errorf("%w: reply %s has a bad text", ErrMalformed, echo(line))
	}
	return false, code, text, nil
}

// Decide applies the version and label rules to a parsed hello. served is the
// set of reserved labels this server answers; any other label, reserved or
// not, is unknown-service. accept is true only for the "OK\n" reply.
func Decide(h Hello, served map[string]bool) (reply []byte, accept bool) {
	if h.Major != ServerMajor {
		return FormatErr(CodeVersion, fmt.Sprintf("server=%d shim=%d: %s", ServerMajor, h.Major, VersionHint)), false
	}
	if !served[h.Service] || !isReserved(h.Service) {
		return FormatErr(CodeUnknownService, h.Service), false
	}
	return OK(), true
}

// SocketPath is <dir>/bridge.sock, refused when longer than the unix socket
// limit.
func SocketPath(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("bridge directory is empty; the bridge stays off")
	}
	p := strings.TrimRight(dir, "/") + "/" + SocketName
	if len(p) > MaxSocketPath {
		return "", fmt.Errorf("bridge socket path %q is %d bytes, over the unix socket limit of %d; the bridge stays off", p, len(p), MaxSocketPath)
	}
	return p, nil
}

// DownMessage is the fixed text the shim prints when the bridge is down.
func DownMessage(reason string) string {
	return fmt.Sprintf("clown: the bridge is down (%s); no tool or model call can leave this session; the session's frontend is gone or restarting", reason)
}

// readLine reads up to limit bytes, one at a time, stopping after the first
// newline. It returns the line with its newline.
func readLine(r io.Reader, limit int) ([]byte, error) {
	line := make([]byte, 0, limit)
	var b [1]byte
	for len(line) < limit {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, fmt.Errorf("%w: stream ended after %s without a newline", ErrMalformed, echo(line))
			}
			return nil, err
		}
		line = append(line, b[0])
		if b[0] == '\n' {
			return line, nil
		}
	}
	return nil, fmt.Errorf("%w: no newline within %d bytes of %s", ErrTooLong, limit, echo(line))
}

func parseMajor(digits string) (int, bool) {
	if len(digits) < 1 || len(digits) > maxMajorDigits || digits[0] == '0' {
		return 0, false
	}
	n := 0
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, false
		}
		n = n*10 + int(digits[i]-'0')
	}
	return n, true
}

func validService(s string) bool {
	if len(s) < 1 || len(s) > maxServiceBytes || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func validCode(code string) bool {
	switch code {
	case CodeVersion, CodeUnknownService, CodeRefused, CodeBusy, CodeError:
		return true
	}
	return false
}

func isReserved(label string) bool {
	for _, r := range ReservedServices {
		if r == label {
			return true
		}
	}
	return false
}

// echo quotes at most 32 bytes of untrusted input with every byte outside
// printable ASCII escaped, so an error text can be logged safely.
func echo(b []byte) string {
	more := ""
	if len(b) > maxEchoBytes {
		b, more = b[:maxEchoBytes], "..."
	}
	var sb strings.Builder
	sb.WriteByte('"')
	for _, c := range b {
		switch {
		case c == '\\' || c == '"':
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case c >= 0x20 && c <= 0x7e:
			sb.WriteByte(c)
		default:
			fmt.Fprintf(&sb, `\x%02x`, c)
		}
	}
	sb.WriteByte('"')
	sb.WriteString(more)
	return sb.String()
}
