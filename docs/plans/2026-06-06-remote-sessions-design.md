# Remote spinclass sessions: `host:` routing for list, completion, and resume

Approved design, 2026-06-06. Brainstormed with the user; approach and all
section decisions confirmed interactively.

## Problem

Spinclass sessions exist on more than one machine, but `sc list`, tab
completion, and `sc resume` only see the local state directory. Reattaching
to a session on another host means remembering it exists, ssh'ing manually,
and running `sc resume` there. The sweatfile should be able to declare
remote hosts so that `host:`-prefixed sessions appear in `sc list` and
completion and can be reattached with one command.

## Decisions (with the question trail)

1. **Attach mechanism**: configurable per-host command template, defaulting
   to `ssh -t <dest> spinclass resume <id>` when unspecified. (User chose
   "configurable, ssh default".)
2. **Discovery**: live SSH queries for `sc list` (parallel, per-host
   timeout, per-host error rows) which also write a cache; tab completion
   reads ONLY the cache — instant, possibly stale, never networks.
3. **Verb scope v1**: list + completion + resume. `close`/`merge` reject
   `host:` targets explicitly.
4. **Approach**: prefix routing at the CLI boundary (no replicated index,
   no daemon). Chosen over state-replication (sync lifecycle for no
   user-visible gain) and zero-config ssh passthrough (list can't enumerate
   hosts; contradicts the sweatfile-declares-routing requirement).

## Design

### `[[remotes]]` sweatfile config

```toml
[[remotes]]
name   = "devbox"                  # the <host>: prefix users type
ssh    = "sasha@devbox.lan"        # optional; defaults to name (~/.ssh/config does the work)
attach = ["ssh", "-t", "{ssh}", "spinclass", "resume", "{id}"]  # optional; shown value is the default
```

- Merge: dedup-by-name across the hierarchy (global → parent → repo),
  name-only entry removes an inherited remote — exactly the
  `[[start-commands]]`/`[[mcps]]` semantics. Remotes normally live in the
  global sweatfile.
- `{ssh}`/`{id}` literal substitution in every argv element (the
  `exec-start` `{arg}` mechanism); exec'd directly, no shell.
- `sc validate`: name must not contain `:` or `/`; attach template, if
  present, must be non-empty.

### `host:` target grammar

`^([^:/]+):(.+)$` with a prefix matching a configured remote routes
remotely; everything else resolves locally as today. v1 routes `sc resume`
only; `close`/`merge` error on `host:` targets ("remote targets support
resume only") instead of mis-resolving.

### Listing + the enabling primitive

- `sc list --format json`: machine-readable rows (id, session key, state,
  description, repo). This is also the remote wire format.
- For each configured remote, `sc list` runs
  `ssh <dest> spinclass list --format json` in parallel with a per-host
  timeout; renders remote rows prefixed (`devbox:crisp-catalpa [active] …`)
  after local rows; writes each host's rows to
  `$XDG_STATE_HOME/spinclass/remotes/<name>.json`.
- Host down / timeout / old remote binary (no json support) → one
  diagnostic row per host; list never fails because a remote is unreachable.

### Completion + resume

- `completeWorktreeTargets` appends `host:`-prefixed entries from the cache
  files only.
- `sc resume host:id` resolves the remote, execs the attach template; the
  remote spinclass owns sweatfile/entrypoint semantics. Stale-cache resumes
  fail with the remote's own error.

### Errors, rollback, testing

- Purely additive: no `[[remotes]]` configured = behavior identical to
  today. Rollback = delete the config entries. Nothing is replaced, so no
  dual-architecture period is needed.
- Per-host isolation everywhere; attach failures pass through the exec'd
  command's exit code.
- Tests: prefix grammar, config merge, template substitution, cache
  read/write as unit tests; stub `ssh` binary on PATH emitting canned json
  (the established stub-binary pattern). Bats lane later if warranted.

## Tuning levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| per-host list timeout | 3s | LAN/tailnet ssh typically <500ms; tolerates wake-from-sleep | list feels slow / WAN timeouts misfire |
| completion staleness | unbounded cache, refreshed by each `sc list` | completion must never network | stale entries mislead → TTL or `(stale)` label |
| parallel host fan-out | unbounded | configs hold 1–5 remotes | someone configures dozens |
| remote list scope | non-abandoned sessions | matches local default | demand for closed/abandoned visibility |

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown) 0.3.10+e8ff9ee ([build commit](https://github.com/amarbel-llc/clown/commit/e8ff9ee351c67cfdf06e9e61ebe262ec3aaa247d)).
