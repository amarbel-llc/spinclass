package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteMCPConfig writes a .mcp.json in worktreePath that configures
// spinclass serve-mcp as a stdio MCP server. If .mcp.json already exists,
// the spinclass entry is merged in without clobbering other servers.
func WriteMCPConfig(worktreePath string) error {
	mcpPath := filepath.Join(worktreePath, ".mcp.json")

	var doc map[string]any
	if data, err := os.ReadFile(mcpPath); err == nil {
		json.Unmarshal(data, &doc)
	}
	if doc == nil {
		doc = make(map[string]any)
	}

	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = make(map[string]any)
	}

	servers["spinclass"] = map[string]any{
		"type":    "stdio",
		"command": "spinclass",
		"args":    []string{"serve-mcp"},
	}
	doc["mcpServers"] = servers

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp := fmt.Sprintf("%s.tmp.%d", mcpPath, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}

	if err := os.Rename(tmp, mcpPath); err != nil {
		os.Remove(tmp)
		return err
	}

	return nil
}
