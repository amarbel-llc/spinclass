package perms

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestGritMigrationReportFindsMoxyGritRules(t *testing.T) {
	dir := t.TempDir()
	if err := SaveTierFile(filepath.Join(dir, "global.json"), Tier{Allow: []string{
		"mcp__plugin_moxy_moxy__grit_stash",
		"mcp__plugin_moxy_moxy__grit_branch(subcommand:log)",
		"mcp__plugin_moxy_moxy__folio_read",
		"mcp__plugin_spinclass_spinclass__grit_stage",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveTierFile(filepath.Join(dir, "repos", "spinclass.json"), Tier{Allow: []string{
		"mcp__plugin_moxy_moxy__grit_commit",
	}}); err != nil {
		t.Fatal(err)
	}

	findings, err := GritMigrationReport(dir)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	for _, f := range findings {
		got[f.Tier+"|"+f.Rule] = f.NewTool
	}
	want := map[string]string{}
	want["global|mcp__plugin_moxy_moxy__grit_stash"] = "mcp__plugin_spinclass_spinclass__grit_stash"
	want["global|mcp__plugin_moxy_moxy__grit_branch(subcommand:log)"] = "mcp__plugin_spinclass_spinclass__grit_branch"
	want["repo spinclass|mcp__plugin_moxy_moxy__grit_commit"] = "mcp__plugin_spinclass_spinclass__grit_commit"
	if len(got) != len(want) {
		t.Fatalf("findings = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("finding %q = %q, want %q", k, got[k], v)
		}
	}
}

func TestGritMigrationReportDoesNotRewriteTierFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "global.json")
	rules := []string{"mcp__plugin_moxy_moxy__grit_stash"}
	if err := SaveTierFile(path, Tier{Allow: rules}); err != nil {
		t.Fatal(err)
	}
	if _, err := GritMigrationReport(dir); err != nil {
		t.Fatal(err)
	}
	tier, err := LoadTierFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tier.Allow) != 1 || tier.Allow[0] != rules[0] {
		t.Fatalf("tier file changed: %v", tier.Allow)
	}
}

func TestGritMigrationReportEmptyWhenNoMatches(t *testing.T) {
	dir := t.TempDir()
	if err := SaveTierFile(filepath.Join(dir, "global.json"), Tier{Allow: []string{"mcp__plugin_moxy_moxy__folio_read"}}); err != nil {
		t.Fatal(err)
	}
	findings, err := GritMigrationReport(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %v, want none", findings)
	}
}

func TestRunListWriterAppendsGritMigrationReport(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SWEATSHOP_PERMS_DIR", dir)
	if err := SaveTierFile(filepath.Join(dir, "global.json"), Tier{Allow: []string{"mcp__plugin_moxy_moxy__grit_tag"}}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := RunListWriter(&buf, ""); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"Grit migration", "mcp__plugin_moxy_moxy__grit_tag", "mcp__plugin_spinclass_spinclass__grit_tag", "not rewritten"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestRunListWriterOmitsReportWithoutMatches(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SWEATSHOP_PERMS_DIR", dir)
	if err := SaveTierFile(filepath.Join(dir, "global.json"), Tier{Allow: []string{"Bash(ls)"}}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := RunListWriter(&buf, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "Grit migration") {
		t.Fatalf("unexpected report:\n%s", buf.String())
	}
}
