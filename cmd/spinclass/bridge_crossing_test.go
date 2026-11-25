package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/protocol"

	"code.linenisgreat.com/spinclass/internal/alfa/bridgeserver"
	"code.linenisgreat.com/spinclass/internal/alfa/crossinglog"
)

const crossingSecret = "sk-live-XXXX"

func openTestCrossingLog(t *testing.T) (*crossinglog.Log, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "spinclass", "bridge")
	return crossinglog.Open(crossinglog.Options{Dir: dir, Principal: "p"}), filepath.Join(dir, "p.ndjson")
}

func readCrossings(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("%q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func waitForCrossings(t *testing.T, path string, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		es := readCrossings(t, path)
		if len(es) >= n || time.Now().After(deadline) {
			return es
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Every tools/call is one line carrying the tool name, the argument names, the
// outcome from the JSON-RPC result, and none of the argument values.
func TestBridgeSessionCallsAreLoggedWithoutValues(t *testing.T) {
	clog, path := openTestCrossingLog(t)
	sock, _ := startSessionBridgeWith(t, clog, nil)
	p := dialSession(t, sock)
	p.initialize(protocol.ProtocolVersionV1)

	p.call("tools/call", map[string]any{"name": "whoami", "arguments": map[string]any{"principal": crossingSecret}})
	p.call("tools/call", map[string]any{"name": "update-this-session-description", "arguments": map[string]any{
		"description": "fix: rotate " + crossingSecret + " before the release",
	}})
	p.call("tools/call", map[string]any{"name": "merge-this-session", "arguments": map[string]any{"message": crossingSecret}})
	p.call("tools/call", map[string]any{"name": "list-handles"})

	es := waitForCrossings(t, path, 4)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), crossingSecret) || strings.Contains(string(raw), "rotate") || strings.Contains(string(raw), "forged") {
		t.Fatalf("an argument value reached the crossing log:\n%s", raw)
	}
	if len(es) != 4 {
		t.Fatalf("%d lines, want 4:\n%s", len(es), raw)
	}
	want := []struct{ verb, outcome, names string }{
		{"whoami", "ok", "[principal]"},
		{"update-this-session-description", "refused", "[description]"},
		{"unknown", "error", "[message]"},
		{"list-handles", "ok", "[]"},
	}
	for i, w := range want {
		e := es[i]
		if e["verb"] != w.verb || e["outcome"] != w.outcome || e["service"] != "session" || e["session"] != sessionTestKey {
			t.Errorf("line %d = %v, want %+v for %s", i, e, w, sessionTestKey)
		}
		if got := jsonString(e["arg_names"]); got != w.names {
			t.Errorf("line %d arg_names = %s, want %s", i, got, w.names)
		}
		if n, _ := e["req_bytes"].(float64); n < 0 {
			t.Errorf("line %d req_bytes = %v", i, e["req_bytes"])
		}
	}
	if n, _ := es[0]["resp_bytes"].(float64); n <= 0 {
		t.Errorf("an ok call logged resp_bytes %v", es[0]["resp_bytes"])
	}
}

// dialSessionRaw sends hello and reads the reply to the end of the stream.
func dialSessionRaw(t *testing.T, sock, hello string) string {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(hello)); err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(c)
	return string(b)
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return strings.ReplaceAll(string(b), `"`, "")
}

// A log that cannot be written must not change what a call returns.
func TestBridgeSessionCallSucceedsWhenTheLogCannotBeWritten(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	clog := crossinglog.Open(crossinglog.Options{Dir: filepath.Join(blocked, "bridge"), Principal: "p"})
	sock, _ := startSessionBridgeWith(t, clog, nil)
	p := dialSession(t, sock)
	p.initialize(protocol.ProtocolVersionV1)
	text, isErr := p.toolText(p.call("tools/call", map[string]any{"name": "whoami", "arguments": map[string]any{}}))
	if isErr || !strings.Contains(text, sessionTestKey) {
		t.Fatalf("whoami with a broken log: %v %q", isErr, text)
	}
	if clog.Failures() == 0 {
		t.Error("the failed write was not counted")
	}
}

func TestBridgeObserverLogsListenerOutcomes(t *testing.T) {
	clog, path := openTestCrossingLog(t)
	obs := &bridgeObserver{log: clog}
	caller := bridgeserver.Caller{SessionKey: "repo/branch", Unit: "u.service", Transport: "bridge"}
	for _, e := range []bridgeserver.Event{
		{Service: "", Outcome: "hello-timeout", Duration: 5 * time.Second, Caller: caller},
		{Service: "", Outcome: "refused", Caller: caller},
		{Service: "control", Outcome: "busy", Caller: caller},
		{Service: "session", Outcome: "version", Caller: caller},
		{Service: "egress", Outcome: "unknown-service", Caller: caller},
		{Service: "control", Outcome: "ok", Duration: 3 * time.Second, Caller: caller},
		{Service: "session", Outcome: "ok", Caller: caller}, // its calls are logged by the handler
		{Service: "x-attacker-text", Outcome: "error", Caller: caller},
	} {
		obs.Crossing(e)
	}
	es := readCrossings(t, path)
	type line struct{ service, verb, outcome string }
	want := []line{
		{"unknown", "hello", "hello-timeout"},
		{"unknown", "hello", "refused"},
		{"control", "hello", "busy"},
		{"session", "hello", "error"},
		{"egress", "hello", "refused"},
		{"control", "stream", "ok"},
		{"unknown", "hello", "error"},
	}
	if len(es) != len(want) {
		t.Fatalf("%d lines, want %d: %v", len(es), len(want), es)
	}
	for i, w := range want {
		got := line{es[i]["service"].(string), es[i]["verb"].(string), es[i]["outcome"].(string)}
		if got != w {
			t.Errorf("line %d = %+v, want %+v", i, got, w)
		}
		if es[i]["session"] != "repo/branch" {
			t.Errorf("line %d session = %v", i, es[i]["session"])
		}
	}
	if es[0]["duration_ms"] != float64(5000) {
		t.Errorf("duration_ms = %v, want 5000", es[0]["duration_ms"])
	}
}

// The wiring end to end: a real listener refusing an unknown service logs it.
func TestBridgeListenerEventsReachTheCrossingLog(t *testing.T) {
	clog, path := openTestCrossingLog(t)
	sock, _ := startSessionBridgeWith(t, clog, &bridgeObserver{log: clog})
	if got := dialSessionRaw(t, sock, "BRIDGE/1 egress\n"); got != "ERR unknown-service egress\n" {
		t.Fatalf("reply %q", got)
	}
	es := waitForCrossings(t, path, 1)
	if len(es) != 1 || es[0]["service"] != "egress" || es[0]["outcome"] != "refused" || es[0]["verb"] != "hello" {
		t.Fatalf("lines = %v", es)
	}
}
