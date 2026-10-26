package hooks

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/shlex"
)

// The agent surface for session verbs is the MCP tools; the sc/spinclass CLI is
// for explicit operator execution (#422). This guard denies an agent's Bash use
// of any verb that is not read-only. The hook fires only on agent tool calls, so
// the operator's terminal is unaffected. It guards against accidental or habitual
// use and is not a security boundary: an alias, a generated command, `bash -c`
// or a copied binary gets past it. Confinement (FDR 0035) is the boundary.

// isReadOnlyScVerb reports the CLI verbs an agent may run through Bash. Each only
// reads: list/validate/whoami/list-handles/perms-list/host-load mirror the
// auto-approved read-only MCP tools, version prints build info, and implicit-session-key is a
// documented pure query that writes no state. list-handles with --accept is
// not read-only (it accepts pending grants) and is refused by the caller.
func isReadOnlyScVerb(verb string) bool {
	switch verb {
	case "list", "version", "validate", "whoami", "list-handles", "perms-list",
		"host-load", "implicit-session-key", "attestation", "session-health":
		return true
	}
	return false
}

// scVerbMCPTool maps a CLI verb to the MCP tool an agent uses instead. Verbs
// absent here have no MCP form (operator-only).
func scVerbMCPTool(verb string) (string, bool) {
	switch verb {
	case "merge":
		return "merge-this-session", true
	case "check":
		return "check-this-session", true
	case "spawn":
		return "spawn-session", true
	case "update-description":
		return "update-this-session-description", true
	case "close-child-session", "close-this-session", "restart-this-session",
		"restart-child-session", "restart-hung-child", "cancel-restart", "grant-session-handle",
		"release-session-handle", "resurrect":
		// The MCP tool carries the CLI verb's name.
		return verb, true
	}
	return "", false
}

const scMCPToolPrefix = "mcp__plugin_spinclass_spinclass__"

// scWrapperValueFlags are the wrappers skipped before argv[0], with the flags
// that consume the following token.
var scWrapperValueFlags = map[string][]string{
	"env":     {"-u", "-C", "-S", "--unset", "--chdir", "--split-string"},
	"command": nil,
	"exec":    {"-a"},
	"nohup":   nil,
	"time":    {"-f", "-o", "--format", "--output"},
	"sudo":    {"-u", "-g", "-h", "-p", "-C", "-D", "-R", "-T", "-U"},
	"nice":    {"-n", "--adjustment"},
}

var shellAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// checkBashScVerb returns a deny reason when the Bash command drives a
// non-read-only sc/spinclass verb, or "" when the command is left alone (#422).
func checkBashScVerb(command string) string {
	for _, segment := range splitSimpleCommands(command) {
		if reason := checkSimpleCommandScVerb(segment); reason != "" {
			return reason
		}
	}
	return ""
}

// splitSimpleCommands splits on the unquoted separators && || ; | & newline,
// plus ( ) and backtick so subshells and command substitutions are inspected as
// their own commands. Quotes and backslash escapes are honoured; an unterminated
// quote keeps the rest in one segment for the caller to fail closed on.
func splitSimpleCommands(command string) []string {
	var segments []string
	var cur strings.Builder
	var quote rune
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segments = append(segments, s)
		}
		cur.Reset()
	}
	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote != 0:
			cur.WriteRune(r)
			if r == '\\' && quote == '"' && i+1 < len(runes) {
				i++
				cur.WriteRune(runes[i])
			} else if r == quote {
				quote = 0
			}
		case r == '\\' && i+1 < len(runes):
			cur.WriteRune(r)
			i++
			cur.WriteRune(runes[i])
		case r == '\'' || r == '"':
			quote = r
			cur.WriteRune(r)
		case strings.ContainsRune("&|;\n()`", r):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return segments
}

func checkSimpleCommandScVerb(segment string) string {
	argv, err := shlex.Split(segment)
	if err != nil {
		// Unparseable: fail closed only when a token looks like an sc invocation.
		for _, field := range strings.Fields(segment) {
			if isScBinary(strings.Trim(field, `"'`)) {
				return scDenyReason("", "")
			}
		}
		return ""
	}
	argv = skipLeadingWrappers(argv)
	if len(argv) == 0 || !isScBinary(argv[0]) {
		return ""
	}
	verb, sub, help, ok := scVerb(argv[1:])
	if !ok {
		return scDenyReason("", "")
	}
	if help || verb == "" || verb == "help" {
		return ""
	}
	if verb == "perms" && sub == "list" {
		return ""
	}
	if verb == "list-handles" && hasAcceptFlag(argv[1:]) {
		return scDenyReason(verb, sub)
	}
	if isReadOnlyScVerb(verb) {
		return ""
	}
	return scDenyReason(verb, sub)
}

func hasAcceptFlag(args []string) bool {
	for _, a := range args {
		if a == "--accept" || strings.HasPrefix(a, "--accept=") {
			return true
		}
	}
	return false
}

func isScBinary(argv0 string) bool {
	base := filepath.Base(argv0)
	return base == "sc" || base == "spinclass"
}

// skipLeadingWrappers drops VAR=value assignments and the wrappers in
// scWrapperValueFlags (with their flags and assignments) ahead of the command.
func skipLeadingWrappers(argv []string) []string {
outer:
	for len(argv) > 0 {
		if shellAssignment.MatchString(argv[0]) {
			argv = argv[1:]
			continue
		}
		valueFlags, isWrapper := scWrapperValueFlags[filepath.Base(argv[0])]
		if !isWrapper {
			return argv
		}
		argv = argv[1:]
		for len(argv) > 0 {
			switch {
			case containsString(valueFlags, argv[0]):
				argv = argv[min(2, len(argv)):]
			case strings.HasPrefix(argv[0], "-") || shellAssignment.MatchString(argv[0]):
				argv = argv[1:]
			default:
				continue outer
			}
		}
	}
	return argv
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// scVerb finds the verb in the arguments after argv[0], skipping the global
// flags (--format takes a value; -v/--verbose does not). sub is the next
// non-flag argument (for `perms <sub>`). help reports --help/-h before the
// verb or as the verb's only argument; anywhere else it is not trusted to stop
// the verb from running, so the call is judged on the verb. ok is false for an
// unknown flag before the verb, which fails closed.
func scVerb(args []string) (verb, sub string, help, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--help" || a == "-h":
			return "", "", true, true
		case a == "--format":
			i++
		case strings.HasPrefix(a, "--format="), a == "-v", a == "--verbose":
		case strings.HasPrefix(a, "-"):
			return "", "", false, false
		default:
			verb = a
			for _, rest := range args[i+1:] {
				if !strings.HasPrefix(rest, "-") {
					sub = rest
					break
				}
			}
			rest := args[i+1:]
			help = len(rest) == 1 && (rest[0] == "--help" || rest[0] == "-h")
			return verb, sub, help, true
		}
	}
	return "", "", false, true
}

func scDenyReason(verb, sub string) string {
	name := verb
	if verb == "perms" && sub != "" {
		name = verb + " " + sub
	}
	if name == "" {
		name = "<unparseable>"
	}
	if tool, ok := scVerbMCPTool(verb); ok {
		return fmt.Sprintf("The sc CLI is the operator's; agents must not run `sc %s` through Bash. Use the MCP tool %s%s instead (#422).",
			name, scMCPToolPrefix, tool)
	}
	return fmt.Sprintf("The sc CLI is the operator's; agents must not run `sc %s` through Bash, and it has no MCP tool. Ask the operator to run it (#422).", name)
}
