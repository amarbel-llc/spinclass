# MCP Sweatfile Config Design

## Problem

External tools that provide MCP servers have no way to integrate with
spinclass sessions. The MCP allow-list (`enabledMcpjsonServers`) and
server registration (`.mcp.json`) are both hardcoded to spinclass only.
Users must manually edit session files to add additional MCP servers.

## Solution

Two new top-level sweatfile fields: `allowed-mcps` (array) and `[[mcps]]`
(array of tables). Together they control which MCP servers are registered
in `.mcp.json` and auto-approved in `enabledMcpjsonServers` when spinclass
creates a session.

## TOML Schema

```toml
# Auto-approve externally-registered MCP servers by name.
# These servers must already have their own .mcp.json entries;
# spinclass only adds them to enabledMcpjsonServers.
allowed-mcps = ["some-external-server"]

# Register and auto-approve MCP servers.
# Each entry is written to .mcp.json and implicitly added to the allow-list.
[[mcps]]
name = "my-linter"
command = "my-linter"
args = ["serve"]

[mcps.env]
DEBUG = "1"
```

### Fields

**`allowed-mcps`** — `[]string`, top-level. Server names to auto-approve
without registering. For servers that manage their own `.mcp.json` entries.

**`[[mcps]]`** — array of tables, top-level. Each entry:

| Field     | Type              | Required | Description                          |
|-----------|-------------------|----------|--------------------------------------|
| `name`    | `string`          | yes      | Server name (key in `.mcp.json`)     |
| `command` | `string`          | no*      | Executable to run                    |
| `args`    | `[]string`        | no       | Arguments passed to command          |
| `env`     | `map[string]string` | no     | Environment variables for the server |

*Empty command with only a name is a removal sentinel (see Merge Semantics).

## Data Model

```go
type MCPServerDef struct {
    Name    string            `toml:"name"`
    Command string            `toml:"command"`
    Args    []string          `toml:"args"`
    Env     map[string]string `toml:"env"`
}

type Sweatfile struct {
    // ... existing fields ...
    AllowedMCPs []string       `toml:"allowed-mcps"`
    MCPs        []MCPServerDef `toml:"mcps"`
}
```

## Merge Semantics

**`allowed-mcps`** — array-append, same as `claude.allow` and `git.excludes`:

- `nil` (field absent) → inherit from parent
- `[]` (empty array) → clear all inherited entries
- `["x"]` (non-empty) → append to inherited entries

**`[[mcps]]`** — dedup-by-name, same as `[[start-commands]]`:

- Child entries matched against parent entries by `name`
- Match found → replace parent entry in-place (preserves position)
- No match → append to end
- After merge, entries where `command == ""` are pruned (removal sentinels)

**Removal example:**

```toml
# global sweatfile
[[mcps]]
name = "my-linter"
command = "my-linter"
args = ["serve"]

# repo sweatfile — removes my-linter from this repo's sessions
[[mcps]]
name = "my-linter"
```

**Full replacement example:**

```toml
# global sweatfile
[[mcps]]
name = "my-linter"
command = "my-linter"
args = ["serve"]
[mcps.env]
DEBUG = "1"

# repo sweatfile — replaces entirely (env is gone)
[[mcps]]
name = "my-linter"
command = "my-linter-v2"
args = ["serve", "--new-flag"]
```

## Output Generation

### `.mcp.json`

`WriteMCPConfig` changes from hardcoded spinclass-only to accepting the
merged `[]MCPServerDef`. For each entry with a non-empty command:

```json
{
  "mcpServers": {
    "spinclass": { "type": "stdio", "command": "spinclass", "args": ["serve-mcp"] },
    "my-linter": { "type": "stdio", "command": "my-linter", "args": ["serve"], "env": { "DEBUG": "1" } }
  }
}
```

The spinclass entry is always present (hardcoded). Existing entries in
`.mcp.json` from other tools are preserved (merge, not overwrite).

### `enabledMcpjsonServers`

`ApplyClaudeSettings` builds the allow-list as:

1. `["spinclass"]` (always present)
2. Append all entries from `allowed-mcps`
3. Append all names from `[[mcps]]` with non-empty command
4. Deduplicate

### Implicit allow

Every `[[mcps]]` entry with a non-empty command automatically adds its
`name` to the effective allow-list. Explicit `allowed-mcps` is only needed
for servers that register themselves outside of spinclass.

## Validation

`sc validate` gains checks for `[[mcps]]` entries:

- `name` is required and non-empty
- Duplicate names within a single sweatfile file (warn, last wins)
- `command` references a findable executable (best-effort `exec.LookPath`, warn only)
