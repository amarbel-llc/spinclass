package claude

import (
	"os"
	"path/filepath"
	"testing"
)

// With no extra servers and no pre-existing .mcp.json, WriteMCPConfig
// is a no-op — the spinclass MCP server is loaded via the clown plugin
// and no longer needs a session-local entry.
func TestWriteMCPConfigNoExtrasNoExistingFileIsNoop(t *testing.T) {
	dir := t.TempDir()

	if err := WriteMCPConfig(dir, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mcpPath := filepath.Join(dir, ".mcp.json")
	if _, err := os.Stat(mcpPath); !os.IsNotExist(err) {
		t.Errorf("expected no .mcp.json to be created; stat err=%v", err)
	}
}

func TestWriteMCPConfigPreservesExistingWhenCalledWithNoExtras(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	existing := map[string]any{
		"mcpServers": map[string]any{
			"other-server": map[string]any{
				"type":    "stdio",
				"command": "other",
				"args":    []string{},
			},
		},
	}
	writeJSON(t, mcpPath, existing)

	if err := WriteMCPConfig(dir, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	doc := readJSON(t, mcpPath)
	servers, _ := doc["mcpServers"].(map[string]any)

	if _, ok := servers["other-server"]; !ok {
		t.Error("expected other-server to be preserved")
	}
	if _, ok := servers["spinclass"]; ok {
		t.Error("did not expect spinclass entry (loaded via clown plugin)")
	}
}

func TestWriteMCPConfigWithExtraServers(t *testing.T) {
	dir := t.TempDir()

	extra := []MCPServerEntry{
		{Name: "linter", Command: "my-linter", Args: []string{"serve"}},
		{Name: "formatter", Command: "fmt", Args: []string{"mcp"}, Env: map[string]string{"DEBUG": "1"}},
	}

	if err := WriteMCPConfig(dir, extra); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mcpPath := filepath.Join(dir, ".mcp.json")
	doc := readJSON(t, mcpPath)
	servers, _ := doc["mcpServers"].(map[string]any)

	if _, ok := servers["spinclass"]; ok {
		t.Error("did not expect spinclass entry (loaded via clown plugin)")
	}

	linter, ok := servers["linter"].(map[string]any)
	if !ok {
		t.Fatal("expected linter server")
	}
	if linter["command"] != "my-linter" {
		t.Errorf("linter command: got %v", linter["command"])
	}

	fmtr, ok := servers["formatter"].(map[string]any)
	if !ok {
		t.Fatal("expected formatter server")
	}
	env, ok := fmtr["env"].(map[string]any)
	if !ok || env["DEBUG"] != "1" {
		t.Errorf("formatter env: got %v", fmtr["env"])
	}
}

func TestWriteMCPConfigExtraServersPreserveExisting(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")

	existing := map[string]any{
		"mcpServers": map[string]any{
			"other-tool": map[string]any{"type": "stdio", "command": "other"},
		},
	}
	writeJSON(t, mcpPath, existing)

	extra := []MCPServerEntry{
		{Name: "linter", Command: "lint"},
	}

	if err := WriteMCPConfig(dir, extra); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	doc := readJSON(t, mcpPath)
	servers, _ := doc["mcpServers"].(map[string]any)

	if _, ok := servers["other-tool"]; !ok {
		t.Error("expected other-tool preserved")
	}
	if _, ok := servers["spinclass"]; ok {
		t.Error("did not expect spinclass entry (loaded via clown plugin)")
	}
	if _, ok := servers["linter"]; !ok {
		t.Error("expected linter")
	}
}
