package check

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/0/hostload"
	"code.linenisgreat.com/spinclass/internal/0/statsd"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/hostqueue"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

// Host admission (#424 step 3, docs/plans/2026-11-10-424-host-admission-queue-
// follow-up.md): every gate that runs through runHookPhase first waits for a
// host slot when the host queue is on. It only delays a gate; it never changes
// what the gate runs. A gate that times out fails with the wait's last reason,
// and like any failed gate it leaves the attestation buffered.

// ErrHostQueueTimeout is a gate's failure when it waited max-wait for a slot.
var ErrHostQueueTimeout = errors.New("host queue timeout")

// ErrHostQueueCancelled is a gate's failure when the operator cancelled its wait
// (spinclass#483); like any failed gate it leaves the attestation buffered.
var ErrHostQueueCancelled = errors.New("host queue: wait cancelled")

// hostQueueConfig decides whether a gate waits for a host slot, and under what
// policy: the trusted [host-queue] keys (#424 step 4), off unless a layer above
// the repo sets `enable = true`, so runHookPhase is unchanged with no section.
// An enabled section with an invalid key is returned with its error; the gate
// then runs unqueued with a warning. A seam for tests.
var hostQueueConfig = func(hierarchy sweatfile.Hierarchy, wtPath string) (hostqueue.Config, bool, error) {
	repoRoot, err := git.CommonDir(wtPath)
	if err != nil {
		repoRoot = wtPath
	}
	home, _ := os.UserHomeDir()
	hq, _ := sweatfileio.TrustedHostQueue(hierarchy, home, repoRoot)
	return hostqueue.ConfigFrom(hq)
}

// Seams for tests: the ledger, the probe, the clock and the heartbeat cadence.
var (
	hostQueueLedger    = hostqueue.DefaultLedger
	hostQueueProbe     = func() (hostload.Snapshot, error) { return hostload.Probe(hostload.ProbeOptions{}) }
	hostQueueNow       = time.Now
	hostQueueHeartbeat = 30 * time.Second
)

// admitHostSlot waits until the host queue admits this gate and returns the
// release for its slot (call it when the gate ends). With the queue off it
// returns at once. A ledger or probe that cannot be read admits with a warning:
// the queue is a scheduling aid and must not wedge every gate on the host.
func admitHostSlot(ctx context.Context, rep *crap.Reporter, hierarchy sweatfile.Hierarchy, wtPath string, activity io.Writer) (release func(), err error) {
	noop := func() {}
	cfg, on, cfgErr := hostQueueConfig(hierarchy, wtPath)
	if !on {
		return noop, nil
	}
	warn := func(msg string) (func(), error) {
		rep.Phase("warning: host queue: " + msg + "; the gate runs without waiting for a slot").Done()
		return noop, nil
	}
	if cfgErr != nil {
		return warn("the [host-queue] section is invalid (" + cfgErr.Error() + ")")
	}
	ledger, err := hostQueueLedger()
	if err != nil {
		return warn(err.Error())
	}

	marker := hostqueue.Marker{Kind: gateKindFrom(ctx), PID: os.Getpid()}
	if guardedGate(ctx) {
		marker.Kind = "guarded"
	}
	marker.Session, _ = session.KeyAtWorktree(wtPath)
	if repoRoot, err := git.CommonDir(wtPath); err == nil {
		marker.Repo = filepath.Base(repoRoot)
	}

	metrics := hostQueueMetrics{client: statsd.Resolve(hierarchy.Merged.MetricsStatsdAddress(), hierarchy.Merged.MetricsStatsdDisabled())}
	start := hostQueueNow()
	var (
		state     hostqueue.WaiterState
		lastBeat  time.Time
		lastWhy   hostqueue.Decision
		waitedFor = map[hostqueue.Reason]bool{}
		// The waiter's own marker: this gate's place in line (spinclass#482),
		// written under the ledger lock at its first look, and for host-load. It
		// is removed however the wait ends.
		waiter *hostqueue.Waiter
	)
	defer func() {
		if waiter != nil {
			waiter.Leave()
		}
	}()
	for {
		unlock, err := ledger.Lock()
		if err != nil {
			return warn("the ledger lock could not be taken: " + err.Error())
		}
		if waiter == nil {
			// Enqueue: the place is taken under the ledger lock, so places rise in
			// the order gates asked, and admission goes by place, not by who polls
			// first. A place that cannot be written means the ledger is unusable:
			// like any ledger failure, the gate runs unqueued with a warning.
			seq, serr := ledger.NextSeq()
			if serr != nil {
				unlock()
				return warn("a place in the queue could not be taken: " + serr.Error())
			}
			marker.Seq = seq
			w := marker
			w.WaitingSince = start
			wt, werr := ledger.OpenWait(w)
			if werr != nil {
				unlock()
				return warn("a place in the queue could not be written: " + werr.Error())
			}
			waiter = wt
			state.Seq = seq
		}
		// Management commands addressed to this waiter (spinclass#483), applied
		// under the lock before the decision, so a new priority counts at once.
		cancelled := ""
		for _, c := range waiter.ApplyCommands() {
			if activity != nil {
				_, _ = fmt.Fprintf(activity, "host queue: %s\n", c.Describe())
			}
			if c.IsCancel() {
				cancelled = c.Actor
			}
		}
		if cancelled != "" {
			// A cancel is taken before the decision, so it wins over an admission
			// this very look would have made; the place is released by the defer.
			unlock()
			waited := hostqueue.WaitedFor(waiter.Marker(), hostQueueNow())
			metrics.ended("cancelled", waited, waitedFor)
			return noop, fmt.Errorf("%w by %s after %s", ErrHostQueueCancelled, cancelled, waited.Round(time.Second))
		}
		if waiter.ExpireHold(hostQueueNow(), cfg.MaxWait) {
			m := waiter.Marker()
			_ = ledger.AppendJournal(hostqueue.JournalRecord{
				Time: hostQueueNow().UTC(), Actor: "queue", Act: "hold-waiter-expired", Target: m.Session, Seq: m.Seq,
				Old: "held", New: "not held", Assurance: "system",
			})
			if activity != nil {
				_, _ = fmt.Fprintf(activity, "host queue: this gate's hold expired after %s and was released\n", cfg.MaxWait)
			}
		}
		view, verr := ledger.View(true)
		snap, perr := hostQueueProbe()
		if verr != nil || perr != nil {
			unlock()
			return warn(fmt.Sprintf("the host could not be judged (%v)", errors.Join(verr, perr)))
		}
		now := hostQueueNow()
		d := hostqueue.Decide(view, snap, cfg, state, now)
		if d.Admit {
			marker.AdmittedAt = now
			rel, aerr := ledger.Admit(marker)
			waiter.Leave() // the place becomes the slot, still under the lock
			unlock()
			if aerr != nil {
				return warn("the slot marker could not be written: " + aerr.Error())
			}
			reportAdmission(rep, activity, now.Sub(start), d, waitedFor)
			metrics.admitted(now.Sub(start), waitedFor, len(view.Live)+1, d.Grace)
			return rel, nil
		}
		unlock()
		state, lastWhy = d.Next, d
		waitedFor[d.Reason] = true
		waiter.Update(d.Detail)

		// Time held by the operator does not count against max-wait.
		waited := hostqueue.WaitedFor(waiter.Marker(), now)
		if activity != nil && (lastBeat.IsZero() || now.Sub(lastBeat) >= hostQueueHeartbeat) {
			_, _ = fmt.Fprintf(activity, "host queue: %s; waited %s\n", d.Detail, waited.Round(time.Second))
			lastBeat = now
		}
		if cfg.MaxWait > 0 && waited >= cfg.MaxWait {
			// The place is released with the wait (it cannot be kept: nothing
			// holds it once this gate ends), so the message says where a retry goes.
			err := fmt.Errorf("%w after %s: %s; this gate's place in the queue is released, and a new merge or check joins the back of the queue", ErrHostQueueTimeout, cfg.MaxWait, lastWhy.Detail)
			rep.Phase("host queue").FailDiag(err, map[string]any{
				"severity": "fail",
				"message":  err.Error(),
				"reason":   string(lastWhy.Reason),
				"waited":   waited.Round(time.Second).String(),
			})
			metrics.ended("timeout", waited, waitedFor)
			return noop, err
		}
		select {
		case <-ctx.Done():
			metrics.ended("cancelled", hostQueueNow().Sub(start), waitedFor)
			return noop, fmt.Errorf("host queue: cancelled while waiting for a slot (%s): %w", lastWhy.Detail, ctx.Err())
		case <-time.After(cfg.Poll):
		}
	}
}

// hostQueueMetrics sends the queue's statsd series (#424 step 6, required by
// the operator's 11-05 Q7 ruling): host.queue_wait.<outcome> (ok, timeout,
// cancelled) as a timer, host.wait_reason.<reason> once per reason a wait met,
// host.gates_running as a gauge at each admission, and host.queue_grace when
// idle-grace admitted. Only when the queue is on; best-effort like every
// spinclass metric.
type hostQueueMetrics struct{ client statsd.Client }

func (m hostQueueMetrics) admitted(waited time.Duration, reasons map[hostqueue.Reason]bool, running int, grace bool) {
	m.ended("ok", waited, reasons)
	m.client.Gauge("host.gates_running", running)
	if grace {
		m.client.Count("host.queue_grace")
	}
}

func (m hostQueueMetrics) ended(outcome string, waited time.Duration, reasons map[hostqueue.Reason]bool) {
	m.client.Timing("host.queue_wait."+outcome, waited)
	for r := range reasons {
		m.client.Count("host.wait_reason." + statsd.Segment(string(r)))
	}
}

// reportAdmission emits one line when the gate waited (a sibling of the merge
// queue's wait point) or was admitted by idle-grace; an admission with no wait
// stays silent, so a quiet host's output does not change.
func reportAdmission(rep *crap.Reporter, activity io.Writer, waited time.Duration, d hostqueue.Decision, waitedFor map[hostqueue.Reason]bool) {
	if d.Grace {
		rep.Phase("warning: host queue: " + d.Detail).Done()
	}
	if len(waitedFor) == 0 {
		return
	}
	reasons := make([]string, 0, len(waitedFor))
	for _, r := range []hostqueue.Reason{
		hostqueue.ReasonHold, hostqueue.ReasonWaiterHold, hostqueue.ReasonQueue, hostqueue.ReasonGates, hostqueue.ReasonMemory, hostqueue.ReasonPressure,
		hostqueue.ReasonSwap, hostqueue.ReasonDisk, hostqueue.ReasonLoad, hostqueue.ReasonSettle,
	} {
		if waitedFor[r] {
			reasons = append(reasons, string(r))
		}
	}
	line := fmt.Sprintf("host queue wait %s (%s)", waited.Round(time.Second), strings.Join(reasons, ", "))
	rep.Phase(line).Done()
	if activity != nil {
		_, _ = fmt.Fprintln(activity, line)
	}
}
