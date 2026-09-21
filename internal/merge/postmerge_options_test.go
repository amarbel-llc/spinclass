package merge

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

func dptr(d time.Duration) *time.Duration { return &d }

// Precedence of the effective post-merge cap: a per-merge override beats the
// sweatfile's [hooks].post-merge-timeout, which beats the 10m default. A zero
// override disables the cap even when the sweatfile sets one.
func TestPostMergeOptionsEffectiveTimeoutPrecedence(t *testing.T) {
	sfWith := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PostMergeTimeout: sptr("20m")}}
	sfWithout := sweatfile.Sweatfile{}

	cases := []struct {
		name string
		opts PostMergeOptions
		sf   sweatfile.Sweatfile
		want time.Duration
	}{
		{"default when neither set", PostMergeOptions{}, sfWithout, sweatfile.DefaultPostMergeTimeout},
		{"sweatfile beats default", PostMergeOptions{}, sfWith, 20 * time.Minute},
		{"override raises past sweatfile", PostMergeOptions{Timeout: dptr(45 * time.Minute)}, sfWith, 45 * time.Minute},
		{"override lowers below sweatfile", PostMergeOptions{Timeout: dptr(30 * time.Second)}, sfWith, 30 * time.Second},
		{"override beats default", PostMergeOptions{Timeout: dptr(time.Hour)}, sfWithout, time.Hour},
		{"zero override disables despite sweatfile", PostMergeOptions{Timeout: dptr(0)}, sfWith, 0},
	}
	for _, c := range cases {
		if got := c.opts.EffectiveTimeout(c.sf); got != c.want {
			t.Errorf("%s: EffectiveTimeout = %v, want %v", c.name, got, c.want)
		}
	}
}

func sptr(s string) *string { return &s }

// The env builder is the one contract both the sweatfile phase and `sc run`'s
// dynamic hooks export: pin every key and the "0"-when-uncapped rule.
func TestPostMergeEnvExportsTimingAndPin(t *testing.T) {
	deadline := time.Unix(1_800_000_000, 0)
	env := PostMergeEnv(PostMergeFacts{
		LandedSha:     "landed",
		PinnedSha:     "pinned",
		Branch:        "feature",
		DefaultBranch: "main",
		RepoPath:      "/repo",
		Pushed:        true,
		Timeout:       25 * time.Minute,
		Deadline:      deadline,
	})
	want := []string{
		"SPINCLASS_MERGED_SHA=landed",
		"SPINCLASS_MERGED_BRANCH=feature",
		"SPINCLASS_DEFAULT_BRANCH=main",
		"SPINCLASS_MERGE_PUSHED=1",
		"SPINCLASS_REPO_PATH=/repo",
		"SPINCLASS_POST_MERGE_TIMEOUT=25m0s",
		"SPINCLASS_POST_MERGE_TIMEOUT_SECONDS=1500",
		"SPINCLASS_POST_MERGE_DEADLINE=" + strconv.FormatInt(deadline.Unix(), 10),
		"SPINCLASS_PINNED_SHA=pinned",
	}
	if got := strings.Join(env, "\n"); got != strings.Join(want, "\n") {
		t.Errorf("PostMergeEnv mismatch:\n got: %v\nwant: %v", env, want)
	}

	// The exported duration must round-trip through the same parser a caller
	// would use to hand it back as post_merge_timeout.
	if d, err := sweatfile.ParsePostMergeTimeout("25m0s"); err != nil || d != 25*time.Minute {
		t.Errorf("exported duration does not round-trip: %v %v", d, err)
	}
}

func TestPostMergeEnvUncappedAndNoPin(t *testing.T) {
	env := PostMergeEnv(PostMergeFacts{LandedSha: "x", Branch: "b", DefaultBranch: "m", RepoPath: "/r"})
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"SPINCLASS_MERGE_PUSHED=0",
		"SPINCLASS_POST_MERGE_TIMEOUT=0",
		"SPINCLASS_POST_MERGE_TIMEOUT_SECONDS=0",
		"SPINCLASS_POST_MERGE_DEADLINE=0",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, env)
		}
	}
	if strings.Contains(joined, "SPINCLASS_PINNED_SHA") {
		t.Errorf("no pin ⇒ SPINCLASS_PINNED_SHA must be omitted, got %v", env)
	}
}

func TestFormatPostMergeTimeout(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "0", -time.Second: "0", 90 * time.Second: "1m30s", 10 * time.Minute: "10m0s"} {
		if got := FormatPostMergeTimeout(d); got != want {
			t.Errorf("FormatPostMergeTimeout(%v) = %q, want %q", d, got, want)
		}
	}
}
