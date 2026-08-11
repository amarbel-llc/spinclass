# Self-healing pre-commit dispatch on flake.lock staleness (spinclass#267)

Status: APPROVED 2026-08-11 (Sasha, interactive decision walkthrough)
Date: 2026-08-11
Refs: conformist#76 (the attestation-WARN detector that surfaced this), #248
(the closed original: mid-session rebase past a flake.lock bump), #250
(creation-time freshening — the complement).

## Problem

The per-worktree pre-commit dispatch (`.spinclass/hooks/_spinclass-dispatch`,
`internal/sweatfile/precommit.go`) resolves the configured hook command (e.g.
`conformist-pre-commit`) from the FROZEN devShell PATH — pinned at
`sc start`/`sc resume` / direnv-load time. When a repo's `flake.lock` is later
bumped and the committed generated files are regenerated with the new toolchain,
a still-running session's dispatch keeps invoking the OLD toolchain. For a
codegen-restaging hook that stamps a tool version into generated files
(cutting-garden's tommy-codegen lane), every commit RESTAMPS those files to the
stale version, corrupting otherwise-unrelated commits. `direnv reload` / session
restart cures it until the next bump.

## Decision (interactive)

- **On flake.lock staleness, re-evaluate the hook in the CURRENT devShell,
  BUILDING it if not realized — a blocking build inside `git commit` is
  acceptable** (Sasha's call). This collapses the design: no store-presence gate,
  no skip-vs-frozen fallback. The re-eval always yields the correct toolchain, so
  the repair is correct and nothing is corrupted.
- **Per-commit re-eval overhead is acceptable as-is** (no recovery nudge): within
  one session, every commit after a bump takes the re-eval path (fast once the
  devShell is built — ~1-3s to enter it) until `sc resume` regenerates the
  dispatch. A terse one-line heads-up prints on the re-eval path so a blocking
  BUILD is not silent.

## Design

### Bake the session-start lock hash

`installPreCommitHook` computes `git hash-object <worktree>/flake.lock` (a
git-only, always-available content hash — the dispatch is a git hook, so `git`
is guaranteed) when a flake.lock exists, and bakes it into the dispatcher script
alongside `orig`/`self`/`cmd`. No flake.lock → bake empty → the staleness dance
is disabled (non-nix repos and any repo without a lock are unaffected).

### Dispatcher pre-commit branch

On pre-commit, before running the formatter:

1. If a baked hash is present AND `flake.lock` exists AND `nix` is on PATH,
   compute the LIVE `git hash-object flake.lock`.
2. live == baked (or any precondition missing) → run the frozen hook
   (`sh -c "$cmd"`) — today's fast path, byte-for-byte.
3. live != baked → print a one-line heads-up ("flake.lock changed since this
   session's devShell loaded; re-evaluating in the current devShell — may
   build") and run `nix develop --command sh -c "$cmd"`. `nix develop` reads the
   worktree's CURRENT flake.lock and builds the devShell if not realized
   (blocking — accepted), giving the correct toolchain by construction.

The nonzero-exit banner (spinclass#183) wraps both run modes; the formatter stays
best-effort/non-blocking (the commit proceeds regardless of the formatter's
exit).

### Why the baked hash must NOT self-update

The baked hash is "the lock the FROZEN devShell (on `$PATH`) was built from." The
frozen devShell is fixed at session start. If a re-eval rewrote the baked hash to
the current lock, the next commit would fast-path back to the STALE frozen hook
and corrupt again. So the baked hash stays pinned to the session-start lock; the
per-commit re-eval after a bump is inherent and correct. `sc resume` (which
regenerates the whole dispatch with a fresh devShell + fresh baked hash) is what
restores the instant path — the "self-terminating" property. The re-eval runs
the CMD (not the dispatch), so there is no dispatch recursion.

## Scope / degradation

- No flake.lock, or `nix` not on PATH → frozen path, unchanged. Non-nix repos see
  no behavior change.
- `git hash-object` failure on the live lock → treat as "can't tell" → frozen
  (fail toward today's behavior, never a spurious build).

## Rollback

Additive to the dispatcher script: with no baked hash (or the feature reverted)
the frozen path is byte-for-byte today's behavior. `[hooks].disable-pre-commit`
remains the whole-hook uninstall.

## Tuning levers

- None with a size/threshold. The one judgement call — "build inside commit is
  acceptable" — is Sasha's ratified decision; if a blocking build ever proves too
  costly, the deferred options from the walkthrough (bounded build, background
  build, opt-in skip) are the fallbacks.

## Ordering

Last of the queued spawn-surface + hook items (after #265/#258/#262/#266).
