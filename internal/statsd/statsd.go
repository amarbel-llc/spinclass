// Package statsd emits best-effort spinclass telemetry to stats-me over UDP in
// the classic statsd line protocol (stats-me-clients(7), spinclass#314).
//
// Dimensions live in the metric NAME, never in DogStatsD tags: the fleet's
// graphite backend drops tags (the troupe/moxy idiom). Names carry no build
// segment, so a counter aggregates across builds — the FDR 0029 promotion
// criterion is a ratio over a week of fleet merges.
//
// Emission is opt-in by environment, like tommy's: nothing is sent unless
// STATSD_HOST or STATSD_PORT is present (the stats-me home-manager module
// exports both), so a sandboxed build or test never sprays UDP.
// SPINCLASS_DISABLE_STATSD=1 turns it off outright. A present-but-empty
// STATSD_HOST means 127.0.0.1; STATSD_PORT defaults to 8125.
//
// UDP is fire-and-forget: every resolve, dial and write failure is swallowed.
// Telemetry must never fail or slow a merge.
package statsd

import (
	"net"
	"os"
)

// Prefix opens every spinclass metric name.
const Prefix = "spinclass."

const (
	defaultHost = "127.0.0.1"
	defaultPort = "8125"
)

// endpoint resolves host:port from the environment; ok=false means emission is
// off (not opted in, or explicitly disabled).
func endpoint() (addr string, ok bool) {
	if os.Getenv("SPINCLASS_DISABLE_STATSD") == "1" {
		return "", false
	}
	host, hostSet := os.LookupEnv("STATSD_HOST")
	port, portSet := os.LookupEnv("STATSD_PORT")
	if !hostSet && !portSet {
		return "", false
	}
	if host == "" {
		host = defaultHost
	}
	if port == "" {
		port = defaultPort
	}
	return net.JoinHostPort(host, port), true
}

// Count increments the counter spinclass.<name> by one. name is a fixed
// compile-time vocabulary of dot-separated segments.
func Count(name string) {
	addr, ok := endpoint()
	if !ok {
		return
	}
	conn, err := net.Dial("udp", addr)
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(Prefix + name + ":1|c"))
}
