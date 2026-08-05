---
status: experimental
date: 2026-06-10
promotion-criteria: |
  experimental -> testing: one real `sc merge` of a nontrivial branch on a
  TTY where (1) the viewport shows the rolling hook tail live during the
  ~minutes-long pre-merge hook (the silent-stage problem is observably
  gone), (2) a failing run persists the failing phase's held tail into the
  final frame, and (3) one MCP `merge-this-session` result reads as plain
  ✓/✗ verdict lines with the resource_link block intact. Plus one
  `sc merge --format ndjson | jq` round-trip validating the wire.
  testing -> accepted: ~1 week of real merges/checks across repos with no
  format-selection surprises (auto picking the wrong renderer), no
  viewport rendering breakage on real terminals, and no agent confusion
  attributable to the plain MCP result shape.
---

# Merge/check on ndjson-crap with a live viewport

## Problem Statement

During the pre-merge hook stage — the slow part of `sc merge` (this repo's
hook runs ~5½ minutes) — a TTY user saw nothing. Hook output streamed to
the madder blob store and a 15-line ring shown only on failure; the TAP
test point for the hook printed only after the hook exited. The terminal
was silent and apparently frozen for the duration. TAP-14, a
result-family-only format, cannot express "stage started" or carry live
output, so no presentation layer on top of it could fix this.

## Interface

`sc merge` and `sc check` (and everything that funnels through them:
`merge-this-session`/`check-this-session`, their `-async` twins,
`MergeImplicit`, merge-on-close) emit **ndjson-crap** (the CRAP-2 wire
format). TAP is retired for these two commands only; every other command
(`sc list`, `sc validate`, …) still speaks TAP, and the repo is
deliberately bilingual.

`--format` for `sc merge` / `sc check`:

- `auto` (default): stdout TTY → `viewport`; piped → `ndjson` (madder
  sync's convention — `sc merge | crap-present` works).
- `viewport`: crap's live bubbletea presenter — spinner, rolling tail of
  hook output, persisted ✓/✗ verdict lines. Renders to **stderr**;
  nothing is written to stdout. Record capture is the `ndjson` mode
  (`sc merge > records.ndjson` auto-resolves to ndjson — no viewport).
- `plain`: boring-terminal escape hatch — verdict-per-line rendering
  (`✓ rebase <branch>`, `✗ pre-merge hook …` with echoed failure output).
- `ndjson`: the raw records.
- `tap`/`table` get a pointed "retired here" error; anything else lists
  the valid vocabulary. (`present.ResolveFormat` is the single seam.)

**MCP handlers** (no TTY, no flag): result text is the **plain
rendering** (`present.RenderPlain` over a buffered Reporter); full hook
output stays behind madder `resource_link` content blocks as before.
Async jobs store the same plain rendering as `ResultText` in `job.json`;
ringmaster's completion wake attaches it as a result blob (#251), and the
clown failed-state wake carries the first `✗ ` line. The live tail (raw
`job.log` hook output, teed into ringmaster's spool) is unchanged — read it
via ringmaster's `status --tail`/`read` (spinclass's own `session-job-status`
was retired, #23).

## Design

`internal/present` owns consumer wiring (`ResolveFormat`, `WithReporter`,
`RenderPlain`, `LineWriter`); the producer side is
`crap/go-crap/v2/crap.Reporter`, threaded through `internal/merge` and
`internal/check` in place of the old `*tap.Writer`.

Emission is **mixed-family** on one stream:

- Merge stages (`pull <defaultBranch>`, `rebase <branch>`,
  `merge <branch>`, `remove worktree <branch>`, `delete branch <branch>`,
  `push` — the implicit flow's push is `push <branch>`) are
  result-family test records on a shared `TestStream`; the orchestrator
  owns `Finish()`.
  `failStep`'s diagnostics (git stderr, exit codes) ride the failing
  record's diagnostic map, echoed by the plain/viewport renderers.
- The pre-merge hook is a single execution-family `Phase` (named
  ``pre-merge hook for <branch>: `<cmd>` ``): `Command`, live `Output`
  records via `present.LineWriter` — the viewport's rolling tail; the
  madder `resource_link:` line also rides the wire here — closed by a
  `node_end` whose verdict is self-sufficient (go-crap ≥ v2.2.x,
  crap#22): on failure, `Phase.FailDiag` rides the diagnostic
  (`command`, `format`, `exit_code`, `elapsed`, failure-output tail,
  `resource_link`) on the `node_end`, and both renderers merge it over
  the synthesized exit-code keys. **Revision 2026-06-11**: the original
  emission paired the Phase with a result-family test point of the same
  description, which double-rendered the verdict (one line per family —
  caught in the first TTY pilot); go-crap v2.2.1 added the `node_end`
  diagnostic so the duplicate record could be dropped, and v2.2.2 made
  the run verdict count failing nodes (crap#24) so the final frame
  reflects a hook failure without a result-family record. The only
  result-family records check itself now emits are the standalone
  no-hook "ok" point and the build-worktree prep-failure point (no hook
  ever ran there, so there is no Phase to carry a verdict).

Single source of truth — no TAP/event drift is possible. The
alternatives (a TAP→viewport bridge; an event seam alongside TAP) were
rejected as translation code the migration immediately obsoletes and a
second emission path that can drift, respectively.

`merge-this-session-async` shares one `crap.Reporter`+buffer between the
synchronous `PrepareMerge` prefix and the backgrounded `FinishMerge`, so
the job result is one continuous stream. `sc close`'s merge-on-close
builds a fresh renderer scope per merge attempt (`runMergeOnClose`).
bubbletea was already a direct dependency (huh dialogs), so the viewport
adds no new terminal-probe exposure.

## Examples

```sh
sc merge                          # TTY: live viewport; piped: ndjson records
sc merge --format plain           # ✓/✗ verdict lines only
sc check --format ndjson | jq .   # wire-level inspection
sc merge > records.ndjson         # auto resolves to ndjson: records captured, no viewport
```

## Tuning Levers

- **Viewport TailLines**: crap's default until real hooks show it's
  wrong. Signal: failure context routinely scrolled away, or the tail
  too tall on small terminals.
- **auto-piped default** (`ndjson` now): flips to `plain` if piping to
  humans (pagers) proves more common than piping to tools.
- **MCP result rendering** (`plain` now): becomes raw records if agents
  prove better at parsing them than reading verdict lines.

## Known Limitations

- **Success-side stage output is not in the result text.** A passing
  hook's output lives only in the madder blob (and the live viewport
  while it ran); the plain rendering shows just the ✓ line. The
  `--verbose` global flag has no effect on merge/check.
- **No success-side hook-format discriminator on the wire.** The
  resolved `[hooks].pre-merge-output-format` appears only in the failure
  diagnostic (`format` key) and the BlobLink MIME type; a successful
  run's records don't say which format the blob was written in.
- **The hook Phase's NodeEnd duration includes post-hook madder/parse
  time.** `Done()`/`FailDiag()` fire after the madder blob write (and, for
  structured formats, the parse of the buffered hook stdout), so the
  wire-reported phase duration slightly exceeds the hook subprocess's
  own runtime (the failure diagnostic's `elapsed` measures only the
  hook).
- **Hook output is opaque.** Even when the hook itself emits
  ndjson-crap (`pre-merge-output-format = "ndjson-crap"`), its records
  travel as flat `Output` lines, not nested phases (see Follow-ups).
- **Detached attestation refusals still speak TAP** — the gate
  (`internal/attestation`) is upstream of the merge stream and was not
  migrated.
- **Viewport and record capture are mutually exclusive** — viewport mode
  does not tee records to stdout. A `--format viewport` run with
  redirected stdout writes nothing to it.

## Follow-ups

- Splice an ndjson-crap-emitting hook's records into the merge stream as
  nested phases instead of opaque `Output` lines (named follow-up from
  the design doc).
- Tee records to a redirected stdout in viewport mode (live viewport +
  captured wire in one run) if real usage wants it.
- ~~Verify in the Task 9 sweep whether `internal/tapblock` (or other TAP
  plumbing) is orphaned and prunable.~~ Done: the sweep confirmed
  `internal/tapblock` is still consumed by `internal/close` and
  `internal/clean` (their nix-gc reap OutputBlocks), so it stays.

## More Information

- Design: `docs/plans/2026-06-10-merge-ndjson-crap-viewport-design.md`;
  plan: `docs/plans/2026-06-10-merge-ndjson-crap-viewport.md`.
- `internal/present/present.go` (format resolution, renderer wiring,
  plain rendering, LineWriter), `internal/merge/merge.go` (stage
  emission, `PrepareMerge`/`FinishMerge`), `internal/check/check.go`
  (`runHookPhase` — the phase-only hook stage),
  `cmd/spinclass/commands_session.go` (CLI `--format` surface),
  `cmd/spinclass/commands_mcp_only.go` (`present.RenderPlain` result
  text, `buildHookResult`), `internal/job/runner.go`
  (`firstFailureLine` — the clown ✗ wake extraction).
- crap#12 (TAP text demoted to a legacy CRAP profile); FDR 0005 (the
  prior merge-this-session output shape), FDR 0013 (build worktree).
