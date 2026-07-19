package sweatfile_test

import (
	"testing"

	. "code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

func TestParseHooksPreCommit(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte(
		"[hooks]\npre-commit = \"conformist --staged --exit-zero-on-fix\"\ndisable-pre-commit = true\n",
	))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sf := doc.Data()
	if sf.Hooks == nil || sf.Hooks.PreCommit == nil ||
		*sf.Hooks.PreCommit != "conformist --staged --exit-zero-on-fix" {
		t.Fatalf("hooks.pre-commit: got %+v", sf.Hooks)
	}
	if sf.Hooks.DisablePreCommit == nil || !*sf.Hooks.DisablePreCommit {
		t.Fatalf("hooks.disable-pre-commit: got %+v", sf.Hooks)
	}
}

func TestPreCommitActive(t *testing.T) {
	cases := []struct {
		name    string
		cmd     *string
		disable *bool
		want    bool
	}{
		{"set", sptr("conformist --staged"), nil, true},
		{"unset", nil, nil, false},
		{"empty", sptr(""), nil, false},
		{"whitespace", sptr("  \n\t "), nil, false},
		{"disabled", sptr("conformist --staged"), bptr(true), false},
		{"disable-false", sptr("conformist --staged"), bptr(false), true},
	}
	for _, c := range cases {
		sf := Sweatfile{Hooks: &Hooks{PreCommit: c.cmd, DisablePreCommit: c.disable}}
		if got := sf.PreCommitActive(); got != c.want {
			t.Errorf("%s: PreCommitActive() = %v, want %v", c.name, got, c.want)
		}
	}
	if (Sweatfile{}).PreCommitActive() {
		t.Error("nil Hooks: PreCommitActive() = true, want false")
	}
}

func TestPreCommitDisabled(t *testing.T) {
	if (Sweatfile{}).PreCommitDisabled() {
		t.Error("nil Hooks: PreCommitDisabled() = true, want false")
	}
	if (Sweatfile{Hooks: &Hooks{}}).PreCommitDisabled() {
		t.Error("nil DisablePreCommit: PreCommitDisabled() = true, want false")
	}
	if !(Sweatfile{Hooks: &Hooks{DisablePreCommit: bptr(true)}}).PreCommitDisabled() {
		t.Error("DisablePreCommit=true: PreCommitDisabled() = false, want true")
	}
}

func TestMergeHooksPreCommitOverride(t *testing.T) {
	base := Sweatfile{Hooks: &Hooks{PreCommit: sptr("fmt-a")}}
	repo := Sweatfile{Hooks: &Hooks{PreCommit: sptr("fmt-b")}}
	merged := base.MergeWith(repo)
	if merged.Hooks == nil || merged.Hooks.PreCommit == nil || *merged.Hooks.PreCommit != "fmt-b" {
		t.Errorf("expected override fmt-b, got %+v", merged.Hooks)
	}
}

func TestMergeHooksPreCommitInherit(t *testing.T) {
	base := Sweatfile{Hooks: &Hooks{PreCommit: sptr("fmt-a")}}
	merged := base.MergeWith(Sweatfile{})
	if merged.Hooks == nil || merged.Hooks.PreCommit == nil || *merged.Hooks.PreCommit != "fmt-a" {
		t.Errorf("expected inherited fmt-a, got %+v", merged.Hooks)
	}
}

func TestMergeHooksDisablePreCommitOverride(t *testing.T) {
	// A child sweatfile can suppress an inherited pre-commit command without
	// clearing the string.
	base := Sweatfile{Hooks: &Hooks{PreCommit: sptr("fmt-a")}}
	repo := Sweatfile{Hooks: &Hooks{DisablePreCommit: bptr(true)}}
	merged := base.MergeWith(repo)
	if merged.PreCommitActive() {
		t.Errorf("expected pre-commit suppressed by inherited disable, got active; hooks=%+v", merged.Hooks)
	}
	if merged.Hooks.PreCommit == nil || *merged.Hooks.PreCommit != "fmt-a" {
		t.Errorf("expected pre-commit command still inherited, got %+v", merged.Hooks)
	}
}
