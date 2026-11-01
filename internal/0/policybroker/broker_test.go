package policybroker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testKey = "spinclass/light-cedar"

// shortDir is a directory short enough for a unix socket path (108 bytes).
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "pb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func allowAll(_ context.Context, c Check) (string, string, error) {
	return VerdictAllow, "test " + c.Op + " " + c.Resource.URI, nil
}

func start(t *testing.T, o Options) *Broker {
	t.Helper()
	if o.Path == "" {
		o.Path = filepath.Join(shortDir(t), "broker.sock")
	}
	if o.SessionKey == "" {
		o.SessionKey = testKey
	}
	if o.Evaluator == nil {
		o.Evaluator = EvaluatorFunc(allowAll)
	}
	if o.BrokerName == "" {
		o.BrokerName = "spinclass serve test"
	}
	if o.Logf == nil {
		o.Logf = t.Logf
	}
	b, err := Listen(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Serve(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after cancel")
		}
	})
	return b
}

type client struct {
	t testing.TB
	net.Conn
	r *bufio.Reader
}

func dial(t testing.TB, b *Broker) *client {
	t.Helper()
	c, err := net.Dial("unix", b.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	return &client{t: t, Conn: c, r: bufio.NewReader(c)}
}

func (c *client) send(m Message) {
	c.t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := c.Write(append(b, '\n')); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) read() (Message, error) {
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		return Message{}, err
	}
	var m Message
	if err := json.Unmarshal(line, &m); err != nil {
		return Message{}, err
	}
	return m, nil
}

func (c *client) mustRead() Message {
	c.t.Helper()
	m, err := c.read()
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return m
}

// hello completes the handshake with the given subjects and returns the
// broker's hello.
func (c *client) hello(subjects ...string) Message {
	c.t.Helper()
	c.send(Message{V: Version, Type: TypeHello, Client: "test", Capabilities: []string{CapCheck}, Subjects: subjects})
	return c.mustRead()
}

func fileCheck(id int64, subject, op, path string) Message {
	return Message{V: Version, Type: TypeCheck, ID: id, Subject: subject, Op: op, Resource: &Resource{Kind: KindFile, URI: "file://" + path}, DeadlineMS: 100}
}

func TestHelloBothWaysThenVerdicts(t *testing.T) {
	b := start(t, Options{})
	c := dial(t, b)
	h := c.hello()
	if h.Type != TypeHello || h.V != Version || h.SessionKey != testKey || len(h.Capabilities) != 1 || h.Capabilities[0] != CapCheck || h.Broker == "" {
		t.Fatalf("broker hello = %+v", h)
	}
	// Pipelined checks are each answered once, by id.
	c.send(fileCheck(7, testKey, OpRead, "/a"))
	c.send(fileCheck(8, testKey, OpWrite, "/b"))
	got := map[int64]Message{}
	for range 2 {
		m := c.mustRead()
		got[m.ID] = m
	}
	if m := got[7]; m.Type != TypeVerdict || m.Verdict != VerdictAllow || m.Reason != "test read file:///a" {
		t.Errorf("verdict 7 = %+v", m)
	}
	if m := got[8]; m.Type != TypeVerdict || m.Verdict != VerdictAllow || m.Reason != "test write file:///b" {
		t.Errorf("verdict 8 = %+v", m)
	}
	// A client that names this session as its subject is admitted too.
	c2 := dial(t, b)
	if h := c2.hello(testKey); h.Type != TypeHello {
		t.Errorf("hello with own subject refused: %+v", h)
	}
}

func TestSocketIsPrivateAndStaleOneIsReplacedLiveOneRefused(t *testing.T) {
	path := filepath.Join(shortDir(t), "broker.sock")
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := start(t, Options{Path: path})
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode = %v, want a 0600 socket", fi.Mode())
	}
	if _, err := Listen(Options{Path: path, SessionKey: testKey, Evaluator: EvaluatorFunc(allowAll)}); err == nil || !strings.Contains(err.Error(), "live") {
		t.Errorf("a live socket must be refused, got %v", err)
	}
	b.Close()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Close should remove the socket, got %v", err)
	}
}

func TestHelloWithAnotherSubjectIsRefused(t *testing.T) {
	b := start(t, Options{})
	c := dial(t, b)
	// The broker's hello comes first, without waiting (RFC 0002 section 3);
	// the refusal follows once the client's hello is read.
	if h := c.hello("other/session"); h.Type != TypeHello {
		t.Fatalf("want the broker hello first, got %+v", h)
	}
	if m := c.mustRead(); m.Type != TypeError || m.Code != CodeSubject {
		t.Fatalf("want a subject error, got %+v", m)
	}
	if _, err := c.read(); err == nil {
		t.Error("the connection should be closed after a refused hello")
	}
}

func TestFirstMessageMustBeAHello(t *testing.T) {
	b := start(t, Options{})
	c := dial(t, b)
	if h := c.mustRead(); h.Type != TypeHello {
		t.Fatalf("want the broker hello first, got %+v", h)
	}
	c.send(fileCheck(1, testKey, OpRead, "/a"))
	if m := c.mustRead(); m.Type != TypeError || m.Code != CodeProtocol {
		t.Fatalf("want a protocol error, got %+v", m)
	}
	if _, err := c.read(); err == nil {
		t.Error("the connection should be closed")
	}
}

func TestChecksTheBrokerCannotEvaluateAreErrorsNotVerdicts(t *testing.T) {
	b := start(t, Options{})
	c := dial(t, b)
	c.hello()
	for _, tc := range []struct {
		name string
		m    Message
		code string
	}{
		{"wrong subject", fileCheck(1, "other/session", OpRead, "/a"), CodeSubject},
		{"unknown op", fileCheck(2, testKey, "delete", "/a"), CodeProtocol},
		{"no resource", Message{V: Version, Type: TypeCheck, ID: 3, Subject: testKey, Op: OpRead}, CodeProtocol},
		{"unknown kind", Message{V: Version, Type: TypeCheck, ID: 4, Subject: testKey, Op: OpRead, Resource: &Resource{Kind: "dir", URI: "x"}}, CodeProtocol},
		{"no id", Message{V: Version, Type: TypeCheck, Subject: testKey, Op: OpRead, Resource: &Resource{Kind: KindFile, URI: "file:///a"}}, CodeProtocol},
		{"unknown type", Message{V: Version, Type: "ping", ID: 5}, CodeProtocol},
	} {
		c.send(tc.m)
		m := c.mustRead()
		if m.Type != TypeError || m.Code != tc.code || m.ID != tc.m.ID || m.Verdict != "" {
			t.Errorf("%s: got %+v, want an %s error for id %d", tc.name, m, tc.code, tc.m.ID)
		}
	}
	// The connection survives every one of them.
	c.send(fileCheck(9, testKey, OpRead, "/a"))
	if m := c.mustRead(); m.Type != TypeVerdict || m.ID != 9 {
		t.Errorf("after protocol errors the connection should still answer, got %+v", m)
	}
}

func TestEvaluatorFailuresAreInternalErrorsNeverVerdicts(t *testing.T) {
	b := start(t, Options{Evaluator: EvaluatorFunc(func(_ context.Context, c Check) (string, string, error) {
		switch c.Resource.URI {
		case "file:///boom":
			return "", "", errors.New("boom")
		case "file:///bogus":
			return "maybe", "", nil
		case "file:///empty":
			return "", "", nil
		}
		return VerdictDeny, "policy", nil
	})})
	c := dial(t, b)
	c.hello()
	for id, path := range map[int64]string{1: "/boom", 2: "/bogus", 3: "/empty"} {
		c.send(fileCheck(id, testKey, OpRead, path))
		m := c.mustRead()
		if m.Type != TypeError || m.Code != CodeInternal || m.ID != id || m.Verdict != "" {
			t.Errorf("%s: got %+v, want an internal error, never a verdict", path, m)
		}
	}
	c.send(fileCheck(4, testKey, OpRead, "/x"))
	if m := c.mustRead(); m.Verdict != VerdictDeny || m.Reason != "policy" {
		t.Errorf("got %+v", m)
	}
}

func TestEvaluatorSeesTheCheckDeadline(t *testing.T) {
	var deadline time.Duration
	b := start(t, Options{Evaluator: EvaluatorFunc(func(ctx context.Context, _ Check) (string, string, error) {
		if dl, ok := ctx.Deadline(); ok {
			deadline = time.Until(dl)
		}
		return VerdictAsk, "", nil
	})})
	c := dial(t, b)
	c.hello()
	c.send(fileCheck(1, testKey, OpRead, "/a"))
	if m := c.mustRead(); m.Verdict != VerdictAsk {
		t.Fatalf("got %+v", m)
	}
	if deadline <= 0 || deadline > 100*time.Millisecond {
		t.Errorf("the evaluator's deadline should be the check's deadline_ms (100ms), got %v", deadline)
	}
}

func TestRefusedPeerIsClosedBeforeAnyByte(t *testing.T) {
	b := start(t, Options{PeerCheck: func(net.Conn) error { return errors.New("not you") }})
	c := dial(t, b)
	if m, err := c.read(); err == nil {
		t.Errorf("a refused peer should get nothing, got %+v", m)
	}
}

func TestOversizeLineClosesTheConnection(t *testing.T) {
	b := start(t, Options{})
	c := dial(t, b)
	c.hello()
	if _, err := c.Write(append([]byte(strings.Repeat("x", MaxLine+1)), '\n')); err != nil {
		t.Fatal(err)
	}
	if m, err := c.read(); err == nil {
		t.Errorf("want the connection closed, got %+v", m)
	}
}

func TestCloseEndsOpenConnections(t *testing.T) {
	b := start(t, Options{})
	c := dial(t, b)
	c.hello()
	b.Close()
	if m, err := c.read(); err == nil {
		t.Errorf("want EOF after Close, got %+v", m)
	}
}

func TestListenRefusesIncompleteOptions(t *testing.T) {
	path := filepath.Join(shortDir(t), "b.sock")
	for name, o := range map[string]Options{
		"relative path": {Path: "broker.sock", SessionKey: testKey, Evaluator: EvaluatorFunc(allowAll)},
		"no session":    {Path: path, Evaluator: EvaluatorFunc(allowAll)},
		"no evaluator":  {Path: path, SessionKey: testKey},
	} {
		if b, err := Listen(o); err == nil {
			b.Close()
			t.Errorf("%s: want an error", name)
		}
	}
}
