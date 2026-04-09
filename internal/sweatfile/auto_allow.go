package sweatfile

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type autoAllowResponse struct {
	Tools   []string `json:"tools"`
	Servers []string `json:"servers"`
}

// ExecAutoAllow runs the auto-allow command declared on an MCPServerDef and
// returns fully-qualified permission rules (mcp__<name>__<tool>) for each
// advertised tool. Returns (nil, nil) when no auto-allow command is configured.
func ExecAutoAllow(mcp MCPServerDef) ([]string, error) {
	if len(mcp.AutoAllow) == 0 {
		return nil, nil
	}

	cmd := exec.Command(mcp.AutoAllow[0], mcp.AutoAllow[1:]...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("executing command: %w", err)
	}

	var resp autoAllowResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("parsing JSON output: %w", err)
	}

	if len(resp.Servers) > 0 {
		return nil, fmt.Errorf("servers field is not supported; MCP servers cannot auto-allow other servers")
	}

	var rules []string
	for _, tool := range resp.Tools {
		if strings.HasPrefix(tool, "mcp__") {
			return nil, fmt.Errorf("tool %q must be a bare name without mcp__ prefix", tool)
		}

		rules = append(rules, fmt.Sprintf("mcp__%s__%s", mcp.Name, tool))
	}

	return rules, nil
}
