package validate

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/folio"
	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/0/statsd"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/0/tap"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

var startCommandNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// RunString executes the same logic as Run but captures output into a string,
// returning (output, exitCode). Used by handlers that need to return output as
// a single value (e.g. MCP tool results) rather than streaming to a writer.
func RunString(home, repoDir string) (string, int) {
	var buf bytes.Buffer
	exitCode := Run(&buf, home, repoDir)
	return buf.String(), exitCode
}

const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

type Issue struct {
	Message  string
	Severity string
	Field    string
	Value    string
}

var KnownTools = []string{
	"Bash", "Read", "Write", "Edit", "Glob", "Grep",
	"WebFetch", "WebSearch", "NotebookEdit", "Task", "Skill", "LSP",
}

func isKnownTool(name string) bool {
	for _, t := range KnownTools {
		if t == name {
			return true
		}
	}
	return strings.HasPrefix(name, "mcp__")
}

func parseRuleSyntax(rule string) (string, error) {
	if rule == "" {
		return "", fmt.Errorf("empty rule")
	}

	parenIdx := strings.Index(rule, "(")
	if parenIdx < 0 {
		return rule, nil
	}

	if !strings.HasSuffix(rule, ")") {
		return "", fmt.Errorf("unmatched parenthesis in rule %q", rule)
	}

	toolName := rule[:parenIdx]
	if toolName == "" {
		return "", fmt.Errorf("empty tool name in rule %q", rule)
	}

	return toolName, nil
}

func CheckClaudeAllow(sf sweatfile.Sweatfile) []Issue {
	var issues []Issue
	if sf.Claude == nil {
		return issues
	}
	for _, rule := range sf.Claude.Allow {
		toolName, err := parseRuleSyntax(rule)
		if err != nil {
			issues = append(issues, Issue{
				Message:  err.Error(),
				Severity: SeverityError,
				Field:    "claude-allow",
				Value:    rule,
			})
			continue
		}
		if !isKnownTool(toolName) {
			issues = append(issues, Issue{
				Message:  fmt.Sprintf("unknown tool name %q", toolName),
				Severity: SeverityWarning,
				Field:    "claude-allow",
				Value:    rule,
			})
		}
	}
	return issues
}

func CheckStartCommands(sf sweatfile.Sweatfile) []Issue {
	var issues []Issue
	seen := make(map[string]bool, len(sf.StartCommands))
	for _, sc := range sf.StartCommands {
		if sc.Name == "" {
			issues = append(issues, Issue{
				Message:  "start-command entry missing `name`",
				Severity: SeverityError,
				Field:    "start-commands.name",
			})
			continue
		}
		if !startCommandNameRE.MatchString(sc.Name) {
			issues = append(issues, Issue{
				Message: fmt.Sprintf(
					"invalid start-command name %q (must match %s)",
					sc.Name, startCommandNameRE.String(),
				),
				Severity: SeverityError,
				Field:    "start-commands.name",
				Value:    sc.Name,
			})
		}
		if seen[sc.Name] {
			issues = append(issues, Issue{
				Message:  fmt.Sprintf("duplicate start-command %q in this file", sc.Name),
				Severity: SeverityError,
				Field:    "start-commands.name",
				Value:    sc.Name,
			})
		}
		seen[sc.Name] = true
		if len(sc.ExecStart) == 0 {
			issues = append(issues, Issue{
				Message:  fmt.Sprintf("start-command %q requires a non-empty `exec-start`", sc.Name),
				Severity: SeverityError,
				Field:    "start-commands.exec-start",
				Value:    sc.Name,
			})
		}
		if sc.ArgRegex != nil && *sc.ArgRegex != "" {
			if _, err := regexp.Compile(*sc.ArgRegex); err != nil {
				issues = append(issues, Issue{
					Message: fmt.Sprintf(
						"start-command %q has invalid arg-regex: %s",
						sc.Name, err,
					),
					Severity: SeverityError,
					Field:    "start-commands.arg-regex",
					Value:    *sc.ArgRegex,
				})
			}
		}
		if len(sc.ExecStart) > 0 && isShellInterpreter(sc.ExecStart[0]) &&
			(sc.ArgRegex == nil || *sc.ArgRegex == "") {
			issues = append(issues, Issue{
				Message: fmt.Sprintf(
					"start-command %q uses a shell interpreter without arg-regex; "+
						"consider adding arg-regex to validate input",
					sc.Name,
				),
				Severity: SeverityWarning,
				Field:    "start-commands.exec-start",
				Value:    sc.Name,
			})
		}
	}
	return issues
}

var validPreMergeOutputFormats = map[string]bool{
	"raw":         true,
	"tap-ndjson":  true,
	"ndjson-crap": true,
}

// CheckHooks validates fields in the [hooks] table: the
// pre-merge-output-format enum and the inactivity-timeout /
// post-merge-timeout / repair-timeout durations. Extend here as more constrained hook fields
// land.
func CheckHooks(sf sweatfile.Sweatfile) []Issue {
	var issues []Issue
	if sf.Hooks == nil {
		return issues
	}
	if sf.Hooks.PreMergeOutputFormat != nil {
		v := *sf.Hooks.PreMergeOutputFormat
		if v != "" && !validPreMergeOutputFormats[v] {
			issues = append(issues, Issue{
				Message:  fmt.Sprintf("unknown pre-merge-output-format %q (valid: raw, tap-ndjson, ndjson-crap)", v),
				Severity: SeverityError,
				Field:    "hooks.pre-merge-output-format",
				Value:    v,
			})
		}
	}
	if sf.Hooks.InactivityTimeout != nil {
		v := *sf.Hooks.InactivityTimeout
		if v != "" {
			if d, err := time.ParseDuration(v); err != nil {
				issues = append(issues, Issue{
					Message:  fmt.Sprintf("invalid inactivity-timeout %q (want a Go duration like \"180s\" or \"3m\"): %s", v, err),
					Severity: SeverityError,
					Field:    "hooks.inactivity-timeout",
					Value:    v,
				})
			} else if d < 0 {
				issues = append(issues, Issue{
					Message:  fmt.Sprintf("inactivity-timeout %q must not be negative", v),
					Severity: SeverityError,
					Field:    "hooks.inactivity-timeout",
					Value:    v,
				})
			}
		}
	}
	// post-merge-timeout: same duration shape as inactivity-timeout, but a bad
	// value degrades to the 10m DEFAULT rather than to "off" at runtime (see
	// sweatfile.PostMergeTimeoutValue), so this check is what surfaces the typo.
	if sf.Hooks.PostMergeTimeout != nil {
		v := *sf.Hooks.PostMergeTimeout
		if v != "" {
			// Same rule the per-merge override uses (sweatfile.ParsePostMergeTimeout),
			// so a value `sc validate` accepts is a value a merge call accepts.
			if _, err := sweatfile.ParsePostMergeTimeout(v); err != nil {
				issues = append(issues, Issue{
					Message:  err.Error(),
					Severity: SeverityError,
					Field:    "hooks.post-merge-timeout",
					Value:    v,
				})
			}
		}
	}
	// repair-timeout degrades to its default the same way.
	if sf.Hooks.RepairTimeout != nil {
		if v := *sf.Hooks.RepairTimeout; v != "" {
			if _, err := sweatfile.ParseRepairTimeout(v); err != nil {
				issues = append(issues, Issue{
					Message:  err.Error(),
					Severity: SeverityError,
					Field:    "hooks.repair-timeout",
					Value:    v,
				})
			}
		}
	}
	return issues
}

// CheckSessionEntry validates the spawn-related fields in the [session-entry]
// table. `spawn-entry` is exec'd directly as the detached harness (FDR-0017
// Piece 1: spinclass no longer wraps in a multiplexer); without "{prompt}"
// anywhere it is suspicious (the worker would boot without the driver's brief)
// but legal, so it only warns. `spawn-window` is a leaf argv: {entry}/{prompt}
// in it is an error, and no {id}/{dir}/{attach-id} anywhere warns (the window
// command could not identify its session). {attach-id} resolves to the worker's
// posh session id from the hello, for a `posh attach {attach-id}` reattach
// window (direction B). See FDR 0006 / FDR 0017.
func CheckSessionEntry(sf sweatfile.Sweatfile) []Issue {
	var issues []Issue
	if sf.SessionEntry == nil {
		return issues
	}
	if len(sf.SessionEntry.SpawnWindow) > 0 {
		badPlaceholder := false
		identified := false
		for _, el := range sf.SessionEntry.SpawnWindow {
			if strings.Contains(el, "{entry}") || strings.Contains(el, "{prompt}") {
				badPlaceholder = true
			}
			if strings.Contains(el, "{id}") || strings.Contains(el, "{dir}") || strings.Contains(el, "{attach-id}") {
				identified = true
			}
		}
		if badPlaceholder {
			issues = append(issues, Issue{
				Message:  `spawn-window must not reference {entry} or {prompt}: it is a leaf argv taking {id}/{dir} only (see FDR 0006)`,
				Severity: SeverityError,
				Field:    "session-entry.spawn-window",
				Value:    strings.Join(sf.SessionEntry.SpawnWindow, " "),
			})
		} else if !identified {
			issues = append(issues, Issue{
				Message:  `spawn-window has no element containing "{id}", "{dir}", or "{attach-id}"; the window command cannot identify which session to open`,
				Severity: SeverityWarning,
				Field:    "session-entry.spawn-window",
				Value:    strings.Join(sf.SessionEntry.SpawnWindow, " "),
			})
		}
	}
	// A confined resume drops only the exact `direnv exec .` prefix from the
	// pinned resume entry (spinclass#462), the untemplated twin of the
	// spawn-entry's `direnv exec {dir}`.
	if r := sf.SessionEntry.Resume; len(r) > 0 && r[0] == "direnv" && !(len(r) > 3 && r[1] == "exec" && r[2] == ".") {
		issues = append(issues, Issue{
			Message:  "resume starts with direnv in a shape a confined resume cannot strip (only the exact `direnv exec .` prefix is dropped); a once-confined session's harness would resume under direnv",
			Severity: SeverityWarning,
			Field:    "session-entry.resume",
			Value:    strings.Join(r, " "),
		})
	}
	if len(sf.SessionEntry.SpawnEntry) > 0 {
		hasPrompt := false
		for _, el := range sf.SessionEntry.SpawnEntry {
			if strings.Contains(el, "{prompt}") {
				hasPrompt = true
				break
			}
		}
		if !hasPrompt {
			issues = append(issues, Issue{
				Message:  `spawn-entry has no element containing "{prompt}"; the spawned harness will not receive the driver's brief`,
				Severity: SeverityWarning,
				Field:    "session-entry.spawn-entry",
				Value:    strings.Join(sf.SessionEntry.SpawnEntry, " "),
			})
		}
		// A confined spawn drops only the exact `direnv exec {dir}` prefix
		// (spawn.stripDirenvExec, spinclass#462); any other direnv shape would
		// put a confined worker's harness under direnv after all.
		e := sf.SessionEntry.SpawnEntry
		if e[0] == "direnv" && !(len(e) > 3 && e[1] == "exec" && e[2] == "{dir}") {
			issues = append(issues, Issue{
				Message:  "spawn-entry starts with direnv in a shape a confined spawn cannot strip (only the exact `direnv exec {dir}` prefix is dropped); a confined worker's harness would run under direnv",
				Severity: SeverityWarning,
				Field:    "session-entry.spawn-entry",
				Value:    strings.Join(sf.SessionEntry.SpawnEntry, " "),
			})
		}
	}
	return issues
}

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

func CheckPreMergeSkills(sf sweatfile.Sweatfile) []Issue {
	var issues []Issue
	seen := make(map[string]bool, len(sf.PreMergeSkills))
	for _, s := range sf.PreMergeSkills {
		if s.Name == "" {
			issues = append(issues, Issue{
				Message:  "pre-merge-skills entry missing `name`",
				Severity: SeverityError,
				Field:    "pre-merge-skills.name",
			})
			continue
		}
		if seen[s.Name] {
			issues = append(issues, Issue{
				Message:  fmt.Sprintf("duplicate pre-merge-skills entry %q in this file", s.Name),
				Severity: SeverityWarning,
				Field:    "pre-merge-skills.name",
				Value:    s.Name,
			})
		}
		seen[s.Name] = true
	}
	return issues
}

func CheckPreMergeExemptions(sf sweatfile.Sweatfile) []Issue {
	var issues []Issue
	seen := make(map[string]bool, len(sf.PreMergeExemptions))
	for _, e := range sf.PreMergeExemptions {
		if e.Name == "" {
			issues = append(issues, Issue{
				Message:  "pre-merge-exemptions entry missing `name`",
				Severity: SeverityError,
				Field:    "pre-merge-exemptions.name",
			})
			continue
		}
		if seen[e.Name] {
			issues = append(issues, Issue{
				Message:  fmt.Sprintf("duplicate pre-merge-exemptions entry %q in this file", e.Name),
				Severity: SeverityWarning,
				Field:    "pre-merge-exemptions.name",
				Value:    e.Name,
			})
		}
		seen[e.Name] = true
	}
	return issues
}

func CheckRemotes(sf sweatfile.Sweatfile) []Issue {
	var issues []Issue
	seen := make(map[string]bool, len(sf.Remotes))
	for _, r := range sf.Remotes {
		if r.Name == "" {
			issues = append(issues, Issue{
				Message:  "remotes entry missing `name`",
				Severity: SeverityError,
				Field:    "remotes.name",
			})
			continue
		}
		if strings.ContainsAny(r.Name, ":/") {
			issues = append(issues, Issue{
				Message:  fmt.Sprintf("invalid remote name %q (must not contain ':' or '/')", r.Name),
				Severity: SeverityError,
				Field:    "remotes.name",
				Value:    r.Name,
			})
		}
		if seen[r.Name] {
			issues = append(issues, Issue{
				Message:  fmt.Sprintf("duplicate remotes entry %q in this file", r.Name),
				Severity: SeverityWarning,
				Field:    "remotes.name",
				Value:    r.Name,
			})
		}
		seen[r.Name] = true
		if dest := r.Dest(); !r.Remove && strings.HasPrefix(dest, "-") {
			issues = append(issues, Issue{
				Message:  fmt.Sprintf("remote %q: ssh destination %q begins with \"-\", which ssh would read as an option (spinclass refuses to run it)", r.Name, dest),
				Severity: SeverityError,
				Field:    "remotes.ssh",
				Value:    r.Name,
			})
		}
		if r.Remove && (r.SSH != "" || len(r.Attach) > 0) {
			issues = append(issues, Issue{
				Message:  fmt.Sprintf("remote %q sets `remove = true` alongside ssh/attach fields (the fields are ignored; intent is unclear)", r.Name),
				Severity: SeverityWarning,
				Field:    "remotes.remove",
				Value:    r.Name,
			})
		}
		for _, el := range r.Attach {
			if el == "" {
				issues = append(issues, Issue{
					Message:  fmt.Sprintf("remote %q has an empty element in `attach`", r.Name),
					Severity: SeverityError,
					Field:    "remotes.attach",
					Value:    r.Name,
				})
				break
			}
		}
	}
	return issues
}

// CheckPostMergeTargets validates the top-level [[post-merge]] array (FDR
// 0026): every entry needs a name, duplicate names in one file warn, and a
// verify on a command-less entry (a removal sentinel) is confused intent.
func CheckPostMergeTargets(sf sweatfile.Sweatfile) []Issue {
	var issues []Issue
	seen := make(map[string]bool, len(sf.PostMerge))
	for _, t := range sf.PostMerge {
		if t.Name == "" {
			issues = append(issues, Issue{
				Message:  "post-merge entry missing `name`",
				Severity: SeverityError,
				Field:    "post-merge.name",
			})
			continue
		}
		if seen[t.Name] {
			issues = append(issues, Issue{
				Message:  fmt.Sprintf("duplicate post-merge entry %q in this file", t.Name),
				Severity: SeverityWarning,
				Field:    "post-merge.name",
				Value:    t.Name,
			})
		}
		seen[t.Name] = true
		// A command-less entry is a removal sentinel; a verify never runs on it
		// (verify runs only after a successful command), so it is confused intent.
		if strings.TrimSpace(t.Command) == "" && t.HasVerify() {
			issues = append(issues, Issue{
				Message: fmt.Sprintf(
					"post-merge target %q sets `verify` with no `command` (verify runs only after a successful command; a command-less entry is a removal sentinel)",
					t.Name,
				),
				Severity: SeverityWarning,
				Field:    "post-merge.verify",
				Value:    t.Name,
			})
		}
	}
	return issues
}

// CheckMetrics warns about a [metrics].statsd-address that statsd.Resolve would
// reject, using the same statsd.ParseAddress: at runtime such an address turns
// emission off, so the warning says that rather than leaving it a silent no-op.
func CheckMetrics(sf sweatfile.Sweatfile) []Issue {
	addr := sf.MetricsStatsdAddress()
	if addr == "" {
		return nil
	}
	if _, err := statsd.ParseAddress(addr); err != nil {
		return []Issue{{
			Message:  fmt.Sprintf("[metrics] statsd-address %q is malformed (%v): metrics emission is disabled until it is fixed", addr, err),
			Severity: SeverityWarning,
			Field:    "metrics.statsd-address",
			Value:    addr,
		}}
	}
	return nil
}

// CheckAuth validates the [auth] table (FDR 0028): a mint-command without a
// revoke-command mints tokens nothing will ever revoke, and a revoke-command
// alone never runs — both are almost certainly a half-written config.
//
// layerAboveRepo says whether sf came from a layer above the repo; a
// url-resolver anywhere else is ignored (#335) and warned about.
func CheckAuth(sf sweatfile.Sweatfile, layerAboveRepo bool) []Issue {
	if sf.Auth == nil {
		return nil
	}
	set := func(p *string) bool { return p != nil && strings.TrimSpace(*p) != "" }
	mint, revoke := set(sf.Auth.MintCommand), set(sf.Auth.RevokeCommand)
	var issues []Issue
	switch {
	case mint && !revoke:
		issues = append(issues, Issue{
			Message:  "[auth] sets `mint-command` without `revoke-command`: minted tokens would never be revoked at close (only the issuer's own sweep would reclaim them)",
			Severity: SeverityWarning,
			Field:    "auth.revoke-command",
		})
	case revoke && !mint:
		issues = append(issues, Issue{
			Message:  "[auth] sets `revoke-command` without `mint-command`: nothing is minted, so it never runs",
			Severity: SeverityWarning,
			Field:    "auth.mint-command",
		})
	}
	// A mint-command with no host allow-list mints for EVERY origin host the
	// entry reaches — fine for one forge repo, a session-creation failure for
	// any GitHub-origin repo under a shared (parent-dir) sweatfile.
	if mint && len(sf.Auth.ForgeHosts) == 0 {
		issues = append(issues, Issue{
			Message:  "[auth] sets `mint-command` without `forge-hosts`: every origin host this entry reaches will try to mint; set `forge-hosts = [...]` so repos on other hosts (GitHub) skip the mint instead of failing session creation",
			Severity: SeverityWarning,
			Field:    "auth.forge-hosts",
		})
	}
	if sf.Auth.URLResolver != nil && !layerAboveRepo {
		issues = append(issues, Issue{
			Message:  "[auth] sets `url-resolver` in a repo-level sweatfile: it is ignored — it decides where a session's token-carrying pushes go, so only sweatfiles above the repo (global, parent directories) may set it; move it up",
			Severity: SeverityWarning,
			Field:    "auth.url-resolver",
		})
	}
	return issues
}

// CheckInheritedHooks is an informational diagnostic (#215): a repo-level
// sweatfile that leaves a scalar-override [hooks] command unset silently
// inherits the nearest ancestor layer's value, so an absent key and "no hook"
// look identical in the repo's own file. ancestors are the found layers above
// the repo, lowest precedence first. A key the repo sets explicitly (even to ""
// -- a deliberate clear, non-nil pointer) is never reported, and neither is one
// whose nearest setter in the cascade sets "" (the effective hook is empty).
func CheckInheritedHooks(sf sweatfile.Sweatfile, ancestors []sweatfile.LoadSource) []Issue {
	hookKeys := []struct {
		name string
		get  func(*sweatfile.Hooks) *string
	}{
		{"create", func(h *sweatfile.Hooks) *string { return h.Create }},
		{"stop", func(h *sweatfile.Hooks) *string { return h.Stop }},
		{"pre-merge", func(h *sweatfile.Hooks) *string { return h.PreMerge }},
		{"repair", func(h *sweatfile.Hooks) *string { return h.Repair }},
		{"on-attach", func(h *sweatfile.Hooks) *string { return h.OnAttach }},
		{"on-detach", func(h *sweatfile.Hooks) *string { return h.OnDetach }},
	}
	var issues []Issue
	for _, key := range hookKeys {
		if sf.Hooks != nil && key.get(sf.Hooks) != nil {
			continue
		}
		for i := len(ancestors) - 1; i >= 0; i-- {
			h := ancestors[i].File.Hooks
			if h == nil || key.get(h) == nil {
				continue
			}
			if *key.get(h) != "" {
				issues = append(issues, Issue{
					Message:  fmt.Sprintf("[hooks] this repo-level sweatfile does not set `%s`, so it inherits the value from %s (nearest ancestor layer that sets it); set `%s` here to override, or \"\" to clear", key.name, ancestors[i].Path, key.name),
					Severity: SeverityWarning,
					Field:    "hooks." + key.name,
				})
			}
			break
		}
	}
	return issues
}

// CheckRestartHandoff warns about a [session-entry].restart set in a layer that
// may not set it (#420): it is a command spinclass execs, so
// sweatfileio.TrustedRestartHandoff ignores it outside the global sweatfile and
// layers above the repo.
func CheckRestartHandoff(sf sweatfile.Sweatfile, layerAboveRepo bool) []Issue {
	if sf.SessionEntry == nil || sf.SessionEntry.Restart == nil || layerAboveRepo {
		return nil
	}
	return []Issue{{
		Message:  "[session-entry] sets `restart` in a repo-level sweatfile: it is ignored — it is a command spinclass runs, so only the global sweatfile or one above the repo (parent directories) may set it; move it up",
		Severity: SeverityWarning,
		Field:    "session-entry.restart",
	}}
}

// CheckUnattendedPermissionMode validates [session-entry]
// unattended-permission-mode: only "dontAsk" and "auto" are accepted (a spawn
// refuses any other value), and a repo-level value is ignored because
// sweatfileio.TrustedUnattendedPermissionMode reads it only from the global
// sweatfile and layers above the repo.
func CheckUnattendedPermissionMode(sf sweatfile.Sweatfile, layerAboveRepo bool) []Issue {
	if sf.SessionEntry == nil || sf.SessionEntry.UnattendedPermissionMode == nil || *sf.SessionEntry.UnattendedPermissionMode == "" {
		return nil
	}
	const field = "session-entry.unattended-permission-mode"
	var issues []Issue
	if mode := *sf.SessionEntry.UnattendedPermissionMode; !sweatfile.ValidUnattendedPermissionMode(mode) {
		issues = append(issues, Issue{
			Message:  fmt.Sprintf("[session-entry] `unattended-permission-mode` is %q: must be %q or %q (a spawn refuses any other value)", mode, sweatfile.UnattendedModeDontAsk, sweatfile.UnattendedModeAuto),
			Severity: SeverityError,
			Field:    field,
		})
	}
	if !layerAboveRepo {
		issues = append(issues, Issue{
			Message:  "[session-entry] sets `unattended-permission-mode` in a repo-level sweatfile: it is ignored — it decides how loosely an unconfined worker runs, so only the global sweatfile or one above the repo (parent directories) may set it; move it up",
			Severity: SeverityWarning,
			Field:    field,
		})
	}
	return issues
}

// CheckAllowPostureWidening warns about a repo-level [session-entry]
// allow-posture-widening: sweatfileio.TrustedAllowPostureWidening reads it only
// from the global sweatfile and layers above the repo, because it decides
// whether a restart may move a session from unattended to attended
// (spinclass#342).
func CheckAllowPostureWidening(sf sweatfile.Sweatfile, layerAboveRepo bool) []Issue {
	if sf.SessionEntry == nil || sf.SessionEntry.AllowPostureWidening == nil || layerAboveRepo {
		return nil
	}
	return []Issue{{
		Message:  "[session-entry] sets `allow-posture-widening` in a repo-level sweatfile: it is ignored — it decides whether a restart may widen a session from unattended to attended, so only the global sweatfile or one above the repo (parent directories) may set it; move it up",
		Severity: SeverityWarning,
		Field:    "session-entry.allow-posture-widening",
	}}
}

// CheckProfileRights validates the [profile-rights] table (FDR 0034). `enforce`
// is honoured only from layers above the repo, and is reliable only from the
// GLOBAL sweatfile: a spawner outside a non-global parent directory never
// consults that directory's layer, so an agent choosing its cwd can sidestep it.
func CheckProfileRights(sf sweatfile.Sweatfile, layerAboveRepo, global bool) []Issue {
	if sf.ProfileRights == nil || sf.ProfileRights.Enforce == nil {
		return nil
	}
	switch {
	case !layerAboveRepo:
		return []Issue{{
			Message:  "[profile-rights] sets `enforce` in a repo-level sweatfile: it is ignored — only sweatfiles above the repo may set it; move it to the global sweatfile",
			Severity: SeverityWarning,
			Field:    "profile-rights.enforce",
		}}
	case !global:
		return []Issue{{
			Message:  "[profile-rights] sets `enforce` outside the global sweatfile: a spawner whose working directory is outside this directory does not consult this layer, so enforcement can be sidestepped; set it in ~/.config/spinclass/sweatfile",
			Severity: SeverityWarning,
			Field:    "profile-rights.enforce",
		}}
	}
	return nil
}

// CheckConfinement validates the [confinement] table (FDR 0035). Both keys are
// honoured only from layers above the repo, and `launch-unit` is reliable only
// from the GLOBAL sweatfile, for the same reason as profile-rights.enforce.
func CheckConfinement(sf sweatfile.Sweatfile, layerAboveRepo, global bool) []Issue {
	c := sf.Confinement
	if c == nil || (c.Enabled == nil && c.DefaultSpawn == nil && c.LaunchUnit == nil && c.EgressAllow == nil && c.HarnessConfigWritable == nil) {
		return nil
	}
	if !layerAboveRepo {
		return []Issue{{
			Message:  "[confinement] is set in a repo-level sweatfile: it is ignored — only sweatfiles above the repo may set it; move it to the global sweatfile",
			Severity: SeverityWarning,
			Field:    "confinement",
		}}
	}
	var issues []Issue
	if c.LaunchUnit != nil {
		issues = append(issues, Issue{
			Message:  "[confinement] `launch-unit` is deprecated: it means enabled = true and default-spawn = true; set `enabled` (and `default-spawn`) instead",
			Severity: SeverityWarning,
			Field:    "confinement.launch-unit",
		})
	}
	if !global {
		for _, key := range []struct {
			name string
			set  bool
		}{
			{"enabled", c.Enabled != nil},
			{"default-spawn", c.DefaultSpawn != nil},
			{"launch-unit", c.LaunchUnit != nil},
		} {
			if key.set {
				issues = append(issues, Issue{
					Message:  fmt.Sprintf("[confinement] sets `%s` outside the global sweatfile: a spawner whose working directory is outside this directory does not consult this layer, so confinement can be sidestepped; set it in ~/.config/spinclass/sweatfile", key.name),
					Severity: SeverityWarning,
					Field:    "confinement." + key.name,
				})
			}
		}
	}
	return issues
}

// CheckPermissions validates the [permissions] table: it is honoured only from
// layers above the repo, so a repo-level value is ignored.
func CheckPermissions(sf sweatfile.Sweatfile, layerAboveRepo bool) []Issue {
	if sf.Permissions == nil || layerAboveRepo {
		return nil
	}
	var issues []Issue
	for _, key := range []struct {
		name string
		set  bool
	}{
		{"deny-sweatfile-edits", sf.Permissions.DenySweatfileEdits != nil},
		{"allow-self-close", sf.Permissions.AllowSelfClose != nil},
	} {
		if key.set {
			issues = append(issues, Issue{
				Message:  fmt.Sprintf("[permissions] sets `%s` in a repo-level sweatfile: it is ignored — only sweatfiles above the repo may set it; move it to the global sweatfile", key.name),
				Severity: SeverityWarning,
				Field:    "permissions." + key.name,
			})
		}
	}
	return issues
}

// CheckConfinedPreMerge warns about a repo-level [hooks].confined-pre-merge
// (spinclass#449): the guarded gate is read only from layers above the repo,
// because the repo's tree, sweatfile included, is what a confined worker
// controls, so a value there is ignored.
func CheckConfinedPreMerge(sf sweatfile.Sweatfile, layerAboveRepo bool) []Issue {
	if layerAboveRepo || sf.Hooks == nil || sf.Hooks.ConfinedPreMerge == nil {
		return nil
	}
	return []Issue{{
		Message:  "[hooks] sets `confined-pre-merge` in a repo-level sweatfile: it is ignored — the guarded gate is read only from sweatfiles above the repo; move it to the global sweatfile",
		Severity: SeverityWarning,
		Field:    "hooks.confined-pre-merge",
	}}
}

// CheckAllowMergeCommits warns about a repo-level [hooks].allow-merge-commits
// (#468): the switch is read only from layers above the repo, because the
// repo's tree, sweatfile included, is what the session worker writes, so a
// value there is ignored. A value above the repo covers every repo under it.
func CheckAllowMergeCommits(sf sweatfile.Sweatfile, layerAboveRepo bool) []Issue {
	if layerAboveRepo || sf.Hooks == nil || sf.Hooks.AllowMergeCommits == nil {
		return nil
	}
	return []Issue{{
		Message:  "[hooks] sets `allow-merge-commits` in a repo-level sweatfile: it is ignored — the switch is read only from sweatfiles above the repo (and then covers every repo under that layer); move it to the global sweatfile",
		Severity: SeverityWarning,
		Field:    "hooks.allow-merge-commits",
	}}
}

// CheckFolio validates the [folio] table (spinclass#438): every entry must
// parse under the stanza grammar (internal/0/folio) and unmatched must be ask
// or deny; both are errors, because an entry the broker could not compile
// makes it refuse the whole policy. The allow lists and unmatched are honoured
// only from layers above the repo, so a repo-level value warns as ignored; a
// repo-level deny only narrows and is honoured.
func CheckFolio(sf sweatfile.Sweatfile, layerAboveRepo bool) []Issue {
	f := sf.Folio
	if f == nil {
		return nil
	}
	var issues []Issue
	for _, list := range []struct {
		name    string
		entries []string
		allow   bool
	}{
		{"read", f.Read, true},
		{"write", f.Write, true},
		{"deny", f.Deny, false},
	} {
		for _, entry := range list.entries {
			pat, err := folio.ParsePattern(entry)
			if err != nil {
				issues = append(issues, Issue{
					Message:  fmt.Sprintf("[folio] %s entry does not parse: %v", list.name, err),
					Severity: SeverityError,
					Field:    "folio." + list.name,
					Value:    entry,
				})
			} else if list.name == "write" && pat.Anchor == folio.AnchorProc {
				issues = append(issues, Issue{
					Message:  "[folio] write entry uses `proc:`, which is read-only: the broker refuses the whole stanza; move it to `read`",
					Severity: SeverityError,
					Field:    "folio.write",
					Value:    entry,
				})
			}
		}
		if list.allow && list.entries != nil && !layerAboveRepo {
			issues = append(issues, Issue{
				Message:  fmt.Sprintf("[folio] sets `%s` in a repo-level sweatfile: it is ignored — only sweatfiles above the repo may widen what a session reaches; move it to the global sweatfile (a repo-level `deny` is honoured)", list.name),
				Severity: SeverityWarning,
				Field:    "folio." + list.name,
			})
		}
	}
	if f.Unmatched != nil {
		if v := *f.Unmatched; v != "ask" && v != "deny" {
			issues = append(issues, Issue{
				Message:  fmt.Sprintf("[folio] unmatched %q is not \"ask\" or \"deny\"", v),
				Severity: SeverityError,
				Field:    "folio.unmatched",
				Value:    v,
			})
		}
		if !layerAboveRepo {
			issues = append(issues, Issue{
				Message:  "[folio] sets `unmatched` in a repo-level sweatfile: it is ignored — only sweatfiles above the repo may set it; move it to the global sweatfile",
				Severity: SeverityWarning,
				Field:    "folio.unmatched",
			})
		}
	}
	return issues
}

// agentHomeSegments is the agent-home's path below the worktree, as pattern
// segments (confinement.AgentHomeOf).
var agentHomeSegments = []string{".tmp", "agent-home"}

// coversAgentHome reports whether a pwd: or spinclass:worktree entry reaches
// the whole agent-home: its segments are a prefix of .tmp/agent-home, with a
// whole-segment * matching either.
func coversAgentHome(p folio.Pattern) bool {
	if p.Anchor != folio.AnchorPwd && !(p.Anchor == folio.AnchorSpinclass && p.Arg == "worktree") {
		return false
	}
	if len(p.Segments) > len(agentHomeSegments) {
		return false
	}
	for i, s := range p.Segments {
		if s != "*" && s != agentHomeSegments[i] {
			return false
		}
	}
	return true
}

// CheckFolioConfinement warns about the two [folio] shapes whose meaning
// changes for a confined session (spinclass#443, operator ruling 2026-11-02),
// when confinement is enabled and a trusted stanza is configured.
//
// env:NAME: reads the serve's environment, not the unit's, in any list; in
// deny that is sharper, since the guard misses the unit's own value. home: is
// the unit's agent-home for a confined session, so an allow list (read or
// write) that has home: entries and no entry covering the agent-home (pwd:,
// pwd:.tmp, ... or spinclass:worktree) newly allows that subtree for confined
// sessions, which no pwd: entry did; each such list warns once.
func CheckFolioConfinement(p sweatfileio.FolioPolicy, confinementEnabled bool) []Issue {
	if !p.Configured || !confinementEnabled {
		return nil
	}
	var issues []Issue
	for _, list := range []struct {
		name    string
		entries []string
		allow   bool
	}{
		{"read", p.Read, true},
		{"write", p.Write, true},
		{"deny", p.Deny, false},
	} {
		var firstHome string
		covered := false
		for _, entry := range list.entries {
			pat, err := folio.ParsePattern(entry)
			if err != nil {
				continue // CheckFolio reports it
			}
			switch {
			case pat.Anchor == folio.AnchorEnv && list.allow:
				issues = append(issues, Issue{
					Message:  fmt.Sprintf("[folio] %s entry %s names the SERVE's environment variable %s, not the confined unit's: a confined session's own value of it is not what the entry reaches", list.name, entry, pat.Arg),
					Severity: SeverityWarning,
					Field:    "folio." + list.name,
					Value:    entry,
				})
			case pat.Anchor == folio.AnchorEnv:
				issues = append(issues, Issue{
					Message:  fmt.Sprintf("[folio] deny entry %s guards the path the SERVE's environment variable %s names; the confined unit's own value of it is NOT covered by this deny", entry, pat.Arg),
					Severity: SeverityWarning,
					Field:    "folio.deny",
					Value:    entry,
				})
			case pat.Anchor == folio.AnchorHome && list.allow && firstHome == "":
				firstHome = entry
			}
			if list.allow && coversAgentHome(pat) {
				covered = true
			}
		}
		if firstHome != "" && !covered {
			issues = append(issues, Issue{
				Message:  fmt.Sprintf("[folio] %s has home: entries (first: %s) but no pwd: entry covering the agent-home: for a confined session home: is the unit's own HOME (<worktree>/.tmp/agent-home), so these entries allow that subtree for confined sessions, which no pwd: entry does; add pwd: if that is intended, or drop the home: entries", list.name, firstHome),
				Severity: SeverityWarning,
				Field:    "folio." + list.name,
				Value:    firstHome,
			})
		}
	}
	return issues
}

// CheckHealth validates the [health] table: busy-bound must be a positive Go
// duration, and is honoured only from layers above the repo.
func CheckHealth(sf sweatfile.Sweatfile, layerAboveRepo bool) []Issue {
	if sf.Health == nil || sf.Health.BusyBound == nil {
		return nil
	}
	var issues []Issue
	if d, err := time.ParseDuration(strings.TrimSpace(*sf.Health.BusyBound)); err != nil || d <= 0 {
		issues = append(issues, Issue{
			Message:  fmt.Sprintf("[health] busy-bound %q is not a positive duration such as \"60m\": it is ignored and the default 60m applies", *sf.Health.BusyBound),
			Severity: SeverityWarning,
			Field:    "health.busy-bound",
		})
	}
	if !layerAboveRepo {
		issues = append(issues, Issue{
			Message:  "[health] sets `busy-bound` in a repo-level sweatfile: it is ignored — only sweatfiles above the repo may set it; move it to the global sweatfile",
			Severity: SeverityWarning,
			Field:    "health.busy-bound",
		})
	}
	return issues
}

// CheckStore validates the [store] table: the identity it stamps on commits is
// honoured only from layers above the repo, so a repo-level value is ignored.
func CheckStore(sf sweatfile.Sweatfile, layerAboveRepo bool) []Issue {
	s := sf.Store
	if s == nil || layerAboveRepo || (s.AuthorName == nil && s.AuthorEmail == nil && s.CommitterName == nil && s.CommitterEmail == nil) {
		return nil
	}
	return []Issue{{
		Message:  "[store] sets a commit identity in a repo-level sweatfile: it is ignored — it names who commits are attributed to, so only sweatfiles above the repo (global, parent directories) may set it; move it up",
		Severity: SeverityWarning,
		Field:    "store",
	}}
}

func isShellInterpreter(cmd string) bool {
	base := filepath.Base(cmd)
	switch base {
	case "sh", "bash", "zsh", "fish", "dash", "ksh":
		return true
	}
	return false
}

func CheckGitExcludes(sf sweatfile.Sweatfile) []Issue {
	var issues []Issue
	if sf.Git == nil {
		return issues
	}
	for _, exc := range sf.Git.Excludes {
		if exc == "" {
			issues = append(issues, Issue{
				Message:  "empty exclude pattern",
				Severity: SeverityError,
				Field:    "git-excludes",
			})
		} else if filepath.IsAbs(exc) {
			issues = append(issues, Issue{
				Message:  fmt.Sprintf("absolute path %q in git-excludes", exc),
				Severity: SeverityError,
				Field:    "git-excludes",
				Value:    exc,
			})
		}
	}
	return issues
}

func CheckMerged(sf sweatfile.Sweatfile) []Issue {
	var issues []Issue

	if len(sf.ActivePreMergeExemptions()) > 0 && len(sf.ActivePreMergeSkills()) == 0 {
		issues = append(issues, Issue{
			Message:  "pre-merge-exemptions declared but no pre-merge-skills: exemptions only apply when the attestation gate is live, so they are dead config",
			Severity: SeverityWarning,
			Field:    "pre-merge-exemptions",
		})
	}

	if sf.Git != nil {
		if dups := findDuplicates(sf.Git.Excludes); len(dups) > 0 {
			issues = append(issues, Issue{
				Message: fmt.Sprintf(
					"duplicate git excludes: %s",
					strings.Join(dups, ", "),
				),
				Severity: SeverityWarning,
				Field:    "git.excludes",
			})
		}
	}

	if sf.Claude != nil {
		if dups := findDuplicates(sf.Claude.Allow); len(dups) > 0 {
			issues = append(issues, Issue{
				Message: fmt.Sprintf(
					"duplicate claude allow: %s",
					strings.Join(dups, ", "),
				),
				Severity: SeverityWarning,
				Field:    "claude.allow",
			})
		}
	}

	return issues
}

func findDuplicates(items []string) []string {
	seen := make(map[string]bool)
	var dups []string
	for _, item := range items {
		if seen[item] {
			dups = append(dups, item)
		}
		seen[item] = true
	}
	return dups
}

func CheckUnknownFields(data []byte) []Issue {
	doc, err := sweatfileio.Parse(data)
	if err != nil {
		return nil
	}

	var issues []Issue
	for _, key := range doc.Undecoded() {
		issues = append(issues, Issue{
			Message:  fmt.Sprintf("unknown field %q", key),
			Severity: SeverityError,
			Field:    key,
		})
	}
	return issues
}

func Run(w io.Writer, home, repoDir string) int {
	tw := tap.NewWriter(w)

	result, err := sweatfileio.LoadHierarchy(home, repoDir)
	if err != nil {
		tw.NotOk("load hierarchy", map[string]string{
			"severity": SeverityError,
			"message":  err.Error(),
		})
		tw.Plan()
		return 1
	}

	// A failed detection treats repoDir as the root; from a worktree dir that
	// would misclassify the main repo's sweatfile as above the repo, which is
	// acceptable because validate is advisory.
	mainRoot, err := git.DetectRepo(repoDir)
	if err != nil {
		mainRoot = repoDir
	}

	for i, src := range result.Sources {
		if src.SkipReason != "" {
			tw.Skip(src.Path, src.SkipReason)
			continue
		}
		if !src.Found {
			tw.Skip(src.Path, "not found")
			continue
		}

		sub := tw.Subtest(src.Path)

		data, readErr := os.ReadFile(src.Path)
		if readErr != nil {
			sub.NotOk("readable", map[string]string{
				"severity": SeverityError,
				"message":  readErr.Error(),
			})
			sub.Plan()
			tw.EndSubtest(src.Path, sub)
			continue
		}

		_, parseErr := sweatfileio.Parse(data)
		if parseErr != nil {
			sub.NotOk("valid TOML", map[string]string{
				"severity": SeverityError,
				"message":  parseErr.Error(),
			})
			sub.Plan()
			tw.EndSubtest(src.Path, sub)
			continue
		}
		sub.Ok("valid TOML")

		if issues := CheckUnknownFields(data); len(issues) > 0 {
			for _, iss := range issues {
				sub.NotOk("no unknown fields", map[string]string{
					"severity": iss.Severity,
					"message":  iss.Message,
					"field":    iss.Field,
				})
			}
		} else {
			sub.Ok("no unknown fields")
		}

		if src.File.Claude != nil && len(src.File.Claude.Allow) > 0 {
			if issues := CheckClaudeAllow(src.File); len(issues) > 0 {
				for _, iss := range issues {
					if iss.Severity == SeverityError {
						diag := map[string]string{
							"severity": iss.Severity,
							"message":  iss.Message,
						}
						if iss.Value != "" {
							diag["rule"] = iss.Value
						}
						sub.NotOk("claude-allow valid", diag)
					} else {
						sub.Ok(fmt.Sprintf("claude-allow valid # warning: %s", iss.Message))
					}
				}
			} else {
				sub.Ok("claude-allow valid")
			}
		}

		if src.File.Git != nil && len(src.File.Git.Excludes) > 0 {
			if issues := CheckGitExcludes(src.File); len(issues) > 0 {
				for _, iss := range issues {
					diag := map[string]string{
						"severity": iss.Severity,
						"message":  iss.Message,
					}
					if iss.Value != "" {
						diag["value"] = iss.Value
					}
					sub.NotOk("git-excludes valid", diag)
				}
			} else {
				sub.Ok("git-excludes valid")
			}
		}

		if len(src.File.StartCommands) > 0 {
			if issues := CheckStartCommands(src.File); len(issues) > 0 {
				for _, iss := range issues {
					diag := map[string]string{
						"severity": iss.Severity,
						"message":  iss.Message,
					}
					if iss.Value != "" {
						diag["value"] = iss.Value
					}
					sub.NotOk("start-commands valid", diag)
				}
			} else {
				sub.Ok("start-commands valid")
			}
		}

		if src.File.Hooks != nil {
			if issues := CheckHooks(src.File); len(issues) > 0 {
				for _, iss := range issues {
					diag := map[string]string{
						"severity": iss.Severity,
						"message":  iss.Message,
					}
					if iss.Value != "" {
						diag["value"] = iss.Value
					}
					sub.NotOk("hooks valid", diag)
				}
			} else {
				sub.Ok("hooks valid")
			}
		}

		if !sweatfileio.LayerAboveRepo(src.Path, home, mainRoot) {
			var ancestors []sweatfile.LoadSource
			for _, prior := range result.Sources[:i] {
				if prior.Found && prior.SkipReason == "" && sweatfileio.LayerAboveRepo(prior.Path, home, mainRoot) {
					ancestors = append(ancestors, prior)
				}
			}
			for _, iss := range CheckInheritedHooks(src.File, ancestors) {
				sub.Ok(fmt.Sprintf("hooks inherited # warning: %s", iss.Message))
			}
		}

		if src.File.Auth != nil {
			if issues := CheckAuth(src.File, sweatfileio.LayerAboveRepo(src.Path, home, mainRoot)); len(issues) > 0 {
				for _, iss := range issues {
					sub.Ok(fmt.Sprintf("auth valid # warning: %s", iss.Message))
				}
			} else {
				sub.Ok("auth valid")
			}
		}

		if src.File.ProfileRights != nil {
			global := src.Path == filepath.Join(home, ".config", "spinclass", "sweatfile")
			if issues := CheckProfileRights(src.File, sweatfileio.LayerAboveRepo(src.Path, home, mainRoot), global); len(issues) > 0 {
				for _, iss := range issues {
					sub.Ok(fmt.Sprintf("profile-rights valid # warning: %s", iss.Message))
				}
			} else {
				sub.Ok("profile-rights valid")
			}
		}

		if src.File.Confinement != nil {
			global := src.Path == filepath.Join(home, ".config", "spinclass", "sweatfile")
			if issues := CheckConfinement(src.File, sweatfileio.LayerAboveRepo(src.Path, home, mainRoot), global); len(issues) > 0 {
				for _, iss := range issues {
					sub.Ok(fmt.Sprintf("confinement valid # warning: %s", iss.Message))
				}
			} else {
				sub.Ok("confinement valid")
			}
		}

		for _, iss := range CheckConfinedPreMerge(src.File, sweatfileio.LayerAboveRepo(src.Path, home, mainRoot)) {
			sub.Ok(fmt.Sprintf("hooks valid # warning: %s", iss.Message))
		}

		for _, iss := range CheckAllowMergeCommits(src.File, sweatfileio.LayerAboveRepo(src.Path, home, mainRoot)) {
			sub.Ok(fmt.Sprintf("hooks valid # warning: %s", iss.Message))
		}

		if src.File.Permissions != nil {
			if issues := CheckPermissions(src.File, sweatfileio.LayerAboveRepo(src.Path, home, mainRoot)); len(issues) > 0 {
				for _, iss := range issues {
					sub.Ok(fmt.Sprintf("permissions valid # warning: %s", iss.Message))
				}
			} else {
				sub.Ok("permissions valid")
			}
		}

		if src.File.Folio != nil {
			if issues := CheckFolio(src.File, sweatfileio.LayerAboveRepo(src.Path, home, mainRoot)); len(issues) > 0 {
				for _, iss := range issues {
					if iss.Severity == SeverityError {
						diag := map[string]string{
							"severity": iss.Severity,
							"message":  iss.Message,
						}
						if iss.Value != "" {
							diag["value"] = iss.Value
						}
						sub.NotOk("folio valid", diag)
					} else {
						sub.Ok(fmt.Sprintf("folio valid # warning: %s", iss.Message))
					}
				}
			} else {
				sub.Ok("folio valid")
			}
		}

		if src.File.Health != nil {
			if issues := CheckHealth(src.File, sweatfileio.LayerAboveRepo(src.Path, home, mainRoot)); len(issues) > 0 {
				for _, iss := range issues {
					sub.Ok(fmt.Sprintf("health valid # warning: %s", iss.Message))
				}
			} else {
				sub.Ok("health valid")
			}
		}

		if src.File.Store != nil {
			if issues := CheckStore(src.File, sweatfileio.LayerAboveRepo(src.Path, home, mainRoot)); len(issues) > 0 {
				for _, iss := range issues {
					sub.Ok(fmt.Sprintf("store valid # warning: %s", iss.Message))
				}
			} else {
				sub.Ok("store valid")
			}
		}

		if src.File.Metrics != nil {
			if issues := CheckMetrics(src.File); len(issues) > 0 {
				for _, iss := range issues {
					sub.Ok(fmt.Sprintf("metrics valid # warning: %s", iss.Message))
				}
			} else {
				sub.Ok("metrics valid")
			}
		}

		if src.File.SessionEntry != nil {
			issues := CheckSessionEntry(src.File)
			layerAbove := sweatfileio.LayerAboveRepo(src.Path, home, mainRoot)
			issues = append(issues, CheckRestartHandoff(src.File, layerAbove)...)
			issues = append(issues, CheckUnattendedPermissionMode(src.File, layerAbove)...)
			issues = append(issues, CheckAllowPostureWidening(src.File, layerAbove)...)
			if len(issues) > 0 {
				for _, iss := range issues {
					if iss.Severity == SeverityError {
						diag := map[string]string{
							"severity": iss.Severity,
							"message":  iss.Message,
						}
						if iss.Value != "" {
							diag["value"] = iss.Value
						}
						sub.NotOk("session-entry valid", diag)
					} else {
						sub.Ok(fmt.Sprintf("session-entry valid # warning: %s", iss.Message))
					}
				}
			} else {
				sub.Ok("session-entry valid")
			}
		}

		if len(src.File.MCPs) > 0 {
			if issues := CheckMCPs(src.File); len(issues) > 0 {
				for _, iss := range issues {
					if iss.Severity == SeverityError {
						diag := map[string]string{
							"severity": iss.Severity,
							"message":  iss.Message,
						}
						if iss.Value != "" {
							diag["value"] = iss.Value
						}
						sub.NotOk("mcps valid", diag)
					} else {
						sub.Ok(fmt.Sprintf("mcps valid # warning: %s", iss.Message))
					}
				}
			} else {
				sub.Ok("mcps valid")
			}
		}

		if len(src.File.Remotes) > 0 {
			if issues := CheckRemotes(src.File); len(issues) > 0 {
				for _, iss := range issues {
					if iss.Severity == SeverityError {
						diag := map[string]string{
							"severity": iss.Severity,
							"message":  iss.Message,
						}
						if iss.Value != "" {
							diag["value"] = iss.Value
						}
						sub.NotOk("remotes valid", diag)
					} else {
						sub.Ok(fmt.Sprintf("remotes valid # warning: %s", iss.Message))
					}
				}
			} else {
				sub.Ok("remotes valid")
			}
		}

		if len(src.File.PreMergeSkills) > 0 {
			if issues := CheckPreMergeSkills(src.File); len(issues) > 0 {
				for _, iss := range issues {
					if iss.Severity == SeverityError {
						diag := map[string]string{
							"severity": iss.Severity,
							"message":  iss.Message,
						}
						if iss.Value != "" {
							diag["value"] = iss.Value
						}
						sub.NotOk("pre-merge-skills valid", diag)
					} else {
						sub.Ok(fmt.Sprintf("pre-merge-skills valid # warning: %s", iss.Message))
					}
				}
			} else {
				sub.Ok("pre-merge-skills valid")
			}
		}

		if len(src.File.PreMergeExemptions) > 0 {
			if issues := CheckPreMergeExemptions(src.File); len(issues) > 0 {
				for _, iss := range issues {
					if iss.Severity == SeverityError {
						diag := map[string]string{
							"severity": iss.Severity,
							"message":  iss.Message,
						}
						if iss.Value != "" {
							diag["value"] = iss.Value
						}
						sub.NotOk("pre-merge-exemptions valid", diag)
					} else {
						sub.Ok(fmt.Sprintf("pre-merge-exemptions valid # warning: %s", iss.Message))
					}
				}
			} else {
				sub.Ok("pre-merge-exemptions valid")
			}
		}

		if len(src.File.PostMerge) > 0 {
			if issues := CheckPostMergeTargets(src.File); len(issues) > 0 {
				for _, iss := range issues {
					if iss.Severity == SeverityError {
						diag := map[string]string{
							"severity": iss.Severity,
							"message":  iss.Message,
						}
						if iss.Value != "" {
							diag["value"] = iss.Value
						}
						sub.NotOk("post-merge valid", diag)
					} else {
						sub.Ok(fmt.Sprintf("post-merge valid # warning: %s", iss.Message))
					}
				}
			} else {
				sub.Ok("post-merge valid")
			}
		}

		sub.Plan()
		tw.EndSubtest(src.Path, sub)
	}

	sub := tw.Subtest("merged result")
	if issues := CheckMerged(result.Merged); len(issues) > 0 {
		for _, iss := range issues {
			if iss.Severity == SeverityError {
				sub.NotOk(iss.Field+" unique", map[string]string{
					"severity": iss.Severity,
					"message":  iss.Message,
				})
			} else {
				sub.Ok(fmt.Sprintf("%s unique # warning: %s", iss.Field, iss.Message))
			}
		}
	} else {
		sub.Ok("no duplicate entries")
	}
	if policy, _ := sweatfileio.TrustedFolio(result, home, mainRoot); policy.Configured {
		if policy.Unmatched == "" {
			sub.NotOk("folio unmatched set", map[string]string{
				"severity": SeverityError,
				"message":  "[folio] is configured above the repo but no trusted layer sets `unmatched` (\"ask\" or \"deny\"): the broker refuses the policy and moxy keeps its static floor",
			})
		} else {
			sub.Ok("folio unmatched set")
		}
		confinementPolicy, _, _ := sweatfileio.TrustedConfinement(result, home, mainRoot)
		if issues := CheckFolioConfinement(policy, confinementPolicy.Enabled); len(issues) > 0 {
			for _, iss := range issues {
				sub.Ok(fmt.Sprintf("folio confinement %s # warning: %s", iss.Field, iss.Message))
			}
		} else if confinementPolicy.Enabled {
			sub.Ok("folio confinement")
		}
	}
	sub.Plan()
	tw.EndSubtest("merged result", sub)

	applySub := tw.Subtest("apply (dry-run)")
	merged := sweatfile.GetDefault().MergeWith(result.Merged)
	if issues := CheckGitExcludes(sweatfile.Sweatfile{Git: merged.Git}); len(
		issues,
	) > 0 {
		for _, iss := range issues {
			applySub.NotOk("git excludes structure valid", map[string]string{
				"severity": iss.Severity,
				"message":  iss.Message,
			})
		}
	} else {
		applySub.Ok("git excludes structure valid")
	}

	if issues := CheckClaudeAllow(result.Merged); len(issues) > 0 {
		hasErrors := false
		for _, iss := range issues {
			if iss.Severity == SeverityError {
				hasErrors = true
				applySub.NotOk(
					"claude settings structure valid",
					map[string]string{
						"severity": iss.Severity,
						"message":  iss.Message,
					},
				)
			}
		}
		if !hasErrors {
			applySub.Ok("claude settings structure valid")
		}
	} else {
		applySub.Ok("claude settings structure valid")
	}
	applySub.Plan()
	tw.EndSubtest("apply (dry-run)", applySub)

	tw.Plan()

	if tw.HasFailures() {
		return 1
	}
	return 0
}
