---
status: proposed
date: 2026-06-23
promotion-criteria: |
  proposed -> experimental: `sc run` lands as a CLI-only command that, given
  `-- <util>` or a stdin script, (1) starts a worktree session, (2) runs the
  one command devshell-scoped with the SPINCLASS_* identity env, (3) on a
  successful commit-producing run merges into the default branch and tears the
  session down, (4) on an empty (no-commit) run reports a clean success and
  tears down, and (5) on a failing step leaves the worktree + session intact
  and propagates the step's exit code. The `--no-merge`/`--no-close` 2x2
  teardown matrix and the shebang-aware stdin form are covered by bats.
  experimental -> testing: eng's `update-nix-repos` cascade drives at least one
  real multi-repo run through `sc run` per repo (commit-hook autofix firing at
  commit time, sweatfile pre-merge as the CI gate) with no key-capture or
  conditional-cleanup glue, and no orphaned sessions left behind on the
  happy path across a full cascade.
---

# `sc run` — one-shot start → exec → merge → cleanup

## Problem Statement

Driving a non-interactive, scripted spinclass session today means assembling
several commands by hand:

```bash
cd "$repo"
out="$(sc --format tap start --description "...")"
wt="$(echo "$out" | grep -oP 'ok \d+ - create \S+ \K\S+')"   # parse TAP for the path
key="$(basename "$wt")"
sc exec --session "$key" -- <step>
sc merge --target "$key"          # removes the worktree on success
# on failure only:
sc close --target "$key" -f
```

Three sharp edges:

- **Key capture is fragile.** Callers must know to pass `--format tap` and grep
  `ok N - create <branch> <path>` to recover the worktree path, then `basename`
  it to get the session target. There is no first-class "give me the key"
  output.
- **Cleanup is conditional.** `sc merge` removes the worktree on success, so a
  trailing `sc close` errors on the happy path — it is only correct on the
  failure path. Every caller encodes that asymmetry.
- **No atomicity.** A failure midway leaves a session the caller must reason
  about and clean up by hand.

This is exactly the shape eng's `update-nix-repos` cascade needs per repo: spin
up a session, run a fixed sequence (so the repo's `[hooks].pre-commit` autofix
fires at commit time), then `sc merge` so the sweatfile's `[hooks].pre-merge`
is the source of truth for the CI loop + push. It currently hand-assembles the
sequence above; #194 asks for a single primitive that collapses it.

## Interface

```
sc run [--description D] [--no-merge] [--no-close] [--local-only] \
       ( -- <util> [args...] | <stdin script> )
```

CLI-only (no MCP tool): it execs a command sequence and reads stdin, neither of
which fits the MCP request/response shape. Output uses the merge/check
`present` stack — `--format auto` (viewport on a TTY, ndjson when piped) |
`viewport` | `plain` | `ndjson`; TAP is rejected, as for merge/check.

### Input forms (mutually exclusive)

- **`-- <util> [args...]`** — a single command after one `--` separator,
  exactly `sc exec`'s grammar. One step.
- **stdin script** (no `--`) — the full stdin is read as a script. **Shebang-
  aware**: if line 1 is a `#!` shebang the script runs under that interpreter
  (materialized to a temp file under `<worktree>/.tmp`, `chmod +x`, exec'd as
  `interp path`), otherwise under `sh`. One step.
- Empty input (no `--` util and blank/absent stdin) is an error
  ("nothing to run").

### Success-path teardown (2×2 matrix)

`--no-merge` and `--no-close` compose orthogonally:

| flags | merge | teardown |
|---|---|---|
| *(default)* | merge into default branch | session torn down (merge runs `inSession=false`, removing worktree+branch; the dangling index entry it leaves is dropped via `session.Remove`) |
| `--no-close` | merge into default branch | worktree + branch + session left (merge runs `inSession=true`, skipping its teardown — the session keeps accumulating like a normal `sc` worktree) |
| `--no-merge` | skipped | session closed ONLY IF no commits were produced; commits present ⇒ session left intact |
| `--no-merge --no-close` | skipped | worktree/session left fully intact |

- An **empty run** (0 commits ahead of the default branch) is a **clean
  success**, not a failure: the merge path emits an ok "nothing to merge
  (skipped)" verdict and tears down per `--no-close`. (This deliberately
  bypasses `merge`'s own 0-commit `failStep`, which would otherwise make a
  no-op repo in a cascade a failure.)
- The **`--no-merge` + close** path never silently discards committed work:
  it closes only when the run is known to have produced no commits; commits
  present (or an ambiguous-default-branch count of "unknown") leaves the
  session intact with a stderr notice. A future `--force-close`/`--no-dirty`
  gate could override this safety default.
- `--local-only` passes through to the merge step (skip the pull-before and
  push-after; `gitSync = !localOnly`).

### Failure path

Any step that exits nonzero leaves the worktree + session intact for inspection
(clean up with `sc close`) and propagates the step's exit code. The teardown
matrix is ignored on the failure path. This matches spawn's hello-timeout
"leave for inspection" convention; `sc clean` does not reap it because the
worktree is present and the state is inactive, not abandoned.

## Design

Implemented in `internal/run` (`run.Run(Spec) (exitCode int, err error)`),
registered as a `PassthroughArgs` CLI command (`run.ParseArgs` hand-parses the
flags up to `--`, like `sc exec`). The orchestration composes existing pieces
rather than re-implementing them:

1. `worktree.DetectRepo` + `worktree.ResolvePath` → the session's
   `ResolvedPath` (random branch, key, abs path).
2. Default branch resolved **before** the reporter scope (`merge.ResolveDefault
   Branch` may `huh`-prompt and cannot nest in the live viewport). On a non-TTY
   ambiguous main/master `sc run` errors instead of hanging — unless
   `--no-merge` makes the default branch irrelevant (then resolved
   non-interactively via `git.DefaultBranch`, "" on ambiguity).
3. `shop.Attach(io.Discard, …, noAttach=true, …)` creates the worktree, applies
   setup, and writes **inactive/PID-0 session state** — the create-then-record
   path the companion `--no-attach` change established (see below). `io.Discard`
   + `format=""` keeps its own output off the crap stream (the reporter owns
   stdout).
4. One `present.WithReporter` scope: the step is a crap `Phase`
   (`rep.Phase` → `Command` → live output teed via `present.NewLineWriter` →
   `Done`/`FailDiag`), and the merge stages (`merge.Resolved`) append to the
   same `TestStream` for one continuous result stream. The numeric exit code is
   stashed in a closure var (the reporter callback returns an error, not a
   code); step-fail vs merge-fail vs success is distinguished outside the scope
   to drive teardown.
5. Teardown applies the 2×2 matrix.

The step reuses `sessionexec.CommandIn`/`IdentityEnv` (exported from
`internal/sessionexec` for this purpose) so the `direnv exec` devshell wrap and
the load-bearing `SPINCLASS_*` var list are not duplicated.

### Companion change: `sc start --no-attach` writes findable state

`sc run` needs a session that is findable on disk between create and operate.
Previously `shop.Attach` skipped the entire state-write block when
`noAttach=true`, so `sc start --no-attach` wrote **no** session state. It now
writes **inactive/PID-0** state in that path too, so `sc exec --session <key>`,
`sc list`, and merge/close can target a `--no-attach`-created session. (The
on-attach hook stays attach-only — nothing is attached.) Blast radius: a
`--no-attach` session now appears in `sc list`; `sc clean` does not reap it
(the worktree is present); resume of a `--no-attach` session now succeeds
(it has state). The bats assertion that asserted "no session state with
`--no-attach`" was inverted accordingly.

## Limitations / out of scope

- **Untracked-content worktree removal.** On a real repo, sweatfile-installed
  untracked files (`.envrc`, `.claude/`) can make the merge's non-force
  `git worktree remove` refuse — a pre-existing `sc merge` hazard, more exposed
  in non-interactive `sc run`. Not addressed here; sweatfile `[git].excludes`
  is the existing mitigation.
- **Single step only.** v1 runs exactly one `-- <util>` or one stdin script.
  Multi-step sequences are expressed inside the stdin script (with the
  interpreter's own `set -e`), not as repeated `--` groups.
- **No MCP surface.** Deliberate; agents merge in-session via
  `merge-this-session`.
