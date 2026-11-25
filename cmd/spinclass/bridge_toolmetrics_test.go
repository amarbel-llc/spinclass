package main

// Per-tool metrics of the session service: for each tools/call, a counter
// spinclass.bridge.session.tool.<tool>.<outcome> and a timer
// spinclass.bridge.session.tool.<tool>.duration_ms, where <tool> is one of the
// five names with "-" as "_" or "unknown", and <outcome> is ok, refused or
// error. The tests run the real handler over a real stream with the fake UDP
// statsd of the other bridge tests.

import (
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"
)

// waitMetricShaped waits until a datagram with the prefix and suffix has
// reached the fake statsd (a timer's value is not known in advance).
func (b *e2eBridge) waitMetricShaped(prefix, suffix string) {
	b.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b.metrics = append(b.metrics, b.drain()...)
		for _, m := range b.metrics {
			if strings.HasPrefix(m, prefix) && strings.HasSuffix(m, suffix) {
				return
			}
		}
		if time.Now().After(deadline) {
			b.t.Fatalf("statsd never saw %s...%s; saw %v", prefix, suffix, b.metrics)
		}
	}
}

// Each outcome of a served call is counted and timed under its own tool. It
// fails if a tool's name does not appear, if an outcome is misfiled (a refusal
// counted as an error or the reverse), or if the timer is missing.
func TestE2EToolMetricsCountAndTimeEachOutcome(t *testing.T) {
	b := startE2E(t, e2eOpts{})
	p := b.open("session")
	p.initialize(protocol.ProtocolVersionV1)

	// ok
	b.wantThisSession(b.whoami(p, map[string]any{}))
	// refused: -32001
	if rep := p.call("tools/call", map[string]any{"name": "cancel-restart", "arguments": map[string]any{"child": "someone/else"}}); rep.Error == nil || rep.Error.Code != -32001 {
		t.Fatalf("cancel-restart naming a child: %+v", rep.Error)
	}
	// error: a tool result with isError that is not a refusal (close-this-session
	// takes no force argument).
	rep := p.call("tools/call", map[string]any{"name": "close-this-session", "arguments": map[string]any{"force": false}})
	if text, isErr := p.toolText(rep); !isErr || strings.HasPrefix(text, "refused: ") {
		t.Fatalf("close-this-session with force: %v %q", isErr, text)
	}

	b.waitMetric(
		"spinclass.bridge.session.tool.whoami.ok:1|c",
		"spinclass.bridge.session.tool.cancel_restart.refused:1|c",
		"spinclass.bridge.session.tool.close_this_session.error:1|c",
	)
	for _, tool := range []string{"whoami", "cancel_restart", "close_this_session"} {
		b.waitMetricShaped("spinclass.bridge.session.tool."+tool+".duration_ms:", "|ms")
	}
}

// A tool outside the five is "unknown", and no byte of its name reaches a
// metric name: dots, newlines and a second metric smuggled behind a newline
// stay out. It fails if the name is echoed in any form.
func TestE2EToolMetricsNeverCarryTheRequestsToolName(t *testing.T) {
	b := startE2E(t, e2eOpts{})
	p := b.open("session")
	p.initialize(protocol.ProtocolVersionV1)

	hostile := []string{
		"merge-this-session",
		"who.ami",
		"whoami\nspinclass.evil:1|c",
		"whoami.evil",
		"WHOAMI",
		"list_handles",
		"",
	}
	for _, name := range hostile {
		rep := p.call("tools/call", map[string]any{"name": name, "arguments": map[string]any{}})
		if rep.Error == nil {
			t.Fatalf("tool %q: want an error, got %s", name, rep.Result)
		}
	}
	b.waitMetric("spinclass.bridge.session.tool.unknown.error:1|c")
	b.waitMetricShaped("spinclass.bridge.session.tool.unknown.duration_ms:", "|ms")
	// Let the rest arrive, then look at everything that did.
	time.Sleep(300 * time.Millisecond)
	b.metrics = append(b.metrics, b.drain()...)
	unknown := 0
	for _, m := range b.metrics {
		if strings.Contains(m, "evil") || strings.ContainsAny(m, "\r\n") || strings.Contains(m, "merge") || strings.Contains(m, "who") || strings.Contains(m, "WHOAMI") || strings.Contains(m, "list_handles") {
			t.Errorf("a request's tool name reached a metric: %q", m)
		}
		if m == "spinclass.bridge.session.tool.unknown.error:1|c" {
			unknown++
		}
	}
	if unknown > len(hostile) {
		t.Errorf("the unknown-tool counter was sent %d times for %d calls", unknown, len(hostile))
	}
}
