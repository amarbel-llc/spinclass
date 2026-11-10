package check

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/gatecost"
	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/0/statsd"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/bravo/hookrun"
)

// Per-gate cost tracking (#424 step 0,
// docs/plans/2026-11-10-424-host-admission-queue-follow-up.md §2, §9b). Every
// gate that runs through runHookPhase records one cost record after it ends:
// statsd series under gate.<repo>.<kind>.* and one history.ndjson line. It is
// tracking only: it changes nothing about when or whether a gate runs, and a
// recording failure never fails or changes a gate (the #349 madder rule); it
// is at most a warning in the activity (job) log.

type gateKindKey struct{}

// WithGateKind returns ctx naming who runs the gate under it: a merge's
// pre-merge sets gatecost.KindMerge; unset is gatecost.KindCheck. It is the
// cost record's kind, and always its caller; a guarded gate records kind
// gatecost.KindGuarded with this as the caller (history line only; the statsd
// names stay gate.<repo>.guarded.*).
func WithGateKind(ctx context.Context, kind string) context.Context {
	return context.WithValue(ctx, gateKindKey{}, kind)
}

func gateKindFrom(ctx context.Context) string {
	if kind, _ := ctx.Value(gateKindKey{}).(string); kind != "" {
		return kind
	}
	return gatecost.KindCheck
}

type guardedGateKey struct{}

func withGuardedGate(ctx context.Context) context.Context {
	return context.WithValue(ctx, guardedGateKey{}, true)
}

func guardedGate(ctx context.Context) bool {
	v, _ := ctx.Value(guardedGateKey{}).(bool)
	return v
}

// daemonCPUStatPath is nix-daemon's cgroup cpu.stat, read from the spinclass
// process before and after a gate: nix-daemon builds in its own cgroup, not the
// gate's scope, so the scope undercounts (§2). The delta is attributable only
// while one gate runs at a time. A seam for tests.
var daemonCPUStatPath = "/sys/fs/cgroup/system.slice/nix-daemon.service/cpu.stat"

// gateCostRecorder carries one gate's cost from just before the hook starts to
// just after it ends. A nil recorder (recording off) does nothing.
type gateCostRecorder struct {
	client       statsd.Client
	repo         string
	kind         string
	caller       string
	session      string
	activity     io.Writer
	daemonBefore uint64
	daemonOK     bool
	scope        gatecost.Scope
}

// startGateCost resolves the SPINCLASS_DISABLE_GATE_COST kill switch and the
// trusted [host-queue].record-costs switch and, when recording is on, takes the
// before-readings. It returns nil when recording is off.
func startGateCost(ctx context.Context, hierarchy sweatfile.Hierarchy, wtPath string, activity io.Writer) *gateCostRecorder {
	if gatecost.Disabled() {
		return nil
	}
	repoRoot, err := git.CommonDir(wtPath)
	if err != nil {
		repoRoot = wtPath
	}
	home, _ := os.UserHomeDir()
	if record, _ := sweatfileio.TrustedHostQueueRecordCosts(hierarchy, home, repoRoot); !record {
		return nil
	}
	r := &gateCostRecorder{
		client:   statsd.Resolve(hierarchy.Merged.MetricsStatsdAddress(), hierarchy.Merged.MetricsStatsdDisabled()),
		repo:     filepath.Base(repoRoot),
		kind:     gateKindFrom(ctx),
		caller:   gateKindFrom(ctx),
		activity: activity,
	}
	if guardedGate(ctx) {
		r.kind = gatecost.KindGuarded
	}
	r.session, _ = session.KeyAtWorktree(wtPath)
	r.daemonBefore, r.daemonOK = gatecost.ReadCPUUsageUsec(daemonCPUStatPath)
	return r
}

// hookContext asks a scoped hook to report its scope cgroup's accounting.
func (r *gateCostRecorder) hookContext(ctx context.Context) context.Context {
	if r == nil {
		return ctx
	}
	return hookrun.WithScopeAccounting(ctx, func(s gatecost.Scope) { r.scope = s })
}

// finish records the gate that just ended. output is the gate's captured
// output, read only when the gate failed (for the failing just recipe).
func (r *gateCostRecorder) finish(ctx context.Context, hookErr error, wall time.Duration, output func() string) {
	if r == nil {
		return
	}
	rec := gatecost.Record{
		TS:           time.Now(),
		Repo:         r.repo,
		Kind:         r.kind,
		Caller:       r.caller,
		Outcome:      statsd.Outcome(ctx, hookErr, errors.Is(hookErr, hookrun.ErrInactivityTimeout)),
		Wall:         wall,
		CPUUsec:      r.scope.CPUUsec,
		MemPeakBytes: r.scope.MemPeakBytes,
		Session:      r.session,
	}
	if r.daemonOK {
		if after, ok := gatecost.ReadCPUUsageUsec(daemonCPUStatPath); ok && after >= r.daemonBefore {
			delta := after - r.daemonBefore
			rec.DaemonCPUUsec = &delta
		}
	}
	if hookErr != nil {
		rec.FailedRecipe = gatecost.LastFailedRecipe(output())
	}
	r.emitMetrics(rec)
	if err := gatecost.AppendHistory(gatecost.HistoryPath(), rec); err != nil && r.activity != nil {
		_, _ = fmt.Fprintf(r.activity, "[spinclass] gate cost not recorded: %v\n", err)
	}
}

// emitMetrics sends gate.<repo>.<kind>.*: the wall timer by outcome, the CPU
// figures as millisecond timers (the client has no float gauge), the memory
// peak as a gauge, and a counter per failing recipe.
func (r *gateCostRecorder) emitMetrics(rec gatecost.Record) {
	prefix := "gate." + statsd.Segment(rec.Repo) + "." + statsd.Segment(rec.Kind) + "."
	r.client.Timing(prefix+"wall."+rec.Outcome, rec.Wall)
	if rec.CPUUsec != nil {
		r.client.Timing(prefix+"cpu_seconds", usecDuration(*rec.CPUUsec))
	}
	if rec.MemPeakBytes != nil {
		r.client.Gauge(prefix+"mem_peak_bytes", int(min(*rec.MemPeakBytes, math.MaxInt)))
	}
	if rec.DaemonCPUUsec != nil {
		r.client.Timing(prefix+"daemon_cpu_seconds", usecDuration(*rec.DaemonCPUUsec))
	}
	if rec.FailedRecipe != "" {
		r.client.Count(prefix + "failed_recipe." + statsd.Segment(rec.FailedRecipe))
	}
}

func usecDuration(usec uint64) time.Duration {
	return time.Duration(min(usec, math.MaxInt64/uint64(time.Microsecond))) * time.Microsecond
}
