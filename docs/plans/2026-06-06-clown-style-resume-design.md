# Clown-style resume/close presentation

Approved design, 2026-06-06. Modeled on clown's `clown resume`
(clown `cmd/clown/resume.go`); the three scope decisions below were
confirmed interactively.

## Problem

`sc resume`/`sc close` pick sessions through a plain huh select with
terse labels, auto-attach with no confirmation, and tab-completion
labels in yet another format. Clown's resume presents the same problem
better: a filterable full-screen picker with title + rich description
rows, a detail-block confirmation dialog with an explicit skip flag,
relative timestamps, and "naming the target is the confirmation"
semantics. Restructure spinclass's presentation to match.

## Decisions

1. **Shared picker** — one bubbletea list picker in
   `internal/sessionpick`, used by both `resume` and `close`
   (per-verb titles). bubbles/bubbletea are already in the module
   graph via huh (promote indirect → direct, `just deps`).
2. **Confirmation step, clown's rules** — auto-detect and
   picker-single-match resume show a huh confirm ("Resume \"<title>\"?"
   + detail block, Affirmative "Resume", esc dismisses); `-y/--yes`
   skips; **explicit `sc resume <id>` shows no dialog** (naming is the
   confirmation — also keeps the remote attach template
   `spinclass resume <id>` non-interactive); resuming a session whose
   state is `active` (live PID elsewhere) warns with default
   **Cancel** (the analog of clown's cwd-mismatch warning). Selecting
   from the multi-match picker launches directly (selection is the
   confirmation), exactly like clown. `close` keeps its existing
   unintegrated-changes confirmation; only its picker presentation
   changes.
3. **Remote rows in the resume picker** — cached `host:` sessions
   (the FDR 0011 completion cache, cache-only, never networks) appear
   after local rows, clearly marked; selecting one routes over the
   attach template. The close picker stays local-only (close rejects
   remote targets).

## Presentation spec (mirroring clown)

- **Picker**: bubbletea `list.New` + default delegate, alt-screen,
  filtering enabled. Title: "Select a session to resume" / "… to
  close". Row title: session description, falling back to the
  worktree dir name. Row description:
  `<state> · <relative-time> · @<branch> · <repo>` for local rows;
  `remote(<name>) · <state> · cached` + description for remote rows.
  enter selects; q/esc/ctrl+c dismisses (nil result, exit 0).
  FilterValue: title + id + repo + host prefix.
- **Confirm dialog**: title `Resume "<title>"?`, detail block
  (indented key: value lines — id, repo/branch, state, last activity
  relative, worktree path), Affirmative "Resume", Negative "Cancel",
  esc bound to dismiss. Warning variant (active-elsewhere) prepends a
  warning paragraph and defaults to Cancel.
- **Relative dates**: clown's formatRelDate tiering (just now / Nm /
  Nh / Nd ago / date). Source: ExitedAt when set, else StartedAt.
- **Non-TTY**: unchanged contract — picker paths error with the ID
  list; confirm paths error with "pass -y" hint (mirrors clown's
  "requires an interactive terminal (or pass -y)").
- **Completion labels**: aligned to the picker description format so
  completion and picker read identically.

## Rollback

Presentation-only; no state or wire changes. The single behavioral
change is the new confirm on auto-detect resume (skippable with -y);
rollback is reverting the commit. Nothing else depends on picker
internals (sessionpick.Choose keeps its signature or gains a
variant).

## Tuning levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| confirm on auto-detect | on (skip with -y) | clown parity; shows what's about to attach | muscle-memory complaints → consider sweatfile default |
| picker size | 60x16 like clown | proven readable | long descriptions truncating |
| remote rows in picker | resume only, cache-only | close rejects remotes; completion already cache-only | demand for live refresh in picker |
