package sweatfile_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestRunPostMergeHookContextRuns(t *testing.T) {
	dir := t.TempDir()
	sf := Sweatfile{Hooks: &Hooks{PostMerge: sptr("echo deployed")}}
	var buf bytes.Buffer
	if err := sf.RunPostMergeHookContext(context.Background(), dir, nil, &buf); err != nil {
		t.Fatalf("expected success, got %v\noutput: %s", err, buf.String())
	}
	if got := strings.TrimSpace(buf.String()); got != "deployed" {
		t.Errorf("expected hook output 'deployed', got %q", got)
	}
}

func TestRunPostMergeHookContextInactiveIsNoop(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := (Sweatfile{Hooks: &Hooks{}}).RunPostMergeHookContext(context.Background(), dir, nil, &buf); err != nil {
		t.Fatalf("inactive post-merge should be a no-op, got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("inactive post-merge wrote output: %q", buf.String())
	}

	sf := Sweatfile{Hooks: &Hooks{PostMerge: sptr("echo nope"), DisablePostMerge: bptr(true)}}
	buf.Reset()
	if err := sf.RunPostMergeHookContext(context.Background(), dir, nil, &buf); err != nil {
		t.Fatalf("disabled post-merge should be a no-op, got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("disabled post-merge ran: %q", buf.String())
	}
}

func TestRunPostMergeHookContextPropagatesFailure(t *testing.T) {
	dir := t.TempDir()
	sf := Sweatfile{Hooks: &Hooks{PostMerge: sptr("echo boom >&2; exit 1")}}
	var buf bytes.Buffer
	if err := sf.RunPostMergeHookContext(context.Background(), dir, nil, &buf); err == nil {
		t.Fatalf("expected nonzero post-merge to error, got nil (output: %q)", buf.String())
	}
	// The runner reports the failure; deciding it is non-fatal is the merge
	// package's job (runPostMergePhase), not the runner's.
	if !strings.Contains(buf.String(), "boom") {
		t.Errorf("expected hook stderr captured, got %q", buf.String())
	}
}

// extraEnv must reach the hook process, and must win over an inherited value
// of the same key (it is appended last).
func TestRunPostMergeHookContextExtraEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SPINCLASS_MERGED_SHA", "inherited-and-should-lose")

	sf := Sweatfile{Hooks: &Hooks{PostMerge: sptr("echo sha=$SPINCLASS_MERGED_SHA pushed=$SPINCLASS_MERGE_PUSHED")}}
	var buf bytes.Buffer
	err := sf.RunPostMergeHookContext(context.Background(), dir, []string{
		"SPINCLASS_MERGED_SHA=abc123",
		"SPINCLASS_MERGE_PUSHED=1",
	}, &buf)
	if err != nil {
		t.Fatalf("expected success, got %v\noutput: %s", err, buf.String())
	}
	if got := strings.TrimSpace(buf.String()); got != "sha=abc123 pushed=1" {
		t.Errorf("extraEnv not applied: got %q", got)
	}
}

// The post-merge hook runs in the devshell of its directory when a .envrc is
// present, matching every other lifecycle hook (spinclass#198).
func TestRunPostMergeHookContextWrapsWithDirenvWhenEnvrcPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".envrc"), []byte("use flake\n"), 0o644); err != nil {
		t.Fatalf("writing .envrc: %v", err)
	}
	pinDirenv(t, "#!/bin/sh\necho DIRENV_WRAPPED\nshift 2\nexec \"$@\"\n")

	sf := Sweatfile{Hooks: &Hooks{PostMerge: sptr("echo deployed")}}
	var buf bytes.Buffer
	if err := sf.RunPostMergeHookContext(context.Background(), dir, nil, &buf); err != nil {
		t.Fatalf("expected success, got %v\noutput: %s", err, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "DIRENV_WRAPPED") {
		t.Errorf("expected hook to run via direnv exec, got %q", out)
	}
	if !strings.Contains(out, "deployed") {
		t.Errorf("expected wrapped script output 'deployed', got %q", out)
	}
}
