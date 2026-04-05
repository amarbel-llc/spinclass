package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteMCPConfigCreatesFile(t *testing.T) {
	dir := t.TempDir()

	if err := WriteMCPConfig(dir); err != nil {
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
	if !ok || len(args) != 1 || args[0] != "serve-mcp" {
		t.Errorf("expected args=[serve-mcp], got %v", sc["args"])
	}
}

func TestWriteMCPConfigIdempotent(t *testing.T) {
	dir := t.TempDir()

	if err := WriteMCPConfig(dir); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteMCPConfig(dir); err != nil {
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

	if err := WriteMCPConfig(dir); err != nil {
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

	if err := WriteMCPConfig(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	doc := readJSON(t, mcpPath)
	servers, _ := doc["mcpServers"].(map[string]any)
	if _, ok := servers["spinclass"]; !ok {
		t.Error("expected spinclass server entry")
	}
}
