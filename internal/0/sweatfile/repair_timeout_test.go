package sweatfile_test

import (
	"strings"
	"testing"
	"time"

	. "code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

func TestParseHooksRepairTimeout(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte("[hooks]\nrepair-timeout = \"20m\"\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := doc.Data().RepairTimeoutValue(); got != 20*time.Minute {
		t.Errorf("RepairTimeoutValue() = %v, want 20m", got)
	}
	// The regenerated tommy decoder must consume the key, else `sc validate`
	// would flag it as unknown.
	if u := doc.Undecoded(); len(u) != 0 {
		t.Errorf("repair-timeout left undecoded: %v", u)
	}
}

func TestRepairTimeoutValue(t *testing.T) {
	cases := []struct {
		name string
		set  *string
		want time.Duration
	}{
		// Capped by default: repair runs in the merge's synchronous prefix with
		// the attestation claimed, so an uncapped wedge hangs the call (#401).
		{"unset", nil, DefaultRepairTimeout},
		{"empty", sptr(""), DefaultRepairTimeout},
		{"explicit", sptr("90s"), 90 * time.Second},
		{"zero disables", sptr("0"), 0},
		// As with post-merge-timeout, a typo must not strip a default-on cap.
		{"unparseable falls back to default", sptr("ten minutes"), DefaultRepairTimeout},
		{"negative falls back to default", sptr("-5m"), DefaultRepairTimeout},
	}
	for _, c := range cases {
		sf := Sweatfile{Hooks: &Hooks{RepairTimeout: c.set}}
		if got := sf.RepairTimeoutValue(); got != c.want {
			t.Errorf("%s: RepairTimeoutValue() = %v, want %v", c.name, got, c.want)
		}
	}
	if got := (Sweatfile{}).RepairTimeoutValue(); got != DefaultRepairTimeout {
		t.Errorf("nil Hooks: RepairTimeoutValue() = %v, want %v", got, DefaultRepairTimeout)
	}
	if DefaultRepairTimeout != 10*time.Minute {
		t.Errorf("DefaultRepairTimeout = %v, want 10m", DefaultRepairTimeout)
	}
}

func TestMergeHooksRepairTimeoutOverride(t *testing.T) {
	base := Sweatfile{Hooks: &Hooks{RepairTimeout: sptr("5m")}}
	if merged := base.MergeWith(Sweatfile{}); merged.RepairTimeoutValue() != 5*time.Minute {
		t.Errorf("expected inherited repair-timeout, got %v", merged.RepairTimeoutValue())
	}
	repo := Sweatfile{Hooks: &Hooks{RepairTimeout: sptr("0")}}
	if merged := base.MergeWith(repo); merged.RepairTimeoutValue() != 0 {
		t.Errorf("expected child override to disable the cap, got %v", merged.RepairTimeoutValue())
	}
}

func TestParseRepairTimeoutNamesTheKnob(t *testing.T) {
	for _, bad := range []string{"ten minutes", "-5m"} {
		if _, err := ParseRepairTimeout(bad); err == nil || !strings.Contains(err.Error(), "repair-timeout") {
			t.Errorf("ParseRepairTimeout(%q) err = %v, want one naming repair-timeout", bad, err)
		}
	}
	if d, err := ParseRepairTimeout("0"); err != nil || d != 0 {
		t.Errorf(`ParseRepairTimeout("0") = %v, %v; want 0, nil`, d, err)
	}
}
