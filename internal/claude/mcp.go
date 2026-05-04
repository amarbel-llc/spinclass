package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// MCPServerEntry represents an MCP server to register in .mcp.json.
type MCPServerEntry struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

// WriteMCPConfig writes a .mcp.json in worktreePath listing the
// user-declared stdio MCP servers (sweatfile [[mcps]] entries). The
// spinclass MCP server itself is loaded via the clown plugin and is no
// longer mirrored into per-worktree .mcp.json.
//
// If .mcp.json already exists, entries are merged in without clobbering
// other servers. When extraServers is empty and no prior .mcp.json
// exists, the function is a no-op so worktrees stay free of empty
// session-local MCP files.
func WriteMCPConfig(worktreePath string, extraServers []MCPServerEntry) error {
	mcpPath := filepath.Join(worktreePath, ".mcp.json")

	existed := false
	var doc map[string]any
	if data, err := os.ReadFile(mcpPath); err == nil {
		existed = true
		json.Unmarshal(data, &doc)
	}
	if !existed && len(extraServers) == 0 {
		return nil
	}
	if doc == nil {
		doc = make(map[string]any)
	}

	servers, _ := doc["mcpServers"].(map[string]any)
	if servers == nil {
		servers = make(map[string]any)
	}

	for _, entry := range extraServers {
		serverDef := map[string]any{
			"type":    "stdio",
			"command": entry.Command,
			"args":    entry.Args,
		}
		if len(entry.Env) > 0 {
			serverDef["env"] = entry.Env
		}
		servers[entry.Name] = serverDef
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
