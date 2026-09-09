---
status: proposed
date: 2026-09-09
promotion-criteria: |
  proposed -> experimental: a sweatfile switches at least one index on and it
  renders in a real clown session — a `## Manpage index` block listing the
  first-party pages by `name(section)` + NAME-derived description, and/or a
  `## Repository index` listing the fleet checkouts by name + flake
  description. Requires the eng-side first-party manpath to exist (see
  Dependencies); until then the mechanism ships inert and this stays proposed.
  experimental -> testing: the pre-`initialize` render stays fast with both
  indexes on (no measurable stall of the clown bridge's `prompts/get`), the
  measured token cost holds near the ~839 estimate for the fleet-sized
  selection, and a page or checkout that cannot be read degrades to a
  diagnostic line rather than a failed render.
  testing -> accepted: ~1 week of real sessions with both indexes on and no
  prompt-bloat complaints, AND evidence about whether agents actually consult
  the indexed pages — the index is a bet that naming a page makes it get read,
  and that bet has not been tested.
---

# Manpage and repository indexes in the dynamic system-prompt

> **Proposed** — the mechanism is built and unit-tested but ships **inert**:
> both indexes are off until a sweatfile selects sources, and spinclass's own
> sweatfile selects none. The manpage index is additionally blocked on an
> eng-side first-party manpath (Dependencies, below). Nothing in any session's
> system prompt changes when this lands.

## Problem Statement

FDR 0021 shipped a design-record index in the dynamic system-prompt fragment —
one substrate, deliberately un-abstracted. Two more are wanted:

- **Manpages.** The eng workspace ships its repo-wide conventions as section-7
  manpages, and spinclass ships its own reference pages. An agent should know
  those pages exist, and what each covers, before guessing from a neighbouring
  repo. This previously lived as a hand-typed static fragment
  (`.clown-plugin/system-prompt-append.d/00-eng-manpages.md`) whose descriptions
  drift from the pages they describe, because nothing regenerates them.
- **Repositories.** A session should be able to tell which repo owns a question
  before researching it in the wrong lane — the research-ownership rule agents
  are given assumes they can name the sibling repos, which today they cannot.

Both want the same shape: one row per item, `name` + a short description scraped
from the item itself rather than hand-written.

## What is NOT built: the composable framework

FDR 0021 named "a second substrate" as the trigger to design the composable
`[[sysprompt-sections]]` pipeline. Two substrates arrive here at once, so the
trigger is re-assessed explicitly rather than silently ignored — and the
conclusion is that it has **not** fired.

The framework's value is *user-defined* sections: arbitrary ordering, and
`kind`s like `file`, `command`, or `dodder-query` that a user composes without a
code change. All three substrates that now exist (`docs`, `man`, `repo`) are
**built-ins with fixed renderers and genuinely different parameter shapes** —
doc dirs are repo-relative scan roots, man sources are host-absolute page
selectors, repo sources are checkout locations. A union struct would paper over
that difference, and an ordering knob would have nothing to order that the
render sequence does not already fix. Three flat, well-named keys on one
`[sysprompt]` table is not worse than one array-of-tables here; it is smaller
and it type-checks.

The real trigger remains a **user-defined** substrate — the first time someone
wants a section spinclass has no renderer for. That is the point at which the
parameter surface stops being knowable in advance, which is what a framework
buys. Recorded here so the next person does not have to re-derive it.

## The provenance finding (why the manpage index is shaped this way)

The obvious ask — "index only the manpages from our own repos" — cannot be
answered by spinclass alone, and this is a property of the system, not a gap in
the implementation.

`~/.nix-profile` resolves to a home-manager profile whose `manifest.nix` holds a
**single** entry: `home-manager-path`, one `buildEnv` symlink farm. Per-package
flake origin is not recorded anywhere in the profile. The only fact recoverable
from a page is its store pname via readlink:

    eng.7.gz       -> /nix/store/…-eng-doc-0.1.0
    spinclass.1.gz -> /nix/store/…-spinclass-0.1.42
    sed.1.gz       -> /nix/store/…-gnused-4.10

and pname is not repo name (`eng-doc` vs repo `eng`; `sc` vs `spinclass`). Any
spinclass-side reconstruction would be inference dressed as structure.

So the boundary is **declared by whatever assembles the profile**, not
reconstructed afterward: eng emits a manpath containing only first-party pages,
and `man-index` points at that one root. The index then needs no membership
logic at all — the root directory *is* the declaration. A name-prefix glob
(`man*/eng*`) remains expressible and is a fine stopgap, but it is explicitly
not the intended shape.

Relatedly, and recorded so it is not re-investigated: `forge.starbrandshoes.com`
and `code.linenisgreat.com` are **one Forgejo instance under two planes** (API/
canonical and vanity-git respectively; papi FDR-0016 "split planes"). A
host-based membership test would therefore be both wrong and unnecessary.

## Interface

### Sections

Two markdown sections, appended after the template body and after the FDR 0021
`## Design records` block, ordered most-specific-first (this repo's records →
the conventions its host documents → the neighbouring repos). Each is a heading,
one instruction line, and the rows:

    ## Manpage index

    These are authoritative for the conventions they describe — consult the
    matching page before guessing. Read them with the `man.*` MCP tools
    (`man_toc`, then `man_section`).

    - `eng(7)` — personal development environment monorepo
    - `eng-direnv(7)` — .envrc chain, home-manager profile augmentation

    ## Repository index

    Sibling repositories checked out on this host. Research and changes belong
    in the owning repo's own session — hand a question off rather than
    re-deriving it here.

    - `spinclass` — Spinclass: shell-agnostic git worktree session manager
    - `tommy` — Tommy: a TOML library for Go

### Config — `[sysprompt].man-index` and `[sysprompt].repo-index`

Both take an array of **source specs**, resolved identically: a leading `~` and
any `$VAR` are expanded, then each result is split on `:`, so a bare `$MANPATH`
expands to its individual roots. A spec containing `*`, `?` or `[` is globbed;
anything else is literal. Duplicates are dropped; a spec matching nothing
contributes nothing.

    [sysprompt]
    man-index  = ["~/.local/share/first-party-manpages/linenisgreat/man"]
    repo-index = ["~/eng/repos"]

The `man-index` value above is the first-party manpath eng emits (see
Dependencies); the name deliberately avoids "eng" so it survives eng being
superseded. Both stanzas belong in the fleet root sweatfile, which eng owns —
spinclass's own sweatfile selects neither.

- **`man-index`** — a directory spec is a *manpath root*, scanned one level into
  its `man*/` section dirs; a glob names page files directly. Rows are
  `name(section)` from the **filename** (what `man(1)` accepts; `awk.1` names
  `gawk` in its NAME line, so the filename is the more useful key) plus the
  description scraped from the NAME block.
- **`repo-index`** — a spec that is itself a checkout is indexed directly; any
  other directory has its immediate children scanned for checkouts, which is
  what makes a directory of repos a valid selector. Descriptions come from the
  `description` attribute of `flake.nix`, else the README's first prose line.

**Merge:** override, not append — identical to `doc-index-dirs`, and for the
same reason (these are scan roots). Non-empty replaces, `[]` clears an inherited
selection, nil inherits.

**No built-in default**, which is the one deliberate divergence from
`doc-index-dirs`: the useful set is host- and profile-specific rather than a
repository convention, so both indexes render nothing until a sweatfile opts in.

### NAME parsing

Designed against a measured census of this host's 1233 profile pages rather than
an assumed format — `just explore-manpage-name-formats` reproduces it:

| shape | count |
|---|---|
| man(7) `.SH NAME` / `.SH "NAME"`, ` \- ` separator | 699 |
| man(7), plain ` - ` separator (scdoc output) | 448 |
| mdoc(7) `.Nd` | 36 |
| no NAME section | 45 |

All four are handled. Three consequences worth stating, because each was a bug
before the census corrected it: the separator must be matched **space-delimited**
(a description legitimately contains `agent\-backed`); roff font escapes
(`\fBage\fR`) must be stripped *before* splitting; and formatting macros (`.PP`)
sit between the heading and the content line in scdoc output. A page with no
NAME section is still listed, without a description.

## Bounding

The selectors accept bulk sources, so both indexes are bounded twice:
`[sysprompt].index-limit` (`defaultIndexLimit` 400 rows each; `<= 0` removes the
cap) and `indexScanTimeout` (1.5s across both scans, checked periodically during
the walk). Only the first 8 KiB of a page and 4 KiB of a flake/README are read.

**Ordering is part of the bound, learned the hard way.** Pages were originally
sorted by file path, which groups them by section directory — all of `man1/`
before any of `man7/`. On the first real deployment (the fleet's first-party
manpath: 329 pages, 266 of them in `man1`) a 200-row cap therefore rendered
200 per-subcommand `man1` pages and dropped *every* `man5`/`man7` page — which
is to say every `eng-*(7)` convention page and `spinclass-sweatfile(5)`, the
exact pages the index was built to surface. The cap was doing its job; the
ordering made its cut pathological. Pages are now named first (filename-only,
no I/O) and sorted by the label they render under, so truncation takes a spread
rather than deleting whole sections.

The cap's original sizing also assumed the bulk-selector footgun was the common
case. Under the provenance finding it is not: the intended source is a curated
manpath, so the default is now set to clear one, and `index-limit` exists so a
larger curated source can raise or remove it deliberately.

A scan that hits either bound **says so** — `…and N more (not indexed; narrow
the selector)` — including when it indexed nothing at all. That last case was a
real bug caught in review: an entirely deadline-starved scan originally rendered
no section, silently indistinguishable from "nothing selected".

Unreadable inputs and unsupported compressions land in a capped `⚠ not indexed`
block rather than being dropped, matching FDR 0021. A `recover()` in each
renderer keeps a malformed input from taking down a render that happens before
the agent's `initialize`.

## Measured cost

`just explore-index-prompt-cost`, against this host (chars÷4, a heuristic, not a
tokenizer):

| index | rows | chars | ~tokens |
|---|---|---|---|
| manpage (the 13 `eng*(7)` pages) | 13 | 980 | 245 |
| repository (34 fleet checkouts) | 34 | 2376 | 594 |
| **total** | 47 | 3356 | **839** |

For comparison, an unfiltered `$MANPATH` would be 1233 pages — roughly 18k
tokens before the cap, which is precisely why the cap exists and why a
purpose-built manpath is the intended selector rather than a broad glob.

## Dependencies

The manpage index is inert until **eng** emits the first-party manpath. That is
eng's lane; the contract spinclass needs is only *a stable directory that is a
manpath root* (directly containing `man1/`, `man7/`, …). One feasibility note
handed over with it: fleet packages have **no separate `man` output** —
spinclass generates its pages into `$out/share/man` in `postInstall` — so the
aggregation is `buildEnv { paths = …; pathsToLink = [ "/share/man" ]; }` behind
a stable symlink, not `map (p: p.man)`. Membership is eng's definition (the
fleet flake inputs that ship a default package, plus eng's own `eng-doc`).

The repository index has no such dependency and works today.

## Limitations

- **Index only**, as with FDR 0021: name + one-line description, no bodies.
- **The manpage index cannot select first-party pages by itself.** See the
  provenance finding — that boundary must be declared upstream.
- **The forge's repo description is not consulted.** It would be a better source
  than a scraped `flake.nix` line, but it is a network round-trip *per repo*,
  and a directory-of-checkouts selector resolves dozens; that cannot fit inside
  a budget that must not stall a pre-`initialize` request. `internal/repoinfo`
  already pays one such call for the single current repo, which is affordable
  precisely because it is one.
- **README fallback is heuristic.** It skips headings, badges, HTML, quotes and
  list items and takes the first prose line, which is right for a conventional
  README and arbitrary for an unconventional one. `flake.nix` is the reliable
  source; every fleet repo has one.
- **Descriptions are as good as their sources.** A stale `flake.nix`
  description renders stale here. That is still strictly better than the static
  fragment this replaces, where the description was hand-typed somewhere else
  entirely.
- **Unproven value.** The index asserts that naming a page makes an agent read
  it. Nothing here tests that; it is the open question behind the
  testing→accepted criterion.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| `index-limit` | 400 (`defaultIndexLimit`), sweatfile-overridable; `<= 0` uncaps | sized to clear the fleet's 329-page first-party manpath, still far below an unfiltered `$MANPATH` (1233) | a legitimate curated source exceeds 400, or the per-session cost stops being worth it |
| `indexScanTimeout` | 1.5s | local I/O over a bounded set should finish in tens of ms; this is a backstop, not a budget | the deadline line appears in a real render |
| `maxDescLen` | 120 chars | fits a NAME description and a flake description without wrapping | descriptions read as clipped |
| description sources | flake.nix → README | flake is authoritative and universal in-fleet; README is the fallback | the forge description becomes cheaply available (a local cache), or README noise dominates |
| section order | records → manpages → repos | most-specific-first | agents demonstrably need the widest context first |
| head-read bounds | 8 KiB page / 4 KiB flake+README | NAME and `description` are header material by construction | a real page or flake puts them past the bound |

## Rollback

Both indexes are **off by default**, so landing this changes no rendered
fragment. Rollback of an enabled index is removing its sweatfile key, or setting
it to `[]` to clear an inherited selection — one line, no revert. The static
eng manpage fragment remains as-is until whoever owns it retires it, so the two
approaches can coexist; nothing here removes it.

## More Information

- **FDR 0021** — the design-record index this extends: the render path, the
  `[sysprompt]` table, the override-not-append merge semantics, the
  best-effort/`recover()` guarantee, and the deferred `[[sysprompt-sections]]`
  North Star this record re-assesses.
- **FDR 0014** — the worktree vs main-checkout split both indexes render under.
- **papi FDR-0016** — the split-planes model behind the two forge hostnames.
- `just explore-manpage-name-formats` / `just explore-index-prompt-cost` — the
  recipes that produced the census and the cost table above.

---

:clown: drafted by [Clown](https://code.linenisgreat.com/clown) 0.4.4+6d3907c
([commit](https://code.linenisgreat.com/clown/commit/6d3907cb8d15a0300eb7149e5d2b891e70d9b4c6)).
