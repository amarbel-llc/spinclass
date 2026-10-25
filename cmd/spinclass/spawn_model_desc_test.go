package main

import (
	"strings"
	"testing"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
)

func modelParam(t *testing.T, params []command.Param) command.Param {
	t.Helper()
	for _, p := range params {
		if p.Name == "model" {
			return p
		}
	}
	t.Fatal("no model param")
	return command.Param{}
}

// spinclass#329: the spawn-session tool's model description lists what each
// alias resolves to in this session's sweatfile hierarchy, so an agent can
// pick the alias for a named model without reading sweatfiles.
func TestSpawnSessionModelDescriptionListsResolvedIDs(t *testing.T) {
	gatedWorktreeFixture(t, gateSweat+"\n[session-entry.model-ids]\nopus = \"claude-test-opus-9\"\n")

	desc := modelParam(t, spawnSessionParams()).Description
	for _, want := range []string{"opus → claude-test-opus-9", "sonnet → "} {
		if !strings.Contains(desc, want) {
			t.Errorf("model description lacks %q:\n%s", want, desc)
		}
	}
	if !strings.Contains(desc, "sibling") {
		t.Errorf("model description should say a sibling repo resolves its own hierarchy:\n%s", desc)
	}
}

// The CLI's params stay static: generated manpages must not bake in the
// build host's sweatfiles.
func TestSpawnCLIModelDescriptionIsStatic(t *testing.T) {
	gatedWorktreeFixture(t, gateSweat+"\n[session-entry.model-ids]\nopus = \"claude-test-opus-9\"\n")
	if desc := modelParam(t, spawnParamList()).Description; strings.Contains(desc, "claude-test-opus-9") {
		t.Errorf("CLI model description rendered host config:\n%s", desc)
	}
}

func TestModelParamDescriptionSortsAliases(t *testing.T) {
	desc := modelParamDescription(map[string]string{"sonnet": "s-id", "haiku": "h-id"})
	if h, s := strings.Index(desc, "haiku → h-id"), strings.Index(desc, "sonnet → s-id"); h < 0 || s < 0 || h > s {
		t.Errorf("aliases missing or unsorted:\n%s", desc)
	}
	if got := modelParamDescription(nil); got != modelParamBaseDescription {
		t.Errorf("no aliases should leave the base description, got:\n%s", got)
	}
}
