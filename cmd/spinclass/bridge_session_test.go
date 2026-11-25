package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"
	"code.linenisgreat.com/purse-first/libs/go-mcp/server"

	"code.linenisgreat.com/spinclass/internal/0/bridge"
	"code.linenisgreat.com/spinclass/internal/alfa/bridgeserver"
)

const sessionTestKey = "bridge-test/session-key"

// startSessionBridge listens on a real in-process bridge listener whose
// session service is the real handler over the real tool registry. The peer
// check admits everyone: the unit check is the listener's own tested concern.
func startSessionBridge(t *testing.T) (sock string, srv *bridgeserver.Server) {
	t.Helper()
	return startSessionBridgeWith(t, nil, nil)
}

// startSessionBridgeWith is startSessionBridge with a crossing recorder for the
// session handler and an observer for the listener.
func startSessionBridgeWith(t *testing.T, rec crossingRecorder, obs bridgeserver.Observer) (sock string, srv *bridgeserver.Server) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLOWN_SESSION_ID", "the-serve-principal")
	reg := server.NewToolRegistryV1()
	buildApp().RegisterMCPToolsV1(reg)
	dir := shortBridgeDir(t)
	srv, err := bridgeserver.Listen(dir, "agent.service", bridgeserver.Options{
		SessionKey: sessionTestKey,
		Handler:    newBridgeSessionHandler(reg, "spinclass", "test", rec),
		Observer:   obs,
		PeerCheck:  func(net.Conn, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = srv.Serve(context.Background()); close(done) }()
	t.Cleanup(func() { _ = srv.Close(); <-done })
	return filepath.Join(dir, bridge.SocketName), srv
}

type rpcReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type sessionPeer struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
	next int
}

func dialSession(t *testing.T, sock string) *sessionPeer {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write([]byte("BRIDGE/1 session\n")); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(c)
	line, err := r.ReadString('\n')
	if err != nil || line != "OK\n" {
		t.Fatalf("hello reply %q %v", line, err)
	}
	return &sessionPeer{t: t, conn: c, r: r}
}

func (p *sessionPeer) call(method string, params any) rpcReply {
	p.t.Helper()
	p.next++
	req := map[string]any{"jsonrpc": "2.0", "id": p.next, "method": method, "params": params}
	b, err := json.Marshal(req)
	if err != nil {
		p.t.Fatal(err)
	}
	if _, err := p.conn.Write(append(b, '\n')); err != nil {
		p.t.Fatal(err)
	}
	line, err := p.r.ReadBytes('\n')
	if err != nil {
		p.t.Fatalf("%s: reading the reply: %v", method, err)
	}
	var rep rpcReply
	if err := json.Unmarshal(line, &rep); err != nil {
		p.t.Fatalf("%s: reply %q: %v", method, line, err)
	}
	return rep
}

func (p *sessionPeer) initialize(version string) {
	p.t.Helper()
	rep := p.call("initialize", map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "0"},
	})
	if rep.Error != nil {
		p.t.Fatalf("initialize: %+v", rep.Error)
	}
}

func (p *sessionPeer) toolText(rep rpcReply) (text string, isErr bool) {
	p.t.Helper()
	if rep.Error != nil {
		p.t.Fatalf("a tool result was expected, got the error %+v", rep.Error)
	}
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(rep.Result, &res); err != nil || len(res.Content) == 0 {
		p.t.Fatalf("tool result %s: %v", rep.Result, err)
	}
	return res.Content[0].Text, res.IsError
}

var wantSessionToolNames = []string{
	"cancel-restart", "close-this-session", "list-handles", "restart-this-session",
	"session-health", "update-this-session-description", "whoami",
}

func TestBridgeSessionListsExactlyTheSevenTools(t *testing.T) {
	sock, _ := startSessionBridge(t)
	for _, version := range []string{protocol.ProtocolVersionV0, protocol.ProtocolVersionV1} {
		t.Run(version, func(t *testing.T) {
			p := dialSession(t, sock)
			p.initialize(version)
			rep := p.call("tools/list", map[string]any{})
			if rep.Error != nil {
				t.Fatalf("tools/list: %+v", rep.Error)
			}
			var list struct {
				Tools []struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(rep.Result, &list); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, tool := range list.Tools {
				got = append(got, tool.Name)
				if tool.Description == "" {
					t.Errorf("tool %s lists without its definition", tool.Name)
				}
			}
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(wantSessionToolNames, ",") {
				t.Errorf("tools/list = %v, want exactly %v", got, wantSessionToolNames)
			}
		})
	}
}

// The identity of a bridged call is the listener's session, whatever the
// arguments or the request body claim.
func TestBridgeSessionWhoamiIsThisSessionsAndIgnoresForgedIdentity(t *testing.T) {
	sock, _ := startSessionBridge(t)
	p := dialSession(t, sock)
	p.initialize(protocol.ProtocolVersionV0)
	rep := p.call("tools/call", map[string]any{
		"name":      "whoami",
		"arguments": map[string]any{"principal": "forged-principal", "session_key": "forged/key"},
		"principal": "forged-principal",
	})
	text, isErr := p.toolText(rep)
	if isErr {
		t.Fatalf("whoami failed: %s", text)
	}
	if !strings.Contains(text, "session_key: "+sessionTestKey+"\n") || !strings.Contains(text, "principal: the-serve-principal\n") {
		t.Errorf("whoami is not this session's:\n%s", text)
	}
	if strings.Contains(text, "forged") {
		t.Errorf("whoami echoed a forged identity:\n%s", text)
	}
}

// A verb a session inside its unit may not use is a JSON-RPC error with code
// -32001 and the contract text, not a tool result.
func TestBridgeSessionRefusesConfinedVerbsWithTheContractError(t *testing.T) {
	sock, _ := startSessionBridge(t)
	p := dialSession(t, sock)
	p.initialize(protocol.ProtocolVersionV1)
	for tool, want := range map[string]string{
		"session-health":                  refusalSessionHealth,
		"update-this-session-description": refusalUpdateDesc,
	} {
		rep := p.call("tools/call", map[string]any{"name": tool, "arguments": map[string]any{"description": "x"}})
		if rep.Error == nil {
			t.Fatalf("%s: want an error, got %s", tool, rep.Result)
		}
		if rep.Error.Code != -32001 || rep.Error.Message != want {
			t.Errorf("%s: got code %d %q\nwant -32001 %q", tool, rep.Error.Code, rep.Error.Message, want)
		}
	}
	// The open half of a mixed verb still works, and a gated form is refused
	// whatever session its arguments name.
	rep := p.call("tools/call", map[string]any{"name": "list-handles", "arguments": map[string]any{"principal": "forged"}})
	if text, isErr := p.toolText(rep); isErr || text != "no handles" {
		t.Errorf("plain list-handles: %v %q", isErr, text)
	}
	rep = p.call("tools/call", map[string]any{"name": "cancel-restart", "arguments": map[string]any{"child": "someone/else"}})
	if rep.Error == nil || rep.Error.Code != -32001 || !strings.HasPrefix(rep.Error.Message, "refused: cancel-restart ") {
		t.Errorf("cancel-restart of another session: %+v", rep.Error)
	}
}

// Everything but the seven is not listed, and calling it is a JSON-RPC error.
func TestBridgeSessionUnknownToolIsAnError(t *testing.T) {
	sock, _ := startSessionBridge(t)
	p := dialSession(t, sock)
	p.initialize(protocol.ProtocolVersionV1)
	for _, tool := range []string{"merge-this-session", "spawn-session", "grit_status", "stop-this-session", "no-such-tool"} {
		rep := p.call("tools/call", map[string]any{"name": tool, "arguments": map[string]any{}})
		if rep.Error == nil || rep.Result != nil {
			t.Errorf("%s: want a JSON-RPC error, got result %s error %+v", tool, rep.Result, rep.Error)
			continue
		}
		if !strings.Contains(rep.Error.Message, "unknown tool: "+tool) {
			t.Errorf("%s: message %q", tool, rep.Error.Message)
		}
	}
}

// The peer closing its write side ends the handler: the listener closes the
// stream after it, so the peer reads EOF.
func TestBridgeSessionEOFEndsTheHandler(t *testing.T) {
	sock, _ := startSessionBridge(t)
	p := dialSession(t, sock)
	p.initialize(protocol.ProtocolVersionV0)
	if err := p.conn.(*net.UnixConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.r.ReadByte(); err != io.EOF {
		t.Errorf("after the peer's EOF the stream should end, got %v", err)
	}
}

// Each stream gets its own handler: a second stream initializes and calls on
// its own, and ending it leaves the first one alive.
func TestBridgeSessionSecondStreamHasItsOwnHandler(t *testing.T) {
	sock, _ := startSessionBridge(t)
	a := dialSession(t, sock)
	a.initialize(protocol.ProtocolVersionV0)
	b := dialSession(t, sock)
	b.initialize(protocol.ProtocolVersionV1)
	if text, isErr := b.toolText(b.call("tools/call", map[string]any{"name": "whoami", "arguments": map[string]any{}})); isErr || !strings.Contains(text, sessionTestKey) {
		t.Errorf("second stream whoami: %v %q", isErr, text)
	}
	_ = b.conn.Close()
	if text, isErr := a.toolText(a.call("tools/call", map[string]any{"name": "whoami", "arguments": map[string]any{}})); isErr || !strings.Contains(text, sessionTestKey) {
		t.Errorf("first stream after the second ended: %v %q", isErr, text)
	}
}

// Closing the listener ends an idle stream and waits for its handler.
func TestBridgeSessionCloseEndsAnOpenStream(t *testing.T) {
	sock, srv := startSessionBridge(t)
	p := dialSession(t, sock)
	p.initialize(protocol.ProtocolVersionV0)
	closed := make(chan struct{})
	go func() { _ = srv.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return while a session stream was open")
	}
	if _, err := p.r.ReadByte(); err == nil {
		t.Error("the stream is still open after Close")
	}
}
