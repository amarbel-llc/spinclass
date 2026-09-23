package merge

import (
	"strconv"
	"time"

	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

// PostMergeOptions is the per-merge configuration of the post-merge phase
// (FDR 0023 / FDR 0026): what a single merge call may vary about the phase
// without touching the sweatfile. It travels one hop at a time through
// Run → Resolved/ResolvedContext → PrepareMerge/FinishMerge →
// runPostMergePhase, exactly where the bare targets
// selection used to.
type PostMergeOptions struct {
	// Targets selects which named [[post-merge]] targets deploy: nil = all
	// active targets (the default), a non-nil list = exactly those names, an
	// empty list = none. An unknown name fails PrepareMerge before anything
	// lands. Absent-vs-empty is load-bearing.
	Targets []string

	// Timeout overrides [hooks].post-merge-timeout for THIS merge only: nil
	// leaves the sweatfile (or its 10m default) in force; a zero duration
	// disables the cap; any positive duration is the cap, raising OR lowering
	// the sweatfile's value with no ceiling — a real deploy that legitimately
	// outruns the repo's usual cap is the caller's call to wait for. Parse
	// caller-supplied strings with sweatfile.ParsePostMergeTimeout.
	Timeout *time.Duration
}

// EffectiveTimeout resolves the wall-clock cap the phase actually enforces:
// the per-merge override when set, else the sweatfile's PostMergeTimeoutValue
// (which already folds in the default). <= 0 means no cap.
func (o PostMergeOptions) EffectiveTimeout(sf sweatfile.Sweatfile) time.Duration {
	if o.Timeout != nil {
		return *o.Timeout
	}
	return sf.PostMergeTimeoutValue()
}

// PostMergeFacts are the merge-context values a post-merge command or verify
// script is handed as SPINCLASS_* environment (PostMergeEnv). One builder feeds
// every site that runs post-merge work — the sweatfile phase in this package
// and `sc run`'s dynamic --post-merge hooks — so the two cannot drift.
type PostMergeFacts struct {
	LandedSha     string // the commit that landed (the LANDING sha on a rebased queued landing)
	PinnedSha     string // the pre-landing pin; "" when the site has no pin to report
	Branch        string // the branch that merged
	DefaultBranch string // the branch it landed on
	RepoPath      string // the main checkout
	Pushed        bool   // whether the landing reached the remote
	Timeout       time.Duration
	Deadline      time.Time // zero when Timeout <= 0 (uncapped)
}

// PostMergeEnv renders the facts as KEY=VALUE pairs for a hook's environment.
//
// The timing trio exists so a verify poll can size itself from the cap it
// actually runs under instead of hard-coding one (the circus/krone failure
// that motivated the per-merge override): SPINCLASS_POST_MERGE_TIMEOUT is the
// effective cap as a Go duration ("0" when uncapped) for Go/structured
// consumers; SPINCLASS_POST_MERGE_TIMEOUT_SECONDS is the same cap as a plain
// integer for shell arithmetic; SPINCLASS_POST_MERGE_DEADLINE is the phase's
// absolute deadline as unix epoch seconds ("0" when uncapped) — what a poll
// that starts after a slow command has already eaten some budget actually
// needs. SPINCLASS_PINNED_SHA lets a script detect a rebased landing
// (PINNED != MERGED) without reconstructing it from git.
func PostMergeEnv(f PostMergeFacts) []string {
	pushed := "0"
	if f.Pushed {
		pushed = "1"
	}
	env := []string{
		"SPINCLASS_MERGED_SHA=" + f.LandedSha,
		"SPINCLASS_MERGED_BRANCH=" + f.Branch,
		"SPINCLASS_DEFAULT_BRANCH=" + f.DefaultBranch,
		"SPINCLASS_MERGE_PUSHED=" + pushed,
		"SPINCLASS_REPO_PATH=" + f.RepoPath,
		"SPINCLASS_POST_MERGE_TIMEOUT=" + FormatPostMergeTimeout(f.Timeout),
		"SPINCLASS_POST_MERGE_TIMEOUT_SECONDS=" + strconv.FormatInt(int64(max(f.Timeout, 0)/time.Second), 10),
		"SPINCLASS_POST_MERGE_DEADLINE=" + formatDeadline(f.Timeout, f.Deadline),
	}
	if f.PinnedSha != "" {
		env = append(env, "SPINCLASS_PINNED_SHA="+f.PinnedSha)
	}
	return env
}

// FormatPostMergeTimeout renders a cap the way SPINCLASS_POST_MERGE_TIMEOUT
// carries it: "0" when disabled (<= 0), else Go's duration form ("20m0s"),
// which sweatfile.ParsePostMergeTimeout round-trips.
func FormatPostMergeTimeout(d time.Duration) string {
	if d <= 0 {
		return "0"
	}
	return d.String()
}

func formatDeadline(timeout time.Duration, deadline time.Time) string {
	if timeout <= 0 || deadline.IsZero() {
		return "0"
	}
	return strconv.FormatInt(deadline.Unix(), 10)
}
