package sweatfile

import "testing"

func TestMCPServerDefFields(t *testing.T) {
	sf := Sweatfile{
		AllowedMCPs: []string{"external-server"},
		MCPs: []MCPServerDef{
			{
				Name:    "my-linter",
				Command: "my-linter",
				Args:    []string{"serve"},
				Env:     map[string]string{"DEBUG": "1"},
			},
		},
	}

	if len(sf.AllowedMCPs) != 1 || sf.AllowedMCPs[0] != "external-server" {
		t.Errorf("AllowedMCPs: got %v", sf.AllowedMCPs)
	}
	if len(sf.MCPs) != 1 || sf.MCPs[0].Name != "my-linter" {
		t.Errorf("MCPs: got %v", sf.MCPs)
	}
	if sf.MCPs[0].Env["DEBUG"] != "1" {
		t.Errorf("MCPs[0].Env: got %v", sf.MCPs[0].Env)
	}
}

func TestEffectiveAllowedMCPs(t *testing.T) {
	sf := Sweatfile{
		AllowedMCPs: []string{"external-server"},
		MCPs: []MCPServerDef{
			{Name: "my-linter", Command: "my-linter"},
			{Name: "removal-sentinel"}, // empty command = removal
		},
	}

	got := sf.EffectiveAllowedMCPs()
	// Should include: external-server, my-linter
	// Should NOT include: removal-sentinel (empty command)
	want := map[string]bool{"external-server": true, "my-linter": true}
	if len(got) != len(want) {
		t.Fatalf("EffectiveAllowedMCPs: got %v, want keys %v", got, want)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("unexpected name %q in EffectiveAllowedMCPs", name)
		}
	}
}

func TestActiveMCPs(t *testing.T) {
	sf := Sweatfile{
		MCPs: []MCPServerDef{
			{Name: "active", Command: "active-cmd"},
			{Name: "sentinel"}, // empty command
		},
	}

	active := sf.ActiveMCPs()
	if len(active) != 1 || active[0].Name != "active" {
		t.Errorf("ActiveMCPs: got %v", active)
	}
}

func TestMergeAllowedMCPsInherit(t *testing.T) {
	parent := Sweatfile{AllowedMCPs: []string{"server-a"}}
	child := Sweatfile{} // nil AllowedMCPs = inherit
	merged := parent.MergeWith(child)

	if len(merged.AllowedMCPs) != 1 || merged.AllowedMCPs[0] != "server-a" {
		t.Errorf("expected inherit, got %v", merged.AllowedMCPs)
	}
}

func TestMergeAllowedMCPsClear(t *testing.T) {
	parent := Sweatfile{AllowedMCPs: []string{"server-a"}}
	child := Sweatfile{AllowedMCPs: []string{}} // empty = clear
	merged := parent.MergeWith(child)

	if merged.AllowedMCPs == nil || len(merged.AllowedMCPs) != 0 {
		t.Errorf("expected clear, got %v", merged.AllowedMCPs)
	}
}

func TestMergeAllowedMCPsAppend(t *testing.T) {
	parent := Sweatfile{AllowedMCPs: []string{"server-a"}}
	child := Sweatfile{AllowedMCPs: []string{"server-b"}}
	merged := parent.MergeWith(child)

	if len(merged.AllowedMCPs) != 2 {
		t.Fatalf("expected 2, got %v", merged.AllowedMCPs)
	}
	if merged.AllowedMCPs[0] != "server-a" || merged.AllowedMCPs[1] != "server-b" {
		t.Errorf("got %v", merged.AllowedMCPs)
	}
}

func TestMergeMCPsDedupByName(t *testing.T) {
	parent := Sweatfile{MCPs: []MCPServerDef{
		{Name: "linter", Command: "lint-v1", Args: []string{"serve"}},
		{Name: "formatter", Command: "fmt", Args: []string{"serve"}},
	}}
	child := Sweatfile{MCPs: []MCPServerDef{
		{Name: "linter", Command: "lint-v2", Args: []string{"serve", "--new"}},
	}}
	merged := parent.MergeWith(child)

	if len(merged.MCPs) != 2 {
		t.Fatalf("expected 2, got %d: %v", len(merged.MCPs), merged.MCPs)
	}
	// linter should be replaced in-place (position 0)
	if merged.MCPs[0].Name != "linter" || merged.MCPs[0].Command != "lint-v2" {
		t.Errorf("expected linter replaced, got %+v", merged.MCPs[0])
	}
	// formatter preserved
	if merged.MCPs[1].Name != "formatter" {
		t.Errorf("expected formatter, got %+v", merged.MCPs[1])
	}
}

func TestMergeMCPsAppendNew(t *testing.T) {
	parent := Sweatfile{MCPs: []MCPServerDef{
		{Name: "linter", Command: "lint"},
	}}
	child := Sweatfile{MCPs: []MCPServerDef{
		{Name: "formatter", Command: "fmt"},
	}}
	merged := parent.MergeWith(child)

	if len(merged.MCPs) != 2 {
		t.Fatalf("expected 2, got %d", len(merged.MCPs))
	}
}

func TestMergeMCPsRemoveSentinel(t *testing.T) {
	parent := Sweatfile{MCPs: []MCPServerDef{
		{Name: "linter", Command: "lint"},
		{Name: "formatter", Command: "fmt"},
	}}
	child := Sweatfile{MCPs: []MCPServerDef{
		{Name: "linter"}, // empty command = remove
	}}
	merged := parent.MergeWith(child)

	// After merge, linter should be gone (sentinel pruned), only formatter remains
	active := merged.ActiveMCPs()
	if len(active) != 1 || active[0].Name != "formatter" {
		t.Errorf("expected only formatter, got %v", active)
	}
}

func TestMergeMCPsInheritWhenNil(t *testing.T) {
	parent := Sweatfile{MCPs: []MCPServerDef{
		{Name: "linter", Command: "lint"},
	}}
	child := Sweatfile{} // nil MCPs = inherit
	merged := parent.MergeWith(child)

	if len(merged.MCPs) != 1 || merged.MCPs[0].Name != "linter" {
		t.Errorf("expected inherit, got %v", merged.MCPs)
	}
}

func TestMergeMCPsFullReplace(t *testing.T) {
	parent := Sweatfile{MCPs: []MCPServerDef{
		{Name: "linter", Command: "lint", Env: map[string]string{"DEBUG": "1"}},
	}}
	child := Sweatfile{MCPs: []MCPServerDef{
		{Name: "linter", Command: "lint-v2", Args: []string{"--new"}},
	}}
	merged := parent.MergeWith(child)

	if merged.MCPs[0].Env != nil {
		t.Errorf("expected env to be nil after full replace, got %v", merged.MCPs[0].Env)
	}
	if merged.MCPs[0].Command != "lint-v2" {
		t.Errorf("expected lint-v2, got %s", merged.MCPs[0].Command)
	}
}
