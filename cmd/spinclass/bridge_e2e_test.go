package main

// Hermetic end-to-end tests of the bridge, as serve wires it: the real
// listener over a real unix socket in a short directory, the real session
// handler over the real tool registry (built the way serve builds it), the real
// crossing log and observer, a fake statsd UDP listener, and a fake peer that
// speaks the wire contract through internal/0/bridge. No unit is involved: the
// peer check is injected (admit, or "outside the unit"), except in the one test
// that runs the listener's own check.
//
// A security-boundary review reads this file first, so each test names the
// property it asserts and states how it could fail.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/jsonrpc"
	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"
	"code.linenisgreat.com/purse-first/libs/go-mcp/server"

	"code.linenisgreat.com/spinclass/internal/0/bridge"
	"code.linenisgreat.com/spinclass/internal/0/statsd"
	"code.linenisgreat.com/spinclass/internal/alfa/bridgeserver"
)

const (
	// A UUID: restart-this-session refuses any other shape of principal.
	e2ePrincipal = "7a1c93d2-4b58-4e0a-9f26-3c8d51e7b0a4"
	e2eUnit      = "agent.service"
	// e2eVictim is the session every forgery below tries to act as.
	e2eVictim = "victim/session"
	// e2eMaxRequestLine is the bound the contract puts on one request line of the
	// session service (the brief's 1 MiB).
	e2eMaxRequestLine = 1 << 20
)

// e2eHandlerFactory is the handler constructor serve passes to startBridge
// (commands_mcp.go), over a registry built the way serve builds it.
func e2eHandlerFactory() func(rec crossingRecorder, stats statsd.Client) bridgeserver.SessionHandler {
	app := buildApp()
	registry := server.NewToolRegistryV1()
	app.RegisterMCPToolsV1(registry)
	return func(rec crossingRecorder, stats statsd.Client) bridgeserver.SessionHandler {
		return newBridgeSessionHandler(registry, app.Name, app.Version, rec, stats)
	}
}

type e2eOpts struct {
	peer         func(net.Conn, string) error // nil admits every peer
	helloTimeout time.Duration                // zero is the contract's 5 s
	// liars stages two real sessions (the listener's A and a victim B), the
	// trusted settings that allow restart and self-close, and an environment
	// and principal mapping that both name B (see newLiarFixture).
	liars bool
}

// e2eBridge is the one small fixture every test shares.
type e2eBridge struct {
	t       *testing.T
	state   string
	dir     string
	sock    string
	logPath string
	liars   liarFixture // set when e2eOpts.liars
	cancel  context.CancelFunc
	stop    func()
	drain   func() []string

	mu      sync.Mutex
	logf    []string
	metrics []string
}

func startE2E(t *testing.T, o e2eOpts) *e2eBridge {
	t.Helper()
	b := &e2eBridge{t: t, state: t.TempDir(), dir: shortBridgeDir(t)}
	t.Setenv("XDG_STATE_HOME", b.state)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", e2ePrincipal)
	b.drain = queueStatsdSink(t)
	b.sock = filepath.Join(b.dir, bridge.SocketName)
	// The listener's session is a real record, as it is in serve: a bridged call
	// acts on the record its origin names.
	listenerKey := sessionTestKey
	if o.liars {
		b.liars = stageLiars(t)
		listenerKey = b.liars.a.key
	} else {
		stageSessionKey(t, sessionTestKey, e2ePrincipal)
	}
	b.logPath = filepath.Join(b.state, "spinclass", "bridge", currentPrincipal()+".ndjson")

	clog := openBridgeCrossingLog()
	stats := statsd.Resolve("", false)
	peer := o.peer
	if peer == nil {
		peer = func(net.Conn, string) error { return nil }
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.stop = startBridgeFrom(ctx, bridgePlan{Dir: b.dir, Unit: e2eUnit}, bridgeserver.Options{
		SessionKey:   listenerKey,
		Handler:      e2eHandlerFactory()(clog, stats),
		Observer:     &bridgeObserver{log: clog, stats: stats},
		PeerCheck:    peer,
		HelloTimeout: o.helloTimeout,
		Logf: func(format string, args ...any) {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.logf = append(b.logf, fmt.Sprintf(format, args...))
		},
	})
	if b.stop == nil {
		cancel()
		t.Fatal("the listener did not start")
	}
	t.Cleanup(func() { cancel(); b.stop() })
	return b
}

func (b *e2eBridge) logLines() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.logf, "\n")
}

// waitMetric waits until every datagram in want has reached the fake statsd.
func (b *e2eBridge) waitMetric(want ...string) {
	b.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b.metrics = append(b.metrics, b.drain()...)
		missing := ""
		for _, w := range want {
			found := false
			for _, m := range b.metrics {
				if m == w {
					found = true
				}
			}
			if !found {
				missing = w
			}
		}
		if missing == "" {
			return
		}
		if time.Now().After(deadline) {
			b.t.Fatalf("statsd never saw %q; saw %v", missing, b.metrics)
		}
	}
}

func (b *e2eBridge) dial() net.Conn {
	b.t.Helper()
	c, err := net.Dial("unix", b.sock)
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	return c
}

// open says hello for service as a contract-speaking shim does (reading the
// reply byte at a time) and returns the open stream.
func (b *e2eBridge) open(service string) *sessionPeer {
	b.t.Helper()
	c := b.dial()
	hello, err := bridge.FormatHello(bridge.ServerMajor, service)
	if err != nil {
		b.t.Fatal(err)
	}
	if _, err := c.Write(hello); err != nil {
		b.t.Fatal(err)
	}
	ok, code, text, err := bridge.ReadReply(c)
	if err != nil || !ok {
		b.t.Fatalf("hello %s: ok=%v code=%q text=%q err=%v", service, ok, code, text, err)
	}
	return &sessionPeer{t: b.t, conn: c, r: bufio.NewReader(c)}
}

// exchange sends raw bytes and returns everything the server answers up to the
// close.
func (b *e2eBridge) exchange(raw string) string {
	b.t.Helper()
	return dialSessionRaw(b.t, b.sock, raw)
}

func (b *e2eBridge) crossings(n int) []map[string]any {
	b.t.Helper()
	es := waitForCrossings(b.t, b.logPath, n)
	if len(es) < n {
		b.t.Fatalf("crossing log has %d lines, want at least %d: %v", len(es), n, es)
	}
	return es
}

func (b *e2eBridge) whoami(p *sessionPeer, args map[string]any) string {
	b.t.Helper()
	text, isErr := p.toolText(p.call("tools/call", map[string]any{"name": "whoami", "arguments": args}))
	if isErr {
		b.t.Fatalf("whoami failed: %s", text)
	}
	return text
}

func (b *e2eBridge) wantThisSession(text string) {
	b.t.Helper()
	if !strings.Contains(text, "session_key: "+sessionTestKey+"\n") || !strings.Contains(text, "principal: "+e2ePrincipal+"\n") {
		b.t.Errorf("not this session's identity:\n%s", text)
	}
	if strings.Contains(text, "victim") || strings.Contains(text, "forged") {
		b.t.Errorf("a forged identity reached the answer:\n%s", text)
	}
}

func initializeRequest(id int) string {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "initialize", "params": map[string]any{
		"protocolVersion": protocol.ProtocolVersionV0,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "e2e", "version": "0"},
	}})
	return string(b) + "\n"
}

// 1. Happy path: every real piece answers, and each leaves its trace.
func TestE2EHappyPath(t *testing.T) {
	const secret = "sk-live-e2e-secret"
	b := startE2E(t, e2eOpts{})
	p := b.open("session")
	p.initialize(protocol.ProtocolVersionV1)

	rep := p.call("tools/list", map[string]any{})
	if rep.Error != nil {
		t.Fatalf("tools/list: %+v", rep.Error)
	}
	var list struct {
		Tools []struct{ Name string } `json:"tools"`
	}
	if err := json.Unmarshal(rep.Result, &list); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tool := range list.Tools {
		got = append(got, tool.Name)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(wantSessionToolNames, ",") {
		t.Errorf("tools/list = %v, want exactly %v", got, wantSessionToolNames)
	}

	b.wantThisSession(b.whoami(p, map[string]any{"principal": secret}))

	es := b.crossings(1)
	raw, err := os.ReadFile(b.logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("an argument value reached the crossing log:\n%s", raw)
	}
	e := es[0]
	if e["verb"] != "whoami" || e["outcome"] != "ok" || e["service"] != "session" || e["session"] != sessionTestKey {
		t.Errorf("crossing line = %v", e)
	}
	if jsonString(e["arg_names"]) != "[principal]" {
		t.Errorf("arg_names = %v", e["arg_names"])
	}
	if h, _ := e["args_sha256"].(string); len(h) != 16 {
		t.Errorf("args_sha256 = %v, want 16 hex characters", e["args_sha256"])
	}

	// The metric is counted when the stream ends.
	_ = p.conn.Close()
	b.waitMetric("spinclass.bridge.session.ok:1|c")
}

// 2. A forged principal: nothing the peer sends names whose session is acted
// on. The identity is the listener's; the victim's record is not touched.
func TestE2EForgedPrincipalNeverChangesWhoseSessionIsActedOn(t *testing.T) {
	b := startE2E(t, e2eOpts{})
	victimRecord := filepath.Join(b.state, "spinclass", "records", "victim-session", "record.json")
	if err := os.MkdirAll(filepath.Dir(victimRecord), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victimRecord, []byte(`{"victim":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, filepath.Join(b.state, "spinclass", "records"))

	forged := map[string]any{
		"session": e2eVictim, "principal": e2eVictim, "child": e2eVictim,
		"target": e2eVictim, "session_key": e2eVictim,
	}
	p := b.open("session")
	p.initialize(protocol.ProtocolVersionV0)

	b.wantThisSession(b.whoami(p, forged))
	// The request envelope claiming an identity is not an identity either.
	rep := p.call("tools/call", map[string]any{
		"name": "whoami", "arguments": forged,
		"session": e2eVictim, "principal": e2eVictim, "_meta": map[string]any{"session": e2eVictim},
	})
	text, _ := p.toolText(rep)
	b.wantThisSession(text)

	// Every verb, with the forgery: the ones the bridge refuses are refused
	// (-32001) whoever the arguments name.
	for _, tool := range []string{"cancel-restart", "session-health", "update-this-session-description"} {
		rep := p.call("tools/call", map[string]any{"name": tool, "arguments": forged})
		if rep.Error == nil || rep.Error.Code != -32001 {
			t.Errorf("%s with forged names: want -32001, got %+v result %s", tool, rep.Error, rep.Result)
		}
	}
	// The open verbs and the ones that act on "this session" ignore the names;
	// whatever they answer, it is about this session or an error, never the victim.
	for _, tool := range []string{"list-handles", "restart-this-session", "close-this-session"} {
		rep := p.call("tools/call", map[string]any{"name": tool, "arguments": forged})
		var reply string
		if rep.Error != nil {
			reply = rep.Error.Message
		} else {
			reply = string(rep.Result)
		}
		if strings.Contains(reply, e2eVictim) {
			t.Errorf("%s answered with the forged session: %s", tool, reply)
		}
	}

	// Stream-level forgeries: a header-looking line, a second hello and a bare
	// identity object after OK are service bytes, not identity.
	noise := "X-Bridge-Session: " + e2eVictim + "\nBRIDGE/1 session\n{\"principal\":\"" + e2eVictim + "\",\"session\":\"" + e2eVictim + "\"}\n"
	if _, err := p.conn.Write([]byte(noise)); err != nil {
		t.Fatal(err)
	}
	b.wantThisSession(whoamiSkippingNoise(t, p))
	// And a fresh stream is this session too.
	b.wantThisSession(b.whoami(func() *sessionPeer { q := b.open("session"); q.initialize(protocol.ProtocolVersionV0); return q }(), forged))

	// Before the hello, the same lines are a malformed hello and nothing more.
	if got := b.exchange("X-Bridge-Session: " + e2eVictim + "\nBRIDGE/1 session\n"); got != string(bridge.MalformedReply()) {
		t.Errorf("identity header before the hello: reply %q", got)
	}

	after := snapshotTree(t, filepath.Join(b.state, "spinclass", "records"))
	if !equalSnapshots(before, after) {
		t.Errorf("the session records changed:\nbefore %v\nafter  %v", before, after)
	}
	if got, err := os.ReadFile(victimRecord); err != nil || string(got) != `{"victim":true}` {
		t.Errorf("the victim's record = %q %v", got, err)
	}
}

// whoamiSkippingNoise asks whoami on a stream that has just been fed garbage
// lines: the server may answer them with errors or end the stream; either way
// no garbage line may have set an identity. A stream that ended falls back to
// the caller's second check (a fresh stream).
func whoamiSkippingNoise(t *testing.T, p *sessionPeer) string {
	t.Helper()
	p.next++
	id := p.next
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": "whoami", "arguments": map[string]any{}}})
	if _, err := p.conn.Write(append(req, '\n')); err != nil {
		return "session_key: " + sessionTestKey + "\nprincipal: " + e2ePrincipal + "\n" // stream ended: nothing was served on it
	}
	for i := 0; i < 20; i++ {
		line, err := p.r.ReadBytes('\n')
		if err != nil {
			return "session_key: " + sessionTestKey + "\nprincipal: " + e2ePrincipal + "\n" // stream ended
		}
		var rep rpcReply
		if json.Unmarshal(line, &rep) != nil || string(rep.ID) != fmt.Sprint(id) {
			continue
		}
		text, _ := p.toolText(rep)
		return text
	}
	t.Fatal("no answer to whoami after the noise")
	return ""
}

// streamEnded says err is the peer's view of the server ending the stream (a
// close with unread input resets a unix socket instead of a clean EOF).
func streamEnded(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(path)
		fi, serr := d.Info()
		if rerr != nil || serr != nil {
			out[path] = "unreadable"
			return nil
		}
		sum := sha256.Sum256(b)
		out[path] = hex.EncodeToString(sum[:]) + fmt.Sprint(fi.ModTime().UnixNano())
		return nil
	})
	return out
}

func equalSnapshots(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// 3. A peer outside the unit, or a check that cannot tell, is refused with the
// contract text, served nothing, counted and logged.
func TestE2EWrongUnitPeerIsRefusedAndNothingIsServed(t *testing.T) {
	for name, check := range map[string]func(net.Conn, string) error{
		"outside the unit":        func(_ net.Conn, unit string) error { return fmt.Errorf("pid 4242 does not run in %s", unit) },
		"the check itself errors": func(net.Conn, string) error { return errors.New("reading /proc/4242/cgroup: permission denied") },
	} {
		t.Run(name, func(t *testing.T) {
			b := startE2E(t, e2eOpts{peer: check})
			hello := "BRIDGE/1 session\n"
			whoami := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"whoami","arguments":{}}}` + "\n"
			got := b.exchange(hello + initializeRequest(1) + whoami)
			want := string(bridge.FormatErr(bridge.CodeRefused, bridgeserver.RefusedPeerText))
			if got != want {
				t.Fatalf("reply %q, want exactly %q (and no JSON-RPC answer)", got, want)
			}
			if !strings.Contains(got, "does not come from this session's unit") {
				t.Errorf("the refusal lacks the contract text: %q", got)
			}
			es := b.crossings(1)
			if len(es) != 1 || es[0]["verb"] != "hello" || es[0]["outcome"] != "refused" || es[0]["service"] != "unknown" {
				t.Errorf("crossing lines = %v, want one hello/refused line and no served call", es)
			}
			b.waitMetric("spinclass.bridge.unknown.refused:1|c")
			if l := b.logLines(); !strings.Contains(l, "refused a peer outside unit "+e2eUnit) {
				t.Errorf("the refusal was not logged: %q", l)
			}
		})
	}
}

// 3b. The listener's own check (no injection): this test process is not in the
// unit named agent.service, so serve's real wiring refuses it.
func TestE2ERealPeerCheckRefusesAProcessOutsideTheUnit(t *testing.T) {
	dir := shortBridgeDir(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", e2ePrincipal)
	t.Setenv("SPINCLASS_SESSION_ID", sessionTestKey)
	old := bridgeResolve
	bridgeResolve = func() bridgePlan { return bridgePlan{Dir: dir, Unit: e2eUnit} }
	t.Cleanup(func() { bridgeResolve = old })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := startBridge(ctx, e2eHandlerFactory())
	if stop == nil {
		t.Fatal("serve's startBridge did not listen")
	}
	defer stop()
	got := dialSessionRaw(t, filepath.Join(dir, bridge.SocketName), "BRIDGE/1 session\n"+initializeRequest(1))
	want := string(bridge.FormatErr(bridge.CodeRefused, bridgeserver.RefusedPeerText))
	if got != want {
		t.Errorf("reply %q, want %q", got, want)
	}
}

// 4. No hello within the timeout: the connection closes with no reply.
func TestE2EHelloTimeoutClosesWithNoReply(t *testing.T) {
	b := startE2E(t, e2eOpts{helloTimeout: 150 * time.Millisecond})
	for name, partial := range map[string]string{"nothing sent": "", "a hello with no newline": "BRIDGE/1 ses"} {
		t.Run(name, func(t *testing.T) {
			c := b.dial()
			if partial != "" {
				if _, err := c.Write([]byte(partial)); err != nil {
					t.Fatal(err)
				}
			}
			started := time.Now()
			got, err := io.ReadAll(c)
			if err != nil || len(got) != 0 {
				t.Fatalf("read %q %v, want a clean close with no bytes", got, err)
			}
			if d := time.Since(started); d < 100*time.Millisecond || d > 4*time.Second {
				t.Errorf("closed after %v, want about the injected 150ms", d)
			}
		})
	}
	b.crossings(2)
	b.waitMetric("spinclass.bridge.unknown.hello-timeout:1|c")
	for _, e := range b.crossings(2) {
		if e["outcome"] != "hello-timeout" || e["verb"] != "hello" {
			t.Errorf("crossing line = %v", e)
		}
	}
}

// 5. A label the server does not serve, reserved or not, is unknown-service.
func TestE2EUnservedLabelsAreUnknownService(t *testing.T) {
	b := startE2E(t, e2eOpts{})
	labels := []string{"tools", "hook", "keys", "egress", "chat", "model", "status", "nosuch"}
	for _, label := range labels {
		if got, want := b.exchange("BRIDGE/1 "+label+"\n"), "ERR unknown-service "+label+"\n"; got != want {
			t.Errorf("%s: reply %q, want %q", label, got, want)
		}
	}
	es := b.crossings(len(labels))
	for _, e := range es {
		if e["outcome"] != "refused" || e["verb"] != "hello" {
			t.Errorf("crossing line = %v, want hello/refused", e)
		}
	}
}

// 6. The version mismatch bytes are the vectors' bytes.
func TestE2EVersionMismatchBytesEqualTheVectors(t *testing.T) {
	// Copied verbatim from internal/0/bridge/testdata/vectors.json (the file is
	// not in the test sandbox, which holds only this package's sources); the
	// bridge package's own tests drive the whole file and pin its digest.
	vectors := []struct{ name, input, reply string }{
		{"major 2 is a version error", "BRIDGE/2 egress\n", "ERR version server=1 shim=2: rebuild or restart the session so the shim and the frontend match\n"},
		{"major 999 is a version error", "BRIDGE/999 control\n", "ERR version server=1 shim=999: rebuild or restart the session so the shim and the frontend match\n"},
		{"version is judged before the label is served", "BRIDGE/2 frobnicate\n", "ERR version server=1 shim=2: rebuild or restart the session so the shim and the frontend match\n"},
	}
	b := startE2E(t, e2eOpts{})
	for _, v := range vectors {
		if got := b.exchange(v.input); got != v.reply {
			t.Errorf("%s: reply %q, want the vector's %q", v.name, got, v.reply)
		}
	}
	es := b.crossings(len(vectors))
	for _, e := range es {
		if e["outcome"] != "error" || e["verb"] != "hello" {
			t.Errorf("crossing line = %v, want hello/error", e)
		}
	}
	// And the vector's own malformed-hello reply is what a malformed hello gets.
	if got := b.exchange("bridge/1 session\n"); got != string(bridge.MalformedReply()) {
		t.Errorf("malformed hello: %q", got)
	}
}

// 7. Stream limits and the control stream.
func TestE2E65thStreamIsBusy(t *testing.T) {
	b := startE2E(t, e2eOpts{})
	var held []*sessionPeer
	for i := 0; i < bridge.MaxStreamsPerSession; i++ {
		held = append(held, b.open("session"))
	}
	c := b.dial()
	if _, err := c.Write([]byte("BRIDGE/1 session\n")); err != nil {
		t.Fatal(err)
	}
	ok, code, text, err := bridge.ReadReply(c)
	if err != nil || ok || code != bridge.CodeBusy {
		t.Fatalf("65th stream: ok=%v code=%q text=%q err=%v, want ERR busy", ok, code, text, err)
	}
	b.waitMetric("spinclass.bridge.session.busy:1|c")
	// The first 64 are unharmed.
	b.wantThisSession(b.whoami(held[0], map[string]any{}))
}

func TestE2ESecondControlIsBusyAndEOFReachesThePeerOnServerClose(t *testing.T) {
	b := startE2E(t, e2eOpts{})
	ctl := b.open("control")
	c := b.dial()
	if _, err := c.Write([]byte("BRIDGE/1 control\n")); err != nil {
		t.Fatal(err)
	}
	if ok, code, text, err := bridge.ReadReply(c); err != nil || ok || code != bridge.CodeBusy {
		t.Fatalf("second control: ok=%v code=%q text=%q err=%v, want ERR busy", ok, code, text, err)
	}
	// The first control is still open (nothing was pushed or closed).
	_ = ctl.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := ctl.r.ReadByte(); err == nil || err == io.EOF {
		t.Fatalf("control stream ended before the server closed: %v", err)
	}
	_ = ctl.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	b.stop()
	if _, err := ctl.r.ReadByte(); err != io.EOF {
		t.Errorf("after the server closed, the control peer read %v, want EOF", err)
	}
}

// 8. A refused verb is -32001 with the contract text; an unlisted tool is
// -32602; both are in the crossing log with the right verb and outcome.
func TestE2EConfinedVerbIsMinus32001AndUnlistedToolIsMinus32602(t *testing.T) {
	b := startE2E(t, e2eOpts{})
	p := b.open("session")
	p.initialize(protocol.ProtocolVersionV1)

	rep := p.call("tools/call", map[string]any{"name": "session-health", "arguments": map[string]any{}})
	const want = refusalSessionHealth
	if rep.Error == nil || rep.Error.Code != -32001 || rep.Error.Message != want {
		t.Errorf("session-health: %+v, want -32001 %q", rep.Error, want)
	}
	rep = p.call("tools/call", map[string]any{"name": "update-this-session-description", "arguments": map[string]any{"description": "x"}})
	if rep.Error == nil || rep.Error.Code != -32001 || !strings.HasPrefix(rep.Error.Message, "refused: update-this-session-description ") {
		t.Errorf("update-this-session-description: %+v", rep.Error)
	}
	rep = p.call("tools/call", map[string]any{"name": "merge-this-session", "arguments": map[string]any{}})
	if rep.Error == nil || rep.Error.Code != jsonrpc.InvalidParams || !strings.Contains(rep.Error.Message, "unknown tool: merge-this-session") {
		t.Errorf("an unlisted tool: %+v, want -32602", rep.Error)
	}

	es := b.crossings(3)
	type line struct{ verb, outcome string }
	wantLines := []line{{"session-health", "refused"}, {"update-this-session-description", "refused"}, {"unknown", "error"}}
	for i, w := range wantLines {
		if got := (line{es[i]["verb"].(string), es[i]["outcome"].(string)}); got != w {
			t.Errorf("crossing line %d = %+v, want %+v", i, got, w)
		}
	}
}

// 9. A request line over the bound, or one that never ends, ends the stream
// without exhausting memory; a line under the bound is served.
func TestE2EOversizedRequestLineDoesNotExhaustMemory(t *testing.T) {
	b := startE2E(t, e2eOpts{})

	t.Run("a line that never ends", func(t *testing.T) {
		p := b.open("session")
		p.initialize(protocol.ProtocolVersionV0)
		_ = p.conn.SetDeadline(time.Now().Add(30 * time.Second))
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		chunk := bytes.Repeat([]byte("a"), 64<<10)
		const budget = 64 << 20
		sent := 0
		for sent < budget {
			n, err := p.conn.Write(chunk)
			sent += n
			if err != nil {
				break
			}
		}
		runtime.ReadMemStats(&after)
		if sent >= 8*e2eMaxRequestLine {
			t.Errorf("the server consumed %d bytes of one unterminated line; it must stop at about %d", sent, e2eMaxRequestLine)
		}
		if grown := after.TotalAlloc - before.TotalAlloc; grown > 48<<20 {
			t.Errorf("%d MiB allocated while reading an unterminated line", grown>>20)
		}
		if _, err := io.Copy(io.Discard, p.conn); err != nil && !streamEnded(err) {
			t.Errorf("the stream did not end after the oversized line: %v", err)
		}
	})

	t.Run("a terminated line over 1 MiB", func(t *testing.T) {
		p := b.open("session")
		p.initialize(protocol.ProtocolVersionV0)
		big := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"whoami","arguments":{"pad":"` +
			strings.Repeat("a", e2eMaxRequestLine+1) + `"}}}` + "\n"
		_, _ = p.conn.Write([]byte(big))
		var answered bool
		for {
			line, err := p.r.ReadBytes('\n')
			if len(line) > 0 && strings.Contains(string(line), `"result"`) {
				answered = true
			}
			if err != nil {
				if !streamEnded(err) {
					t.Fatalf("the stream did not end after an oversized line: %v", err)
				}
				break
			}
		}
		if answered {
			t.Error("an oversized request line was served")
		}
	})

	t.Run("a line under the bound is served", func(t *testing.T) {
		p := b.open("session")
		p.initialize(protocol.ProtocolVersionV0)
		pad := strings.Repeat("a", e2eMaxRequestLine*3/4)
		b.wantThisSession(b.whoami(p, map[string]any{"pad": pad}))
	})
}

// 10. The server sends no byte before the hello is answered, nor any service
// byte before OK, and does not swallow service bytes sent with the hello.
func TestE2EServerSendsNoServiceByteBeforeOK(t *testing.T) {
	b := startE2E(t, e2eOpts{})

	c := b.dial()
	_ = c.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	if n, err := c.Read(make([]byte, 16)); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("before the hello the server sent %d bytes (%v)", n, err)
	}

	// The hello and the first MCP request in one write: the reply is OK\n
	// alone, read byte at a time, and the request that followed is answered.
	c = b.dial()
	if _, err := c.Write([]byte("BRIDGE/1 session\n" + initializeRequest(1))); err != nil {
		t.Fatal(err)
	}
	ok, _, _, err := bridge.ReadReply(c)
	if err != nil || !ok {
		t.Fatalf("reply: ok=%v err=%v", ok, err)
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil || !strings.Contains(string(line), `"result"`) || !strings.HasPrefix(string(line), "{") {
		t.Errorf("the first service bytes after OK were %q (%v); the pipelined initialize should be answered", line, err)
	}

	// After OK, and with nothing asked, the server stays silent.
	q := b.open("session")
	_ = q.conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	if n, err := q.r.Read(make([]byte, 16)); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("after OK, unprompted, the server sent %d bytes (%v)", n, err)
	}
}

// 11. The socket goes when serve's listener closes, however it closes, and a
// successor's socket at the same path survives the old listener's close.
func TestE2ESocketIsRemovedWhenTheListenerCloses(t *testing.T) {
	t.Run("stop", func(t *testing.T) {
		b := startE2E(t, e2eOpts{})
		if _, err := os.Lstat(b.sock); err != nil {
			t.Fatal(err)
		}
		b.stop()
		if _, err := os.Lstat(b.sock); !os.IsNotExist(err) {
			t.Errorf("the socket remains after stop: %v", err)
		}
	})
	t.Run("serve's context ends", func(t *testing.T) {
		b := startE2E(t, e2eOpts{})
		b.cancel()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Lstat(b.sock); os.IsNotExist(err) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("the socket remains after the context ended")
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

func TestE2ESuccessorsSocketSurvivesTheOldListenersClose(t *testing.T) {
	old := startE2E(t, e2eOpts{})
	if err := os.Remove(old.sock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopSuccessor := startBridgeFrom(ctx, bridgePlan{Dir: old.dir, Unit: e2eUnit}, bridgeserver.Options{
		SessionKey: "successor/key",
		PeerCheck:  func(net.Conn, string) error { return nil },
	})
	if stopSuccessor == nil {
		t.Fatal("the successor did not listen")
	}
	defer stopSuccessor()

	old.stop()
	if _, err := os.Lstat(old.sock); err != nil {
		t.Fatalf("the old listener's close removed the successor's socket: %v", err)
	}
	c := old.dial()
	if _, err := c.Write([]byte("BRIDGE/1 control\n")); err != nil {
		t.Fatal(err)
	}
	if ok, code, text, err := bridge.ReadReply(c); err != nil || !ok {
		t.Errorf("the successor does not answer: ok=%v code=%q text=%q err=%v", ok, code, text, err)
	}
	_ = c.Close()
	stopSuccessor()
	if _, err := os.Lstat(old.sock); !os.IsNotExist(err) {
		t.Errorf("the successor's own close left its socket: %v", err)
	}
}

// 12. With the switch off there is no socket at all: no env, no [bridge]
// enable, or the enable only in a repo layer. The control case with everything
// on makes a socket, so the checks above could fail.
func TestE2ESwitchOffMeansNoSocket(t *testing.T) {
	cases := []struct {
		name    string
		env     bool
		global  string
		repo    string
		wantSet bool
	}{
		{name: "no env, trusted enable", global: "[bridge]\nenable = true\n"},
		{name: "env, no [bridge] enable", env: true},
		{name: "env, enable = false in a trusted layer", env: true, global: "[bridge]\nenable = false\n"},
		{name: "env, enable only in the repo layer", env: true, repo: "[bridge]\nenable = true\n"},
		{name: "control: env and trusted enable", env: true, global: "[bridge]\nenable = true\n", wantSet: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			repo := filepath.Join(home, "work", "repo")
			if err := os.MkdirAll(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			if c.global != "" {
				writeBridgeSweatfile(t, filepath.Join(home, ".config", "spinclass", "sweatfile"), c.global)
			}
			if c.repo != "" {
				writeBridgeSweatfile(t, filepath.Join(repo, "sweatfile"), c.repo)
			}
			dir := shortBridgeDir(t)
			getenv := bridgeTestEnv("", "")
			if c.env {
				getenv = bridgeTestEnv(dir, e2eUnit)
			}
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("HOME", home)
			t.Setenv("CLOWN_SESSION_ID", e2ePrincipal)
			t.Setenv("SPINCLASS_SESSION_ID", sessionTestKey)
			old := bridgeResolve
			bridgeResolve = func() bridgePlan { return resolveBridgePlan(getenv, home, repo) }
			t.Cleanup(func() { bridgeResolve = old })

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stop := startBridge(ctx, e2eHandlerFactory())
			if stop != nil {
				defer stop()
			}
			_, err := os.Lstat(filepath.Join(dir, bridge.SocketName))
			switch {
			case c.wantSet && (stop == nil || err != nil):
				t.Fatalf("the control case made no socket: stop=%v err=%v", stop != nil, err)
			case !c.wantSet && (stop != nil || !os.IsNotExist(err)):
				t.Errorf("the switch is off but stop=%v and the socket lstat is %v", stop != nil, err)
			}
			if entries, _ := os.ReadDir(dir); !c.wantSet && len(entries) != 0 {
				t.Errorf("the bridge directory is not empty: %v", entries)
			}
		})
	}
}
