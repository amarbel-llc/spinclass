package merge

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/0/statsd"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/landing"
	"code.linenisgreat.com/spinclass/internal/bravo/hookrun"
)

// Merge-landing counters (spinclass#314), emitted only on the queued remote
// landing path, so local_advance.ok + local_advance.skip == landed holds by
// construction — the invariant FDR 0029's promotion criterion checks. The
// per-cause skip counter is a sibling name (skip_reason.<slug>), not a child
// of skip, so graphite never has to hold skip as both a leaf and a branch.
const (
	MetricLanded                 = "merge.landed"
	MetricPushRefused            = "merge.push_refused"
	MetricLocalAdvanceOk         = "merge.local_advance.ok"
	MetricLocalAdvanceSkip       = "merge.local_advance.skip"
	MetricLocalAdvanceSkipReason = "merge.local_advance.skip_reason."
)

// phaseMetrics emits the per-phase merge timers (spinclass#363) as
// merge.<repo>.<phase>.<outcome>, through the Client resolved once from the
// session worktree's sweatfile hierarchy. The zero value emits nothing.
type phaseMetrics struct {
	client statsd.Client
	repo   string // statsd.Segment of the repo dirname
}

// newPhaseMetrics resolves the statsd Client from sf's [metrics] table (the
// zero Sweatfile when the hierarchy did not load, which leaves the environment
// opt-in as the only switch).
func newPhaseMetrics(sf sweatfile.Sweatfile, repoPath string) phaseMetrics {
	return phaseMetrics{
		client: statsd.Resolve(sf.MetricsStatsdAddress(), sf.MetricsStatsdDisabled()),
		repo:   statsd.Segment(filepath.Base(repoPath)),
	}
}

// observe emits the timer for phase (one or more dot-separated segments that
// are already name-safe) as <phase>.<outcome>, covering start until now.
func (m phaseMetrics) observe(phase string, start time.Time, outcome string) {
	m.client.Timing("merge."+m.repo+"."+phase+"."+outcome, time.Since(start))
}

// observeErr is observe with the outcome classified from the phase's error.
func (m phaseMetrics) observeErr(ctx context.Context, phase string, start time.Time, err error) {
	m.observe(phase, start, outcomeOf(ctx, err))
}

// observePreMergeHook emits the pre_merge_hook timer, but only when sf
// configures a hook: with none nothing ran, and a stream of near-zero samples
// would drag the phase's percentiles toward zero.
func (m phaseMetrics) observePreMergeHook(ctx context.Context, sf sweatfile.Sweatfile, start time.Time, err error) {
	if cmd := sf.PreMergeHookCommand(); cmd == nil || *cmd == "" {
		return
	}
	m.observeErr(ctx, "pre_merge_hook", start, err)
}

// startedAtOr is pm.StartedAt, or fallback when the caller did not stamp one.
func startedAtOr(pm PostMergeOptions, fallback time.Time) time.Time {
	if pm.StartedAt.IsZero() {
		return fallback
	}
	return pm.StartedAt
}

// timedFetchTarget is fetchTarget timed as phase; a self target runs nothing,
// so it emits nothing.
func timedFetchTarget(ctx context.Context, mx phaseMetrics, phase string, ts *crap.TestStream, target landing.Target, credDir, suffix string) error {
	if target.IsSelf() {
		return nil
	}
	start := time.Now()
	err := fetchTarget(ctx, ts, target, credDir, suffix)
	mx.observeErr(ctx, phase, start, err)
	return err
}

// postMergeHookOutcome classifies the legacy [hooks].post-merge string hook: a
// kill by its own wall-clock cap is timeout, distinct from the caller's cancel.
func postMergeHookOutcome(ctx context.Context, err error) string {
	return statsd.Outcome(ctx, err, errors.Is(err, hookrun.ErrPostMergeTimeout))
}

// observePostMergeTarget emits post_merge.<target>.command.<outcome> and, only
// when verify ran, post_merge.<target>.verify.<outcome> for one named target.
// timedOut/cancelled are the phase's own judgement of why the failed stage died
// (the shared deadline vs the caller's ctx); a stage that succeeded is ok, so a
// verify failure reads command.ok + verify.failed.
func (m phaseMetrics) observePostMergeTarget(name string, run hookrun.TargetRun, runErr error, timedOut, cancelled bool) {
	stageOutcome := func(stageFailed bool) string {
		switch {
		case !stageFailed:
			return statsd.OutcomeOK
		case timedOut:
			return statsd.OutcomeTimeout
		case cancelled:
			return statsd.OutcomeCancelled
		}
		return statsd.OutcomeFailed
	}
	prefix := "merge." + m.repo + ".post_merge." + statsd.Segment(name)
	m.client.Timing(prefix+".command."+stageOutcome(runErr != nil && !run.VerifyRan), run.CommandDuration)
	if run.VerifyRan {
		m.client.Timing(prefix+".verify."+stageOutcome(runErr != nil), run.VerifyDuration)
	}
}

// outcomeOf classifies a finished phase: nil error is ok, a done ctx is
// cancelled, an inactivity-watchdog or repair-timeout kill is timeout, anything
// else is failed.
func outcomeOf(ctx context.Context, err error) string {
	return statsd.Outcome(ctx, err, errors.Is(err, hookrun.ErrInactivityTimeout) || errors.Is(err, hookrun.ErrRepairTimeout))
}
