package sweatfile_test

import (
	"testing"
	"time"

	. "code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

func TestParseHooksPostMerge(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte(
		"[hooks]\npost-merge = \"deploy.sh\"\ndisable-post-merge = true\n",
	))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sf := doc.Data()
	if sf.Hooks == nil || sf.Hooks.PostMerge == nil || *sf.Hooks.PostMerge != "deploy.sh" {
		t.Fatalf("hooks.post-merge: got %+v", sf.Hooks)
	}
	if sf.Hooks.DisablePostMerge == nil || !*sf.Hooks.DisablePostMerge {
		t.Fatalf("hooks.disable-post-merge: got %+v", sf.Hooks)
	}
	// The regenerated tommy decoder must consume both keys, else `sc validate`
	// would flag them as unknown.
	if u := doc.Undecoded(); len(u) != 0 {
		t.Errorf("post-merge/disable-post-merge left undecoded: %v", u)
	}
}

func TestPostMergeActive(t *testing.T) {
	cases := []struct {
		name    string
		cmd     *string
		disable *bool
		want    bool
	}{
		{"unset", nil, nil, false},
		{"empty", sptr(""), nil, false},
		{"whitespace-only", sptr("  \n\t\n"), nil, false},
		{"set", sptr("deploy.sh"), nil, true},
		{"set-but-disabled", sptr("deploy.sh"), bptr(true), false},
		{"set-disable-false", sptr("deploy.sh"), bptr(false), true},
	}
	for _, c := range cases {
		sf := Sweatfile{Hooks: &Hooks{PostMerge: c.cmd, DisablePostMerge: c.disable}}
		if got := sf.PostMergeActive(); got != c.want {
			t.Errorf("%s: PostMergeActive() = %v, want %v", c.name, got, c.want)
		}
	}
	if (Sweatfile{}).PostMergeActive() {
		t.Error("nil Hooks: PostMergeActive() = true, want false")
	}
}

func TestPostMergeDisabled(t *testing.T) {
	if (Sweatfile{}).PostMergeDisabled() {
		t.Error("nil Hooks: PostMergeDisabled() = true, want false")
	}
	if (Sweatfile{Hooks: &Hooks{}}).PostMergeDisabled() {
		t.Error("nil DisablePostMerge: PostMergeDisabled() = true, want false")
	}
	if !(Sweatfile{Hooks: &Hooks{DisablePostMerge: bptr(true)}}).PostMergeDisabled() {
		t.Error("DisablePostMerge=true: PostMergeDisabled() = false, want true")
	}
}

func TestMergeHooksPostMergeOverride(t *testing.T) {
	base := Sweatfile{Hooks: &Hooks{PostMerge: sptr("deploy-a")}}
	repo := Sweatfile{Hooks: &Hooks{PostMerge: sptr("deploy-b")}}
	merged := base.MergeWith(repo)
	if merged.Hooks.PostMerge == nil || *merged.Hooks.PostMerge != "deploy-b" {
		t.Errorf("expected scalar override to deploy-b, got %+v", merged.Hooks)
	}
}

func TestMergeHooksPostMergeInherit(t *testing.T) {
	base := Sweatfile{Hooks: &Hooks{PostMerge: sptr("deploy-a")}}
	merged := base.MergeWith(Sweatfile{})
	if merged.Hooks == nil || merged.Hooks.PostMerge == nil || *merged.Hooks.PostMerge != "deploy-a" {
		t.Errorf("expected inherited post-merge, got %+v", merged.Hooks)
	}
}

// A child sweatfile can suppress an inherited post-merge command without
// clearing the string (the disable-* opt-out shape).
func TestMergeHooksDisablePostMergeOverride(t *testing.T) {
	base := Sweatfile{Hooks: &Hooks{PostMerge: sptr("deploy-a")}}
	repo := Sweatfile{Hooks: &Hooks{DisablePostMerge: bptr(true)}}
	merged := base.MergeWith(repo)
	if merged.PostMergeActive() {
		t.Errorf("expected post-merge suppressed by disable-post-merge; hooks=%+v", merged.Hooks)
	}
	if merged.Hooks.PostMerge == nil || *merged.Hooks.PostMerge != "deploy-a" {
		t.Errorf("expected command still inherited (suppression, not clearing), got %+v", merged.Hooks)
	}
}

func TestPostMergeTimeoutValue(t *testing.T) {
	cases := []struct {
		name string
		set  *string
		want time.Duration
	}{
		// Capped by default: post-merge runs under the landing lock, so an
		// uncapped wedge would hold the whole repo's queue (#246).
		{"unset", nil, DefaultPostMergeTimeout},
		{"empty", sptr(""), DefaultPostMergeTimeout},
		{"explicit", sptr("90s"), 90 * time.Second},
		{"minutes", sptr("30m"), 30 * time.Minute},
		// "0" is the documented off switch, NOT a degenerate default.
		{"zero disables", sptr("0"), 0},
		{"zero seconds disables", sptr("0s"), 0},
		// A typo must not silently strip a protection that is on by default,
		// so bad input falls back to the default rather than to 0. This is the
		// deliberate divergence from InactivityTimeoutValue, whose default is
		// off so degrading to 0 is a no-op there.
		{"unparseable falls back to default", sptr("ten minutes"), DefaultPostMergeTimeout},
		{"negative falls back to default", sptr("-5m"), DefaultPostMergeTimeout},
	}
	for _, c := range cases {
		sf := Sweatfile{Hooks: &Hooks{PostMergeTimeout: c.set}}
		if got := sf.PostMergeTimeoutValue(); got != c.want {
			t.Errorf("%s: PostMergeTimeoutValue() = %v, want %v", c.name, got, c.want)
		}
	}
	if got := (Sweatfile{}).PostMergeTimeoutValue(); got != DefaultPostMergeTimeout {
		t.Errorf("nil Hooks: PostMergeTimeoutValue() = %v, want %v", got, DefaultPostMergeTimeout)
	}
}

func TestMergeHooksPostMergeTimeoutOverride(t *testing.T) {
	base := Sweatfile{Hooks: &Hooks{PostMergeTimeout: sptr("5m")}}
	if merged := base.MergeWith(Sweatfile{}); merged.PostMergeTimeoutValue() != 5*time.Minute {
		t.Errorf("expected inherited post-merge-timeout, got %v", merged.PostMergeTimeoutValue())
	}
	repo := Sweatfile{Hooks: &Hooks{PostMergeTimeout: sptr("0")}}
	if merged := base.MergeWith(repo); merged.PostMergeTimeoutValue() != 0 {
		t.Errorf("expected child override to disable the cap, got %v", merged.PostMergeTimeoutValue())
	}
}
