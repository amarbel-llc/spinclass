package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteMCPConfigCreatesFile(t *testing.T) {
	dir := t.TempDir()

	if err := WriteMCPConfig(dir, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mcpPath := filepath.Join(dir, ".mcp.json")
	doc := readJSON(t, mcpPath)

	servers, ok := doc["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("expected mcpServers key")
	}

	sc, ok := servers["spinclass"].(map[string]any)
	if !ok {
		t.Fatal("expected spinclass server entry")
	}

	if sc["type"] != "stdio" {
		t.Errorf("expected type=stdio, got %v", sc["type"])
	}
	if sc["command"] != "spinclass" {
		t.Errorf("expected command=spinclass, got %v", sc["command"])
	}

	args, ok := sc["args"].([]any)
	if !ok || len(args) != 1 || args[0] != "serve" {
		t.Errorf("expected args=[serve], got %v", sc["args"])
	}
}

func TestWriteMCPConfigIdempotent(t *testing.T) {
	dir := t.TempDir()

	if err := WriteMCPConfig(dir, nil); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteMCPConfig(dir, nil); err != nil {
		t.Fatalf("second write: %v", err)
	}

	mcpPath := filepath.Join(dir, ".mcp.json")
	doc := readJSON(t, mcpPath)
	servers, _ := doc["mcpServers"].(map[string]any)
	if len(servers) != 1 {
		t.Errorf("expected exactly 1 server, got %d", len(servers))
	}
}

func TestWriteMCPConfigDoesNotClobberExisting(t *testing.T) {
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
	if _, ok := servers["spinclass"]; !ok {
		t.Error("expected spinclass server to be added")
	}
}

func TestWriteMCPConfigCorruptFile(t *testing.T) {
	dir := t.TempDir()
	mcpPath := filepath.Join(dir, ".mcp.json")
	os.WriteFile(mcpPath, []byte("not valid json!!!"), 0o644)

	if err := WriteMCPConfig(dir, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	doc := readJSON(t, mcpPath)
	servers, _ := doc["mcpServers"].(map[string]any)
	if _, ok := servers["spinclass"]; !ok {
		t.Error("expected spinclass server entry")
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

	// spinclass always present
	if _, ok := servers["spinclass"]; !ok {
		t.Error("expected spinclass server")
	}

	// linter
	linter, ok := servers["linter"].(map[string]any)
	if !ok {
		t.Fatal("expected linter server")
	}
	if linter["command"] != "my-linter" {
		t.Errorf("linter command: got %v", linter["command"])
	}

	// formatter with env
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
	if _, ok := servers["spinclass"]; !ok {
		t.Error("expected spinclass")
	}
	if _, ok := servers["linter"]; !ok {
		t.Error("expected linter")
	}
}
