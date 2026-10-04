package main

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"
	"code.linenisgreat.com/purse-first/libs/go-mcp/server"
	"code.linenisgreat.com/purse-first/libs/go-mcp/transport"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestServeDrainsInFlightHandlerOnStdinEOF characterizes Decision 3 of #188:
// merges are session-durable. Closing the client's stdin (EOF) must neither
// cancel an in-flight tool call nor end Run before the handler returns. It pins
// purse-first server/server.go (Run's io.EOF branch and gracefulShutdown's
// wg.Wait), so a purse-first bump that changes EOF semantics fails loudly.
func TestServeDrainsInFlightHandlerOnStdinEOF(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	ctxErrAtRelease := make(chan error, 1)

	registry := server.NewToolRegistryV1()
	registry.Register(
		protocol.ToolV1{Name: "block", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(ctx context.Context, _ json.RawMessage) (*protocol.ToolCallResultV1, error) {
			close(entered)
			<-release
			ctxErrAtRelease <- ctx.Err()
			return &protocol.ToolCallResultV1{
				Content: []protocol.ContentBlockV1{protocol.TextContentV1("drained")},
			}, nil
		},
	)

	clientToServer, clientWriter := io.Pipe()
	var serverOutput lockedBuffer
	srv, err := server.New(transport.NewStdio(clientToServer, &serverOutput), server.Options{
		ServerName:        "durability-test",
		Tools:             registry,
		PreferV1Providers: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(context.Background()) }()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"block","arguments":{}}}` + "\n"
	if _, err := io.WriteString(clientWriter, request); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := clientWriter.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-runDone:
		t.Fatalf("Run returned (%v) on stdin EOF while a handler was in flight", err)
	case <-time.After(300 * time.Millisecond):
	}

	close(release)
	if got := <-ctxErrAtRelease; got != nil {
		t.Errorf("handler ctx cancelled by stdin EOF: %v", got)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Errorf("Run after drain = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the handler finished")
	}
	if !strings.Contains(serverOutput.String(), "drained") {
		t.Errorf("response not written before shutdown; output: %q", serverOutput.String())
	}
}
