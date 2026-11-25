package bridgeserver

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/bridge"
)

// failingAcceptor stands in for a listener whose accept loop dies on its own.
type failingAcceptor struct {
	real acceptor
	err  error
}

func (f failingAcceptor) Accept() (net.Conn, error) { return nil, f.err }
func (f failingAcceptor) Close() error              { return f.real.Close() }

func TestServeReturnsAnAcceptFailureLogsOnceAndRemovesTheSocket(t *testing.T) {
	var logged []string
	dir := shortDir(t)
	s, err := Listen(dir, testUnit, Options{
		PeerCheck: allowAll,
		Logf:      func(f string, a ...any) { logged = append(logged, f) },
	})
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("accept tcp: too many open files")
	s.l = failingAcceptor{real: s.l, err: boom}

	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, boom) {
			t.Fatalf("Serve = %v, want the accept error", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after the accept loop died")
	}
	if len(logged) != 0 {
		t.Errorf("the server logged %d lines itself; the supervisor logs the failure once: %v", len(logged), logged)
	}
	if _, err := os.Lstat(filepath.Join(dir, bridge.SocketName)); !os.IsNotExist(err) {
		t.Errorf("the socket outlived a dead accept loop: %v", err)
	}
	if err := s.Close(); err != nil && !strings.Contains(err.Error(), "closed") {
		t.Errorf("Close after the failure: %v", err)
	}
}

func TestServeReturnsNilOnCloseAndOnContextEnd(t *testing.T) {
	s, _ := start(t, Options{})
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	dir := shortDir(t)
	s2, err := Listen(dir, testUnit, Options{PeerCheck: allowAll})
	if err != nil {
		t.Fatal(err)
	}
	go func() { done <- s2.Serve(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve after ctx end = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return on ctx end")
	}
	_ = s.Close()
}
