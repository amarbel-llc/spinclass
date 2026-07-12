# Model selection for spawn-session / fork-session — design

**Date:** 2026-07-11
**Status:** implemented 2026-07-12; extended 2026-07-12 (see Addendum)

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

`model` is a short alias. **As of the 2026-07-12 addendum**, the fixed
known-set validation (`sonnet`, `opus`, `haiku`, `fable`) only applies when
the resolved provider is `claude`; for any other provider the alias is
passed through unvalidated (see Addendum). Validation now happens inside
`renderSpawn` (where the provider is known), not eagerly in the `cmd/spinclass`
layer — still before worktree creation on the `sc spawn` path, but after
`worktree.CreateFrom` on the `sc fork --brief` path, matching the pre-existing
contract for other bad-`spawn-entry`-config errors.

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

**Known, documented gap, closed 2026-07-12 (see Addendum):** originally, an
entry selecting a provider via `--profile` rather than an explicit
`--provider` resolved to `"claude"` regardless, misidentifying the provider.
`resolveProvider` now also recognizes `--profile <name>`/`--profile=<name>`
(before the literal `--`), using the profile name itself as the
`model-flags` lookup key when no explicit `--provider` is also present.

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
- Validating that the resolved model alias is actually a legal value for
  whatever provider ends up receiving it against that provider's own
  registry (e.g. querying juggler's `ListModels`/`ResolveModel` JSON-RPC to
  confirm a model name really exists before spawning). Non-claude aliases
  are passed through entirely unvalidated by spinclass; a bad one only
  surfaces as a failure from the provider itself at spawn time. Considered
  and explicitly deferred during the 2026-07-12 addendum scoping — see
  Addendum.
- ~~`--profile`-based provider detection~~ — closed 2026-07-12, see Addendum.

## Addendum (2026-07-12): provider-conditional validation + `--profile` detection

Prompted by scoping how this feature composes with clown's `juggler`
provider (a local/gateway LLM control plane — see `juggler(7)`, `juggler(1)`,
`clownfile(5)` `[profile]`). Two findings motivated this addendum:

1. `juggler` is a real clown `--provider` value (confirmed live against
   clown 0.3.19; a clown 0.3.18 session pinned earlier in this repo's history
   only knew `claude`/`codex`/`circus`/`opencode` — provider names have
   drifted between clown releases, so don't trust `clown --help` output
   cached in an old session). Per `clownfile(5)`, clown's own
   `[profile].model` is injected as `--model` for "claude-family providers
   (claude, juggler, clownbox)" — i.e. juggler and clownbox take the exact
   same `--model <value>` shape as claude, just with a completely different
   model *namespace* (GGUF filenames, registered gateway endpoint names,
   ...) that the fixed 4-alias Claude set can never validate correctly.
2. As shipped, `ValidateModelAlias` enforced the fixed Claude alias set
   **unconditionally**, regardless of resolved provider — directly
   contradicting this doc's own original "Out of scope" note ("validation is
   against the fixed Claude alias set only"). This was a bug, not a design
   choice: it meant a juggler model name would always hard-error before ever
   reaching juggler, even with a correct `model-flags.juggler = "--model"`
   sweatfile entry.

### What changed

- `ValidateModelAlias(alias, provider string) error` — now takes the
  resolved provider; returns `nil` immediately unless `provider == "claude"`.
  Called internally by `SpliceModelFlag` (single validation path, not
  duplicated at the `cmd/spinclass` layer — see below for why).
- `resolveProvider` also recognizes `--profile <name>`/`--profile=<name>`
  before the literal `--`, mirroring clown's own precedence
  (`clownfile(5)`: "an explicit `--provider` suppresses the pin ... but
  never an explicit `--profile`" — an explicit `--provider` still wins if
  both are present). The profile name becomes the `model-flags` lookup key
  when no explicit `--provider` is present, since spinclass has no way to
  resolve a `--profile` name to its underlying provider without querying
  clown's own profile registry (`clown profile list`/`profiles.toml`) — out
  of scope here, so `[session-entry.model-flags]` entries may now be keyed
  by profile name as well as provider name.
- The eager, pre-worktree `spawn.ValidateModelAlias` calls in
  `cmd/spinclass`'s `runSpawn`/`runForkDetached`/`handleForkSession` were
  **removed** — they ran before the worker's sweatfile hierarchy loads, so
  they structurally could not know the provider. Validation now happens
  entirely inside `renderSpawn` → `SpliceModelFlag`, which does know it. On
  the `sc spawn` path this still runs before `shop.Create` (no behavior
  change to the "no worktree litter" guarantee); on `sc fork --brief` a bad
  Claude alias can now leave a forked worktree behind, exactly like every
  other bad-`spawn-entry`-config error already did on that path before this
  addendum (`runForkDetached`'s pre-existing doc comment already documented
  this asymmetry for the no-`--`/unmapped-provider cases — the model-alias
  case just wasn't consistent with it until now).

### Tuning lever added

- **Whether to validate non-claude model names against the provider's own
  registry** (e.g. juggler's `ListModels`). Currently: no — pass-through,
  fail at spawn time via the provider itself. Signal to revisit: users
  report confusing failures from typo'd juggler model names that a cheap
  `juggler models`/`ResolveModel` check would have caught before spawning a
  worker.
