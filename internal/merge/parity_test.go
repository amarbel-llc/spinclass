package merge

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/crap"
	"code.linenisgreat.com/crap/go-crap/v2/ndjsoncrap"
)

// phaseVerdict is how a merge phase showed up in a merge's test-point stream.
type phaseVerdict string

const (
	verdictAbsent  phaseVerdict = "absent"
	verdictOk      phaseVerdict = "ok"
	verdictSkipped phaseVerdict = "skipped"
	verdictNotOk   phaseVerdict = "not ok"
)

// verdictFor classifies how the phase labelled phasePrefix showed up in a merge
// stream. Phases report either as a result-family test point (gate, repair,
// legacy post-merge) or as an execution-family node whose node_end carries the
// verdict (the pre-merge hook, named post-merge targets), so both are searched.
// Prefixes omit the branch, which differs between the two kinds.
func verdictFor(recs []ndjsoncrap.Record, phasePrefix string) phaseVerdict {
	if tr, ok := findTest(testRecords(recs), phasePrefix); ok {
		switch {
		case tr.Directive != nil:
			return verdictSkipped
		case tr.OK:
			return verdictOk
		default:
			return verdictNotOk
		}
	}

	started := map[int]bool{}
	for _, rec := range recs {
		switch r := rec.(type) {
		case ndjsoncrap.NodeStart:
			if strings.HasPrefix(r.Name, phasePrefix) {
				started[r.TP] = true
			}
		case ndjsoncrap.NodeEnd:
			if started[r.TP] {
				if r.ExitCode != nil && *r.ExitCode == 0 {
					return verdictOk
				}
				return verdictNotOk
			}
		}
	}
	return verdictAbsent
}

// TestMergeKindPhaseParity runs the worktree merge (Resolved) and the implicit
// merge (MergeImplicit) against the same sweatfile and asserts every phase the
// two kinds share reports the same verdict. It exists because the kinds spell
// out their phase order separately (spinclass#299): a shared phase wired into
// one kind but not the other — as repair was (#297) — fails here.
//
// Both runs use their kind's normal shape: a worktree branch one commit ahead of
// main, and a main checkout with one unpushed commit (so implicit repair's
// already-pushed skip does not apply).
func TestMergeKindPhaseParity(t *testing.T) {
	cases := []struct {
		name      string
		sweatfile string
		targets   []string
		phase     string
		want      phaseVerdict
	}{
		{"disable-merge gate", "[hooks]\ndisable-merge = true\n", nil, "merge ", verdictNotOk},
		{"unknown post-merge target", "", []string{"no-such-target"}, "post-merge selection ", verdictNotOk},
		{"repair no-op", "[hooks]\nrepair = \"true\"\n", nil, "repair ", verdictOk},
		{"repair amend", "[hooks]\nrepair = \"" + repairAmendCmd + "\"\n", nil, "repair ", verdictOk},
		{"repair failure", "[hooks]\nrepair = \"exit 1\"\n", nil, "repair ", verdictNotOk},
		{"repair disabled", "[hooks]\nrepair = \"exit 1\"\ndisable-repair = true\n", nil, "repair ", verdictAbsent},
		{"pre-merge hook ok", "[hooks]\npre-merge = \"true\"\n", nil, "pre-merge hook for ", verdictOk},
		{"pre-merge hook failure", "[hooks]\npre-merge = \"exit 1\"\n", nil, "pre-merge hook for ", verdictNotOk},
		{"post-merge hook", "[hooks]\npost-merge = \"true\"\n", nil, "post-merge ", verdictOk},
		{"named post-merge target", "[[post-merge]]\nname = \"deploy\"\ncommand = \"true\"\n", nil, "post-merge deploy", verdictOk},
		{"named post-merge target failure", "[[post-merge]]\nname = \"deploy\"\ncommand = \"exit 1\"\n", nil, "post-merge deploy", verdictNotOk},
		{"post-merge target deselected", "[[post-merge]]\nname = \"deploy\"\ncommand = \"true\"\n", []string{}, "post-merge deploy", verdictAbsent},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			worktree := worktreeMergeVerdict(t, tc.sweatfile, tc.targets, tc.phase)
			implicit := implicitMergeVerdict(t, tc.sweatfile, tc.targets, tc.phase)
			if worktree != implicit {
				t.Errorf("phase %q diverges: worktree merge = %s, implicit merge = %s", tc.phase, worktree, implicit)
			}
			if worktree != tc.want {
				t.Errorf("phase %q: worktree merge = %s, want %s", tc.phase, worktree, tc.want)
			}
		})
	}
}

func worktreeMergeVerdict(t *testing.T, sweatfileBody string, targets []string, phase string) phaseVerdict {
	t.Helper()
	repoDir, wtPath, _ := setupRepairRepo(t, "feature")
	writeRepoSweatfile(t, repoDir, sweatfileBody)

	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	_, _ = ResolvedContext(context.Background(), &mockExecutor{}, rep, ts,
		repoDir, wtPath, "feature", "main", false, true, nil, PostMergeOptions{Targets: targets})
	ts.Finish()
	return verdictFor(decodeRecords(t, buf.Bytes()), phase)
}

func implicitMergeVerdict(t *testing.T, sweatfileBody string, targets []string, phase string) phaseVerdict {
	t.Helper()
	_, checkout := setupImplicitCheckout(t, sweatfileBody, true)

	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	_, _ = MergeImplicit(context.Background(), rep, ts, checkout, checkout, "master", nil, PostMergeOptions{Targets: targets})
	ts.Finish()
	return verdictFor(decodeRecords(t, buf.Bytes()), phase)
}
