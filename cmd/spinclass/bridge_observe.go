package main

import (
	"fmt"
	"sync"

	"code.linenisgreat.com/spinclass/internal/0/statsd"
	"code.linenisgreat.com/spinclass/internal/alfa/bridgeserver"
	"code.linenisgreat.com/spinclass/internal/alfa/crossinglog"
)

// bridgeMetricSlots bounds the metric emissions in flight at once. A UDP send
// is bounded already (statsd's 250 ms dial rule); the cap keeps a burst of
// crossings against an unreachable endpoint from piling up goroutines.
const bridgeMetricSlots = 16

// bridgeObserver is the listener's Observer: every connection's outcome goes
// to the crossing log (spinclass-bridge(7), "Observability") and to statsd as a
// counter and a timer.
type bridgeObserver struct {
	log   crossingRecorder
	stats statsd.Client
	// spawn runs a metric emission off the crossing's goroutine; nil is the
	// shared bounded spawner.
	spawn func(func())
	// signal reports a condition a holder should hear about (kind is "version"
	// or "peer-refused"); nil reports nothing. Serve sets it to the wake.
	signal func(session, kind, msg string)

	mu          sync.Mutex
	refusedRun  int
	versionSeen bool
}

// bridgePeerRefusalWake is how many peer refusals in a row (no served stream
// between them) are worth a wake: one is a stray process, five is a bridge
// nobody can use.
const bridgePeerRefusalWake = 5

var sharedBridgeMetricSpawner = newBoundedSpawner(bridgeMetricSlots)

// newBoundedSpawner returns a spawner that runs each task on its own
// goroutine, at most n at a time; a task offered while all n slots are taken
// is dropped (metrics are best effort), never queued and never waited for.
func newBoundedSpawner(n int) func(func()) {
	slots := make(chan struct{}, n)
	return func(fn func()) {
		select {
		case slots <- struct{}{}:
		default:
			return
		}
		go func() {
			defer func() { <-slots }()
			fn()
		}()
	}
}

// Crossing records a listener-level event. A hello that did not become a
// stream is verb "hello"; a served control stream is verb "stream". A served
// session stream is not logged here: the handler logs each of its calls.
func (o *bridgeObserver) Crossing(e bridgeserver.Event) {
	o.logCrossing(e)
	o.emitMetrics(e)
	o.watch(e)
}

// watch turns two patterns into one signal each: the first ERR version (the
// shim and this frontend were built apart, and every connection will say the
// same) and bridgePeerRefusalWake peer refusals in a row (the bridge is up and
// nobody it admits can reach it). A peer refusal is a refused outcome with no
// service: a hello that named an unknown service has one. A served stream ends
// the run.
func (o *bridgeObserver) watch(e bridgeserver.Event) {
	if o.signal == nil {
		return
	}
	var kind, msg string
	o.mu.Lock()
	switch {
	case e.Outcome == crossinglog.OutcomeVersion && !o.versionSeen:
		o.versionSeen = true
		kind = "version"
		msg = fmt.Sprintf("a shim of %s said hello with a bridge version this frontend does not speak (ERR version): its bridge is down until the shim and this frontend come from builds that agree", e.Caller.SessionKey)
	case e.Outcome == crossinglog.OutcomeRefused && e.Service == "":
		o.refusedRun++
		if o.refusedRun >= bridgePeerRefusalWake {
			o.refusedRun = 0
			kind = "peer-refused"
			msg = fmt.Sprintf("%d connections in a row to the bridge of %s were refused: they did not come from its unit, or its cgroup file is not valid, so the bridge is not usable until that is fixed", bridgePeerRefusalWake, e.Caller.SessionKey)
		}
	case e.Outcome == crossinglog.OutcomeOK:
		o.refusedRun = 0
	}
	o.mu.Unlock()
	if kind != "" {
		o.signal(e.Caller.SessionKey, kind, msg)
	}
}

// bridgeWakeSignal is the signal serve wires into its observer.
func bridgeWakeSignal(session, kind, msg string) {
	bridgeWake(session, kind, kind, msg)
}

func (o *bridgeObserver) logCrossing(e bridgeserver.Event) {
	if o.log == nil {
		return
	}
	verb := "hello"
	if e.Outcome == crossinglog.OutcomeOK {
		if e.Service == bridgeserver.ServiceSession {
			return
		}
		verb = "stream"
	}
	o.log.Record(crossinglog.Crossing{
		Session:  e.Caller.SessionKey,
		Service:  e.Service,
		Verb:     verb,
		Outcome:  e.Outcome,
		Duration: e.Duration,
	})
}

// emitMetrics counts spinclass.bridge.<service>.<outcome> and times
// spinclass.bridge.<service>.duration_ms. The service comes from the closed
// reserved set and the outcome from the closed outcome set (anything else is
// "unknown" / "error"), so a metric name never carries a peer's text.
func (o *bridgeObserver) emitMetrics(e bridgeserver.Event) {
	if !o.stats.Enabled() {
		return
	}
	service := crossinglog.ServiceLabel(e.Service)
	outcome := crossinglog.NormalizeOutcome(e.Outcome)
	spawn := o.spawn
	if spawn == nil {
		spawn = sharedBridgeMetricSpawner
	}
	stats, d := o.stats, e.Duration
	spawn(func() {
		stats.Count("bridge." + service + "." + outcome)
		stats.Timing("bridge."+service+".duration_ms", d)
	})
}
