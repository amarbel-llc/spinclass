# MCP Sweatfile Config Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Add `allowed-mcps` and `[[mcps]]` sweatfile config fields so external tools can declare MCP servers that spinclass registers and auto-approves during session creation.

**Architecture:** Two new top-level sweatfile fields. `allowed-mcps` is an array-append field (same as `claude.allow`). `[[mcps]]` is an array-of-tables with dedup-by-name merge (same as `[[start-commands]]`). Both feed into `WriteMCPConfig` (`.mcp.json`) and `ApplyClaudeSettings` (`enabledMcpjsonServers`).

**Tech Stack:** Go, tommy TOML library, existing sweatfile merge infrastructure.

**Rollback:** N/A — purely additive. Existing sessions unaffected; new fields are optional.

---

### Task 1: Add MCPServerDef type and Sweatfile fields

**Files:**
- Modify: `internal/sweatfile/sweatfile.go:41-61`

**Step 1: Write the failing test**

Create `internal/sweatfile/mcp_test.go`:

```go
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
```

**Step 2: Run test to verify it fails**

Run: `nix develop ../.. --command go test ./internal/sweatfile/ -run 'TestMCPServerDef|TestEffectiveAllowed|TestActiveMCPs' -v`
Expected: FAIL — `MCPServerDef` type undefined.

**Step 3: Write minimal implementation**

Add to `internal/sweatfile/sweatfile.go` before the `Sweatfile` struct (after `StartCommand`):

```go
// MCPServerDef declares an MCP server to register and auto-approve
// in Claude Code sessions. See CLAUDE.md "MCP Sweatfile Config" design.
type MCPServerDef struct {
	Name    string            `toml:"name"`
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
}
```

Add two fields to the `Sweatfile` struct:

```go
type Sweatfile struct {
	Claude        *Claude        `toml:"claude"`
	Git           *Git           `toml:"git"`
	Direnv        *Direnv        `toml:"direnv"`
	Hooks         *Hooks         `toml:"hooks"`
	SessionEntry  *SessionEntry  `toml:"session-entry"`
	StartCommands []StartCommand `toml:"start-commands"`
	AllowedMCPs   []string       `toml:"allowed-mcps"`
	MCPs          []MCPServerDef `toml:"mcps"`
}
```

Add accessor methods:

```go
// EffectiveAllowedMCPs returns the deduplicated list of MCP server names
// that should be auto-approved. Combines explicit allowed-mcps entries
// with implicit names from [[mcps]] entries that have a non-empty command.
func (sf Sweatfile) EffectiveAllowedMCPs() []string {
	seen := make(map[string]bool)
	var result []string

	for _, name := range sf.AllowedMCPs {
		if !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}

	for _, mcp := range sf.MCPs {
		if mcp.Command != "" && !seen[mcp.Name] {
			seen[mcp.Name] = true
			result = append(result, mcp.Name)
		}
	}

	return result
}

// ActiveMCPs returns only [[mcps]] entries with a non-empty command
// (i.e., excluding removal sentinels).
func (sf Sweatfile) ActiveMCPs() []MCPServerDef {
	var active []MCPServerDef
	for _, mcp := range sf.MCPs {
		if mcp.Command != "" {
			active = append(active, mcp)
		}
	}
	return active
}
```

**Step 4: Run test to verify it passes**

Run: `nix develop ../.. --command go test ./internal/sweatfile/ -run 'TestMCPServerDef|TestEffectiveAllowed|TestActiveMCPs' -v`
Expected: PASS

**Step 5: Commit**

```
feat(sweatfile): add MCPServerDef type and AllowedMCPs/MCPs fields
```

---

### Task 2: Add merge logic for allowed-mcps and [[mcps]]

**Files:**
- Modify: `internal/sweatfile/hierarchy.go:124-256`
- Test: `internal/sweatfile/mcp_test.go` (append)

**Step 1: Write the failing tests**

Append to `internal/sweatfile/mcp_test.go`:

```go
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
```

**Step 2: Run tests to verify they fail**

Run: `nix develop ../.. --command go test ./internal/sweatfile/ -run 'TestMergeAllowedMCPs|TestMergeMCPs' -v`
Expected: FAIL — merge logic doesn't handle new fields yet.

**Step 3: Write minimal implementation**

Add to `MergeWith` in `hierarchy.go` before `return merged` (after the `[[start-commands]]` block at line 253):

```go
	// allowed-mcps: nil=inherit, empty=clear, non-empty=append
	if other.AllowedMCPs != nil {
		if len(other.AllowedMCPs) == 0 {
			merged.AllowedMCPs = []string{}
		} else {
			merged.AllowedMCPs = append(merged.AllowedMCPs, other.AllowedMCPs...)
		}
	}

	// [[mcps]] — dedup-by-name, same pattern as [[start-commands]]
	if len(other.MCPs) > 0 {
		cp := make([]MCPServerDef, len(merged.MCPs))
		copy(cp, merged.MCPs)
		merged.MCPs = cp
		index := make(map[string]int, len(merged.MCPs))
		for i, mcp := range merged.MCPs {
			index[mcp.Name] = i
		}
		for _, mcp := range other.MCPs {
			if i, ok := index[mcp.Name]; ok {
				merged.MCPs[i] = mcp
				continue
			}
			index[mcp.Name] = len(merged.MCPs)
			merged.MCPs = append(merged.MCPs, mcp)
		}
	}
```

**Step 4: Run tests to verify they pass**

Run: `nix develop ../.. --command go test ./internal/sweatfile/ -run 'TestMergeAllowedMCPs|TestMergeMCPs' -v`
Expected: PASS

**Step 5: Commit**

```
feat(sweatfile): add merge logic for allowed-mcps and [[mcps]]
```

---

### Task 3: Add tommy decode/encode for [[mcps]] and allowed-mcps

**Files:**
- Modify: `internal/sweatfile/sweatfile_tommy.go`
- Modify: `internal/sweatfile/coding.go`

**Step 1: Write the failing test**

Append to `internal/sweatfile/mcp_test.go`:

```go
func TestParseMCPsFromTOML(t *testing.T) {
	input := []byte(`
allowed-mcps = ["external-server"]

[[mcps]]
name = "my-linter"
command = "my-linter"
args = ["serve"]

[mcps.env]
DEBUG = "1"

[[mcps]]
name = "formatter"
command = "fmt"
`)
	doc, err := Parse(input)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	sf := *doc.Data()

	if len(sf.AllowedMCPs) != 1 || sf.AllowedMCPs[0] != "external-server" {
		t.Errorf("AllowedMCPs: got %v", sf.AllowedMCPs)
	}
	if len(sf.MCPs) != 2 {
		t.Fatalf("MCPs: expected 2, got %d", len(sf.MCPs))
	}
	if sf.MCPs[0].Name != "my-linter" || sf.MCPs[0].Command != "my-linter" {
		t.Errorf("MCPs[0]: got %+v", sf.MCPs[0])
	}
	if sf.MCPs[0].Env["DEBUG"] != "1" {
		t.Errorf("MCPs[0].Env: got %v", sf.MCPs[0].Env)
	}
	if sf.MCPs[1].Name != "formatter" || sf.MCPs[1].Command != "fmt" {
		t.Errorf("MCPs[1]: got %+v", sf.MCPs[1])
	}
}

func TestParseMCPsEmptyAllowedMCPs(t *testing.T) {
	input := []byte(`allowed-mcps = []`)
	doc, err := Parse(input)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	sf := *doc.Data()

	if sf.AllowedMCPs == nil {
		t.Error("expected non-nil empty slice for AllowedMCPs")
	}
	if len(sf.AllowedMCPs) != 0 {
		t.Errorf("expected empty, got %v", sf.AllowedMCPs)
	}
}

func TestParseMCPsNoUndecodedKeys(t *testing.T) {
	input := []byte(`
allowed-mcps = ["ext"]

[[mcps]]
name = "linter"
command = "lint"
args = ["serve"]

[mcps.env]
KEY = "val"
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

func TestParseMCPsRemovalSentinel(t *testing.T) {
	input := []byte(`
[[mcps]]
name = "linter"
`)
	doc, err := Parse(input)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	sf := *doc.Data()

	if len(sf.MCPs) != 1 {
		t.Fatalf("expected 1, got %d", len(sf.MCPs))
	}
	if sf.MCPs[0].Name != "linter" || sf.MCPs[0].Command != "" {
		t.Errorf("expected removal sentinel, got %+v", sf.MCPs[0])
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `nix develop ../.. --command go test ./internal/sweatfile/ -run 'TestParseMCPs' -v`
Expected: FAIL — tommy doesn't decode `allowed-mcps` or `[[mcps]]` yet.

**Step 3: Write minimal implementation**

Add `decodeMCPs` function to `sweatfile_tommy.go` (modeled on `decodeStartCommands`):

```go
func decodeMCPs(
	doc *document.Document,
	data *Sweatfile,
	consumed map[string]bool,
	keyPrefix string,
) {
	nodes := doc.FindArrayTableNodes("mcps")
	if len(nodes) == 0 {
		return
	}
	consumed[keyPrefix+"mcps"] = true
	data.MCPs = make([]MCPServerDef, len(nodes))
	for i, node := range nodes {
		if v, err := document.GetFromContainer[string](doc, node, "name"); err == nil {
			data.MCPs[i].Name = v
			consumed[keyPrefix+"mcps.name"] = true
		}
		if v, err := document.GetFromContainer[string](doc, node, "command"); err == nil {
			data.MCPs[i].Command = v
			consumed[keyPrefix+"mcps.command"] = true
		}
		if v, err := document.GetFromContainer[[]string](doc, node, "args"); err == nil {
			data.MCPs[i].Args = v
			consumed[keyPrefix+"mcps.args"] = true
		}
		if envNode := doc.FindTableInContainer(node, "env"); envNode != nil {
			data.MCPs[i].Env = document.GetStringMapFromTable(envNode)
			consumed[keyPrefix+"mcps.env"] = true
			document.MarkAllConsumed(envNode, keyPrefix+"mcps.env", consumed)
		}
	}
}

func encodeMCPs(doc *document.Document, data *Sweatfile) error {
	for i := range data.MCPs {
		entry := doc.AppendArrayTableEntry("mcps")
		mcp := &data.MCPs[i]
		if err := doc.SetInContainer(entry, "name", mcp.Name); err != nil {
			return err
		}
		if mcp.Command != "" {
			if err := doc.SetInContainer(entry, "command", mcp.Command); err != nil {
				return err
			}
		}
		if mcp.Args != nil {
			if err := doc.SetInContainer(entry, "args", mcp.Args); err != nil {
				return err
			}
		}
		if len(mcp.Env) > 0 {
			envNode := doc.EnsureTableInContainer(entry, "env")
			for k, v := range mcp.Env {
				if err := doc.SetInContainer(envNode, k, v); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
```

In `DecodeSweatfile`, add after `decodeStartCommands(d.cstDoc, &d.data, d.consumed, "")` (line 175):

```go
	// allowed-mcps (top-level array)
	if v, err := document.GetFromContainer[[]string](d.cstDoc, d.cstDoc.Root(), "allowed-mcps"); err == nil {
		d.data.AllowedMCPs = v
		d.consumed["allowed-mcps"] = true
	}
	decodeMCPs(d.cstDoc, &d.data, d.consumed, "")
```

In `Encode`, add after `encodeStartCommands` call (line 345):

```go
	if d.data.AllowedMCPs != nil {
		if err := d.cstDoc.SetInContainer(d.cstDoc.Root(), "allowed-mcps", d.data.AllowedMCPs); err != nil {
			return nil, err
		}
	}
	if err := encodeMCPs(d.cstDoc, &d.data); err != nil {
		return nil, err
	}
```

In `coding.go` `Parse` function, add normalization for `allowed-mcps` after the existing `direnv.envrc` block (line 27):

```go
	if doc.consumed["allowed-mcps"] && doc.data.AllowedMCPs == nil {
		doc.data.AllowedMCPs = []string{}
	}
```

**Step 4: Run tests to verify they pass**

Run: `nix develop ../.. --command go test ./internal/sweatfile/ -run 'TestParseMCPs' -v`
Expected: PASS

**Step 5: Run full test suite**

Run: `nix develop ../.. --command go test ./internal/sweatfile/ -v`
Expected: All PASS (no regressions, no undecoded key errors).

**Step 6: Commit**

```
feat(sweatfile): add tommy decode/encode for allowed-mcps and [[mcps]]
```

---

### Task 4: Update WriteMCPConfig to accept MCP server defs

**Files:**
- Modify: `internal/claude/mcp.go:13-54`
- Modify: `internal/claude/mcp_test.go`
- Modify: `internal/worktree/worktree.go:254`

**Step 1: Write the failing tests**

Add to `internal/claude/mcp_test.go`:

```go
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
```

**Step 2: Run tests to verify they fail**

Run: `nix develop ../.. --command go test ./internal/claude/ -run 'TestWriteMCPConfigWith|TestWriteMCPConfigExtra' -v`
Expected: FAIL — `MCPServerEntry` undefined, `WriteMCPConfig` wrong signature.

**Step 3: Write minimal implementation**

Update `internal/claude/mcp.go`:

```go
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

// WriteMCPConfig writes a .mcp.json in worktreePath that configures
// spinclass serve-mcp as a stdio MCP server plus any additional servers.
// If .mcp.json already exists, entries are merged in without clobbering
// other servers.
func WriteMCPConfig(worktreePath string, extraServers []MCPServerEntry) error {
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
```

Update existing tests that call `WriteMCPConfig(dir)` to pass `nil`:
- `TestWriteMCPConfigCreatesFile`: `WriteMCPConfig(dir, nil)`
- `TestWriteMCPConfigIdempotent`: both calls `WriteMCPConfig(dir, nil)`
- `TestWriteMCPConfigDoesNotClobberExisting`: `WriteMCPConfig(dir, nil)`
- `TestWriteMCPConfigCorruptFile`: `WriteMCPConfig(dir, nil)`

Update the call site in `internal/worktree/worktree.go:254`:

```go
	// Build MCP server entries from sweatfile
	var mcpEntries []claude.MCPServerEntry
	for _, mcp := range sweetfile.Merged.ActiveMCPs() {
		mcpEntries = append(mcpEntries, claude.MCPServerEntry{
			Name:    mcp.Name,
			Command: mcp.Command,
			Args:    mcp.Args,
			Env:     mcp.Env,
		})
	}

	if err := claude.WriteMCPConfig(worktreePath, mcpEntries); err != nil {
		return fmt.Errorf("writing .mcp.json: %w", err)
	}
```

**Step 4: Run tests to verify they pass**

Run: `nix develop ../.. --command go test ./internal/claude/ -v`
Expected: All PASS

**Step 5: Run full test suite**

Run: `nix develop ../.. --command go test ./... -v 2>&1 | head -100`
Expected: All PASS (check worktree package compiles).

**Step 6: Commit**

```
feat(claude): update WriteMCPConfig to register sweatfile MCP servers
```

---

### Task 5: Update ApplyClaudeSettings to use effective allow-list

**Files:**
- Modify: `internal/sweatfile/apply.go:255-257`
- Modify: `internal/sweatfile/apply_test.go`

**Step 1: Write the failing test**

Append to `internal/sweatfile/apply_test.go`:

```go
func TestApplyClaudeSettingsEnabledMCPs(t *testing.T) {
	dir := t.TempDir()

	sf := Sweatfile{
		AllowedMCPs: []string{"external-server"},
		MCPs: []MCPServerDef{
			{Name: "my-linter", Command: "lint"},
		},
	}

	err := ApplyClaudeSettings(dir, sf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	var doc map[string]any
	json.Unmarshal(data, &doc)

	enabledRaw, _ := doc["enabledMcpjsonServers"].([]any)
	enabled := make(map[string]bool)
	for _, v := range enabledRaw {
		enabled[v.(string)] = true
	}

	if !enabled["spinclass"] {
		t.Error("expected spinclass in enabledMcpjsonServers")
	}
	if !enabled["external-server"] {
		t.Error("expected external-server in enabledMcpjsonServers")
	}
	if !enabled["my-linter"] {
		t.Error("expected my-linter in enabledMcpjsonServers")
	}
}

func TestApplyClaudeSettingsEnabledMCPsDedup(t *testing.T) {
	dir := t.TempDir()

	sf := Sweatfile{
		AllowedMCPs: []string{"spinclass", "my-linter"},
		MCPs: []MCPServerDef{
			{Name: "my-linter", Command: "lint"},
		},
	}

	err := ApplyClaudeSettings(dir, sf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	var doc map[string]any
	json.Unmarshal(data, &doc)

	enabledRaw, _ := doc["enabledMcpjsonServers"].([]any)
	// Should have spinclass and my-linter, no duplicates
	names := make(map[string]int)
	for _, v := range enabledRaw {
		names[v.(string)]++
	}
	if names["spinclass"] != 1 {
		t.Errorf("expected spinclass once, got %d", names["spinclass"])
	}
	if names["my-linter"] != 1 {
		t.Errorf("expected my-linter once, got %d", names["my-linter"])
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `nix develop ../.. --command go test ./internal/sweatfile/ -run 'TestApplyClaudeSettingsEnabled' -v`
Expected: FAIL — `enabledMcpjsonServers` still hardcoded to `["spinclass"]`.

**Step 3: Write minimal implementation**

Replace line 257 of `apply.go`:

```go
	// from:
	doc["enabledMcpjsonServers"] = []string{"spinclass"}

	// to:
	enabledMCPs := []string{"spinclass"}
	seen := map[string]bool{"spinclass": true}
	for _, name := range sweatfile.EffectiveAllowedMCPs() {
		if !seen[name] {
			seen[name] = true
			enabledMCPs = append(enabledMCPs, name)
		}
	}
	doc["enabledMcpjsonServers"] = enabledMCPs
```

**Step 4: Run tests to verify they pass**

Run: `nix develop ../.. --command go test ./internal/sweatfile/ -run 'TestApplyClaudeSettingsEnabled' -v`
Expected: PASS

**Step 5: Run full test suite**

Run: `nix develop ../.. --command go test ./internal/sweatfile/ -v`
Expected: All PASS

**Step 6: Commit**

```
feat(sweatfile): populate enabledMcpjsonServers from allowed-mcps and [[mcps]]
```

---

### Task 6: Add validation for [[mcps]] entries

**Files:**
- Modify: `internal/validate/validate.go`
- Test: `internal/validate/validate_test.go`

**Step 1: Write the failing test**

Check if `validate_test.go` exists and what test pattern it uses:

Run: `ls internal/validate/validate_test.go 2>/dev/null && head -30 internal/validate/validate_test.go`

If it exists, follow the existing pattern. If not, create it.

Add `CheckMCPs` tests:

```go
func TestCheckMCPsValid(t *testing.T) {
	sf := sweatfile.Sweatfile{
		MCPs: []sweatfile.MCPServerDef{
			{Name: "linter", Command: "lint", Args: []string{"serve"}},
		},
	}
	issues := CheckMCPs(sf)
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

func TestCheckMCPsMissingName(t *testing.T) {
	sf := sweatfile.Sweatfile{
		MCPs: []sweatfile.MCPServerDef{
			{Command: "lint"},
		},
	}
	issues := CheckMCPs(sf)
	if len(issues) != 1 || issues[0].Severity != SeverityError {
		t.Errorf("expected 1 error for missing name, got %v", issues)
	}
}

func TestCheckMCPsDuplicateName(t *testing.T) {
	sf := sweatfile.Sweatfile{
		MCPs: []sweatfile.MCPServerDef{
			{Name: "linter", Command: "lint"},
			{Name: "linter", Command: "lint2"},
		},
	}
	issues := CheckMCPs(sf)
	hasWarn := false
	for _, iss := range issues {
		if iss.Severity == SeverityWarning && iss.Field == "mcps.name" {
			hasWarn = true
		}
	}
	if !hasWarn {
		t.Errorf("expected warning for duplicate name, got %v", issues)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `nix develop ../.. --command go test ./internal/validate/ -run 'TestCheckMCPs' -v`
Expected: FAIL — `CheckMCPs` undefined.

**Step 3: Write minimal implementation**

Add to `internal/validate/validate.go`:

```go
func CheckMCPs(sf sweatfile.Sweatfile) []Issue {
	var issues []Issue
	seen := make(map[string]bool, len(sf.MCPs))
	for _, mcp := range sf.MCPs {
		if mcp.Name == "" {
			issues = append(issues, Issue{
				Message:  "mcps entry missing `name`",
				Severity: SeverityError,
				Field:    "mcps.name",
			})
			continue
		}
		if seen[mcp.Name] {
			issues = append(issues, Issue{
				Message:  fmt.Sprintf("duplicate mcps entry %q in this file", mcp.Name),
				Severity: SeverityWarning,
				Field:    "mcps.name",
				Value:    mcp.Name,
			})
		}
		seen[mcp.Name] = true
	}
	return issues
}
```

Wire it into the `Run` function, after the `start-commands` block (~line 379):

```go
		if len(src.File.MCPs) > 0 {
			if issues := CheckMCPs(src.File); len(issues) > 0 {
				for _, iss := range issues {
					diag := map[string]string{
						"severity": iss.Severity,
						"message":  iss.Message,
					}
					if iss.Value != "" {
						diag["value"] = iss.Value
					}
					sub.NotOk("mcps valid", diag)
				}
			} else {
				sub.Ok("mcps valid")
			}
		}
```

**Step 4: Run tests to verify they pass**

Run: `nix develop ../.. --command go test ./internal/validate/ -run 'TestCheckMCPs' -v`
Expected: PASS

**Step 5: Run full test suite**

Run: `nix develop ../.. --command go test ./... -v 2>&1 | head -100`
Expected: All PASS

**Step 6: Commit**

```
feat(validate): add CheckMCPs for [[mcps]] entries
```

---

### Task 7: Update CLAUDE.md documentation

**Files:**
- Modify: `CLAUDE.md`

**Step 1: Add documentation**

Add a section to the sweatfile config description in CLAUDE.md, after the `[[start-commands]]` documentation:

```markdown
## MCP Server Configuration

`allowed-mcps` and `[[mcps]]` control which MCP servers are registered
and auto-approved in Claude Code sessions.

```toml
# Auto-approve externally-registered MCP servers by name
allowed-mcps = ["some-external-server"]

# Register and auto-approve MCP servers
[[mcps]]
name = "my-linter"
command = "my-linter"
args = ["serve"]

[mcps.env]
DEBUG = "1"
```

- `allowed-mcps` uses array-append merge (nil=inherit, `[]`=clear,
  non-empty=append)
- `[[mcps]]` uses dedup-by-name merge (same as `[[start-commands]]`).
  Name-only entry (empty command) removes an inherited server.
- Every `[[mcps]]` entry with a command implicitly adds to the allow-list.
```

**Step 2: Commit**

```
docs: document allowed-mcps and [[mcps]] sweatfile config
```

---

### Task 8: Build and verify

**Step 1: Build**

Run: `nix develop ../.. --command go build ./cmd/spinclass/`
Expected: Success

**Step 2: Full test suite**

Run: `nix develop ../.. --command go test ./... -v 2>&1 | tail -30`
Expected: All PASS

**Step 3: Nix build**

Run: `just build`
Expected: Success

**Step 4: Commit (if any formatting fixes needed)**

```
style: apply gofumpt formatting
```
