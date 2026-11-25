package main

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/jsonrpc"
	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"
	"code.linenisgreat.com/purse-first/libs/go-mcp/server"
	"code.linenisgreat.com/purse-first/libs/go-mcp/transport"
)

func okResult() *protocol.ToolCallResultV1 {
	return &protocol.ToolCallResultV1{Content: []protocol.ContentBlockV1{protocol.TextContentV1("ok")}}
}

// registryWith registers the named tools with one handler.
func registryWith(h server.ToolHandlerV1, names ...string) *server.ToolRegistryV1 {
	reg := server.NewToolRegistryV1()
	for _, n := range names {
		reg.Register(protocol.ToolV1{Name: n, InputSchema: json.RawMessage(`{"type":"object"}`)}, h)
	}
	return reg
}

func TestLifecycleVerbsOfOneSessionNeverRunAtOnce(t *testing.T) {
	var active, maxActive atomic.Int32
	h := func(context.Context, json.RawMessage) (*protocol.ToolCallResultV1, error) {
		n := active.Add(1)
		for {
			m := maxActive.Load()
			if n <= m || maxActive.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		active.Add(-1)
		return okResult(), nil
	}
	reg := registryWith(h, "restart-this-session", "cancel-restart", "close-this-session", "whoami")
	run := func(session string, verbs ...string) {
		p := bridgeSessionProvider{registry: reg, session: session, slots: make(chan struct{}, bridgeMaxInFlight)}
		var wg sync.WaitGroup
		for _, v := range verbs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := p.CallToolV1(context.Background(), v, nil); err != nil {
					t.Errorf("%s: %v", v, err)
				}
			}()
		}
		wg.Wait()
	}
	run("repo/serial", "restart-this-session", "restart-this-session", "cancel-restart", "close-this-session", "restart-this-session")
	if m := maxActive.Load(); m != 1 {
		t.Errorf("%d lifecycle calls of one session ran at once, want 1", m)
	}

	// Control: a verb outside the lifecycle set is not serialised, so the
	// check above could fail.
	maxActive.Store(0)
	run("repo/parallel", "whoami", "whoami", "whoami", "whoami")
	if m := maxActive.Load(); m < 2 {
		t.Errorf("whoami calls never overlapped (max %d): the control for the serialisation check is not discriminating", m)
	}
}

func TestACallOverTheInFlightCapIsRefusedNotQueued(t *testing.T) {
	entered := make(chan struct{}, bridgeMaxInFlight)
	release := make(chan struct{})
	h := func(context.Context, json.RawMessage) (*protocol.ToolCallResultV1, error) {
		entered <- struct{}{}
		<-release
		return okResult(), nil
	}
	p := bridgeSessionProvider{registry: registryWith(h, "whoami"), session: "repo/cap", slots: make(chan struct{}, bridgeMaxInFlight)}
	var wg sync.WaitGroup
	for i := 0; i < bridgeMaxInFlight; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.CallToolV1(context.Background(), "whoami", nil)
		}()
	}
	for i := 0; i < bridgeMaxInFlight; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("the first eight calls were not all admitted")
		}
	}
	_, err := p.CallToolV1(context.Background(), "whoami", nil)
	if err == nil || !strings.HasPrefix(err.Error(), bridgeRefusalPrefix) || !strings.Contains(err.Error(), "calls in flight") {
		t.Errorf("the ninth call = %v, want a refusal naming the in-flight cap", err)
	}
	close(release)
	wg.Wait()
	if _, err := p.CallToolV1(context.Background(), "whoami", nil); err != nil {
		t.Errorf("a call after the others finished was refused: %v", err)
	}
}

func TestAPanickingCallBecomesAnErrorAndFreesItsSlot(t *testing.T) {
	var calls atomic.Int32
	h := func(context.Context, json.RawMessage) (*protocol.ToolCallResultV1, error) {
		if calls.Add(1) == 1 {
			panic("verb bug with a secret path /home/x")
		}
		return okResult(), nil
	}
	p := bridgeSessionProvider{registry: registryWith(h, "whoami"), session: "repo/panic", slots: make(chan struct{}, 1)}
	_, err := p.CallToolV1(context.Background(), "whoami", nil)
	if err == nil {
		t.Fatal("a panicking call returned no error")
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "/home") {
		t.Errorf("the error carries the panic's text: %v", err)
	}
	if _, err := p.CallToolV1(context.Background(), "whoami", nil); err != nil {
		t.Errorf("the slot of the panicked call was not freed: %v", err)
	}
	// The V0 entry point is guarded the same way.
	calls.Store(0)
	if _, err := p.CallTool(context.Background(), "whoami", nil); err == nil {
		t.Error("a panic through CallTool returned no error")
	}
}

func TestAReplyToAPeerThatDoesNotReadTimesOut(t *testing.T) {
	orig := bridgeReplyWriteTimeout
	bridgeReplyWriteTimeout = 100 * time.Millisecond
	t.Cleanup(func() { bridgeReplyWriteTimeout = orig })
	a, b := net.Pipe() // nobody reads b
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	tr := bridgeErrorCodes{Transport: transport.NewStdio(a, a), conn: a}
	id := jsonrpc.NewNumberID(1)
	msg, err := jsonrpc.NewResponse(id, map[string]string{"x": "y"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- tr.Write(msg) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a reply to a peer that never reads reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reply write never gave up")
	}
}
