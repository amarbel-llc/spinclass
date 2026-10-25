// Package statsd emits best-effort spinclass telemetry to stats-me over UDP in
// the classic statsd line protocol (stats-me-clients(7), spinclass#314,
// spinclass#363): counters (|c), millisecond timers (|ms) and gauges (|g).
//
// Dimensions live in the metric NAME, never in DogStatsD tags: the fleet's
// graphite backend drops tags (the troupe/moxy idiom). Names carry no build
// segment, so a metric aggregates across builds. A caller-supplied dimension
// (repo dirname, post-merge target name) goes through Segment first, so it is
// always exactly one graphite name segment.
//
// The endpoint resolves once per merge into a Client (Resolve), from the
// sweatfile's [metrics] table and the environment. Precedence: the sweatfile's
// disable-statsd or SPINCLASS_DISABLE_STATSD=1 turn emission off outright;
// else the sweatfile's statsd-address (host:port, or a bare host on port 8125;
// a malformed one disables emission, see ParseAddress) wins; else the
// environment's tommy-style opt-in applies — nothing is sent unless STATSD_HOST or STATSD_PORT is present (the stats-me home-manager
// module exports both), so a sandboxed build or test never sprays UDP. A
// present-but-empty STATSD_HOST means 127.0.0.1; STATSD_PORT defaults to 8125.
//
// UDP is fire-and-forget: every resolve, dial and write failure is swallowed,
// and the dial is bounded so a hostname address cannot stall a merge on DNS.
// Telemetry must never fail or slow a merge.
package statsd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Prefix opens every spinclass metric name.
const Prefix = "spinclass."

const (
	defaultHost = "127.0.0.1"
	defaultPort = "8125"

	// dialTimeout bounds name resolution plus the (connectionless) UDP dial.
	dialTimeout = 250 * time.Millisecond
)

// Outcome names for timers, the trailing segment of a phase's metric name.
const (
	OutcomeOK        = "ok"
	OutcomeFailed    = "failed"
	OutcomeTimeout   = "timeout"
	OutcomeCancelled = "cancelled"
)

// Client emits to one resolved endpoint. The zero value emits nothing.
type Client struct{ addr string }

// Resolve builds the Client for a sweatfile's [metrics] values: see the package
// doc for the precedence.
func Resolve(address string, disabled bool) Client {
	if disabled || os.Getenv("SPINCLASS_DISABLE_STATSD") == "1" {
		return Client{}
	}
	if address != "" {
		hostPort, err := ParseAddress(address)
		if err != nil {
			return Client{}
		}
		return Client{addr: hostPort}
	}
	host, hostSet := os.LookupEnv("STATSD_HOST")
	port, portSet := os.LookupEnv("STATSD_PORT")
	if !hostSet && !portSet {
		return Client{}
	}
	if host == "" {
		host = defaultHost
	}
	if port == "" {
		port = defaultPort
	}
	return Client{addr: net.JoinHostPort(host, port)}
}

// ParseAddress normalises a sweatfile statsd-address into a dialable host:port,
// or says why it is malformed. It accepts host, host:port, an IPv4 literal, a
// bare IPv6 literal (::1) and a bracketed [::1] or [::1]:8125; a bare host gets
// port 8125. It rejects a URL scheme, whitespace, an empty host, and a port that
// is not an integer in 1..65535.
//
// Resolve uses it to normalise and `sc validate` uses it to judge, so the two
// cannot disagree. At runtime a malformed address turns emission off (the zero
// Client) rather than sending to a garbage endpoint.
func ParseAddress(address string) (hostPort string, err error) {
	if address == "" {
		return "", errors.New("empty address")
	}
	if strings.Contains(address, "://") {
		return "", errors.New("contains a URL scheme; use host or host:port")
	}
	if strings.IndexFunc(address, unicode.IsSpace) >= 0 {
		return "", errors.New("contains whitespace")
	}
	host, port, splitErr := net.SplitHostPort(address)
	if splitErr != nil {
		var ok bool
		if host, ok = bareHost(address); !ok {
			return "", fmt.Errorf("not a host or host:port: %w", splitErr)
		}
		port = defaultPort
	}
	if host == "" {
		return "", errors.New("empty host")
	}
	if !validPort(port) {
		return "", fmt.Errorf("port %q is not an integer in 1..65535", port)
	}
	return net.JoinHostPort(host, port), nil
}

// bareHost extracts the host from an address with no port: a plain name or IPv4
// literal, a bare IPv6 literal, or a bracketed IPv6 literal.
func bareHost(address string) (string, bool) {
	if inner, ok := strings.CutPrefix(address, "["); ok {
		inner, ok = strings.CutSuffix(inner, "]")
		return inner, ok && net.ParseIP(inner) != nil
	}
	if strings.Contains(address, ":") {
		return address, net.ParseIP(address) != nil
	}
	return address, true
}

func validPort(port string) bool {
	if port == "" || strings.IndexFunc(port, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

// Enabled reports whether the Client will send anything.
func (c Client) Enabled() bool { return c.addr != "" }

// Count increments the counter spinclass.<name> by one. name is a dot-separated
// vocabulary of fixed segments (and Segment-sanitized ones).
func (c Client) Count(name string) {
	c.send(Prefix + name + ":1|c")
}

// Timing records d, in whole milliseconds, on the timer spinclass.<name>.
func (c Client) Timing(name string, d time.Duration) {
	c.send(Prefix + name + ":" + strconv.FormatInt(d.Milliseconds(), 10) + "|ms")
}

// Gauge sets the gauge spinclass.<name> to value. statsd keeps only the last
// value per flush interval, so a gauge is a sample at the moment it is sent.
func (c Client) Gauge(name string, value int) {
	c.send(Prefix + name + ":" + strconv.Itoa(value) + "|g")
}

func (c Client) send(payload string) {
	if c.addr == "" {
		return
	}
	conn, err := net.DialTimeout("udp", c.addr, dialTimeout)
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(payload))
}

// Segment makes an arbitrary string (a repo dirname, a post-merge target name)
// safe as ONE graphite name segment: lowercased, every character outside
// [a-z0-9_-] (dots included, so it never splits a segment) becomes "_", and an
// empty string becomes "unknown".
func Segment(s string) string {
	if s == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '_'
	}, s)
}

// Outcome classifies a finished phase for its timer's trailing segment: nil
// err is ok; a done ctx is cancelled (the caller's cancel, not the phase's
// failure); timedOut — the caller's own judgement that a deadline or watchdog
// killed the phase — is timeout; anything else is failed.
func Outcome(ctx context.Context, err error, timedOut bool) string {
	switch {
	case err == nil:
		return OutcomeOK
	case ctx != nil && ctx.Err() != nil:
		return OutcomeCancelled
	case timedOut:
		return OutcomeTimeout
	}
	return OutcomeFailed
}
