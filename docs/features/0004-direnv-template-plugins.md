---
status: exploring
date: 2026-05-02
promotion-criteria: |
  Promote to `proposed` once the open questions below have decisions:
  template-declaration scope (sweatfile-only vs. discoverable on disk),
  argument grammar, error semantics on template failure, and naming
  collision rules. The motivating use case (per-worktree build caches)
  must remain expressible end-to-end with the chosen design.
---

# Direnv template plugins

## Problem Statement

Sweatfile already ships generated direnv state to the worktree via
`[direnv].envrc` (raw lines) and `[direnv].dotenv` (key/value pairs).
For one-shot exports that's enough, but it scales poorly when the
*same* shell snippet should be applied across many repos with small
variations — the canonical example being per-worktree build caches:
each language has a different env var (`GOCACHE`, `npm_config_cache`,
`PIP_CACHE_DIR`, `CARGO_TARGET_DIR`, …), and every user who wants
worktree-isolated caches has to remember the matrix and re-paste the
exports into every project's sweatfile. The result is that the design
either (a) bakes the language matrix into the spinclass binary — fine
for caches, less fine for the next idea — or (b) leaves users to
copy-paste shell across N sweatfiles. Neither is a good fit for
spinclass's existing aesthetic, which already pushes language-specific
behaviour to user-declared plugins (see `[[start-commands]]`).

The shape we want: spinclass knows nothing about Go caches, npm caches,
or any specific tool. It only knows how to map a *template name* to an
executable that produces direnv-compatible output, then inlines that
output into the rendered `.envrc`. Users (and eventually a small
community library) declare the templates they want; spinclass treats
them as opaque shell-emitting plugins.

## Sketch

The proposal mirrors the existing `[[start-commands]]` plugin model
(see `internal/sweatfile/sweatfile.go` `defaultStartCommands` and the
discussion in `CLAUDE.md` "Custom start commands"). The schema below is
illustrative, not final — the open questions are listed afterwards.

### Declaring a template

```toml
[[direnv-templates]]
name        = "worktree-cache"
description = "Per-worktree cache directory for a build tool"
arg-name    = "tool"
arg-help    = "Tool name (go, node, python, rust, …)"
arg-regex   = "^[a-z][a-z0-9_-]*$"
exec-render = ["sh", "-c", '''
case "$1" in
  go)     echo "export GOCACHE=$PWD/.spinclass/cache/go" ;;
  node)   echo "export npm_config_cache=$PWD/.spinclass/cache/npm" ;;
  python) echo "export PIP_CACHE_DIR=$PWD/.spinclass/cache/pip" ;;
  rust)   echo "export CARGO_TARGET_DIR=$PWD/.spinclass/cache/cargo" ;;
  *)      echo "# unknown tool: $1" >&2; exit 1 ;;
esac
''', "_", "{arg}"]
```

Templates merge across the sweatfile cascade with the same dedup-by-name
semantics already used for `[[start-commands]]` and `[[mcps]]`. The
shell quoting / argv handling reuses the existing `{arg}` substitution
for `[[start-commands]]`.

### Using a template

```toml
[direnv]
templates = [
  { name = "worktree-cache", arg = "go" },
  { name = "worktree-cache", arg = "node" },
]
```

At `.envrc` render time, spinclass invokes each template's
`exec-render` with `{arg}` substituted, captures stdout, and concatenates
the result into the generated `.envrc` alongside the existing
`[direnv].envrc` raw lines. A failed template is a sweatfile validation
error, not a silent skip.

### Where templates live

Templates can be declared:

1. Inline in any sweatfile in the cascade (`[[direnv-templates]]`) —
   smallest mental model, mirrors `[[start-commands]]`.
2. As baked-in defaults in `sweatfile.GetDefault()` (the same place
   `gh_pr` and `gh_issue` ship from). The community-curated cache
   matrix is a candidate for this slot once a real one is settled.

Out-of-tree template files (e.g. `~/.config/spinclass/templates/*.sh`
discovered on PATH-like rules) are listed as a deferred extension —
see open questions.

## Open questions

### Template scope

Should templates be sweatfile-only (declarative, single mental model),
or also discoverable on disk (more direnv-stdlib-flavoured)? The
filesystem option is more powerful — users could share templates as
small repos, drop them in a known dir, and reference them by name —
but it widens the surface considerably (search path, name collisions,
sandboxing). The lean is sweatfile-only at v1, with an explicit "later"
for filesystem discovery.

### Argument grammar

`{arg}` covers a single positional argument cleanly (matching
`[[start-commands]]`). Multi-arg templates (e.g. `worktree-cache go
shared` to flag a shared cache mode) need either a richer substitution
scheme or a convention of accepting JSON on stdin. The lean is to keep
v1 single-arg and revisit when a real multi-arg template appears.

### Error semantics

If a template's `exec-render` exits non-zero, options are:

- **Hard fail** at session start. Forces template authors to handle
  the bad-arg case explicitly. Matches sweatfile validation behaviour
  for `[[start-commands]]`.
- **Soft skip** with a TAP `not ok`. Lets a session start even if a
  template plugin is broken or missing; lower friction in mixed
  team setups.

The cache-matrix use case prefers hard fail — silently dropping the
GOCACHE export means falling back to the global cache, which defeats
the feature. Lean: hard fail, mirroring `[[start-commands]]`.

### Naming collisions

If two cascade levels declare a `[[direnv-templates]]` entry with the
same name, the existing dedup-by-name policy (last definition wins)
applies trivially. The open question is whether a template name
should also be uniqueness-checked against other plugin namespaces
(`[[start-commands]]`, `[[mcps]]`) or whether each namespace is
independent. Lean: independent — templates only collide with
templates.

### Sandboxing

Templates execute arbitrary shell that lands in the user's `.envrc`
and gets sourced by direnv on every worktree entry. This is no worse
than the existing `[direnv].envrc = ["..."]` raw passthrough — but
making it easy to declare templates makes it easier for one to be
malicious. The lean is to inherit the existing trust model
(sweatfiles are user-controlled config) and revisit only if a
discovery mechanism is added.

## Motivating example: per-worktree build caches

A baked-in `worktree-cache` template plus a personal global sweatfile
entry like:

```toml
[direnv]
templates = [{ name = "worktree-cache", arg = "go" }]
```

…replaces the proposal's predecessor design (a hard-coded
`[worktree-caches]` table that names languages directly in the
spinclass binary). The cache-matrix becomes a community-curatable
plugin instead of a feature spinclass has to ship and version.

The forcing-function intent (slow naked `go build`, unaffected
`nix build`, automatic cleanup on worktree removal) is fully
preserved — it's just expressed via a template the user opted into,
not a flag in the binary.

## Limitations

- **Direnv required.** Templates render through the existing
  `[direnv]` flow, so users without direnv enabled get nothing.
  Pushing template output into a non-direnv session env (so naked
  `sc resume` shells also see the exports) is a possible follow-up
  but is **not** included in this design.
- **Render-time, not runtime.** Templates are evaluated when spinclass
  writes the `.envrc`. They cannot react to runtime state inside the
  worktree (e.g. detect `package.json` and self-enable). Users who
  want detection wrap that logic inside the template's own shell.
- **No coordination across templates.** Two templates that both want
  to set the same env var will produce duplicate exports; last write
  wins per shell semantics. A "claim a name" mechanism is out of
  scope.
- **No structured output protocol.** Templates emit raw shell.
  Switching to a JSON protocol (e.g. `{"exports": {"GOCACHE": "..."}}`)
  is a possible v2; v1 keeps the same trust model as
  `[direnv].envrc` raw lines.

## More Information

- FDR 0003 (`docs/features/0003-per-worktree-madder-blob-store.md`) —
  parallel "spinclass exposes a plugin point, doesn't bake in the
  detail" pattern; that one chose Candidate A (auto-on when a binary
  is on PATH), this one is closer in shape to Candidate C
  (sweatfile-declared opt-in).
- `internal/sweatfile/sweatfile.go` `defaultStartCommands()` — the
  existing plugin shape this design mirrors.
- `CLAUDE.md` — "Custom start commands" — the prior art for
  user-extensible plugin schemas in spinclass.
- Origin conversation: per-worktree GOCACHE forcing-function design
  thread, May 2026; concluded that baking the language matrix into
  the binary was the wrong shape and that a template plugin pattern
  was the right one.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown).
