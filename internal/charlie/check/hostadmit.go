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
		// The waiter's own marker, for host-load and sc queue-status; written on
		// the first refusal, removed however the wait ends.
		updateWaiter func(string)
		leaveWaiter  = func() {}
	)
	defer func() { leaveWaiter() }()
	for {
		unlock, err := ledger.Lock()
		if err != nil {
			return warn("the ledger lock could not be taken: " + err.Error())
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
		if updateWaiter == nil {
			w := marker
			w.WaitingSince, w.Reason = start, d.Detail
			if u, leave, werr := ledger.Wait(w); werr == nil {
				updateWaiter, leaveWaiter = u, leave
			} else {
				updateWaiter = func(string) {} // display only: a failed write never stops the wait
			}
		} else {
			updateWaiter(d.Detail)
		}

		waited := now.Sub(start)
		if activity != nil && (lastBeat.IsZero() || now.Sub(lastBeat) >= hostQueueHeartbeat) {
			_, _ = fmt.Fprintf(activity, "host queue: %s; waited %s\n", d.Detail, waited.Round(time.Second))
			lastBeat = now
		}
		if cfg.MaxWait > 0 && waited >= cfg.MaxWait {
			err := fmt.Errorf("%w after %s: %s", ErrHostQueueTimeout, cfg.MaxWait, lastWhy.Detail)
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
		hostqueue.ReasonHold, hostqueue.ReasonGates, hostqueue.ReasonMemory, hostqueue.ReasonPressure,
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
