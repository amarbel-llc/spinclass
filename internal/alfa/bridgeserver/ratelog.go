package bridgeserver

import (
	"sync"
	"time"
)

// logWindow is how often one kind of listener log line may be written.
const logWindow = 10 * time.Second

// maxLogKinds bounds the kinds tracked at once; a line whose kind is not
// tracked yet when the table is full is written, so nothing is hidden by the
// bound itself.
const maxLogKinds = 64

// rateLimitedLogf wraps logf so that one kind of line (its format string) is
// written at most once per window. A peer that connects in a loop and is
// refused each time would otherwise write a line per attempt into serve's log;
// the lines it did not get are counted and the next one written says how many.
// The format is a literal at every call site, so a peer's text is never the key.
func rateLimitedLogf(logf func(string, ...any), window time.Duration, now func() time.Time) func(string, ...any) {
	type kind struct {
		last       time.Time
		suppressed int
	}
	var (
		mu    sync.Mutex
		kinds = map[string]*kind{}
	)
	return func(format string, args ...any) {
		t := now()
		mu.Lock()
		k, ok := kinds[format]
		if !ok && len(kinds) >= maxLogKinds {
			mu.Unlock()
			logf(format, args...)
			return
		}
		if !ok {
			k = &kind{}
			kinds[format] = k
		}
		if !k.last.IsZero() && t.Sub(k.last) < window {
			k.suppressed++
			mu.Unlock()
			return
		}
		n := k.suppressed
		k.suppressed = 0
		k.last = t
		mu.Unlock()
		logf(format, args...)
		if n > 0 {
			logf("bridge: %d similar log lines were suppressed in the last %v", n, window)
		}
	}
}
