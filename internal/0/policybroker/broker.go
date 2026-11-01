// Package policybroker is the broker side of moxy's policy-broker protocol
// (moxy RFC 0002; spinclass#438, docs/plans/2026-11-01-438-policy-broker-design.md):
// a unix-socket listener in `spinclass serve` that moxy dials, exchanges a
// hello with, and asks one verdict per resource a tool call names. The
// verdict is always one of allow, ask or deny; anything the broker cannot
// answer is an error, which the client treats as no answer and falls to its
// static floor, so nothing here ever widens what the floor allows.
//
// The package owns the wire and the connection discipline only. What a
// verdict is comes from the Evaluator the caller supplies, and who may
// connect from the PeerCheck.
package policybroker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Version is the protocol version this broker speaks.
const Version = 1

// MaxLine bounds one message on the wire (RFC 0002 section 2).
const MaxLine = 64 << 10

// EnvAddr is the variable clown exports with the broker address (clown#333).
// Serve listens only when it is an absolute unix socket path.
const EnvAddr = "CLOWN_POLICY_BROKER"

// Message types.
const (
	TypeHello   = "hello"
	TypeCheck   = "check"
	TypeVerdict = "verdict"
	TypeError   = "error"
)

// Verdicts.
const (
	VerdictAllow = "allow"
	VerdictAsk   = "ask"
	VerdictDeny  = "deny"
)

// Error codes (RFC 0002 section 5).
const (
	CodeSubject  = "subject"
	CodeProtocol = "protocol"
	CodeInternal = "internal"
)

// CapCheck is the one capability defined.
const CapCheck = "check"

// Operations a check may name.
const (
	OpRead  = "read"
	OpWrite = "write"
	OpExec  = "exec"
)

// Resource kinds.
const (
	KindFile = "file"
	KindBlob = "blob"
)

// Defaults.
const (
	DefaultHelloTimeout = time.Second
	// MaxDeadline caps what a check's deadline_ms can hold the evaluator for;
	// the client's own deadline is shorter in practice.
	MaxDeadline = 5 * time.Second
)

// Message is the wire union, field for field what moxy's client encodes;
// unknown fields are ignored on decode.
type Message struct {
	V    int    `json:"v"`
	Type string `json:"type"`

	Client       string   `json:"client,omitempty"`
	Broker       string   `json:"broker,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Subjects     []string `json:"subjects,omitempty"`
	SessionKey   string   `json:"session_key,omitempty"`

	ID         int64     `json:"id,omitempty"`
	Subject    string    `json:"subject,omitempty"`
	Op         string    `json:"op,omitempty"`
	Resource   *Resource `json:"resource,omitempty"`
	DeadlineMS int64     `json:"deadline_ms,omitempty"`
	Verdict    string    `json:"verdict,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Code       string    `json:"code,omitempty"`
	Message    string    `json:"message,omitempty"`
}

// Resource is what a check names.
type Resource struct {
	Kind string `json:"kind"`
	URI  string `json:"uri"`
}

// Check is one question for the Evaluator: the subject is already verified
// to be the broker's session, the op one of OpRead/OpWrite/OpExec and the
// resource kind one of KindFile/KindBlob.
type Check struct {
	Subject  string
	Op       string
	Resource Resource
}

// Evaluator answers checks. It must return one of the three verdicts; an
// error, or any other string, is answered to the client as an internal error
// (no answer), never as a verdict.
type Evaluator interface {
	Evaluate(ctx context.Context, c Check) (verdict, reason string, err error)
}

// EvaluatorFunc adapts a function to Evaluator.
type EvaluatorFunc func(ctx context.Context, c Check) (verdict, reason string, err error)

// Evaluate calls f.
func (f EvaluatorFunc) Evaluate(ctx context.Context, c Check) (string, string, error) {
	return f(ctx, c)
}

// Options configures Listen.
type Options struct {
	// Path is the unix socket to create (0600; its directory must exist, as
	// clown creates it 0700). A live socket there is refused, a stale one
	// replaced.
	Path string
	// SessionKey is the exact SPINCLASS_SESSION_ID string: the hello's
	// session_key and the only subject a check may name.
	SessionKey string
	// BrokerName goes in the hello, e.g. "spinclass serve 0.2.1".
	BrokerName string
	// Evaluator answers the checks. Required.
	Evaluator Evaluator
	// PeerCheck admits a connection before any byte is exchanged; a non-nil
	// error closes it. Nil means SameUID.
	PeerCheck func(conn net.Conn) error
	// HelloTimeout bounds the client's hello; DefaultHelloTimeout when zero.
	HelloTimeout time.Duration
	// Logf receives one line per refused peer, refused hello and protocol
	// error; nil discards.
	Logf func(format string, args ...any)
}

// Broker is a listening policy broker.
type Broker struct {
	o  Options
	ln net.Listener

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// Listen creates the socket and returns the broker; Serve accepts on it. It
// refuses to take over a socket that still answers (another serve for the
// same principal) and replaces one that does not, mirroring clown's
// ListenServeSocket, because clown names the path but never unlinks a stale
// socket there.
func Listen(o Options) (*Broker, error) {
	switch {
	case o.Path == "" || !filepath.IsAbs(o.Path):
		return nil, fmt.Errorf("policy broker: %q is not an absolute socket path", o.Path)
	case o.SessionKey == "":
		return nil, errors.New("policy broker: no session key to answer for")
	case o.Evaluator == nil:
		return nil, errors.New("policy broker: no evaluator")
	}
	if c, err := net.DialTimeout("unix", o.Path, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("policy broker socket %s is live: another serve answers for this session; refusing to take it over", o.Path)
	}
	_ = os.Remove(o.Path)
	ln, err := net.Listen("unix", o.Path)
	if err != nil {
		return nil, fmt.Errorf("policy broker: %w", err)
	}
	if err := os.Chmod(o.Path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("policy broker: %w", err)
	}
	if o.PeerCheck == nil {
		o.PeerCheck = func(c net.Conn) error { _, err := SameUID(c); return err }
	}
	if o.HelloTimeout <= 0 {
		o.HelloTimeout = DefaultHelloTimeout
	}
	return &Broker{o: o, ln: ln, conns: map[net.Conn]struct{}{}}, nil
}

// Path is the socket the broker listens on.
func (b *Broker) Path() string { return b.o.Path }

// Serve accepts connections until ctx is done or the broker is closed, then
// closes every connection and returns. It never returns an accept error
// while the broker is open: a transient accept failure is logged and retried.
func (b *Broker) Serve(ctx context.Context) {
	stop := context.AfterFunc(ctx, func() { b.Close() })
	defer stop()
	for {
		c, err := b.ln.Accept()
		if err != nil {
			if b.isClosed() {
				break
			}
			b.logf("policy broker: accept: %v", err)
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if !b.track(c) {
			c.Close()
			break
		}
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			defer b.untrack(c)
			b.handle(ctx, c)
		}()
	}
	b.wg.Wait()
}

// Close stops accepting, closes every connection and removes the socket.
// Idempotent.
func (b *Broker) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	conns := make([]net.Conn, 0, len(b.conns))
	for c := range b.conns {
		conns = append(conns, c)
	}
	b.mu.Unlock()
	err := b.ln.Close()
	for _, c := range conns {
		c.Close()
	}
	_ = os.Remove(b.o.Path)
	return err
}

func (b *Broker) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

func (b *Broker) track(c net.Conn) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	b.conns[c] = struct{}{}
	return true
}

func (b *Broker) untrack(c net.Conn) {
	b.mu.Lock()
	delete(b.conns, c)
	b.mu.Unlock()
	c.Close()
}

func (b *Broker) logf(format string, args ...any) {
	if b.o.Logf != nil {
		b.o.Logf(format, args...)
	}
}

// conn is one accepted connection: a reader bounded at MaxLine and a write
// lock, since verdicts are written from one goroutine per check.
type conn struct {
	net.Conn
	r   *bufio.Reader
	wmu sync.Mutex
}

func (c *conn) send(m Message) error {
	m.V = Version
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.Write(append(b, '\n'))
	return err
}

func (c *conn) read() (Message, error) {
	line, err := c.r.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			return Message{}, fmt.Errorf("message longer than %d bytes", MaxLine)
		}
		return Message{}, err
	}
	var m Message
	if err := json.Unmarshal(line, &m); err != nil {
		return Message{}, fmt.Errorf("message does not parse: %w", err)
	}
	return m, nil
}

func (b *Broker) handle(ctx context.Context, raw net.Conn) {
	if err := b.o.PeerCheck(raw); err != nil {
		b.logf("policy broker: refused peer: %v", err)
		return
	}
	c := &conn{Conn: raw, r: bufio.NewReaderSize(raw, MaxLine)}
	if err := c.send(Message{Type: TypeHello, Broker: b.o.BrokerName, Capabilities: []string{CapCheck}, SessionKey: b.o.SessionKey}); err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Now().Add(b.o.HelloTimeout))
	hello, err := c.read()
	if err != nil {
		b.logf("policy broker: no client hello: %v", err)
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	if hello.Type != TypeHello || hello.V != Version {
		b.logf("policy broker: refused hello: type %q version %d", hello.Type, hello.V)
		_ = c.send(Message{Type: TypeError, Code: CodeProtocol, Message: fmt.Sprintf("expected a version %d hello first", Version)})
		return
	}
	for _, s := range hello.Subjects {
		if s != b.o.SessionKey {
			b.logf("policy broker: refused hello from %q: subject %q is not this session", hello.Client, s)
			_ = c.send(Message{Type: TypeError, Code: CodeSubject, Message: fmt.Sprintf("this broker answers for session %q only", b.o.SessionKey)})
			return
		}
	}
	var checks sync.WaitGroup
	defer checks.Wait()
	for {
		m, err := c.read()
		if err != nil {
			return
		}
		switch m.Type {
		case TypeCheck:
			if msg := b.rejectCheck(m); msg != "" {
				code := CodeProtocol
				if m.Subject != b.o.SessionKey {
					code = CodeSubject
				}
				_ = c.send(Message{Type: TypeError, ID: m.ID, Code: code, Message: msg})
				continue
			}
			checks.Add(1)
			go func() {
				defer checks.Done()
				_ = c.send(b.answer(ctx, m))
			}()
		default:
			_ = c.send(Message{Type: TypeError, ID: m.ID, Code: CodeProtocol, Message: fmt.Sprintf("unexpected message type %q", m.Type)})
		}
	}
}

// rejectCheck returns why m is not a check this broker can evaluate, or "".
func (b *Broker) rejectCheck(m Message) string {
	switch {
	case m.ID == 0:
		return "check has no id"
	case m.Subject != b.o.SessionKey:
		return fmt.Sprintf("subject %q is not the session this broker answers for", m.Subject)
	case m.Op != OpRead && m.Op != OpWrite && m.Op != OpExec:
		return fmt.Sprintf("op %q is not read, write or exec", m.Op)
	case m.Resource == nil || m.Resource.URI == "":
		return "check names no resource"
	case m.Resource.Kind != KindFile && m.Resource.Kind != KindBlob:
		return fmt.Sprintf("resource kind %q is not file or blob", m.Resource.Kind)
	}
	return ""
}

// answer evaluates one accepted check within its deadline and returns the
// verdict message, or an internal error when the evaluator failed or
// answered outside the three verdicts. Never a verdict it did not get.
func (b *Broker) answer(ctx context.Context, m Message) Message {
	wait := time.Duration(m.DeadlineMS) * time.Millisecond
	if wait <= 0 || wait > MaxDeadline {
		wait = MaxDeadline
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	verdict, reason, err := b.o.Evaluator.Evaluate(ctx, Check{Subject: m.Subject, Op: m.Op, Resource: *m.Resource})
	switch {
	case err != nil:
		b.logf("policy broker: check %d (%s %s): %v", m.ID, m.Op, m.Resource.URI, err)
		return Message{Type: TypeError, ID: m.ID, Code: CodeInternal, Message: err.Error()}
	case verdict != VerdictAllow && verdict != VerdictAsk && verdict != VerdictDeny:
		b.logf("policy broker: check %d: evaluator answered %q, not a verdict", m.ID, verdict)
		return Message{Type: TypeError, ID: m.ID, Code: CodeInternal, Message: "evaluator answered outside allow, ask, deny"}
	}
	return Message{Type: TypeVerdict, ID: m.ID, Verdict: verdict, Reason: reason}
}
