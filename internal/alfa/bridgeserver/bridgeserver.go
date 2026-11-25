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
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

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

	// NotReadyText is the text of the refusal while the unit's cgroup file is
	// missing or invalid: the bridge admits nobody until it reads a valid one.
	// The code is the same ERR refused, which the shim retries.
	NotReadyText = "refused: the bridge does not know this unit's cgroup yet; try again shortly"

	writeTimeout      = 2 * time.Second
	busyWriteTimeout  = 100 * time.Millisecond
	staleProbeTimeout = time.Second
	drainTimeout      = 200 * time.Millisecond
	drainBytes        = 4096
)

// Seams tests replace; production reads the real thing.
var (
	// statUID reads the owner uid out of a directory's FileInfo.
	statUID = func(fi fs.FileInfo) (uid uint32, ok bool) {
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return 0, false
		}
		return st.Uid, true
	}
	// dialStale probes an existing socket file for a live listener.
	dialStale = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		return net.DialTimeout(network, addr, timeout)
	}
	// afterBind runs between the bind and the check of what was bound; tests
	// plant things there.
	afterBind = func(path string) {}
	// peerUID is the same-uid check of a peer; it returns the peer's pid.
	peerUID = policybroker.SameUID
)

var (
	// ErrOff marks a bridge that stays off for a reason that is not a refusal
	// of its configuration (the socket path is over the unix limit).
	ErrOff = errors.New("bridge off")
	// ErrNoPeerCheck is Listen's refusal when no exact peer check was supplied:
	// the bridge admits a peer only by the exact cgroup of its unit, and a
	// weaker default would admit its neighbours.
	ErrNoPeerCheck = errors.New("the bridge needs peercheck.CheckPeerExact from a merged clown; none is wired in this build")
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
	// CheckPeer is the exact peer check (clown's peercheck.CheckPeerExact):
	// it admits conn only when its process runs in exactly wantCgroup, the
	// cgroup the unit's cgroup file names. There is no default: a listener
	// given neither CheckPeer nor PeerCheck refuses to start (ErrNoPeerCheck).
	CheckPeer func(conn net.Conn, wantCgroup string) error
	// CgroupFile is the file clown writes the unit's cgroup path into
	// (CLOWN_BRIDGE_CGROUP_FILE). It is read on every connection; while it is
	// missing or invalid every peer is answered ERR refused. It must lie
	// outside the bridge directory.
	CgroupFile string
	// PeerCheck replaces the whole peer decision (the cgroup file, CheckPeer
	// and the uid check). It exists for tests that need a connection decided
	// without a unit; production code leaves it nil.
	PeerCheck func(conn net.Conn, unit string) error
	// Observer receives one Event per connection; nil is a no-op.
	Observer Observer
	// HelloTimeout overrides bridge.HelloTimeout (tests).
	HelloTimeout time.Duration
	// Tick makes the ticker of the periodic socket check; nil is a real
	// time.Ticker. It returns the channel and what stops it.
	Tick func(d time.Duration) (<-chan time.Time, func())
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

// acceptor is the part of a listener the server uses; tests inject one that
// fails.
type acceptor interface {
	Accept() (net.Conn, error)
	Close() error
}

// Server is a listening bridge.
type Server struct {
	l    acceptor
	path string
	id   socketID
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
	cgroupSeen  atomic.Bool
	deadMu      sync.Mutex
	dead        error
	ctlMu       sync.Mutex
	control     net.Conn
	controlGone chan struct{}
}

// Listen validates dir and unit and listens on <dir>/bridge.sock with mode
// 0600. A socket path over the unix limit is ErrOff. A live socket already
// there is refused; a stale one (connect refused) is replaced.
func Listen(dir, unit string, opts Options) (*Server, error) {
	if err := checkUnit(unit); err != nil {
		return nil, err
	}
	// Nothing below touches the disk until the path is known to fit.
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("bridge: %s=%q is not an absolute path", bridge.EnvDir, dir)
	}
	path, err := bridge.SocketPath(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOff, err)
	}
	if opts.PeerCheck == nil {
		if opts.CheckPeer == nil {
			return nil, ErrNoPeerCheck
		}
		if err := checkCgroupFilePath(dir, opts.CgroupFile); err != nil {
			return nil, err
		}
	}
	if err := checkDir(dir); err != nil {
		return nil, err
	}
	// The lock is held around clear-and-bind, so two listeners of one directory
	// cannot both find the path stale and both bind; whoever binds second sees
	// the first's live socket and is refused. It is not held while serving: a
	// successor may take a path whose socket was removed.
	lock, err := lockDir(dir)
	if err != nil {
		return nil, err
	}
	l, id, err := bindSocket(path)
	_ = lock.Close()
	if err != nil {
		return nil, err
	}
	if opts.Observer == nil {
		opts.Observer = noopObserver{}
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	// A peer that connects in a loop must not write a line per attempt.
	opts.Logf = rateLimitedLogf(opts.Logf, logWindow, time.Now)
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
	return &Server{l: l, path: path, id: id, unit: unit, opts: opts, ctx: ctx, cancel: cancel, conns: map[net.Conn]struct{}{}}, nil
}

const (
	lockName = ".bridge.lock"
	// socketCheckInterval is how often a serving listener checks that its socket
	// file is still the one it made.
	socketCheckInterval = 2 * time.Second
)

// umaskMu serialises the umask change around a bind: the umask is process wide.
var umaskMu sync.Mutex

// lockDir takes the directory's exclusive lock, without blocking. The lock file
// is opened without following a link and kept 0600.
func lockDir(dir string) (*os.File, error) {
	p := filepath.Join(dir, lockName)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("bridge: opening %s: %w", p, err)
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("bridge: %s is not a regular file", p)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("bridge: chmod %s: %w", p, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("bridge: a live listener already holds %s (%w): another frontend serves this session; not replacing its socket", p, err)
	}
	return f, nil
}

// bindSocket clears a stale socket and binds path with its mode right from the
// bind (the umask is narrowed around it, so no moment exists with a wider mode
// and no chmod by path is needed), then checks what is on disk is the socket it
// made: a socket, owner only, ours.
func bindSocket(path string) (*net.UnixListener, socketID, error) {
	if err := clearStaleSocket(path); err != nil {
		return nil, socketID{}, err
	}
	umaskMu.Lock()
	old := syscall.Umask(0o177)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	syscall.Umask(old)
	umaskMu.Unlock()
	if err != nil {
		return nil, socketID{}, fmt.Errorf("bridge: listen on %s: %w", path, err)
	}
	// The server removes its own socket on Close, and only if it is still the
	// socket this server made (see removeOwnSocket).
	l.SetUnlinkOnClose(false)
	bound, _ := fileID(path)
	afterBind(path)
	id, err := checkBound(path)
	if err != nil || id != bound {
		_ = l.Close()
		// Only what this bind made is removed; whatever else stands there is
		// not ours to touch.
		if cur, ok := fileID(path); ok && cur == bound {
			_ = os.Remove(path)
		}
		if err == nil {
			err = fmt.Errorf("the file at %s is not the socket that was bound", path)
		}
		return nil, socketID{}, fmt.Errorf("bridge: %w", err)
	}
	return l, id, nil
}

// checkBound requires path, looked at without following a link, to be a socket
// of this uid that nobody else can open.
func checkBound(path string) (socketID, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return socketID{}, err
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return socketID{}, fmt.Errorf("%s is not a socket after the bind", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return socketID{}, fmt.Errorf("%s has mode %o after the bind", path, fi.Mode().Perm())
	}
	if uid, ok := statUID(fi); !ok || int(uid) != os.Getuid() {
		return socketID{}, fmt.Errorf("%s is not owned by this uid after the bind", path)
	}
	id, ok := fileID(path)
	if !ok {
		return socketID{}, fmt.Errorf("cannot identify the socket at %s", path)
	}
	return id, nil
}

// socketID is the identity of a file on disk: device and inode.
type socketID struct{ dev, ino uint64 }

// fileID reads the device and inode of path without following a symlink.
func fileID(path string) (socketID, bool) {
	fi, err := os.Lstat(path)
	if err != nil {
		return socketID{}, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return socketID{}, false
	}
	return socketID{dev: uint64(st.Dev), ino: uint64(st.Ino)}, true //nolint:unconvert // widths differ by platform
}

// removeOwnSocket removes the socket file only while it is still the one this
// server made: a successor that took the path over keeps its socket.
func (s *Server) removeOwnSocket() error {
	if cur, ok := fileID(s.path); !ok || cur != s.id {
		return nil
	}
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// closeHandler is the session handler of a listener given none: it ends the
// stream after OK.
type closeHandler struct{}

func (closeHandler) Serve(context.Context, net.Conn, Caller) {}

// checkPeer decides one connection. text is the refusal's text when err is
// set. Not being in the unit and not being able to tell are the same answer
// (ERR refused); the text differs only for a cgroup file that is not (yet)
// there, so the shim can tell "retry" from "never".
func (s *Server) checkPeer(conn net.Conn) (text string, err error) {
	if s.opts.PeerCheck != nil {
		if err := s.opts.PeerCheck(conn, s.unit); err != nil {
			return RefusedPeerText, err
		}
		return "", nil
	}
	if _, err := peerUID(conn); err != nil {
		return RefusedPeerText, err
	}
	cg, err := ReadCgroupFile(s.opts.CgroupFile)
	if err != nil {
		return NotReadyText, err
	}
	s.cgroupSeen.Store(true)
	if err := s.opts.CheckPeer(conn, cg); err != nil {
		return RefusedPeerText, err
	}
	return "", nil
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
	uid, ok := statUID(fi)
	if !ok {
		return fmt.Errorf("bridge: cannot read the owner of %q", dir)
	}
	if int(uid) != os.Getuid() {
		return fmt.Errorf("bridge: %q is owned by uid %d, not %d", dir, uid, os.Getuid())
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
	c, err := dialStale("unix", path, staleProbeTimeout)
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

// Serve accepts connections until ctx ends or Close is called, and returns nil
// then. It closes the server on return. If the accept loop dies on its own it
// returns that error (after closing) without logging it.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.ctx.Done():
		}
	}()
	go s.watchSocket()
	var backoff time.Duration
	for {
		conn, err := s.l.Accept()
		if err != nil {
			if d := s.deadErr(); d != nil {
				return d
			}
			if s.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			// A temporary failure (out of descriptors, an aborted connect) is
			// waited out with a growing pause, not turned into a dead listener
			// and not spun on.
			if isTemporaryAcceptError(err) {
				backoff = nextBackoff(backoff)
				s.opts.Logf("bridge accept: %v; retrying in %v", err, backoff)
				if !sleepFor(s.ctx, backoff) {
					return nil
				}
				continue
			}
			// The accept loop died on its own: close the server so its
			// socket does not outlive it, and hand the error to the caller,
			// which is the one that logs and wakes the holders.
			_ = s.Close()
			return fmt.Errorf("bridge accept: %w", err)
		}
		backoff = 0
		switch s.track(conn) {
		case trackClosed:
			_ = conn.Close()
			return nil
		case trackFull:
			s.rejectBusy(conn)
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.untrack(conn)
			s.handle(conn)
		}()
	}
}

const (
	acceptBackoffMin = 5 * time.Millisecond
	acceptBackoffMax = time.Second
	// MaxConnections bounds the connections held at once, from accept: the
	// streams the contract allows plus a few still reading their hello or being
	// refused.
	MaxConnections = bridge.MaxStreamsPerSession + 16
)

func nextBackoff(cur time.Duration) time.Duration {
	if cur < acceptBackoffMin {
		return acceptBackoffMin
	}
	if cur *= 2; cur > acceptBackoffMax {
		return acceptBackoffMax
	}
	return cur
}

// isTemporaryAcceptError reports an accept failure that a later accept can
// outlive: descriptor or memory pressure, an aborted connection, an interrupt.
func isTemporaryAcceptError(err error) bool {
	for _, errno := range []syscall.Errno{syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS, syscall.ENOMEM, syscall.ECONNABORTED, syscall.EINTR, syscall.EAGAIN} {
		if errors.Is(err, errno) {
			return true
		}
	}
	var t interface{ Temporary() bool }
	return errors.As(err, &t) && t.Temporary()
}

// sleepFor waits d and reports whether it waited it out (false: ctx ended).
// Tests replace it.
var sleepFor = func(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// rejectBusy answers a connection over the cap with ERR busy and closes it,
// without a goroutine and without waiting on the peer: the reply is one short
// write on a fresh socket, bounded by a deadline.
func (s *Server) rejectBusy(conn net.Conn) {
	started := time.Now()
	_ = conn.SetWriteDeadline(time.Now().Add(busyWriteTimeout))
	_, _ = conn.Write(bridge.FormatErr(bridge.CodeBusy, fmt.Sprintf("%d connections are open; the limit is %d per session", MaxConnections, MaxConnections)))
	_ = conn.Close()
	s.observe("", bridge.CodeBusy, started)
}

// closeWait bounds how long Close waits for connection handlers. A handler
// that ignores its context and its closed connection must not hold the serve
// process's shutdown; tests shorten it.
var closeWait = 5 * time.Second

// waitBounded waits for wg for at most d and reports whether it finished.
func waitBounded(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// declareDead records why the listener cannot serve any more and closes it:
// Serve then returns that reason.
func (s *Server) declareDead(reason error) {
	s.deadMu.Lock()
	if s.dead == nil {
		s.dead = reason
	}
	s.deadMu.Unlock()
	_ = s.Close()
}

func (s *Server) deadErr() error {
	s.deadMu.Lock()
	defer s.deadMu.Unlock()
	return s.dead
}

// socketGone is the reason a listener whose file is not its own is dead.
func (s *Server) socketGone() error {
	if cur, ok := fileID(s.path); !ok || cur != s.id {
		return fmt.Errorf("bridge: the socket at %s was removed or replaced; nothing can connect to this listener", s.path)
	}
	return nil
}

// watchSocket checks every interval that the socket file is still the one this
// server made. A listener on an unlinked inode is silently unreachable, so it
// is declared dead and the caller is told.
func (s *Server) watchSocket() {
	defer func() {
		if r := recover(); r != nil {
			s.opts.Logf("bridge: the socket watcher panicked: %v", r)
			s.declareDead(fmt.Errorf("bridge: the socket watcher panicked: %v", r))
		}
	}()
	tick := s.opts.Tick
	if tick == nil {
		tick = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	ch, stop := tick(socketCheckInterval)
	defer stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ch:
			if err := s.socketGone(); err != nil {
				s.declareDead(err)
				return
			}
			if err := s.cgroupFileGone(); err != nil {
				s.declareDead(err)
				return
			}
		}
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
		if !waitBounded(&s.wg, closeWait) && s.closeErr == nil {
			s.closeErr = fmt.Errorf("bridge: handlers were still running %v after close; closing without them", closeWait)
		}
		if err := s.removeOwnSocket(); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}

type trackResult int

const (
	trackOK trackResult = iota
	trackClosed
	trackFull
)

func (s *Server) track(c net.Conn) trackResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return trackClosed
	}
	if len(s.conns) >= MaxConnections {
		return trackFull
	}
	s.conns[c] = struct{}{}
	return trackOK
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

// observe hands one connection's outcome to the Observer. A panicking Observer
// is the Observer's bug, not the connection's: it is logged and swallowed.
func (s *Server) observe(service, outcome string, started time.Time) {
	defer func() {
		if r := recover(); r != nil {
			s.opts.Logf("bridge: the observer panicked: %v", r)
		}
	}()
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

// handle serves one accepted connection and observes it exactly once, however
// it ends: a refusal, a hello that never came, a peer that went away, a failed
// reply, a served stream, or a panic (which is recovered here, in the
// connection's own goroutine, and counted as an error).
func (s *Server) handle(conn net.Conn) {
	started := time.Now()
	service, outcome := "", bridge.CodeError
	defer func() {
		if r := recover(); r != nil {
			s.opts.Logf("bridge: a connection handler panicked: %v", r)
			outcome = bridge.CodeError
		}
		s.observe(service, outcome, started)
	}()

	if text, err := s.checkPeer(conn); err != nil {
		s.refused.Add(1)
		s.opts.Logf("bridge refused a peer outside unit %s: %v", s.unit, err)
		s.refuse(conn, bridge.FormatErr(bridge.CodeRefused, text))
		outcome = bridge.CodeRefused
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(s.opts.HelloTimeout))
	h, err := bridge.ReadHello(conn)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrDeadlineExceeded):
			outcome = "hello-timeout"
		case errors.Is(err, bridge.ErrMalformed) || errors.Is(err, bridge.ErrTooLong):
			s.opts.Logf("bridge hello refused: %v", err)
			s.refuse(conn, bridge.MalformedReply())
		}
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	service = h.Service

	rep, accept := bridge.Decide(h, s.opts.Served())
	if !accept {
		s.refuse(conn, rep)
		outcome = bridge.CodeUnknownService
		if h.Major != bridge.ServerMajor {
			outcome = bridge.CodeVersion
		}
		return
	}

	release, busy := s.reserve(h.Service)
	if busy != "" {
		s.refuse(conn, bridge.FormatErr(bridge.CodeBusy, busy))
		outcome = bridge.CodeBusy
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
	outcome = "ok"
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

// controlProbeInterval is how often a control stream whose peer stopped
// sending is checked for a peer that went away. Tests shorten it.
var controlProbeInterval = 500 * time.Millisecond

// serveControl keeps the control stream open until the peer goes away or the
// server ends; the peer's bytes are discarded (the control service reads
// nothing from the shim yet). The shim writes nothing after its hello, and a
// peer that only shut its write side (EOF on our read) is still listening, so
// EOF alone does not end the stream: it ends on a read error, a write that
// fails (Push, or the periodic probe of a peer that is fully gone), or the
// server's end.
func (s *Server) serveControl(conn net.Conn) {
	gone := make(chan struct{})
	s.ctlMu.Lock()
	s.control = conn
	s.controlGone = gone
	s.ctlMu.Unlock()
	defer func() {
		s.ctlMu.Lock()
		if s.control == conn {
			s.control = nil
			s.controlGone = nil
		}
		s.ctlMu.Unlock()
	}()
	if _, err := io.Copy(io.Discard, conn); err != nil {
		return
	}
	probe := time.NewTicker(controlProbeInterval)
	defer probe.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-gone:
			return
		case <-probe.C:
			// A zero-length write moves no bytes; it fails once the peer
			// closed its end.
			if _, err := conn.Write(nil); err != nil {
				return
			}
		}
	}
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
	c := s.control
	_ = c.SetWriteDeadline(time.Now().Add(writeTimeout))
	defer func() { _ = c.SetWriteDeadline(time.Time{}) }()
	if _, err := c.Write(b); err != nil {
		// A control stream that cannot be written is over: free its slot.
		if s.controlGone != nil {
			close(s.controlGone)
			s.controlGone = nil
		}
		s.control = nil
		return fmt.Errorf("bridge: pushing %q on the control stream: %w", f.Type, err)
	}
	return nil
}
