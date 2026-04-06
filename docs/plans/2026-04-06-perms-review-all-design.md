# Design: `sc perms review --all`

## Problem

`sc perms review` today operates on a single worktree: it resolves the repo and
branch from cwd (or `--worktree-dir`), loads that one session's tool-use log,
and lets the user promote rules to the global or repo tier. There is no way to
review permissions across multiple sessions or repos in one pass, so accumulated
noise from many historical worktrees has to be reviewed one at a time --- or, in
practice, never.

We want an `--all` mode that:

- Merges rules from every log in `$XDG_LOG_HOME/spinclass/tool-uses/` across all
  repos and all branches.
- Offers only `global | keep | discard` for each rule. The `repo` action is
  dropped because there is no single repo context when reviewing across repos.
- Is mutually exclusive with the `[worktree-path]` positional arg and
  `--worktree-dir`.
- Still honors `--dry-run`.

## Approach

### 1. Log discovery

New helper:

``` go
func DiscoverToolUseLogs() ([]string, error)
```

Walks the flat `tool-uses/` dir and returns every `*.jsonl` path. We do **not**
need to reverse-parse `<repo>--<branch>.jsonl` back into repo/branch for rule
merging --- we concatenate and dedupe. That sidesteps the ambiguity of `--` in
branch names.

### 2. Exclusion logic

`ComputeReviewableRules` currently excludes:

- global Claude settings --- still applies
- repo-specific tier rules --- can't scope across all repos (see Q1)
- `Read($HOME/.claude/*)` --- still applies
- `Read/Edit/Write($worktreePath/*)` --- no single worktree (see Q2)

New function:

``` go
func ComputeReviewableRulesAll(tiersDir, globalSettingsPath string) ([]string, error)
```

Proposed behavior:

- Load rules from **every** log in `DiscoverToolUseLogs()`, union + dedupe.
- Exclude rules in the global Claude settings file.
- Exclude rules in the global tier (`global.json`).
- **Do not** exclude per-repo tiers --- a rule already in `repos/A.json` is
  still a legitimate candidate for global promotion when it appears in repo B's
  log.
- Exclude `Read($HOME/.claude/*)`.
- Worktree-scoped exclusions: see Q2 below.

### 3. Editor content

`FormatEditorContent` today writes `# Repo: %s` and defaults every rule to
`discard`. For `--all`:

- Header becomes `# All sessions (global promotion only)` with a note that
  `repo` is not a valid action.
- Same `discard` default.
- Optionally (Q3): trailing `# seen in: repoA, repoB` comment per rule to
  preserve origin context. `ParseEditorContent` already strips trailing `#`
  comments, so no parser changes needed.

### 4. Decision validation

In `--all`, reject `ReviewPromoteRepo` after parsing:

``` go
for _, d := range decisions {
    if d.Action == ReviewPromoteRepo {
        return fmt.Errorf("line %q: 'repo' action is not allowed with --all", d.Rule)
    }
}
```

Loop back to the editor on failure, matching the existing parse-error path.

### 5. `RouteDecisions` / `DryRunDecisions`

These take a `repo` string today, used only for the repo-tier path. In `--all`,
`repo` is unused and any `ReviewPromoteRepo` would already have been rejected in
step 4, so we pass `""`. No signature change, though an assertion at the top of
`RouteDecisions` (`if repo == "" && any decision is ReviewPromoteRepo, panic`)
would catch regressions.

### 6. Cmd wiring

``` go
cmd.Flags().BoolVar(&all, "all", false,
    "review across all sessions; only global promotion allowed")
```

Branch in `RunE`: when `--all` is set, call a new `RunReviewEditorAll(dryRun)`
that skips worktree/repo/branch detection entirely and uses
`ComputeReviewableRulesAll`. Reject combination with `[worktree-path]` or
`--worktree-dir` up front.

## Open questions

1.  **Worktree-noise strategy.** Without a single worktree path, rules like
    `Read(/home/.../worktrees/foo/src/main.go)` will appear in the review.
    Options:

    - **(a) Accept the noise.** These rules are almost always `discard` anyway.
      Cheapest path.
    - **(b) Per-log exclusions.** For each log, derive the worktree path
      (reverse-parse filename, or look up session state in
      `~/.local/state/spinclass/sessions/`), filter that log's rules against its
      own worktree path, then union. More correct, more moving parts.
    - **(c) Heuristic glob.** Exclude anything matching
      `Read/Edit/Write(*/.worktrees/*/*)`. Catches the common case cheaply.

    Lean: start with (a), escalate to (b) if the noise is bad in practice.

2.  **Repo tier exclusions.** Confirm the proposal: `--all` excludes only the
    global tier, not per-repo tiers. Rationale: a rule in `repos/A.json` is
    handled for A but may still be worth promoting to global when seen from B.

3.  **Origin comments.** Track origin sessions per rule and emit
    `# seen in: repoA, repoB` trailing comments? Cost is \~10 lines; benefit is
    context for judging whether a rule is global-worthy.

4.  **Flag name.** `--all` is slightly ambiguous ("all what?"). Alternatives:
    `--all-sessions`, `--merge-all`. Current lean: `--all` with clear help text.

5.  **Scope creep.** Should `--all` support a filter like `--since <duration>`
    to only review recent logs? Proposal: no, separate issue.

## Out of scope

- Deleting or pruning logs after review.
- Per-repo bulk review (all sessions within a single repo). If wanted, follow-up
  as `--repo <name>`.
- Any change to `sc perms check`, `list`, or `edit`.
- Changes to the repo-tier file format or `RouteDecisions` semantics for the
  non-`--all` path.

## Test plan

- `ComputeReviewableRulesAll` unit tests:
  - empty `tool-uses/` dir → empty result
  - two logs with overlapping rules → deduped
  - rule already in global tier → excluded
  - rule only in a repo tier → **not** excluded (Q2 confirmation)
  - `Read($HOME/.claude/*)` auto-exclusion still applies
- Parse-error path: `repo` action in `--all` mode → editor reopens with error
- Cmd-level: `--all` + positional arg → error before editor opens
- `--all --dry-run` prints expected global promotions and no writes
- End-to-end: populate two fake logs, run `--all`, accept all as `global`,
  assert `global.json` contains the union
