package sweatfile_test

import (
	"testing"

	. "code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

func TestRemotesParse(t *testing.T) {
	input := `
[[remotes]]
name = "devbox"
ssh = "sasha@devbox.lan"
attach = ["ssh", "-t", "{ssh}", "spinclass", "resume", "{id}"]
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if len(sf.Remotes) != 1 {
		t.Fatalf("remotes: got %d, want 1", len(sf.Remotes))
	}
	r := sf.Remotes[0]
	if r.Name != "devbox" || r.SSH != "sasha@devbox.lan" || len(r.Attach) != 6 {
		t.Fatalf("remote: %+v", r)
	}
}

func TestRemotesParseRemove(t *testing.T) {
	input := `
[[remotes]]
name = "devbox"
remove = true
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sf := doc.Data()
	if len(sf.Remotes) != 1 || !sf.Remotes[0].Remove {
		t.Fatalf("remove not parsed: %+v", sf.Remotes)
	}
}

func TestRemoteDestFallsBackToName(t *testing.T) {
	r := Remote{Name: "devbox"}
	if got := r.Dest(); got != "devbox" {
		t.Errorf("Dest() = %q, want devbox", got)
	}
	r.SSH = "sasha@devbox.lan"
	if got := r.Dest(); got != "sasha@devbox.lan" {
		t.Errorf("Dest() = %q, want sasha@devbox.lan", got)
	}
}

func TestRemotesMergeDedupByName(t *testing.T) {
	parent := Sweatfile{Remotes: []Remote{
		{Name: "devbox", SSH: "sasha@devbox.lan"},
	}}
	child := Sweatfile{Remotes: []Remote{
		{Name: "devbox", SSH: "sasha@devbox.tail"},
		{Name: "lab", SSH: "sasha@lab.lan"},
	}}
	merged := parent.MergeWith(child)
	if len(merged.Remotes) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(merged.Remotes))
	}
	if merged.Remotes[0].Name != "devbox" || merged.Remotes[0].SSH != "sasha@devbox.tail" {
		t.Errorf("devbox should be overridden in place, got %+v", merged.Remotes[0])
	}
	if merged.Remotes[1].Name != "lab" {
		t.Errorf("lab missing: %v", merged.Remotes)
	}
}

func TestRemotesMergeDoesNotMutateBase(t *testing.T) {
	base := Sweatfile{Remotes: []Remote{
		{Name: "devbox", SSH: "sasha@devbox.lan"},
	}}
	other := Sweatfile{Remotes: []Remote{
		{Name: "devbox", SSH: "sasha@devbox.tail"},
	}}
	_ = base.MergeWith(other)
	if base.Remotes[0].SSH != "sasha@devbox.lan" {
		t.Errorf("base.Remotes[0] mutated: SSH = %q", base.Remotes[0].SSH)
	}
}

func TestActiveRemotesFiltersSentinels(t *testing.T) {
	parent := Sweatfile{Remotes: []Remote{
		{Name: "devbox", SSH: "sasha@devbox.lan"},
		{Name: "lab", SSH: "sasha@lab.lan"},
	}}
	child := Sweatfile{Remotes: []Remote{
		{Name: "devbox", Remove: true}, // explicit removal sentinel
	}}
	merged := parent.MergeWith(child)

	// The sentinel is preserved in the slice (overrides in-place);
	// ActiveRemotes filters it out so only lab remains configured.
	active := merged.ActiveRemotes()
	if len(active) != 1 || active[0].Name != "lab" {
		t.Errorf("ActiveRemotes: got %v", active)
	}
}

func TestRemotesNameOnlyIsActive(t *testing.T) {
	// A name-only entry is an all-defaults remote (~/.ssh/config does the
	// work) — it must NOT be treated as a removal sentinel.
	sf := Sweatfile{Remotes: []Remote{
		{Name: "devbox"},
	}}
	active := sf.ActiveRemotes()
	if len(active) != 1 || active[0].Name != "devbox" {
		t.Errorf("ActiveRemotes: got %v, want name-only devbox active", active)
	}
}

func TestRemotesMergeRemoveOverridesInherited(t *testing.T) {
	parent := Sweatfile{Remotes: []Remote{
		{Name: "devbox", SSH: "sasha@devbox.lan"},
	}}
	child := Sweatfile{Remotes: []Remote{
		{Name: "devbox", Remove: true},
	}}
	merged := parent.MergeWith(child)
	if len(merged.Remotes) != 1 || !merged.Remotes[0].Remove {
		t.Fatalf("expected the remove sentinel to override in place, got %+v", merged.Remotes)
	}
	if active := merged.ActiveRemotes(); active != nil {
		t.Errorf("ActiveRemotes: got %v, want nil after removal", active)
	}
}

func TestActiveRemotesEmpty(t *testing.T) {
	sf := Sweatfile{}
	if active := sf.ActiveRemotes(); active != nil {
		t.Errorf("expected nil for empty Remotes, got %v", active)
	}
}
