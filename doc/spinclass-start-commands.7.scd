spinclass-start-commands(7) "spinclass" "Spinclass Reference"

# NAME

spinclass-start-commands - authoring guide for custom start-\* plugins

# DESCRIPTION

Spinclass supports config-driven *start-\** subcommands declared via
*[[start-commands]]* entries in the sweatfile hierarchy. Each entry registers a
*sc start-*_name_ subcommand that validates a positional argument, offers tab
completion, and creates a worktree session.

This page documents the sweatfile schema, the JSON protocols for *exec-start*
and *exec-completions*, and the runtime behaviour so that agents and users can
author new plugins without reading Go source.

# SWEATFILE SCHEMA

Each plugin is declared as a TOML array-of-tables entry:

```
[[start-commands]]
name             = "jira"
description      = "Start session for a JIRA ticket"
arg-name         = "ticket"
arg-help         = "JIRA ticket ID"
arg-regex        = "^[A-Z]+-[0-9]+$"
exec-completions = ["sh", "-c", "jira list --json | jq '[.[] | {arg: .key, description: .summary}]'"]
exec-start       = ["sh", "-c", "jira show {arg} --json | jq '{context: .body}'"]
```

## Fields

*name*
	(required) Lowercase identifier matching _^[a-z][a-z0-9\_-]\*$_. Registers
	the subcommand as *sc start-*_name_. Duplicate names within a single file
	are rejected by *sc validate*.

*description*
	Short description shown in *--help* and manpages. Defaults to “Start a
	session via the _name_ plugin”.

*arg-name*
	Name of the positional parameter (e.g. “ticket”, “issue”). Defaults to
	“arg”.

*arg-help*
	One-line help text for the positional parameter.

*arg-regex*
	Optional RE2 regular expression. The positional argument is validated
	against this before *exec-start* runs. *sc validate* warns when
	*exec-start* uses a shell interpreter (sh, bash, zsh, fish, dash, ksh) and
	*arg-regex* is not set.

*exec-completions*
	Argv array exec'd at tab-completion time. Stdout must be JSON; see
	*EXEC-COMPLETIONS PROTOCOL*. Failures are silent. May be omitted if no
	completions are needed.

*exec-start*
	(required) Argv array exec'd when the command runs. Every element undergoes
	literal *{arg}* substitution with the positional value. Stdout must be
	JSON; see *EXEC-START PROTOCOL*. The command is exec'd directly (no shell);
	wrap in *sh -c* for shell features like pipes and redirects.

# EXEC-START PROTOCOL

*exec-start* must produce a single JSON object on stdout:

```
{
  "branch":      "<optional string>",
  "description": "<optional string>",
  "context":     "<required string>"
}
```

*context*
	(required) Session context string. This is the primary output — it provides
	context to the AI agent about what the session is working on.

*description*
	Used as the session description if the user did not pass *--description*.
	Falls back to “_name_: _arg_”.

*branch*
	When present, checks out an existing local or remote branch instead of
	creating a new one. The branch must already exist; if it exists only on
	*origin*, it is fetched automatically. This mirrors the behaviour of
	*sc start-gh\_pr*. When absent, a new branch is created with an
	auto-generated name derived from the description.

Invalid JSON causes the command to fail with a diagnostic. An empty *context*
value is allowed.

# EXEC-COMPLETIONS PROTOCOL

*exec-completions* must produce a JSON array on stdout:

```
[
  {"arg": "FOO-1", "description": "First ticket"},
  {"arg": "FOO-2", "description": "Second ticket"}
]
```

Each object has:

*arg*
	The completion value inserted at the command line.

*description*
	Human-readable description shown alongside the completion.

An empty array or invalid JSON silently produces no completions.

# MERGE BEHAVIOUR

Entries merge across the sweatfile hierarchy (global → intermediate parent dirs
→ repo-level). Later definitions override earlier ones by *name*, preserving
the position of the first occurrence.

The built-in *gh\_issue* entry ships via *sweatfile.GetDefault()* and can be
overridden by declaring a *name = "gh\_issue"* entry in any user sweatfile.
Built-in subcommands (e.g. *start* or *start-gh\_pr*) always win over a
sweatfile entry with the same name.

# VALIDATION

*sc validate* checks each *[[start-commands]]* entry for:

- Missing or invalid *name*.
- Missing *exec-start*.
- Duplicate names within a single file.
- Non-compiling *arg-regex*.
- Shell interpreter in *exec-start[0]* without *arg-regex* (warning).

# EXAMPLES

A minimal plugin that adds ticket context from a hypothetical CLI:

```
[[start-commands]]
name       = "ticket"
arg-name   = "id"
arg-regex  = "^[0-9]+$"
exec-start = ["sh", "-c", "ticket show {arg} --json | jq '{context: .body}'"]
```

A plugin that checks out a feature branch and provides context:

```
[[start-commands]]
name             = "feature"
arg-name         = "branch"
exec-completions = ["sh", "-c", "git branch --format '%(refname:short)' | jq -Rs '[split(\"\\n\") | .[] | select(. != \"\") | {arg: ., description: \"\"}]'"]
exec-start       = ["sh", "-c", "echo '{\"branch\": \"{arg}\", \"context\": \"# Working on branch {arg}\"}'"]
```

Override the built-in gh\_issue with a custom implementation:

```
[[start-commands]]
name             = "gh_issue"
arg-regex        = "^[0-9]+$"
exec-completions = ["sh", "-c", "gh issue list --json number,title --limit 20 | jq '[.[] | {arg: (.number | tostring), description: .title}]'"]
exec-start       = ["sh", "-c", "gh issue view {arg} --json number,title,body | jq '{context: (\"# Issue \" + (.number|tostring) + \": \" + .title + \"\\n\\n\" + .body)}'"]
```

# FILES

_~/.config/spinclass/sweatfile_
	Global sweatfile (lowest precedence).

_<repo>/sweatfile_
	Repository-level sweatfile (highest precedence).

# SEE ALSO

*spinclass*(1), *spinclass-sweatfile*(5), *spinclass-start*(1),
*spinclass-start-gh\_pr*(1), *spinclass-validate*(1)
