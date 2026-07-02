package main

import (
	"strings"
	"testing"
)

func TestParseStorePathBinary_StandardPath(t *testing.T) {
	got := mustParse(t, "/nix/store/wpg177xj66s03zn3yfh6n06zwkxmqn39-spinclass-0.1.5/bin/spinclass")
	want := pinTriple{
		component: "spinclass-0.1.5/spinclass",
		version:   "0.1.5",
		rev:       "wpg177xj66s03zn3yfh6n06zwkxmqn39",
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseStorePathBinary_PnameWithDashes(t *testing.T) {
	got := mustParse(t, "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-claude-code-2.1.111/bin/claude")
	if got.component != "claude-code-2.1.111/claude" {
		t.Errorf("component: got %q, want claude-code-2.1.111/claude", got.component)
	}
	if got.version != "2.1.111" {
		t.Errorf("version: got %q, want 2.1.111", got.version)
	}
}

func TestParseStorePathBinary_VPrefixVersion(t *testing.T) {
	got := mustParse(t, "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-tool-v1.0.0/bin/tool")
	if got.version != "v1.0.0" {
		t.Errorf("version: got %q, want v1.0.0", got.version)
	}
}

func TestParseStorePathBinary_NonStorePath(t *testing.T) {
	component, version, rev := parseStorePathBinary("/usr/bin/madder")
	if component != "" || version != "" || rev != "" {
		t.Errorf("expected zero values for non-store path, got (%q, %q, %q)", component, version, rev)
	}
}

func TestFormatVersionTable_AllPinned(t *testing.T) {
	out := formatVersionTable(
		"0.1.5", "ff06182",
		"/nix/store/wpg177xj66s03zn3yfh6n06zwkxmqn39-madder-0.1.5/bin/madder",
		"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-direnv-2.32.3/bin/direnv",
		"/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-papi-0.2.0/bin/papi",
		"/nix/store/cccccccccccccccccccccccccccccccc-gh-2.63.0/bin/gh",
	)

	requireLine(t, out, "spinclass-0.1.5/spinclass", "0.1.5+ff06182", "ff06182")
	requireLine(t, out, "madder-0.1.5/madder", "0.1.5", "wpg177xj66s03zn3yfh6n06zwkxmqn39")
	requireLine(t, out, "direnv-2.32.3/direnv", "2.32.3", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	requireLine(t, out, "papi-0.2.0/papi", "0.2.0", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	requireLine(t, out, "gh-2.63.0/gh", "2.63.0", "cccccccccccccccccccccccccccccccc")
	if !strings.HasPrefix(out, "COMPONENT") {
		t.Errorf("expected header line first, got:\n%s", out)
	}
}

func TestFormatVersionTable_DormantPins(t *testing.T) {
	out := formatVersionTable("dev", "unknown", "", "", "", "")

	requireLine(t, out, "madder", "-", "dormant")
	requireLine(t, out, "direnv", "-", "dormant")
	requireLine(t, out, "papi", "-", "dormant")
	requireLine(t, out, "gh", "-", "dormant")
}

func TestFormatVersionTable_OnlyMadderPinned(t *testing.T) {
	out := formatVersionTable(
		"0.1.5", "ff06182",
		"/nix/store/wpg177xj66s03zn3yfh6n06zwkxmqn39-madder-0.1.5/bin/madder",
		"", "", "",
	)

	requireLine(t, out, "madder-0.1.5/madder", "0.1.5", "wpg177xj66s03zn3yfh6n06zwkxmqn39")
	requireLine(t, out, "direnv", "-", "dormant")
	requireLine(t, out, "papi", "-", "dormant")
	requireLine(t, out, "gh", "-", "dormant")
}

type pinTriple struct {
	component string
	version   string
	rev       string
}

func mustParse(t *testing.T, path string) pinTriple {
	t.Helper()
	c, v, r := parseStorePathBinary(path)
	if c == "" {
		t.Fatalf("parseStorePathBinary(%q) failed to parse", path)
	}
	return pinTriple{component: c, version: v, rev: r}
}

// requireLine asserts that a row with the given column values appears
// in tabwriter output. tabwriter pads with spaces, so we compare on
// whitespace-collapsed fields rather than exact byte equality.
func requireLine(t *testing.T, table, component, version, rev string) {
	t.Helper()
	want := []string{component, version, rev}
	for _, line := range strings.Split(strings.TrimRight(table, "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		if fields[0] == want[0] && fields[1] == want[1] && fields[2] == want[2] {
			return
		}
	}
	t.Errorf("expected row %v in table:\n%s", want, table)
}
