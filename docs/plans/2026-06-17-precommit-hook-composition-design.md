# Pre-commit hook composition — design

**Date:** 2026-06-17
**Status:** approved (brainstorm); implementation pending
**Issue:** amarbel-llc/spinclass#183
**Builds on:** docs/plans/2026-06-16-per-commit-repair-hook-design.md

## Problem

The per-session pre-commit repair hook installs by pointing the worktree's
`core.hooksPath` at `<worktree>/.spinclass/hooks`. Git consults **exactly one**
hooks directory — `core.hooksPath` *replaces* `$GIT_DIR/hooks`, never merges —
so in a spinclass worktree the repo's **native git hooks are shadowed**: an
existing `pre-commit` *and every other hook type* (`pre-push`, `commit-msg`,
`prepare-commit-msg`, …) that lived in the original hooks dir stop firing,
because `.spinclass/hooks` only holds our `pre-commit`.

This blocks repos that use native git hooks **and** spinclass sweatfiles (e.g.
work repos with their own hook suite).

## Goal

Our hook **composes** with the repo's native hooks instead of shadowing them:
conformist still repairs staged content at authoring time, AND every native hook
that would have fired still fires, with its exit code preserved.

## Approach (chosen: dispatcher, default-on)

Considered:

- **Dispatcher + captured-original (chosen).** Our dir becomes a dispatcher that
  runs conformist on `pre-commit` and delegates to the captured original hooks
  for every hook type. Default-on, no config knob.
- **pre-commit-only chaining.** Chain just an existing `pre-commit`; leave other
  hook types shadowed. Rejected — incomplete; a `commit-msg`/`pre-push` hook
  would silently stop firing.
- **Opt-in knob.** Approach 1 behind `[hooks].pre-commit-compose`. Rejected —
  composition is never *worse* than shadowing, so a knob is pure surface.

Default-on is safe because the dispatcher is a no-op delegation when a repo has
no native hooks (nothing to delegate to), exactly reproducing today's behavior.

## Mechanism

### Capture & persist the original hooks dir

At install, resolve the **original** hooks dir — what git would use absent our
override:

1. If a `.spinclass/` sentinel file (e.g. `.spinclass/precommit-original-hooks`)
   already records it, use that (the source of truth on every re-install).
2. Else read the effective `core.hooksPath` (`git config --get core.hooksPath`).
   - set and resolving to a path ≠ our `.spinclass/hooks` → original = that
     (relative values resolved against the worktree top);
   - unset → original = `$GIT_COMMON_DIR/hooks` (worktrees share the common dir).
   Persist the result to the sentinel.

Persistence is **load-bearing**: install re-runs idempotently on every
`sc start`/`sc resume`, by which time `core.hooksPath` already points at our dir
— without the sentinel we'd capture *ourselves* and self-reference. The capture
also guarantees `original ≠ ourdir`; the dispatcher re-checks defensively.

### Dispatcher + shims

`.spinclass/hooks/` is rebuilt deterministically each install:

- one dispatcher script (e.g. `_spinclass-dispatch`);
- symlinked under `pre-commit` (always) **plus every active hook present in the
  original dir** — executable, non-`.sample` files matching standard hook names
  (`pre-push`, `commit-msg`, `prepare-commit-msg`, `post-commit`, …).

Rebuilding each install drops stale shims when the original dir loses a hook.

The dispatcher, keyed on its own basename `$0`:

```sh
#!/bin/sh
hook=$(basename "$0")
orig="<ORIGINAL_DIR>"   # baked from the sentinel at generate time (absolute)
self="<HOOKS_DIR>"      # baked; self-reference guard
if [ "$hook" = pre-commit ]; then
    if command -v '<BIN>' >/dev/null 2>&1; then
        sh -c '<CMD>' || echo 'spinclass pre-commit: formatter exited nonzero; continuing' >&2
    fi
fi
if [ "$orig" != "$self" ] && [ -x "$orig/$hook" ]; then
    exec "$orig/$hook" "$@"   # preserves args + stdin + exit code
fi
exit 0
```

- **Run order:** conformist **first** (format + restage), then the repo's own
  hook runs on the conformant tree.
- **Exit codes:** `exec` hands the commit's gate to the original hook — a
  blocking lint still blocks. Our conformist part stays best-effort/non-blocking
  (a missing formatter or nonzero conformist exit never blocks).
- **stdin/args:** `exec "$orig/$hook" "$@"` passes through `commit-msg`'s file
  arg, `pre-push`'s ref stream, etc. The conformist block runs only for
  `pre-commit` (no stdin), so it never consumes another hook's input.
- **Hook managers** (lefthook / husky / pre-commit framework): transparently
  supported — we delegate to whatever they installed at the original path.

### Uninstall on disable (forced by the rollback story)

`installPreCommitHook` becomes **install-or-restore**:

- **active** (`PreCommitActive`): capture original (once) → rebuild dispatcher +
  shims → set `--worktree core.hooksPath` to our dir.
- **inactive**: if previously installed (sentinel present / `core.hooksPath` ==
  our dir), **restore** — `git config --worktree --unset core.hooksPath` (git
  falls back to the original repo/global value or the default) and drop our
  shims. Native hooks fire normally again.

This fixes a latent gap in the shipped hook (setting `disable-pre-commit` after
install left `core.hooksPath` overridden, so native hooks stayed shadowed) and
makes `disable-pre-commit` the clean one-config rollback.

## Rollback strategy

- **Rollback:** `[hooks].disable-pre-commit = true` → true uninstall (restores
  native-hooks-only via the unset above). The composition change is isolated to
  `internal/sweatfile/precommit.go` and independently revertable.
- **Dual-architecture:** the non-composing behavior is the prior commit; the
  composing dispatcher is purely additive (no-op delegation when no native hooks
  exist), so there's no flag-day.

## Tuning levers

- **Persistence location** (`.spinclass/precommit-original-hooks` sentinel vs a
  `spinclass.*` worktree git-config key). Current: `.spinclass/` sentinel
  (spinclass-owned, git-excluded, torn down with the worktree). Change signal: a
  case where `.spinclass/` is cleaned independently of git config.
- **Shimmed hook set** (every active hook in the original dir). Change signal: a
  hook type we should *not* delegate (none known today).

## Known limitations

- A hook manager that **rewrites `core.hooksPath` itself** (some husky/lefthook
  setups) can clobber our override; we re-set on the next `sc start`/`resume`,
  but between those the manager's dir wins. Documented, not solved.
- Composition only covers hooks present at install time; a native hook added
  mid-session isn't picked up until the next `Apply` (resume).

## Deliverables

1. `installPreCommitHook` → capture+persist original; build dispatcher + shims;
   install-or-restore on active/inactive.
2. The dispatcher generator (basename dispatch, conformist-first, exec-delegate,
   self-guard) + active-hook enumeration of the original dir.
3. Go tests: original capture/persist; delegation + exit-code preservation;
   conformist-runs-first; multiple hook types; uninstall-on-disable restores;
   no-native-hooks no-op (today's behavior preserved).
4. bats: repo with native `pre-commit` + `pre-push` → both fire alongside
   conformist; `disable-pre-commit` restores native-only.
5. Docs: CLAUDE.md, README, `spinclass-sweatfile(5)` `[hooks].pre-commit`
   (note composition + disable=uninstall).
