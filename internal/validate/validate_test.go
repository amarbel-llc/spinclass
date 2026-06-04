package validate

import (
	"testing"

	"github.com/amarbel-llc/spinclass/internal/sweatfile"
)

func TestCheckClaudeAllowSyntaxValid(t *testing.T) {
	sf := sweatfile.Sweatfile{
		Claude: &sweatfile.Claude{Allow: []string{"Read", "Bash(git *)", "Write(/foo/*)"}},
	}
	issues := CheckClaudeAllow(sf)
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

func TestCheckClaudeAllowSyntaxInvalid(t *testing.T) {
	sf := sweatfile.Sweatfile{
		Claude: &sweatfile.Claude{Allow: []string{"Bash(git *", "Read("}},
	}
	issues := CheckClaudeAllow(sf)
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %v", issues)
	}
	for _, iss := range issues {
		if iss.Severity != SeverityError {
			t.Errorf("expected error severity, got %s", iss.Severity)
		}
	}
}

func TestCheckClaudeAllowMCPTool(t *testing.T) {
	sf := sweatfile.Sweatfile{
		Claude: &sweatfile.Claude{Allow: []string{
			"mcp__plugin_lux_lux__diagnostics",
			"mcp__plugin_grit_grit__status",
			"mcp__foo",
		}},
	}
	issues := CheckClaudeAllow(sf)
	if len(issues) != 0 {
		t.Errorf("expected no issues for MCP tool names, got %v", issues)
	}
}

func TestCheckClaudeAllowUnknownTool(t *testing.T) {
	sf := sweatfile.Sweatfile{
		Claude: &sweatfile.Claude{Allow: []string{"FooBar", "Read"}},
	}
	issues := CheckClaudeAllow(sf)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %v", issues)
	}
	if issues[0].Severity != SeverityWarning {
		t.Errorf("expected warning severity, got %s", issues[0].Severity)
	}
}

func TestCheckGitExcludesValid(t *testing.T) {
	sf := sweatfile.Sweatfile{
		Git: &sweatfile.Git{Excludes: []string{".claude/", ".direnv/"}},
	}
	issues := CheckGitExcludes(sf)
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

func TestCheckGitExcludesEmpty(t *testing.T) {
	sf := sweatfile.Sweatfile{
		Git: &sweatfile.Git{Excludes: []string{".claude/", "", ".direnv/"}},
	}
	issues := CheckGitExcludes(sf)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %v", issues)
	}
}

func TestCheckGitExcludesAbsolutePath(t *testing.T) {
	sf := sweatfile.Sweatfile{
		Git: &sweatfile.Git{Excludes: []string{"/absolute/path"}},
	}
	issues := CheckGitExcludes(sf)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %v", issues)
	}
}

func TestCheckMergedDuplicates(t *testing.T) {
	sf := sweatfile.Sweatfile{
		Git:    &sweatfile.Git{Excludes: []string{".claude/", ".direnv/", ".claude/"}},
		Claude: &sweatfile.Claude{Allow: []string{"Read", "Read"}},
	}
	issues := CheckMerged(sf)
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues (one per field), got %v", issues)
	}
	for _, iss := range issues {
		if iss.Severity != SeverityWarning {
			t.Errorf("expected warning severity, got %s", iss.Severity)
		}
	}
}

func TestCheckUnknownFields(t *testing.T) {
	data := []byte(`
unknown_field = "bad"
`)
	issues := CheckUnknownFields(data)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %v", issues)
	}
	if issues[0].Severity != SeverityError {
		t.Errorf("expected error severity, got %s", issues[0].Severity)
	}
}

func TestCheckUnknownFieldsClean(t *testing.T) {
	data := []byte(`
[git]
excludes = [".claude/"]

[claude]
allow = ["Read"]
`)
	issues := CheckUnknownFields(data)
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

func TestCheckUnknownFieldsHooksTable(t *testing.T) {
	data := []byte(`
[hooks]
create = "npm install"
stop = "just build test"
`)
	issues := CheckUnknownFields(data)
	if len(issues) != 0 {
		t.Errorf("expected no issues for [hooks] table, got %v", issues)
	}
}

// TestCheckUnknownFieldsDirenvDotenv is the regression guard for #96 part 1:
// `sc validate` must NOT reject the documented [direnv.dotenv] sub-table (the
// shipped 0.1.15 binary did, silently dropping the field at parse), including
// arbitrary user-chosen keys inside it. The sub-table form is the canonical,
// supported spelling (see spinclass-sweatfile(5)).
func TestCheckUnknownFieldsDirenvDotenv(t *testing.T) {
	data := []byte(`
[direnv]
envrc = ["source_up"]

[direnv.dotenv]
FOO = "$WORKTREE/bar"
ETSYWEB_PWD = "$WORKTREE"
`)
	issues := CheckUnknownFields(data)
	if len(issues) != 0 {
		t.Errorf("[direnv.dotenv] sub-table should validate clean, got %v", issues)
	}
}

// TestCheckUnknownFieldsDirenvDotenvInlineKnownGap documents a known
// limitation tracked upstream in amarbel-llc/tommy: the inline-table spelling
// `dotenv = { ... }` is NOT consumed by the generated decoder (tommy's
// FindChildTable matches only [parent.dotenv] header tables, not inline-table
// key-values), so `sc validate` flags it as unknown. The canonical sub-table
// form works (test above). This test pins the current behavior so a future
// tommy fix that closes the gap will surface here as a deliberate update,
// not a silent change.
func TestCheckUnknownFieldsDirenvDotenvInlineKnownGap(t *testing.T) {
	data := []byte(`
[direnv]
dotenv = { FOO = "$WORKTREE/bar" }
`)
	issues := CheckUnknownFields(data)
	if len(issues) == 0 {
		t.Skip("inline-table direnv.dotenv now validates clean — tommy gap fixed; " +
			"promote the inline form in spinclass-sweatfile(5) and fold this into the test above")
	}
	// Until then, assert the gap is exactly the documented one.
	if issues[0].Field != "direnv.dotenv" {
		t.Errorf("expected unknown-field issue for direnv.dotenv, got %v", issues)
	}
}

func TestCheckStartCommandsValid(t *testing.T) {
	sf := sweatfile.Sweatfile{
		StartCommands: []sweatfile.StartCommand{
			{
				Name:      "jira",
				ExecStart: []string{"echo", "hi"},
			},
		},
	}
	if issues := CheckStartCommands(sf); len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

func TestCheckStartCommandsMissingPrompt(t *testing.T) {
	sf := sweatfile.Sweatfile{
		StartCommands: []sweatfile.StartCommand{{Name: "jira"}},
	}
	issues := CheckStartCommands(sf)
	if len(issues) != 1 || issues[0].Field != "start-commands.exec-start" {
		t.Fatalf("expected 1 prompt issue, got %v", issues)
	}
}

func TestCheckStartCommandsBadName(t *testing.T) {
	sf := sweatfile.Sweatfile{
		StartCommands: []sweatfile.StartCommand{
			{Name: "Bad Name", ExecStart: []string{"echo"}},
		},
	}
	issues := CheckStartCommands(sf)
	if len(issues) != 1 || issues[0].Field != "start-commands.name" {
		t.Fatalf("expected 1 name issue, got %v", issues)
	}
}

func TestCheckStartCommandsDuplicateName(t *testing.T) {
	sf := sweatfile.Sweatfile{
		StartCommands: []sweatfile.StartCommand{
			{Name: "jira", ExecStart: []string{"echo"}},
			{Name: "jira", ExecStart: []string{"echo"}},
		},
	}
	issues := CheckStartCommands(sf)
	if len(issues) != 1 || issues[0].Field != "start-commands.name" {
		t.Fatalf("expected 1 duplicate issue, got %v", issues)
	}
}

func TestCheckStartCommandsBadRegex(t *testing.T) {
	bad := "["
	sf := sweatfile.Sweatfile{
		StartCommands: []sweatfile.StartCommand{
			{Name: "jira", ExecStart: []string{"echo"}, ArgRegex: &bad},
		},
	}
	issues := CheckStartCommands(sf)
	if len(issues) != 1 || issues[0].Field != "start-commands.arg-regex" {
		t.Fatalf("expected 1 regex issue, got %v", issues)
	}
}

func TestCheckStartCommandsShellWithoutRegexWarns(t *testing.T) {
	sf := sweatfile.Sweatfile{
		StartCommands: []sweatfile.StartCommand{
			{Name: "custom", ExecStart: []string{"sh", "-c", "echo {arg}"}},
		},
	}
	issues := CheckStartCommands(sf)
	if len(issues) != 1 {
		t.Fatalf("expected 1 warning, got %v", issues)
	}
	if issues[0].Severity != SeverityWarning {
		t.Errorf("expected warning severity, got %s", issues[0].Severity)
	}
}

func TestCheckStartCommandsShellWithRegexNoWarning(t *testing.T) {
	regex := "^[0-9]+$"
	sf := sweatfile.Sweatfile{
		StartCommands: []sweatfile.StartCommand{
			{Name: "custom", ExecStart: []string{"bash", "-c", "echo {arg}"}, ArgRegex: &regex},
		},
	}
	issues := CheckStartCommands(sf)
	if len(issues) != 0 {
		t.Errorf("expected no issues when shell has arg-regex, got %v", issues)
	}
}

func TestCheckStartCommandsNonShellNoWarning(t *testing.T) {
	sf := sweatfile.Sweatfile{
		StartCommands: []sweatfile.StartCommand{
			{Name: "custom", ExecStart: []string{"jira", "show", "{arg}"}},
		},
	}
	issues := CheckStartCommands(sf)
	if len(issues) != 0 {
		t.Errorf("expected no issues for non-shell exec-start, got %v", issues)
	}
}

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

func TestCheckHooksRejectsUnknownPreMergeOutputFormat(t *testing.T) {
	v := "nonsense"
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMergeOutputFormat: &v}}
	issues := CheckHooks(sf)
	if len(issues) != 1 || issues[0].Severity != SeverityError ||
		issues[0].Field != "hooks.pre-merge-output-format" {
		t.Fatalf("expected one error issue, got %+v", issues)
	}
}

func TestCheckHooksAcceptsKnownPreMergeOutputFormat(t *testing.T) {
	for _, val := range []string{"raw", "tap-ndjson"} {
		v := val
		sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PreMergeOutputFormat: &v}}
		if issues := CheckHooks(sf); len(issues) != 0 {
			t.Errorf("format %q produced unexpected issues: %+v", val, issues)
		}
	}
}

func TestCheckHooksAllowsUnset(t *testing.T) {
	sf := sweatfile.Sweatfile{}
	if issues := CheckHooks(sf); len(issues) != 0 {
		t.Errorf("nil Hooks produced unexpected issues: %+v", issues)
	}
}

func TestCheckHooksAllowsNilFormat(t *testing.T) {
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{}}
	if issues := CheckHooks(sf); len(issues) != 0 {
		t.Errorf("non-nil Hooks with nil PreMergeOutputFormat produced unexpected issues: %+v", issues)
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
