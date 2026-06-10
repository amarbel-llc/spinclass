# Merge/check on ndjson-crap: native records, viewport on a TTY

Date: 2026-06-10
Status: approved

## Problem

During the pre-merge hook stage — the slow part of `sc merge` (this repo's
hook runs ~5½ minutes) — a TTY user sees nothing. Hook output streams to the
madder blob store and a 15-line ring shown only on failure; the TAP test
point for the hook prints only after the hook exits. The terminal is silent
and apparently frozen for the duration.

The ask: when `sc merge`/`sc check` run on a TTY, present a live viewport
(crap/go-crap/v2's bubbletea presenter: spinner, rolling tail of hook
output, persisted verdict lines) at the hook stage.

## Decision

Rather than bridging TAP into the viewport, **merge/check discard TAP
entirely** and natively emit ndjson-crap — the canonical CRAP-2 wire format
(crap#12 demoted TAP text to a legacy profile). The event seam IS the
format: stage-start granularity (`node_start`) is part of the wire, so the
bridge-vs-event-seam tradeoff dissolves. The viewport pattern follows
madder's `sync` (crap.Reporter producer + output_format-style consumer
selection), which is the proven embedding.

Alternatives considered and rejected:

- **TAP-stream bridge** (parse our own TAP into viewport messages):
  smallest diff, but builds translation code the ndjson-crap migration
  immediately obsoletes, and cannot express stage-start.
- **Event-seam interface alongside TAP** (ctx-carried no-op sink):
  safe, but duplicates verdicts across two emission paths (drift risk)
  for the full variant; the hybrid start-only variant still leaves TAP
  as a second format to maintain.

## Producer layer

`internal/merge` and `internal/check` drop their `*tap.Writer` parameter
for a `*crap.Reporter` (`crap/go-crap/v2/crap`). Each merge stage is an
execution-family Phase:

- `pull master`, `rebase <branch>`, `merge <branch>`, `push` →
  `Reporter.Phase(name)` … `Done()` / `Fail(err)`.
- pre-merge hook → `Phase("pre-merge hook for <branch>")`,
  `Command(hookCmd)`, live output via the existing `activity io.Writer`
  seam through a line-splitting adapter into `Phase.Output(stream, data)`,
  then `Done()`/`Fail(err)`. NodeEnd carries exit code and elapsed
  natively.
- `failStep`'s TAP YAML diagnostics (git stderr, exit codes) become
  `Output` lines on the failing phase + the NodeEnd exit code. The
  viewport persists a failing phase's held tail, so failure context
  survives into the final frame.
- madder blob links are untouched: they travel as `[]check.BlobLink`
  return values into MCP `resource_link` content blocks, independent of
  the text stream.

Single source of truth — no TAP/event drift is possible.

**Scope boundary**: only merge/check and what funnels through them
(`sc merge`, `sc check`, `merge-this-session`/`check-this-session` and
async twins, `MergeImplicit`). Other commands (`sc list`, `validate`, …)
keep TAP; the repo is bilingual and documented as such.

bubbletea is already a direct spinclass dependency (huh confirm dialogs),
so importing the viewport adds no new terminal-probe exposure (the OSC 11
concern that keeps crap-present a separate binary does not apply here).

## Presentation layer / CLI surface

`--format` for `sc merge` / `sc check` becomes
`auto` (default) | `viewport` | `plain` | `ndjson`. TAP is no longer a
value for these two commands.

- **auto**: stdout TTY → viewport; piped → raw `ndjson` records on stdout
  (madder sync's piped default; `sc merge | crap-present` works).
- **viewport / plain / ndjson** force that rendering. `plain` is the
  boring-terminal escape hatch (crap's `presentPlain` verdict-per-line
  rendering: `✓ rebase`, `✗ pre-merge hook …` with echoed failure output).
- Wiring: Reporter writes into an `io.Pipe`;
  `viewport.Present(pipeReader, Options{IsTTY, Out, Title})` runs while
  the merge executes and returns when the pipe closes. `ndjson` writes
  stdout directly. The viewport renders to **stderr**, records to
  **stdout**, so `sc merge > records.ndjson` with a TTY stderr shows the
  live viewport AND captures clean records.

**MCP handlers** (no TTY, no flag): result text is the **plain
rendering**; full hook output stays behind madder `resource_link` blocks
as today. Async `ResultText` stores the same plain rendering;
`session-job-status`'s live tail (`job.log` raw hook output) unchanged.

## Consumer migration (single series — no dual period)

- **Clown wake emit**: failed-state extraction moves from first `not ok`
  line to first `✗` line of the plain rendering.
- **bats**: merge/hooks/implicit-session assertions move from TAP shapes
  to plain-rendering shapes; add `--format ndjson` + `jq` wire
  assertions.
- **Docs**: CLAUDE.md "TAP-14 everywhere" carve-out for merge/check; MCP
  tool descriptions stop saying "TAP payload"; FDR documents the format
  vocabulary.
- **`[hooks].pre-merge-output-format` stays orthogonal**: hook output is
  opaque Output lines in v1. Splicing an ndjson-crap hook's records as
  nested phases is a named follow-up.

## Testing

- Unit: drive merge/check with a buffer-backed Reporter; assert on
  decoded records (madder's `emitSyncCrap` seam style). Rendering is
  crap's tested responsibility.
- bats: one lane runs real `sc check --format ndjson` end-to-end and
  validates the wire with jq.

## Rollback

Breaking presentation change, deliberately **without** a
dual-architecture period: a TAP adapter would be bridge code built only
to be deleted, and `--format plain` covers the "just give me lines" need.
Rollback = `git revert` of the series. Risk containment: the change never
touches git operations, session state, blob storage, or the attestation
gate — presentation only.

## Tuning levers

- **Viewport TailLines**: crap's default until real hooks show it's
  wrong. Signal: failure context routinely scrolled away / tail too tall
  on small terminals.
- **auto-piped default** (`ndjson` now): flips to `plain` if piping to
  humans (pagers) proves more common than piping to tools.
- **MCP result rendering** (`plain` now): becomes raw records if agents
  prove better at parsing them than reading verdict lines.
