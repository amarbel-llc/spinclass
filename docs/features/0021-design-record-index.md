---
status: experimental
date: 2026-07-02
promotion-criteria: |
  proposed -> experimental: DONE — the docs-index renders in spinclass's OWN
  clown sessions (a live cold `prompts/get` shows the `## Design records` block
  listing all 21 FDRs grouped by status, `docs/adrs`/`docs/rfcs` absent →
  omitted), backed by `internal/sysprompt/docsindex.go` +
  `[sysprompt].doc-index-dirs` (override/`[]`-disable proven by unit tests).
  proposed -> experimental (original): the docs-index renders in spinclass's OWN clown
  sessions — a `## Design records` block listing the FDRs by number · title ·
  status, grouped by status, sourced from the three default dirs
  (`docs/features`, `docs/adrs`, `docs/rfcs`), scan-if-exists so a repo without
  any of them shows nothing. The `[sysprompt].doc-index-dirs` override is proven
  to change the scanned set, and `doc-index-dirs = []` is proven to omit the
  section entirely.
  experimental -> testing: the pre-`initialize` render stays fast (no measurable
  stall of the clown bridge's `prompts/get`) with the sweatfile-hierarchy load
  added; malformed/absent frontmatter degrades to a skipped file (never a failed
  render); and the worktree-branch-local view is confirmed (an FDR added on the
  session branch appears in that session's own fragment).
  testing -> accepted: ~1 week of real sessions across repos under the default
  docs-index with no prompt-bloat complaints and no render failures attributable
  to the scanner — AND the accumulated usage has answered whether a second
  substrate (dodder query, arbitrary file) is actually wanted, which is the
  trigger to design the composable `[[sysprompt-sections]]` framework (below).
---

# Design-record index in the dynamic system-prompt

> **Experimental** — the docs-index shipped: `internal/sysprompt/docsindex.go`
> (scanner + grouped renderer), the `[sysprompt].doc-index-dirs` sweatfile knob
> (override, not append; `[]` disables), and the Go-composed `## Design records`
> trailer appended in `sysprompt.Render`. Verified live against this repo's 21
> FDRs. The composable `[[sysprompt-sections]]` framework below remains the
> deferred North Star — deliberately not built on a sample size of one substrate.

## Problem Statement

The dynamic system-prompt fragment (`internal/sysprompt`, #187) is a single
hardcoded render: it branches only on worktree-vs-main-checkout and emits a fixed
session-orientation preamble plus the repository line. There is no way for a repo
to compose *its own* orientation into the fragment from structured data it
already owns. The concrete need: an at-a-glance **index of the repo's design
records** — `docs/features/` (FDRs), `docs/adrs/` (ADRs), `docs/rfcs/` (RFCs) —
so an agent knows which subsystems have design docs and their lifecycle status
(`proposed`/`experimental`/`accepted`/…) without reading dozens of files.

This is deliberately scoped to that one substrate. A general "composable pipeline
of section providers over arbitrary substrates" is the tempting abstraction, but
with exactly one substrate in hand it would be premature indirection — a provider
registry justified by a single consumer. We ship the direct, un-abstracted
docs-index first, learn from it, and generalize only once a second substrate
(e.g. a dodder query) proves the demand and teaches the real parameter surface.
The composable framework is captured below as the North Star, not built here.

## Interface

### The docs-index (what ships now)

A `## Design records` section is appended to the dynamic fragment, after the
fixed session-orientation preamble the templates already emit:

- **Index only.** Per file: the number (from the `NNNN-…` filename), the title
  (first `# ` heading), and `status` (from YAML frontmatter). No bodies.
- **Grouped by status**, so "what's still in flight" reads at a glance. A file
  with no `status` frontmatter lists under an "(unstatused)" bucket.
- **Genre-tagged** by source dir so numbers don't collide across genres:
  `docs/features` → `FDR`, `docs/adrs` → `ADR`, `docs/rfcs` → `RFC`; a custom
  dir falls back to its basename as the tag.
- **Scan-if-exists.** A missing dir contributes nothing; a repo with none of the
  three renders no `## Design records` section at all — so the feature is
  invisible in every repo that hasn't adopted the `docs/` conventions.
- **Malformed records are surfaced, not swallowed.** A record file that can't be
  read, or whose frontmatter is opened with `---` but never closed, is listed in
  a trailing `**⚠ malformed — not indexed**` diagnostic block (capped, with a
  "…and N more" overflow line) rather than silently dropped or mis-bucketed as
  "(unstatused)". The distinction is deliberate: a *terminated* frontmatter with
  no `status:` and a file with *no* frontmatter are both legitimate (they land in
  "(unstatused)"); only a structural break is flagged. This tells the user and
  the agent which files need fixing.

### Config — `[sysprompt].doc-index-dirs`

The default dir list (`docs/features`, `docs/adrs`, `docs/rfcs`) is a built-in
constant. One sweatfile knob overrides it:

    [sysprompt]
    doc-index-dirs = ["design", "specs"]

Semantics (a deliberate divergence from the append default of other string
arrays — scan roots want override, not accumulation): **unset** falls back to the
built-in default; a **non-empty** value replaces it (override-wins down the
hierarchy); **`[]`** clears it, which omits the section — the off switch.

### Wiring change

`sysprompt.Resolve()` today reads env + git + a bounded `repoinfo.Fetch`. It
gains a **merged-sweatfile load** (`sweatfileio.LoadHierarchy` rooted at the
resolved worktree/checkout) to obtain `[sysprompt].doc-index-dirs`. This is local
file I/O — fast, no network — so it is safe before `initialize`, unlike the
deadline-capped forge lookup it sits beside. Because the load is rooted at the
worktree, the docs index is **branch-local**: an FDR added on the session's own
branch appears in that session's fragment. The render stays **best-effort**, and
that guarantee is hard: the fragment is fetched before the agent's `initialize`,
so a malformed doc must never take it down. An absent dir is silent; a broken
record becomes a diagnostic line (above); and a `recover()` converts any
unexpected panic into a warning rather than letting it escape the render.

## Examples

Default (no config) — spinclass's own worktree session fragment gains:

    ## Design records

    **accepted**
    - FDR 0014 — Implicit sessions
    - FDR 0017 — clown session/attach grouping + chat rescope

    **experimental**
    - FDR 0013 — Isolated build worktree for pre-merge hooks
    - FDR 0019 — Native conformist support in spinclass

    **proposed**
    - FDR 0020 — sc run one-shot session
    - FDR 0021 — Design-record index in the dynamic system-prompt

Override the scanned dirs for a repo that keeps records elsewhere:

    [sysprompt]
    doc-index-dirs = ["design", "specs"]

Turn the index off entirely:

    [sysprompt]
    doc-index-dirs = []

## Limitations

- **Index only, by design.** Bodies would blow up the prompt (the FDRs alone
  total ~250 KB); the value is the number · title · status triple.
- **`docs/plans/` is excluded from the defaults.** Plans are freeform working
  docs with no frontmatter/status — not lifecycle-tracked design records. A repo
  can add the dir via `doc-index-dirs`, but its entries land under "(unstatused)".
- **Frontmatter-shaped inputs only.** A dir whose files lack `status` still lists
  (number + title) under "(unstatused)"; a structurally broken record (unreadable,
  or unterminated frontmatter) is reported in the malformed-diagnostic block
  rather than indexed.
- **Per-session token cost.** The section is appended to every session's system
  prompt for a repo that has the dirs; a few dozen records is ~two dozen lines,
  but a repo with hundreds pays proportionally (see Tuning Levers).
- **Deliberately un-abstracted.** This ships the docs-index directly in the
  render path with one config knob — NOT a general provider framework. Adding a
  second substrate later will mean designing that framework and migrating this
  knob into it; that migration is the accepted cost of not abstracting on a
  sample size of one.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| default dirs | `docs/features`, `docs/adrs`, `docs/rfcs` | the three lifecycle-tracked design-record conventions | a fourth conventional dir emerges across repos |
| section entry cap | none | a few dozen records is cheap; premature to cap | a real repo's index bloats the prompt past usefulness |
| grouping | by `status` | surfaces in-flight vs settled at a glance | users prefer flat numeric order, or per-genre grouping |
| render shape | number · title · status | minimal high-signal triple | agents repeatedly need one more field (e.g. date) |

## Future direction — composable `[[sysprompt-sections]]` (North Star, not built)

When a second substrate justifies the abstraction, the docs-index generalizes
into an ordered, sweatfile-driven pipeline of provider-backed sections, following
the repo's dedup-by-name array-of-tables idiom (`[[mcps]]`, `[[start-commands]]`,
`[[remotes]]`):

    [[sysprompt-sections]]
    name = "docs-index"          # the migrated built-in, default-seeded
    kind = "docs-index"
    dirs = ["docs/features", "docs/adrs", "docs/rfcs"]

    [[sysprompt-sections]]
    name = "must-do"
    kind = "dodder-query"
    query = ["!task", "priority-0_must"]

    [[sysprompt-sections]]
    name = "runbook"
    kind = "file"
    path = "docs/AGENT-RUNBOOK.md"

Decided ahead of time so the migration is mechanical: each entry is `name`-keyed
(dedup + render order), `kind`-dispatched to a pure-Go renderer, with a
**`remove = true`** removal sentinel (matching `[[remotes]]`). Kind-specific
params are **flat, `kind`-gated fields on a single union struct** — tommy-
friendly, matching `StartCommand`'s flat-union layout — rather than a generic
`params` map. The `docs-index` becomes the first default-seeded provider (via
`GetDefault()`, as it seeds the built-in `gh_issue` start-command); new substrates
are new `kind`s with no config-schema migration. A `command` kind (splicing an
arbitrary command's stdout) is the highest-risk future provider and would be
deferred until the pure-Go substrates prove the seam.

## More Information

- **#187 / the dynamic fragment** — `internal/sysprompt`, `internal/repoinfo`,
  the clown stdio-bridge `prompts/get` before `initialize` (clown plugin protocol
  RFC-0002 §5). This FDR extends that render path; it does not replace it.
- **FDR 0003 (per-worktree madder blob store)** and the retired static
  `.clown-plugin/system-prompt-append.d/` fragments — the dynamic path replaced
  the static fragments precisely so it could branch on runtime state; the
  docs-index is the next step along that axis.
- **Config idiom precedent** — the `[hooks]` pointer-substruct scalar-override
  pattern (the model for `[sysprompt]`); `[[start-commands]]`/`[[mcps]]`/
  `[[remotes]]` + `sweatfile.GetDefault()` (the model for the future framework).

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown) 0.3.15+b181a40
([commit](https://github.com/amarbel-llc/clown/commit/b181a400f8bc687d7eb953d6741f9522e47cf7c3)).
