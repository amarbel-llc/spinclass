package sweatfile

import "testing"

func TestExecAutoAllowNilCommand(t *testing.T) {
	mcp := MCPServerDef{Name: "test", Command: "test"}
	tools, err := ExecAutoAllow(mcp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tools != nil {
		t.Errorf("expected nil, got %v", tools)
	}
}

func TestExecAutoAllowValidJSON(t *testing.T) {
	mcp := MCPServerDef{
		Name:      "moxy",
		Command:   "moxy",
		AutoAllow: []string{"echo", `{"tools": ["lsp_execute-command", "grit_commit"]}`},
	}

	tools, err := ExecAutoAllow(mcp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"mcp__moxy__lsp_execute-command", "mcp__moxy__grit_commit"}
	if len(tools) != len(want) {
		t.Fatalf("expected %d tools, got %d: %v", len(want), len(tools), tools)
	}
	for i, tool := range tools {
		if tool != want[i] {
			t.Errorf("tools[%d]: got %q, want %q", i, tool, want[i])
		}
	}
}

func TestExecAutoAllowEmptyTools(t *testing.T) {
	mcp := MCPServerDef{
		Name:      "moxy",
		Command:   "moxy",
		AutoAllow: []string{"echo", `{"tools": []}`},
	}

	tools, err := ExecAutoAllow(mcp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("expected empty, got %v", tools)
	}
}

func TestExecAutoAllowCommandFails(t *testing.T) {
	mcp := MCPServerDef{
		Name:      "bad",
		Command:   "bad",
		AutoAllow: []string{"false"},
	}

	_, err := ExecAutoAllow(mcp)
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
}

func TestExecAutoAllowMalformedJSON(t *testing.T) {
	mcp := MCPServerDef{
		Name:      "bad",
		Command:   "bad",
		AutoAllow: []string{"echo", "not json"},
	}

	_, err := ExecAutoAllow(mcp)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestExecAutoAllowPrefixedToolName(t *testing.T) {
	mcp := MCPServerDef{
		Name:      "moxy",
		Command:   "moxy",
		AutoAllow: []string{"echo", `{"tools": ["mcp__moxy__bad"]}`},
	}

	_, err := ExecAutoAllow(mcp)
	if err == nil {
		t.Fatal("expected error for mcp__ prefixed tool name")
	}
}

func TestExecAutoAllowServersFieldRejected(t *testing.T) {
	mcp := MCPServerDef{
		Name:      "moxy",
		Command:   "moxy",
		AutoAllow: []string{"echo", `{"tools": ["foo"], "servers": ["other"]}`},
	}

	_, err := ExecAutoAllow(mcp)
	if err == nil {
		t.Fatal("expected error for servers field")
	}
}

func TestParseMCPsAutoAllowFromTOML(t *testing.T) {
	input := []byte(`
[[mcps]]
name = "moxy"
command = "moxy"
args = ["serve"]
auto-allow = ["moxy", "auto-allow"]
`)
	doc, err := Parse(input)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	sf := *doc.Data()

	if len(sf.MCPs) != 1 {
		t.Fatalf("expected 1 MCP, got %d", len(sf.MCPs))
	}
	if len(sf.MCPs[0].AutoAllow) != 2 {
		t.Fatalf("expected 2 auto-allow args, got %v", sf.MCPs[0].AutoAllow)
	}
	if sf.MCPs[0].AutoAllow[0] != "moxy" || sf.MCPs[0].AutoAllow[1] != "auto-allow" {
		t.Errorf("auto-allow: got %v", sf.MCPs[0].AutoAllow)
	}
}

func TestParseMCPsAutoAllowNoUndecodedKeys(t *testing.T) {
	input := []byte(`
[[mcps]]
name = "moxy"
command = "moxy"
auto-allow = ["moxy", "auto-allow"]
`)
	doc, err := Parse(input)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	undecoded := doc.Undecoded()
	if len(undecoded) != 0 {
		t.Errorf("expected no undecoded keys, got %v", undecoded)
	}
}
