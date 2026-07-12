# Model selection for spawn-session / fork-session — design

**Date:** 2026-07-11
**Status:** implemented 2026-07-12

## Goal

Let a driver session pick which model a spawned/forked worker boots into,
per call, instead of always inheriting whatever the harness's default is.

**Motivation:** per-call cost/capability tradeoff — a cheap/fast model for a
simple worker task, a stronger model for a hard one, decided at spawn time.

## Background

`spawn-session`/`sc spawn` and `fork-session`/`sc fork --brief` both launch a
worker via `[session-entry].spawn-entry` (`internal/spawn`, FDR 0006). The
default entry is:

```
["clown", "--clown-attach=spawn", "--", "{prompt}"]
```

Per `clown --help`, clown's own grammar is `clown [clown-flags] -- [provider-args]`
— everything after `--` is forwarded verbatim to the provider (default
`claude`, selectable via `--provider`/`--profile`). Clown itself has no
`--model` flag; model selection is a provider CLI concern. This means model
selection is already possible today with zero spinclass changes, via a
sweatfile `spawn-entry` override — this design adds a first-class, validated,
per-call way to do it instead of requiring a static override.

`posh` (mentioned in FDR 0017 / clown RFC-0014) is clown's own internal
window-management layer, invisible to spinclass's `spawn-entry` template —
not something users address directly, and irrelevant to where provider-args
land.

The only existing per-call override on these tools today is `hello-timeout`
(optional string param → parsed → threaded into `spawn.Launch`). This design
follows that pattern.

## Design

### 1. Sweatfile knob — `[session-entry.model-flags]`

New map field on `SessionEntry`, merged like `[env]` (map merge across the
sweatfile hierarchy — later levels add/override individual keys, not the
whole map). Ships with one built-in default entry:

```toml
[session-entry.model-flags]
claude = "--model"
```

This is the only provider→flag mapping verified against an actual CLI
(`claude`, forwarded through clown's `--` boundary). Other providers
(`codex`, `circus`, `opencode`) are not populated — their CLI flag for model
selection is unverified and not guessed at. Users can add entries for other
providers as they confirm the correct flag, without a spinclass code change.

### 2. Value shape

`model` is a short alias, validated against a fixed known set: `sonnet`,
`opus`, `haiku`, `fable`. An unrecognized alias is a hard error before any
spawn/worktree work happens — never passed through unvalidated.

### 3. Provider resolution + insertion

In `spawn.Launch` (or a new pure helper alongside `SubstituteEntry`):

1. Scan the resolved `spawn-entry` argv, elements before the literal `--`,
   for `--provider <name>` or `--provider=<name>`. Default to `"claude"` if
   absent (clown's own default).
2. Look up that provider name in `[session-entry.model-flags]`.
3. If found: splice `[<flag>, "<alias>"]` into the argv immediately after
   the `--` separator, before `{prompt}` is substituted in.
4. If the provider isn't in the map, **or** the entry has no `--` at all,
   and `model` was passed: hard error, no silent fallback or best-effort
   guess. (Matches the existing hard-error pattern for `hello-timeout`
   parsing — refuse to spawn rather than launch something that might be
   wrong.)

**Known, documented gap (not solved by this design):** if an entry selects
a non-default provider via `--profile` rather than an explicit `--provider`,
resolution still defaults to `"claude"` and may misidentify the provider.
Out of scope for v1 — `--profile`-based provider selection combined with
per-call `model` is not supported; document the limitation rather than
building profile-config lookup.

### 4. Surfaces

`model` becomes an optional parameter, mirrored across all four existing
entry points (matching how `hello-timeout` is mirrored today):

- MCP tool `spawn-session`
- MCP tool `fork-session`
- CLI `sc spawn`
- CLI `sc fork --brief`

### 5. Error handling summary

| Condition | Behavior |
|---|---|
| `model` omitted | No change — current behavior, no `--model` spliced |
| `model` given, unrecognized alias | Hard error, no spawn attempted |
| `model` given, provider resolved but unmapped in `model-flags` | Hard error, no spawn attempted |
| `model` given, entry has no `--` | Hard error, no spawn attempted |
| `model` given, provider mapped | `[<flag>, "<alias>"]` spliced after `--`, spawn proceeds |

## Tuning Levers

- **Known alias set (`sonnet`/`opus`/`haiku`/`fable`).** Fixed list, will
  need updating as models are renamed/added. Signal to revisit: a new model
  ships and someone wants to spawn with it.
- **Built-in `model-flags` seed (`claude` only).** Signal to add a new
  provider's flag: someone verifies the correct CLI flag for `codex`/
  `circus`/`opencode` (or a new clown provider) and wants it usable without
  a manual sweatfile override.

## Rollback

Purely additive: new optional param on 4 existing surfaces, new optional
sweatfile map field with a harmless default. Omitting `model` reproduces
today's exact behavior. Rollback is deleting the param plumbing and the
sweatfile field; no data migration in either direction.

## Out of scope

- Per-provider alias *value* translation (e.g. mapping `"opus"` to a
  different name for a hypothetical non-Anthropic provider). Only the flag
  name varies by provider in this design; the alias string is passed
  through verbatim.
- `--profile`-based provider detection.
- Validating that the resolved model alias is actually a legal value for
  whatever provider ends up receiving it (validation is against the fixed
  Claude alias set only).
