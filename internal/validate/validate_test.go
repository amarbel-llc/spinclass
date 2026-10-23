package validate

import (
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/sweatfile"
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

func TestCheckUnknownFieldsAcceptsRequireHookScope(t *testing.T) {
	issues := CheckUnknownFields([]byte("[hooks]\nrequire-hook-scope = true\n"))
	if len(issues) != 0 {
		t.Errorf("expected no issues for [hooks].require-hook-scope, got %v", issues)
	}
}

// TestCheckUnknownFieldsDirenvDotenv is the regression guard for #96 part 1:
// `sc validate` must NOT reject the documented [direnv.dotenv] field (the
// shipped 0.1.15 binary did, silently dropping it at parse). Both TOML
// spellings of the same map must validate clean: the sub-table header form
// and the inline-table form. The inline form was a tommy decode gap
// (amarbel-llc/tommy#106), fixed by FindChildInlineTable; this test guards
// against a regression in either spelling.
func TestCheckUnknownFieldsDirenvDotenv(t *testing.T) {
	cases := map[string]string{
		"subtable": `
[direnv]
envrc = ["source_up"]

[direnv.dotenv]
FOO = "$WORKTREE/bar"
ETSYWEB_PWD = "$WORKTREE"
`,
		"inline": `
[direnv]
dotenv = { FOO = "$WORKTREE/bar" }
`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			issues := CheckUnknownFields([]byte(data))
			if len(issues) != 0 {
				t.Errorf("[direnv.dotenv] %s form should validate clean, got %v", name, issues)
			}
		})
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

// post-merge-timeout degrades to the 10m default at runtime rather than to
// "off", so validation is the only place a typo is surfaced — it must be.
func TestCheckHooksRejectsUnparseablePostMergeTimeout(t *testing.T) {
	v := "ten minutes"
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PostMergeTimeout: &v}}
	issues := CheckHooks(sf)
	if len(issues) != 1 || issues[0].Severity != SeverityError ||
		issues[0].Field != "hooks.post-merge-timeout" {
		t.Fatalf("expected one error issue, got %+v", issues)
	}
}

func TestCheckHooksRejectsNegativePostMergeTimeout(t *testing.T) {
	v := "-5m"
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PostMergeTimeout: &v}}
	issues := CheckHooks(sf)
	if len(issues) != 1 || issues[0].Field != "hooks.post-merge-timeout" {
		t.Fatalf("expected one error issue, got %+v", issues)
	}
}

// "0" is the documented off switch, so it must validate clean.
func TestCheckHooksAcceptsPostMergeTimeoutValues(t *testing.T) {
	for _, val := range []string{"0", "0s", "30s", "10m", "1h"} {
		v := val
		sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{PostMergeTimeout: &v}}
		if issues := CheckHooks(sf); len(issues) != 0 {
			t.Errorf("post-merge-timeout %q produced unexpected issues: %+v", val, issues)
		}
	}
}

func TestCheckSessionEntrySpawnEntryMissingPromptPlaceholder(t *testing.T) {
	sf := sweatfile.Sweatfile{
		SessionEntry: &sweatfile.SessionEntry{
			SpawnEntry: []string{"clown"},
		},
	}
	issues := CheckSessionEntry(sf)
	if len(issues) != 1 || issues[0].Severity != SeverityWarning ||
		issues[0].Field != "session-entry.spawn-entry" {
		t.Fatalf("expected one warning issue for spawn-entry without {prompt}, got %+v", issues)
	}
}

func TestCheckSessionEntryPromptMayBeEmbedded(t *testing.T) {
	sf := sweatfile.Sweatfile{
		SessionEntry: &sweatfile.SessionEntry{
			SpawnEntry: []string{"sh", "-c", "claude '{prompt}'"},
		},
	}
	if issues := CheckSessionEntry(sf); len(issues) != 0 {
		t.Errorf("embedded {prompt} produced unexpected issues: %+v", issues)
	}
}

func TestCheckSessionEntrySpawnWindowRejectsEntryAndPrompt(t *testing.T) {
	for _, bad := range []string{"{entry}", "x {prompt} y"} {
		sf := sweatfile.Sweatfile{
			SessionEntry: &sweatfile.SessionEntry{
				SpawnWindow: []string{"kitty", bad, "{id}"},
			},
		}
		issues := CheckSessionEntry(sf)
		if len(issues) != 1 || issues[0].Severity != SeverityError ||
			issues[0].Field != "session-entry.spawn-window" {
			t.Fatalf("%s: expected one error issue, got %+v", bad, issues)
		}
	}
}

func TestCheckSessionEntrySpawnWindowWarnsWithoutIDOrDir(t *testing.T) {
	sf := sweatfile.Sweatfile{
		SessionEntry: &sweatfile.SessionEntry{
			SpawnWindow: []string{"kitty", "--detach"},
		},
	}
	issues := CheckSessionEntry(sf)
	if len(issues) != 1 || issues[0].Severity != SeverityWarning ||
		issues[0].Field != "session-entry.spawn-window" {
		t.Fatalf("expected one warning issue, got %+v", issues)
	}
}

func TestCheckSessionEntrySpawnWindowClean(t *testing.T) {
	sf := sweatfile.Sweatfile{
		SessionEntry: &sweatfile.SessionEntry{
			SpawnWindow: []string{"sc-spawn-window", "{id}", "{dir}"},
		},
	}
	if issues := CheckSessionEntry(sf); len(issues) != 0 {
		t.Errorf("valid spawn-window produced unexpected issues: %+v", issues)
	}
}

func TestCheckSessionEntryClean(t *testing.T) {
	sf := sweatfile.Sweatfile{
		SessionEntry: &sweatfile.SessionEntry{
			SpawnEntry:  []string{"clown", "--clown-attach=spawn", "--", "{prompt}"},
			SpawnWindow: []string{"sc-spawn-window", "{id}", "{dir}"},
		},
	}
	if issues := CheckSessionEntry(sf); len(issues) != 0 {
		t.Errorf("valid spawn config produced unexpected issues: %+v", issues)
	}
}

func TestCheckSessionEntryAllowsUnset(t *testing.T) {
	for _, sf := range []sweatfile.Sweatfile{
		{},
		{SessionEntry: &sweatfile.SessionEntry{}},
	} {
		if issues := CheckSessionEntry(sf); len(issues) != 0 {
			t.Errorf("unset spawn fields produced unexpected issues: %+v", issues)
		}
	}
}

func TestCheckRemotesValid(t *testing.T) {
	sf := sweatfile.Sweatfile{
		Remotes: []sweatfile.Remote{
			{Name: "devbox", SSH: "sasha@devbox.lan"},
		},
	}
	issues := CheckRemotes(sf)
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

// A remote name or ssh destination beginning with "-" would reach ssh as an
// option (-oProxyCommand runs a local command): an error.
func TestCheckRemotesRefusesAnOptionLikeDestination(t *testing.T) {
	for _, r := range []sweatfile.Remote{
		{Name: "devbox", SSH: "-oProxyCommand=evil"},
		{Name: "-oProxyCommand=evil"},
	} {
		issues := CheckRemotes(sweatfile.Sweatfile{Remotes: []sweatfile.Remote{r}})
		found := false
		for _, is := range issues {
			if is.Severity == SeverityError && strings.Contains(is.Message, `begins with "-"`) {
				found = true
			}
		}
		if !found {
			t.Errorf("remote %+v: want an error naming the leading \"-\", got %v", r, issues)
		}
	}
}

func TestCheckRemotesMissingName(t *testing.T) {
	sf := sweatfile.Sweatfile{
		Remotes: []sweatfile.Remote{
			{SSH: "sasha@devbox.lan"},
		},
	}
	issues := CheckRemotes(sf)
	if len(issues) != 1 || issues[0].Field != "remotes.name" {
		t.Fatalf("expected 1 name issue, got %v", issues)
	}
}

func TestCheckRemotesBadName(t *testing.T) {
	for _, name := range []string{"bad:name", "bad/name"} {
		sf := sweatfile.Sweatfile{
			Remotes: []sweatfile.Remote{
				{Name: name, SSH: "sasha@devbox.lan"},
			},
		}
		issues := CheckRemotes(sf)
		if len(issues) != 1 || issues[0].Severity != SeverityError ||
			issues[0].Field != "remotes.name" {
			t.Fatalf("name %q: expected 1 name error, got %v", name, issues)
		}
	}
}

func TestCheckRemotesDuplicateName(t *testing.T) {
	sf := sweatfile.Sweatfile{
		Remotes: []sweatfile.Remote{
			{Name: "devbox", SSH: "a@devbox"},
			{Name: "devbox", SSH: "b@devbox"},
		},
	}
	issues := CheckRemotes(sf)
	if len(issues) != 1 || issues[0].Severity != SeverityWarning ||
		issues[0].Field != "remotes.name" {
		t.Fatalf("expected 1 duplicate warning, got %v", issues)
	}
}

func TestCheckRemotesEmptyAttachElement(t *testing.T) {
	sf := sweatfile.Sweatfile{
		Remotes: []sweatfile.Remote{
			{Name: "devbox", Attach: []string{"ssh", "", "{id}"}},
		},
	}
	issues := CheckRemotes(sf)
	if len(issues) != 1 || issues[0].Severity != SeverityError ||
		issues[0].Field != "remotes.attach" {
		t.Fatalf("expected 1 attach error, got %v", issues)
	}
}

func TestCheckRemotesNameOnlyValid(t *testing.T) {
	// Name-only entries are all-defaults remotes (~/.ssh/config does the
	// work), not removal sentinels — they must validate clean.
	sf := sweatfile.Sweatfile{
		Remotes: []sweatfile.Remote{
			{Name: "devbox"},
		},
	}
	if issues := CheckRemotes(sf); len(issues) != 0 {
		t.Errorf("expected no issues for name-only remote, got %v", issues)
	}
}

func TestCheckRemotesRemoveWithFieldsWarns(t *testing.T) {
	for _, sf := range []sweatfile.Sweatfile{
		{Remotes: []sweatfile.Remote{
			{Name: "devbox", Remove: true, SSH: "sasha@devbox.lan"},
		}},
		{Remotes: []sweatfile.Remote{
			{Name: "devbox", Remove: true, Attach: []string{"ssh", "{ssh}"}},
		}},
	} {
		issues := CheckRemotes(sf)
		if len(issues) != 1 || issues[0].Severity != SeverityWarning ||
			issues[0].Field != "remotes.remove" {
			t.Fatalf("expected 1 remove warning, got %v", issues)
		}
	}
}

func TestCheckRemotesBareRemoveOK(t *testing.T) {
	sf := sweatfile.Sweatfile{
		Remotes: []sweatfile.Remote{
			{Name: "devbox", Remove: true},
		},
	}
	if issues := CheckRemotes(sf); len(issues) != 0 {
		t.Errorf("expected no issues for bare remove sentinel, got %v", issues)
	}
}

func TestCheckRemotesEmptyAttachArrayOK(t *testing.T) {
	// An absent/empty attach array means "use the default template" —
	// only present-but-empty elements are rejected.
	sf := sweatfile.Sweatfile{
		Remotes: []sweatfile.Remote{
			{Name: "devbox", SSH: "sasha@devbox.lan", Attach: []string{}},
		},
	}
	if issues := CheckRemotes(sf); len(issues) != 0 {
		t.Errorf("expected no issues for empty attach array, got %v", issues)
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

func TestCheckAuthWarnsRepoLayerURLResolver(t *testing.T) {
	r := "smith repo resolve {origin}"
	sf := sweatfile.Sweatfile{Auth: &sweatfile.Auth{URLResolver: &r}}
	has := func(issues []Issue) bool {
		for _, iss := range issues {
			if iss.Field == "auth.url-resolver" && iss.Severity == SeverityWarning {
				return true
			}
		}
		return false
	}
	if !has(CheckAuth(sf, false)) {
		t.Error("repo-layer url-resolver should warn")
	}
	if has(CheckAuth(sf, true)) {
		t.Error("above-repo url-resolver should not warn")
	}
}

func TestCheckProfileRightsWarnsUnlessGlobal(t *testing.T) {
	on := true
	sf := sweatfile.Sweatfile{ProfileRights: &sweatfile.ProfileRights{Enforce: &on}}
	warns := func(issues []Issue) bool {
		for _, iss := range issues {
			if iss.Field == "profile-rights.enforce" && iss.Severity == SeverityWarning {
				return true
			}
		}
		return false
	}
	if !warns(CheckProfileRights(sf, false, false)) {
		t.Error("repo-layer enforce should warn")
	}
	if !warns(CheckProfileRights(sf, true, false)) {
		t.Error("non-global above-repo enforce should warn")
	}
	if warns(CheckProfileRights(sf, true, true)) {
		t.Error("global enforce should not warn")
	}
	if warns(CheckProfileRights(sweatfile.Sweatfile{}, false, false)) {
		t.Error("absent table should not warn")
	}
}

func TestCheckConfinementWarnsUnlessGlobal(t *testing.T) {
	on := true
	sf := sweatfile.Sweatfile{Confinement: &sweatfile.Confinement{LaunchUnit: &on}}
	if len(CheckConfinement(sf, false, false)) != 1 {
		t.Error("repo-layer launch-unit should warn once (ignored)")
	}
	// launch-unit is deprecated everywhere it is honoured, plus the sidestep
	// warning outside the global layer.
	if len(CheckConfinement(sf, true, false)) != 2 {
		t.Error("non-global above-repo launch-unit should warn (deprecated + sidestep)")
	}
	if got := CheckConfinement(sf, true, true); len(got) != 1 || got[0].Field != "confinement.launch-unit" ||
		!strings.Contains(got[0].Message, "deprecated") {
		t.Errorf("global launch-unit should warn only that it is deprecated, got %+v", got)
	}
	enabled := sweatfile.Sweatfile{Confinement: &sweatfile.Confinement{Enabled: &on, DefaultSpawn: &on}}
	if len(CheckConfinement(enabled, true, true)) != 0 {
		t.Error("global enabled/default-spawn should not warn")
	}
	if len(CheckConfinement(enabled, true, false)) != 2 {
		t.Error("non-global above-repo enabled/default-spawn should each warn (sidestep)")
	}
	if len(CheckConfinement(enabled, false, false)) != 1 {
		t.Error("repo-layer enabled/default-spawn should warn once (ignored)")
	}
	egress := sweatfile.Sweatfile{Confinement: &sweatfile.Confinement{EgressAllow: []string{"a:1"}}}
	if len(CheckConfinement(egress, true, false)) != 0 {
		t.Error("above-repo egress-allow should not warn")
	}
	if len(CheckConfinement(sweatfile.Sweatfile{}, false, false)) != 0 {
		t.Error("absent table should not warn")
	}
}

func TestCheckPermissionsWarnsInRepoLayer(t *testing.T) {
	on := true
	sf := sweatfile.Sweatfile{Permissions: &sweatfile.Permissions{DenySweatfileEdits: &on}}
	if len(CheckPermissions(sf, false)) != 1 {
		t.Error("repo-layer deny-sweatfile-edits should warn (ignored)")
	}
	if len(CheckPermissions(sf, true)) != 0 {
		t.Error("above-repo deny-sweatfile-edits should not warn")
	}
	if len(CheckPermissions(sweatfile.Sweatfile{}, false)) != 0 {
		t.Error("absent table should not warn")
	}

	sc := sweatfile.Sweatfile{Permissions: &sweatfile.Permissions{AllowSelfClose: &on}}
	if got := CheckPermissions(sc, false); len(got) != 1 || got[0].Field != "permissions.allow-self-close" {
		t.Errorf("repo-layer allow-self-close should warn once, got %+v", got)
	}
	if len(CheckPermissions(sc, true)) != 0 {
		t.Error("above-repo allow-self-close should not warn")
	}
}

func TestCheckStoreWarnsInRepoLayer(t *testing.T) {
	name := "A"
	sf := sweatfile.Sweatfile{Store: &sweatfile.Store{AuthorName: &name}}
	if len(CheckStore(sf, false)) != 1 {
		t.Error("repo-layer [store] identity should warn (ignored)")
	}
	if len(CheckStore(sf, true)) != 0 {
		t.Error("above-repo [store] identity should not warn")
	}
	if len(CheckStore(sweatfile.Sweatfile{Store: &sweatfile.Store{}}, false)) != 0 {
		t.Error("an empty table should not warn")
	}
	if len(CheckStore(sweatfile.Sweatfile{}, false)) != 0 {
		t.Error("absent table should not warn")
	}
}

func TestCheckMetricsWarnsOnMalformedStatsdAddress(t *testing.T) {
	check := func(addr string) []Issue {
		return CheckMetrics(sweatfile.Sweatfile{Metrics: &sweatfile.Metrics{StatsdAddress: &addr}})
	}
	for _, bad := range []string{"udp://stats.example.com:8125", "stats host", "stats:0", "stats:65536", "stats:http", ":8125"} {
		issues := check(bad)
		if len(issues) != 1 || issues[0].Severity != SeverityWarning || issues[0].Field != "metrics.statsd-address" || issues[0].Value != bad {
			t.Errorf("CheckMetrics(%q) = %+v; want one metrics.statsd-address warning", bad, issues)
		}
	}
	for _, good := range []string{"stats.example.com", "stats.example.com:8125", "10.0.0.7", "10.0.0.7:9000", "::1", "[::1]:8125"} {
		if issues := check(good); len(issues) != 0 {
			t.Errorf("CheckMetrics(%q) = %+v; want none", good, issues)
		}
	}
}

func TestCheckMetricsAllowsUnset(t *testing.T) {
	if issues := CheckMetrics(sweatfile.Sweatfile{}); len(issues) != 0 {
		t.Errorf("no [metrics]: got %+v", issues)
	}
	disabled := true
	if issues := CheckMetrics(sweatfile.Sweatfile{Metrics: &sweatfile.Metrics{DisableStatsd: &disabled}}); len(issues) != 0 {
		t.Errorf("disable-statsd only: got %+v", issues)
	}
}

func TestCheckPostMergeTargetsValid(t *testing.T) {
	verify := "check-health"
	sf := sweatfile.Sweatfile{
		PostMerge: []sweatfile.PostMergeTarget{
			{Name: "krone", Command: "deploy", Verify: &verify},
			{Name: "nikulin", Command: "deploy2"},
			{Name: "stale"}, // name-only removal sentinel is valid
		},
	}
	if issues := CheckPostMergeTargets(sf); len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

func TestCheckPostMergeTargetsMissingName(t *testing.T) {
	sf := sweatfile.Sweatfile{
		PostMerge: []sweatfile.PostMergeTarget{
			{Command: "deploy"},
		},
	}
	issues := CheckPostMergeTargets(sf)
	if len(issues) != 1 || issues[0].Severity != SeverityError || issues[0].Field != "post-merge.name" {
		t.Fatalf("expected 1 name error, got %v", issues)
	}
}

func TestCheckPostMergeTargetsDuplicateName(t *testing.T) {
	sf := sweatfile.Sweatfile{
		PostMerge: []sweatfile.PostMergeTarget{
			{Name: "krone", Command: "deploy"},
			{Name: "krone", Command: "deploy2"},
		},
	}
	issues := CheckPostMergeTargets(sf)
	if len(issues) != 1 || issues[0].Severity != SeverityWarning || issues[0].Field != "post-merge.name" {
		t.Fatalf("expected 1 duplicate warning, got %v", issues)
	}
}

func TestCheckPostMergeTargetsVerifyWithoutCommandWarns(t *testing.T) {
	verify := "check"
	sf := sweatfile.Sweatfile{
		PostMerge: []sweatfile.PostMergeTarget{
			{Name: "krone", Verify: &verify}, // sentinel with a verify — confused
		},
	}
	issues := CheckPostMergeTargets(sf)
	if len(issues) != 1 || issues[0].Severity != SeverityWarning || issues[0].Field != "post-merge.verify" {
		t.Fatalf("expected 1 verify-without-command warning, got %v", issues)
	}
}
