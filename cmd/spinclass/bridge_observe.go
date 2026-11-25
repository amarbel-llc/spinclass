package main

import (
	"code.linenisgreat.com/spinclass/internal/alfa/bridgeserver"
	"code.linenisgreat.com/spinclass/internal/alfa/crossinglog"
)

// bridgeObserver is the listener's Observer: every connection's outcome goes
// to the crossing log (bridge contract v1, "Crossing log").
type bridgeObserver struct {
	log crossingRecorder
}

// Crossing records a listener-level event. A hello that did not become a
// stream is verb "hello"; a served control stream is verb "stream". A served
// session stream is not logged here: the handler logs each of its calls.
func (o *bridgeObserver) Crossing(e bridgeserver.Event) {
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
