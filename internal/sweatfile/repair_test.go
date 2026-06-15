package sweatfile_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	. "github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/sweatfileio"
)

func bptr(b bool) *bool { return &b }

func TestParseHooksRepair(t *testing.T) {
	doc, err := sweatfileio.Parse([]byte(
		"[hooks]\nrepair = \"conformist --commit --amend --exit-zero-on-fix\"\ndisable-repair = true\n",
	))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sf := doc.Data()
	if sf.Hooks == nil || sf.Hooks.Repair == nil ||
		*sf.Hooks.Repair != "conformist --commit --amend --exit-zero-on-fix" {
		t.Fatalf("hooks.repair: got %+v", sf.Hooks)
	}
	if sf.Hooks.DisableRepair == nil || !*sf.Hooks.DisableRepair {
		t.Fatalf("hooks.disable-repair: got %+v", sf.Hooks)
	}
	// The regenerated tommy decoder must consume both keys, else `sc validate`
	// would flag them as unknown.
	if u := doc.Undecoded(); len(u) != 0 {
		t.Errorf("repair/disable-repair left undecoded: %v", u)
	}
}

func TestRepairActive(t *testing.T) {
	cases := []struct {
		name    string
		repair  *string
		disable *bool
		want    bool
	}{
		{"nil-hooks-field", nil, nil, false},
		{"empty", sptr(""), nil, false},
		{"whitespace-only", sptr("   \n\t"), nil, false},
		{"set", sptr("conformist --commit --amend"), nil, true},
		{"set-but-disabled", sptr("conformist --commit --amend"), bptr(true), false},
		{"set-disable-false", sptr("conformist --commit --amend"), bptr(false), true},
	}
	for _, c := range cases {
		sf := Sweatfile{Hooks: &Hooks{Repair: c.repair, DisableRepair: c.disable}}
		if got := sf.RepairActive(); got != c.want {
			t.Errorf("%s: RepairActive() = %v, want %v", c.name, got, c.want)
		}
	}
	// No Hooks table at all → inactive.
	if (Sweatfile{}).RepairActive() {
		t.Error("nil Hooks: RepairActive() = true, want false")
	}
}

func TestRepairDisabled(t *testing.T) {
	if (Sweatfile{}).RepairDisabled() {
		t.Error("nil Hooks: RepairDisabled() = true, want false")
	}
	if (Sweatfile{Hooks: &Hooks{}}).RepairDisabled() {
		t.Error("nil DisableRepair: RepairDisabled() = true, want false")
	}
	if !(Sweatfile{Hooks: &Hooks{DisableRepair: bptr(true)}}).RepairDisabled() {
		t.Error("DisableRepair=true: RepairDisabled() = false, want true")
	}
}

func TestMergeHooksRepairOverride(t *testing.T) {
	base := Sweatfile{Hooks: &Hooks{Repair: sptr("fmt-a")}}
	repo := Sweatfile{Hooks: &Hooks{Repair: sptr("fmt-b")}}
	merged := base.MergeWith(repo)
	if merged.Hooks == nil || merged.Hooks.Repair == nil || *merged.Hooks.Repair != "fmt-b" {
		t.Errorf("expected override fmt-b, got %+v", merged.Hooks)
	}
}

func TestMergeHooksRepairInherit(t *testing.T) {
	base := Sweatfile{Hooks: &Hooks{Repair: sptr("fmt-a")}}
	merged := base.MergeWith(Sweatfile{})
	if merged.Hooks == nil || merged.Hooks.Repair == nil || *merged.Hooks.Repair != "fmt-a" {
		t.Errorf("expected inherited fmt-a, got %+v", merged.Hooks)
	}
}

func TestMergeHooksDisableRepairOverride(t *testing.T) {
	// A child sweatfile can suppress an inherited repair command without
	// clearing the string.
	base := Sweatfile{Hooks: &Hooks{Repair: sptr("fmt-a")}}
	repo := Sweatfile{Hooks: &Hooks{DisableRepair: bptr(true)}}
	merged := base.MergeWith(repo)
	if merged.RepairActive() {
		t.Errorf("expected repair suppressed by inherited disable-repair, got active; hooks=%+v", merged.Hooks)
	}
	// The command string is still inherited (suppression, not clearing).
	if merged.Hooks.Repair == nil || *merged.Hooks.Repair != "fmt-a" {
		t.Errorf("expected repair command still inherited, got %+v", merged.Hooks)
	}
}

func TestRunRepairHookContextRuns(t *testing.T) {
	wt := t.TempDir()
	sf := Sweatfile{Hooks: &Hooks{Repair: sptr("echo repaired")}}
	var buf bytes.Buffer
	if err := sf.RunRepairHookContext(context.Background(), wt, &buf); err != nil {
		t.Fatalf("expected success, got %v\noutput: %s", err, buf.String())
	}
	if got := strings.TrimSpace(buf.String()); got != "repaired" {
		t.Errorf("expected hook output 'repaired', got %q", got)
	}
}

func TestRunRepairHookContextInactiveIsNoop(t *testing.T) {
	wt := t.TempDir()
	// No repair command → the runner is a no-op even though the writer is wired.
	sf := Sweatfile{Hooks: &Hooks{}}
	var buf bytes.Buffer
	if err := sf.RunRepairHookContext(context.Background(), wt, &buf); err != nil {
		t.Fatalf("inactive repair should be a no-op, got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("inactive repair wrote output: %q", buf.String())
	}
	// Disabled-but-set is also a no-op.
	sf = Sweatfile{Hooks: &Hooks{Repair: sptr("echo nope"), DisableRepair: bptr(true)}}
	buf.Reset()
	if err := sf.RunRepairHookContext(context.Background(), wt, &buf); err != nil {
		t.Fatalf("disabled repair should be a no-op, got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("disabled repair ran: %q", buf.String())
	}
}

func TestRunRepairHookContextPropagatesFailure(t *testing.T) {
	wt := t.TempDir()
	sf := Sweatfile{Hooks: &Hooks{Repair: sptr("echo boom >&2; exit 1")}}
	var buf bytes.Buffer
	if err := sf.RunRepairHookContext(context.Background(), wt, &buf); err == nil {
		t.Fatalf("expected nonzero repair to error, got nil (output: %q)", buf.String())
	}
}
