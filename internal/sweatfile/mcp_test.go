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
