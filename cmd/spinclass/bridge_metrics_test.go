package main

import (
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/statsd"
	"code.linenisgreat.com/spinclass/internal/alfa/bridgeserver"
)

func metricDatagrams(t *testing.T, drain func() []string, want int) []string {
	t.Helper()
	var got []string
	deadline := time.Now().Add(5 * time.Second)
	for len(got) < want && time.Now().Before(deadline) {
		got = append(got, drain()...)
	}
	sort.Strings(got)
	return got
}

func TestBridgeObserverCountsAndTimesEachCrossing(t *testing.T) {
	drain := queueStatsdSink(t)
	obs := &bridgeObserver{stats: statsd.Resolve("", false)}
	caller := bridgeserver.Caller{SessionKey: "repo/branch"}
	for _, e := range []bridgeserver.Event{
		{Service: "session", Outcome: "ok", Duration: 1500 * time.Millisecond, Caller: caller},
		{Service: "egress", Outcome: "unknown-service", Duration: 2 * time.Millisecond, Caller: caller},
		{Service: "", Outcome: "hello-timeout", Duration: 5 * time.Second, Caller: caller},
		{Service: "evil.name;x", Outcome: "weird.outcome", Duration: time.Millisecond, Caller: caller},
	} {
		obs.Crossing(e)
	}
	got := metricDatagrams(t, drain, 8)
	want := []string{
		"spinclass.bridge.egress.duration_ms:2|ms",
		"spinclass.bridge.egress.refused:1|c",
		"spinclass.bridge.session.duration_ms:1500|ms",
		"spinclass.bridge.session.ok:1|c",
		"spinclass.bridge.unknown.duration_ms:1|ms",
		"spinclass.bridge.unknown.duration_ms:5000|ms",
		"spinclass.bridge.unknown.error:1|c",
		"spinclass.bridge.unknown.hello-timeout:1|c",
	}
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("datagrams:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The metrics leave the crossing's goroutine: Crossing returns before any UDP
// is sent, so a slow or unreachable endpoint cannot hold a crossing.
func TestBridgeObserverEmitsOffTheCrossingPath(t *testing.T) {
	drain := queueStatsdSink(t)
	var queued []func()
	obs := &bridgeObserver{stats: statsd.Resolve("", false), spawn: func(fn func()) { queued = append(queued, fn) }}
	obs.Crossing(bridgeserver.Event{Service: "control", Outcome: "busy"})
	if len(queued) == 0 {
		t.Fatal("nothing was handed to the spawner")
	}
	if got := drain(); len(got) != 0 {
		t.Fatalf("Crossing sent %v before returning", got)
	}
	for _, fn := range queued {
		fn()
	}
	if got := metricDatagrams(t, drain, 2); len(got) != 2 {
		t.Errorf("after running the queued work got %v", got)
	}
}

func TestBridgeObserverWithMetricsOffSendsNothingAndDoesNotPanic(t *testing.T) {
	t.Setenv("SPINCLASS_DISABLE_STATSD", "1")
	spawned := false
	obs := &bridgeObserver{stats: statsd.Resolve("", false), spawn: func(func()) { spawned = true }}
	obs.Crossing(bridgeserver.Event{Service: "session", Outcome: "ok"})
	if spawned {
		t.Error("disabled metrics still spawned work")
	}
	(&bridgeObserver{}).Crossing(bridgeserver.Event{Service: "session", Outcome: "ok"})
}

func TestBoundedSpawnerDropsWhenSaturated(t *testing.T) {
	spawn := newBoundedSpawner(2)
	release := make(chan struct{})
	var mu sync.Mutex
	started := 0
	for i := 0; i < 5; i++ {
		spawn(func() {
			mu.Lock()
			started++
			mu.Unlock()
			<-release
		})
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := started
		mu.Unlock()
		if n == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	n := started
	mu.Unlock()
	close(release)
	if n != 2 {
		t.Errorf("%d tasks ran with 2 slots; the rest must be dropped, not queued", n)
	}
}
