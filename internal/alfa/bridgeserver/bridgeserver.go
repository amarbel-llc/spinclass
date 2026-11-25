// Package bridgeserver is the per-session listener of bridge contract v1
// (docs/plans/2026-11-25-bridge-wire-v1.md) inside the serve process. It owns
// the socket, the peer check, the hello and its reply, the per-session stream
// caps and the control stream; the byte streams after "OK\n" belong to
// services (the session service is a SessionHandler a later batch supplies).
//
// The identity a handler acts for is the Caller this listener builds from the
// session it was created for and the unit it checked the peer against, never
// anything read from the stream.
package bridgeserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"code.linenisgreat.com/clown/pkgs/peercheck"

	"code.linenisgreat.com/spinclass/internal/0/bridge"
	"code.linenisgreat.com/spinclass/internal/0/policybroker"
)

const (
	// ServiceControl and ServiceSession are the two services this listener
	// serves by default.
	ServiceControl = "control"
	ServiceSession = "session"

	// TransportBridge is the Caller.Transport of every stream this listener
	// accepts.
	TransportBridge = "bridge"

	// RefusedPeerText is the text of the refusal sent to a peer that is not in
	// the session's unit.
	RefusedPeerText = "refused: this connection does not come from this session's unit; the bridge answers only the unit it was created for"

	writeTimeout   = 2 * time.Second
	connectTimeout = 2 * time.Second
	drainTimeout   = 200 * time.Millisecond
	drainBytes     = 4096
)

var (
	// ErrOff marks a bridge that stays off for a reason that is not a refusal
	// of its configuration (the socket path is over the unix limit).
	ErrOff = errors.New("bridge off")
	// ErrNoControl is Push's error when no control stream is open.
	ErrNoControl = errors.New("bridge: no control stream is open")
)

// Caller is who a service call is made for, as the listener established it.
type Caller struct {
	SessionKey string
	Unit       string
	Transport  string
}

// SessionHandler serves the session service on an accepted stream. The
// reply "OK\n" has been written; the handler owns the connection's bytes from
// here and returns when the stream is done (the listener closes conn after).
type SessionHandler interface {
	Serve(ctx context.Context, conn net.Conn, caller Caller)
}

// Event is one connection's outcome, for an Observer.
type Event struct {
	// Service is the hello's service label, empty before a hello was read.
	Service string
	// Outcome is "ok" for a stream that was served, an ERR code of the wire
	// (version, unknown-service, refused, busy, error), or "hello-timeout".
	Outcome  string
	Duration time.Duration
	Caller   Caller
}

// Observer is the hook crossing signals plug into. The default is a no-op.
type Observer interface{ Crossing(Event) }

type noopObserver struct{}

func (noopObserver) Crossing(Event) {}

// Frame is one NDJSON frame pushed on the control stream. Unknown types are
// the shim's problem.
type Frame struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Options configure a Server. The zero value is usable.
type Options struct {
	// SessionKey is the session this listener serves; it becomes
	// Caller.SessionKey.
	SessionKey string
	// Handler serves the session service. Nil closes such streams after OK.
	Handler SessionHandler
	// Served is the set of labels answered; nil serves control and session.
	Served func() map[string]bool
	// PeerCheck decides whether a peer may speak; nil is the same-uid and
	// unit-cgroup check. An error refuses (fail closed).
	PeerCheck func(conn net.Conn, unit string) error
	// Observer receives one Event per connection; nil is a no-op.
	Observer Observer
	// HelloTimeout overrides bridge.HelloTimeout (tests).
	HelloTimeout time.Duration
	// Logf logs one line; nil discards.
	Logf func(format string, args ...any)
}

// EnvConfig reads the directory and unit clown exported. ok is false when
// either is unset.
func EnvConfig(getenv func(string) string) (dir, unit string, ok bool) {
	dir = strings.TrimSpace(getenv(bridge.EnvDir))
	unit = strings.TrimSpace(getenv(bridge.EnvUnit))
	return dir, unit, dir != "" && unit != ""
}

// Server is a listening bridge.
type Server struct {
	l    *net.UnixListener
	path string
	unit string
	opts Options

	ctx    context.Context
	cancel context.CancelFunc

	refused atomic.Int64

	mu          sync.Mutex
	streams     int
	controlHeld bool
	conns       map[net.Conn]struct{}
	closed      bool
	wg          sync.WaitGroup
	closeOnce   sync.Once
	closeErr    error
	ctlMu       sync.Mutex
	control     net.Conn
}

// Listen validates dir and unit and listens on <dir>/bridge.sock with mode
// 0600. A socket path over the unix limit is ErrOff. A live socket already
// there is refused; a stale one (connect refused) is replaced.
func Listen(dir, unit string, opts Options) (*Server, error) {
	if err := checkUnit(unit); err != nil {
		return nil, err
	}
	if err := checkDir(dir); err != nil {
		return nil, err
	}
	path, err := bridge.SocketPath(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOff, err)
	}
	if err := clearStaleSocket(path); err != nil {
		return nil, err
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("bridge: listen on %s: %w", path, err)
	}
	l.SetUnlinkOnClose(true)
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("bridge: chmod %s: %w", path, err)
	}
	if opts.Observer == nil {
		opts.Observer = noopObserver{}
	}
	if opts.PeerCheck == nil {
		opts.PeerCheck = defaultPeerCheck
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	if opts.HelloTimeout <= 0 {
		opts.HelloTimeout = bridge.HelloTimeout
	}
	if opts.Handler == nil {
		opts.Handler = closeHandler{}
	}
	if opts.Served == nil {
		opts.Served = func() map[string]bool {
			return map[string]bool{ServiceControl: true, ServiceSession: true}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{l: l, path: path, unit: unit, opts: opts, ctx: ctx, cancel: cancel, conns: map[net.Conn]struct{}{}}, nil
}

// closeHandler is the session handler of a listener given none: it ends the
// stream after OK.
type closeHandler struct{}

func (closeHandler) Serve(context.Context, net.Conn, Caller) {}

// defaultPeerCheck admits a peer of this uid that runs in the unit's cgroup.
// Same uid is not enough: the unit is what the bridge was created for.
func defaultPeerCheck(c net.Conn, unit string) error {
	pid, err := policybroker.SameUID(c)
	if err != nil {
		return err
	}
	return peercheck.CheckUnitCgroup(pid, unit, nil)
}

func checkUnit(unit string) error {
	const suffix = ".service"
	if !strings.HasSuffix(unit, suffix) || len(unit) == len(suffix) || unit[0] == '-' || unit[0] == '.' {
		return fmt.Errorf("bridge: %s=%q is not a plain unit name ending in %s", bridge.EnvUnit, unit, suffix)
	}
	for _, r := range unit {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == '@', r == ':', r == '\\':
		default:
			return fmt.Errorf("bridge: %s=%q is not a plain unit name", bridge.EnvUnit, unit)
		}
	}
	return nil
}

// checkDir requires an absolute path to a real directory (not a symlink) that
// this uid owns and nobody else can enter.
func checkDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("bridge: %s=%q is not an absolute path", bridge.EnvDir, dir)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("bridge: %s=%q: %w", bridge.EnvDir, dir, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("bridge: %s=%q is a symlink; the bridge directory must be a real directory", bridge.EnvDir, dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("bridge: %s=%q is not a directory", bridge.EnvDir, dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("bridge: cannot read the owner of %q", dir)
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("bridge: %q is owned by uid %d, not %d", dir, st.Uid, os.Getuid())
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("bridge: %q has mode %o; it must be 0700 or stricter", dir, perm)
	}
	return nil
}

// clearStaleSocket refuses when something answers on path and removes a
// leftover socket file only when connecting is refused (nobody listens).
func clearStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("bridge: %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("bridge: %s exists and is not a socket; not replacing it", path)
	}
	c, err := net.DialTimeout("unix", path, connectTimeout)
	if err == nil {
		_ = c.Close()
		return fmt.Errorf("bridge: a live socket already answers at %s; another frontend serves this session", path)
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return fmt.Errorf("bridge: removing stale socket %s: %w", path, rmErr)
		}
		return nil
	}
	return fmt.Errorf("bridge: cannot tell whether %s is stale (%v); not replacing it", path, err)
}

// Path is the socket path.
func (s *Server) Path() string { return s.path }

// Refused counts connections refused for coming from outside the unit.
func (s *Server) Refused() int64 { return s.refused.Load() }

// Serve accepts connections until ctx ends or Close is called. It closes the
// server on return.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.ctx.Done():
		}
	}()
	for {
		conn, err := s.l.Accept()
		if err != nil {
			if s.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			s.opts.Logf("bridge accept: %v", err)
			return err
		}
		if !s.track(conn) {
			_ = conn.Close()
			return nil
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.untrack(conn)
			s.handle(conn)
		}()
	}
}

// Close stops accepting, ends every stream, waits for the handlers and removes
// the socket file.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		conns := make([]net.Conn, 0, len(s.conns))
		for c := range s.conns {
			conns = append(conns, c)
		}
		s.mu.Unlock()
		s.cancel()
		s.closeErr = s.l.Close()
		for _, c := range conns {
			_ = c.Close()
		}
		s.wg.Wait()
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) && s.closeErr == nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}

func (s *Server) track(c net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[c] = struct{}{}
	return true
}

func (s *Server) untrack(c net.Conn) {
	_ = c.Close()
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

func (s *Server) caller() Caller {
	return Caller{SessionKey: s.opts.SessionKey, Unit: s.unit, Transport: TransportBridge}
}

func (s *Server) observe(service, outcome string, started time.Time) {
	s.opts.Observer.Crossing(Event{Service: service, Outcome: outcome, Duration: time.Since(started), Caller: s.caller()})
}

func (s *Server) reply(conn net.Conn, b []byte) error {
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err := conn.Write(b)
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

// refuse writes an ERR line and lets the peer read it before the close: a unix
// socket closed with unread input resets the peer, which could then lose the
// reply, so the write side is shut first and what the peer already sent is
// drained, bounded in bytes and time.
func (s *Server) refuse(conn net.Conn, errLine []byte) {
	_ = s.reply(conn, errLine)
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	_ = conn.SetReadDeadline(time.Now().Add(drainTimeout))
	_, _ = io.Copy(io.Discard, io.LimitReader(conn, drainBytes))
}

func (s *Server) handle(conn net.Conn) {
	started := time.Now()
	if err := s.opts.PeerCheck(conn, s.unit); err != nil {
		s.refused.Add(1)
		s.opts.Logf("bridge refused a peer outside unit %s: %v", s.unit, err)
		s.refuse(conn, bridge.FormatErr(bridge.CodeRefused, RefusedPeerText))
		s.observe("", bridge.CodeRefused, started)
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(s.opts.HelloTimeout))
	h, err := bridge.ReadHello(conn)
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			s.observe("", "hello-timeout", started)
			return
		}
		if errors.Is(err, bridge.ErrMalformed) || errors.Is(err, bridge.ErrTooLong) {
			s.opts.Logf("bridge hello refused: %v", err)
			s.refuse(conn, bridge.MalformedReply())
			s.observe("", bridge.CodeError, started)
		}
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	rep, accept := bridge.Decide(h, s.opts.Served())
	if !accept {
		s.refuse(conn, rep)
		code := bridge.CodeUnknownService
		if h.Major != bridge.ServerMajor {
			code = bridge.CodeVersion
		}
		s.observe(h.Service, code, started)
		return
	}

	release, busy := s.reserve(h.Service)
	if busy != "" {
		s.refuse(conn, bridge.FormatErr(bridge.CodeBusy, busy))
		s.observe(h.Service, bridge.CodeBusy, started)
		return
	}
	defer release()
	if err := s.reply(conn, rep); err != nil {
		return
	}
	switch h.Service {
	case ServiceControl:
		s.serveControl(conn)
	case ServiceSession:
		s.opts.Handler.Serve(s.ctx, conn, s.caller())
	}
	s.observe(h.Service, "ok", started)
}

// reserve takes a stream slot (and the control slot for control) or says why
// not. The returned release frees what was taken.
func (s *Server) reserve(service string) (release func(), busy string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.streams >= bridge.MaxStreamsPerSession {
		return nil, fmt.Sprintf("%d streams are open; the limit is %d per session", s.streams, bridge.MaxStreamsPerSession)
	}
	if service == ServiceControl && s.controlHeld {
		return nil, "a control stream is already open for this session"
	}
	s.streams++
	if service == ServiceControl {
		s.controlHeld = true
	}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.streams--
		if service == ServiceControl {
			s.controlHeld = false
		}
	}, ""
}

// serveControl keeps the control stream open until the peer closes it or the
// server ends; the peer's bytes are discarded (the control service reads
// nothing from the shim yet).
func (s *Server) serveControl(conn net.Conn) {
	s.ctlMu.Lock()
	s.control = conn
	s.ctlMu.Unlock()
	defer func() {
		s.ctlMu.Lock()
		if s.control == conn {
			s.control = nil
		}
		s.ctlMu.Unlock()
	}()
	_, _ = io.Copy(io.Discard, conn)
}

// Push writes one NDJSON frame to the open control stream. It is the future
// restart, stop and wake notices' way out; nothing calls it yet.
func (s *Server) Push(f Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	s.ctlMu.Lock()
	defer s.ctlMu.Unlock()
	if s.control == nil {
		return ErrNoControl
	}
	_ = s.control.SetWriteDeadline(time.Now().Add(writeTimeout))
	defer func() { _ = s.control.SetWriteDeadline(time.Time{}) }()
	if _, err := s.control.Write(b); err != nil {
		return fmt.Errorf("bridge: pushing %q on the control stream: %w", f.Type, err)
	}
	return nil
}
