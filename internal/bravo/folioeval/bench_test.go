package folioeval

import (
	"slices"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

// realisticPolicy is the circus#399 worked case plus about twenty entries of
// the kind an operator's global sweatfile would carry: the latency the
// broker's deadline must cover is one Evaluate over this.
func realisticPolicy(tb testing.TB) *Policy {
	tb.Helper()
	p, err := Compile(sweatfileio.FolioPolicy{
		Configured: true,
		Read: []string{
			"pwd:", "nix-store:", "home:.claude", "home:.config/spinclass", "home:.local/log/moxy",
			"env:CLAUDE_CODE_TMPDIR:", "env:TMPDIR:", "env:XDG_RUNTIME_DIR:clown",
			"spinclass:siblings", "spinclass:children", "spinclass:repo/docs", "spinclass:repo/doc",
			"spinclass:worktrees", "spinclass:worktrees/*/.spinclass/spawn.log",
			"spinclass:worktrees/*/docs", "spinclass:worktrees/*/.tmp/**", "madder:",
		},
		Write: []string{
			"pwd:", "home:.claude/plans", "home:.claude/projects", "env:CLAUDE_CODE_TMPDIR:", "env:TMPDIR:",
		},
		Deny: []string{
			"spinclass:worktrees/*/.spinclass", "env:XDG_STATE_HOME:circus", "home:.ssh",
			"home:.config/piggy", "pwd:.spinclass/credentials",
		},
		Unmatched: "ask",
	})
	if err != nil {
		tb.Fatal(err)
	}
	return p
}

// reportPercentiles records p50 and p99 of per-call durations as custom
// benchmark metrics, since ns/op is a mean.
func reportPercentiles(b *testing.B, durations []time.Duration) {
	b.Helper()
	if len(durations) == 0 {
		return
	}
	slices.Sort(durations)
	at := func(q float64) float64 {
		i := int(q * float64(len(durations)-1))
		return float64(durations[i].Nanoseconds())
	}
	b.ReportMetric(at(0.50), "p50-ns/op")
	b.ReportMetric(at(0.99), "p99-ns/op")
}

// BenchmarkEvaluate is the evaluator alone, roots already built, over paths
// that exercise the deepest matches (a held child's spawn log, which matches
// three entries) and the unmatched case.
func BenchmarkEvaluate(b *testing.B) {
	p := realisticPolicy(b)
	paths := []string{
		"/w/repo/.worktrees/kid/.spinclass/spawn.log",
		"/w/repo/.worktrees/kid/.spinclass/state.json",
		"/w/repo/.worktrees/me/internal/0/folio/pattern.go",
		"/w/other/README.md",
		"/nix/store/abc-hello/bin/hello",
		"/etc/hostname",
	}
	durations := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		start := time.Now()
		p.Evaluate(roots, "read", paths[i%len(paths)])
		durations = append(durations, time.Since(start))
	}
	reportPercentiles(b, durations)
}

// BenchmarkResolve is the path rule's filesystem cost: one symlink walk on
// the parent per check, against a real directory.
func BenchmarkResolve(b *testing.B) {
	dir := b.TempDir()
	path := dir + "/a/b/c.go"
	durations := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for b.Loop() {
		start := time.Now()
		Resolve(path)
		durations = append(durations, time.Since(start))
	}
	reportPercentiles(b, durations)
}
