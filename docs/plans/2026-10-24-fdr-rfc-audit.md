# FDR / epic audit, 2026-10-24

Scope: `docs/features/` 0001-0036 and `docs/epics/`. `docs/rfcs/` does not
exist (V: `docs/` holds only `epics`, `features`, `plans`).

Method: four auditors each compared a slice of the records against code,
manpages (`doc/*.scd`) and neighbouring records, classing each finding as
PLAIN DRIFT (a fact is wrong and the fix is mechanical) or NEEDS DECISION
(a status, design or behaviour choice for the operator). A second pass
re-verified each PLAIN DRIFT finding at the cited location before editing.
Evidence classes: V observed (file:line), R read in a doc, I inferred.
Issue (#NNN) open/closed states were not checked anywhere.

Edits were limited to `docs/features/*.md`, `docs/epics/*.md` and `doc/*.scd`.
Historical records got dated notes instead of rewritten reasoning; manpages
were corrected in place. No status line was changed.

## Summary table

| doc | declared status | status after this change | findings | outcome |
|---|---|---|---|---|
| 0001 worktree-local session state | proposed | proposed | 4 | 3 applied (1 partial), 1 needs decision |
| 0002 madder integration | accepted | accepted | 1 | applied |
| 0003 per-worktree madder store | accepted | accepted | 3 | applied |
| 0004 direnv template plugins | exploring | exploring | 2 | applied |
| 0005 merge output shape | superseded by 0015 | unchanged | 2 | 1 applied, 1 partly not applied |
| 0006 spawn sibling sessions | proposed | proposed | 4 | 3 applied, 1 needs decision |
| 0007 pre-merge skill attestation | experimental | experimental | 2 | applied |
| 0008 per-worktree dodder repo | exploring | exploring | 3 | 1 applied (manpage), 1 not applied, 2 need decision |
| 0009 cross-session chat monitor | superseded by 0017 | unchanged | 1 | applied (manpage) |
| 0010 clown job wakeup producer | accepted | accepted | 3 | 2 applied, 1 not applied |
| 0011 remote sessions | experimental | experimental | 2 | applied |
| 0012 session presence | proposed | proposed | 1 | needs decision |
| 0013 isolated build worktree | testing | testing | 3 | 2 applied, 1 not applied |
| 0014 implicit sessions | experimental | experimental | 3 | applied (FDR + manpage) |
| 0015 merge ndjson-crap viewport | experimental | experimental | 1 | applied |
| 0016 clown session contract | proposed | proposed | 2 | 1 applied, 1 needs decision |
| 0017 clown session attach/grouping | experimental | experimental | 4 | 3 applied, 1 needs decision |
| 0018 pre-merge repair phase | proposed | proposed | 3 | 2 applied, 1 needs decision |
| 0019 native conformist support | testing | testing | 3 | 1 applied, 1 not applied, 1 needs decision |
| 0020 sc run | proposed | proposed | 3 | 1 applied, 1 not applied, 1 needs decision |
| 0021 design-record index | experimental | experimental | 4 | 3 applied, 1 partly not applied |
| 0022 per-repo merge queue | testing | testing | 0 | clean |
| 0023 post-merge hook | experimental | experimental | 0 | clean |
| 0024 freshened base branch | experimental | experimental | 1 | applied |
| 0025 stacked queued merges | experimental | experimental | 0 | clean |
| 0026 named post-merge targets | experimental | experimental | 0 | clean |
| 0027 resurrect | accepted | accepted | 2 | 1 applied, 1 needs decision |
| 0028 forge push credentials | accepted | accepted | 1 | applied |
| 0029 detached landing worktree | experimental | experimental | 1 | applied |
| 0030 sysprompt man/repo indexes | proposed | proposed | 1 | applied |
| 0031 pre-merge exemption predicates | proposed | proposed | 0 | clean |
| 0032 principals/handles/provenance | proposed | proposed | 1 | needs decision |
| 0033 one-shot plugin protocol | proposed | proposed | 0 | clean |
| 0034 spawn with clown profile | proposed | proposed | 3 | 2 applied, 1 needs decision |
| 0035 session confinement | proposed | proposed | 2 | 2 need decision |
| 0036 grit store tools | proposed | proposed | 4 | 2 applied (+1 manpage), 2 need decision |
| epics/README.md | n/a | n/a | 0 | clean |
| epics/0001 trustworthy-agent-delegation | active | active | 1 | applied |

Totals: 53 PLAIN DRIFT findings audited; 16 NEEDS DECISION. Some PLAIN
DRIFT findings were applied in several files (the shared manpage items), so
the lists below count by finding.

## Applied

Manpage fixes (`doc/`):

- `spinclass-sweatfile.5.scd` `pre-merge-output-format`: now lists `ndjson-crap`
  and the closed set for `sc validate` (0005-1; V, `validate.go:177-196`).
- `spinclass-sweatfile.5.scd` `on-attach`: `[session].start/resume` ->
  `[session-entry]` (0001-4; V).
- `spinclass-sweatfile.5.scd` `on-detach`: dropped the deprecated
  `[session].liveness-probe` reference, now "decided from clown presence"
  (0001-4; V).
- `spinclass-sweatfile.5.scd` `spawn-entry` and `model-flags`: removed the
  `sc fork --brief` / `fork-session` references (0006-4; V, fork's detached
  mode removed in #262 per FDR 0006).
- `spinclass-sweatfile.5.scd` `tombstone-retention`: removed the chatroom
  sentence (0009-1, 0017-3; V, no chat code remains).
- `spinclass-sweatfile.5.scd` `post-merge`: removed the implicit-session
  applicability and the `MERGED_BRANCH == DEFAULT_BRANCH` remark (0014-3; V,
  implicit merge refused, #317).
- `spinclass-sweatfile.5.scd` `disable-implicit-sessions`: any branch (not
  detached), no chat, merge unsupported (#317), `sc check` works (0014-2; V,
  `hooks.go:274,291`).
- `spinclass-build-pins.7.scd` dodder section: added the auto-registered
  `dodder` MCP server and the `[[mcps]]` override (0008-3 manpage half; V,
  `worktree.go:272-290`).
- `spinclass-grit.7.scd` KNOWN LIMITATIONS: replaced the misstatement of FDR
  0036 on `force` with "FDR 0036 agrees: plain force asks" (0036-2; V).

FDR and epic fixes (`docs/`):

- 0001: `sweatfile.Apply` -> `apply.Setup`, `sweatfile.writeEnvrc` ->
  `apply.writeEnvrc`, dead manpage path, stale line-number refs replaced by
  symbol names (0001-2, 0001-3; V).
- 0002: Limitations no longer say nothing is implemented; dated note that
  idea 2 shipped as FDR 0003 (0002-1; V).
- 0003: dead manpage paths -> `doc/*.scd`, "Open question" heading renamed to
  "Activation model (decided: Candidate F)", `mkSpinclass` signature note
  (0003-1..3; V/R).
- 0004: FDR 0003 chose Candidate F, not A; dead `CLAUDE.md` "Custom start
  commands" refs -> `doc/spinclass-start-commands.7.scd` (0004-1, 0004-2; V).
- 0005: dead manpage path (0005-2 part; V).
- 0006: `[session]` -> `[session-entry]` in snippets and prose, dated note for
  async `spawn-session` return shape/timeouts, `fork-session` note
  (0006-2, 0006-3; V).
- 0007: promotion-criteria wording ("Promoted to experimental"), amended-by
  FDR 0035 note on state location, dead `state.go` and manpage refs
  (0007-1, 0007-2; V).
- 0010: Interface sentence now `$RINGMASTER_BIN`/`PATH`, historical notes on
  the chat paragraph and example, References symbol list corrected
  (0010-1, 0010-2; V, `clown.go`).
- 0011: `"--"` added to the default attach template, list command and
  example; leading-`-` destination refusal documented (0011-1, 0011-2; V,
  `remote.go:35`, `query.go:33`).
- 0013: amendment note: merge queue/detached landing supersede the ff-only
  flow; current `PrepareMerge` sequence (0013-1, 0013-2; V, `merge.go`).
- 0014: `MergeImplicit` example and refs marked removed (#317) (0014-1; V).
- 0015: stream-routing bullet for session-record warnings (#405) (0015-1; V,
  `present.go`).
- 0016: `clown job` -> `ringmaster job`; chat bullet marked historical
  (0016-1 PLAIN part; V).
- 0017: banner and Status paragraph updated to landed state, "current state"
  subsection marked historical (0017-1, 0017-2; V).
- 0018: implicit-session limitation marked historical (#317), autostash
  amendment on the dirty-worktree refusal (0018 PLAIN 1, 2; V).
- 0019: More Information refs to `conformistFmt`/`conformist.toml` annotated
  as replaced by `conformistEval`/`conformist.nix` (0019-2; V, `flake.nix`).
- 0020: Interface synopsis lists current flags (0020-2; V, `run.go`).
- 0021: default dir list (`docs/epics`/`EPIC`), FDR count qualified,
  `indexScanTimeout` and FDR 0030 pointer (0021-1, 0021-2 part, 0021-3,
  0021-4; V).
- 0024: hello deadline "5m for spawn-session, 60s for sc spawn" (V).
- 0027: confined-caller refusal line (0027-2; V, `resurrect_cmd.go:48`).
- 0028: `merge.pullDefault` -> `merge.fetchTarget (formerly pullDefault)` (V).
- 0029: `pushInsteadOf` -> `insteadOf` (V; FDR 0028 line 33 states this).
- 0030: dated note that `00-eng-manpages.md` was removed from the repo (V).
- 0034: banner, Interface and Limitations notes for landed phase 2, historical
  note on the Not-implemented paragraph, rows 2b/2c now cite
  `harnessScrubbedEnvVars` (0034-1, 0034-2; V, `spawn.go:516`).
- 0036: header and Limitations no longer say "design only" (0036-1; V).
- epics/0001: `children` now lists FDR 0034 and FDR 0035; appendix
  confinement row cites FDR 0035 (V).

## Not applied

- 0001-3 (partial): `sweatfile.ApplyClaudeSettings` row left as is; no such
  function was found (V: rg), but its current owner was not identified.
- 0005-2 (partial): stale line ranges in the superseded record and the
  "FDR 0015" vs "FDR-0017" header style were left; low value, no ground
  truth for the old ranges.
- 0008-3 (FDR half): replacing the "Open question"/"Lean: C first" text with
  "Decided" depends on 0008-1 and 0008-2 (a status and a behaviour decision).
  Only the manpage half was applied.
- 0010-3: cosmetic `clown job read` spelling; the doc already has a
  "read as ringmaster" note.
- 0013-3: autostash story is an undocumented-decision gap, not a text
  correction; see Shipped but undocumented.
- 0014 (Storage section note): unverified whether implicit sessions also get
  a frontend record (I); not edited.
- 0019-3: commit and issue references not checkable (I).
- 0020-3: `--format ... TAP rejected` claim not independently verified (I).
- 0021-2 (part): the example block showing FDR 0020/0021 statuses and the
  "Verified live against this repo's 21 FDRs" line were not refreshed; only
  the count in the header was qualified.

## Needs decision

Status calls (all unchanged; suggestions only):

- 0001-1 (V): `proposed`, but index dir, tombstones, migration, on-attach/
  on-detach hooks and authoritative records shipped; `sc attach`,
  `running-attached`/`dead` states, `attached_pid` and `sc migrate-state` did
  not, and the promotion criteria still cite `sc attach`. Options: (a)
  `accepted` with an amendment saying the attach/probe slices were superseded
  by FDR 0017; (b) split: accept storage/index/tombstones, mark the attach
  lifecycle superseded by 0017; (c) leave `proposed`.
- 0006-1 (V/I): shipped and in daily use. Options: (a) `accepted` with a
  promoted-on note citing production use; (b) leave `proposed` until the
  operator attests; (c) `accepted` and rewrite the locked-in sections to the
  current shape.
- 0008-1 (V): `exploring` is false; all three promotion questions are answered
  in code (Option C block, key from `info-ssh_agent` at `sc start`, MCP
  auto-registered). Options: `accepted`, or `proposed`/`experimental` if the
  Option A+B follow-up should stay tracked (no filed issue seen, I).
- 0008-2 (R/V): FDR leans "skip with a diagnostic" on a locked agent; code
  hard-fails worktree creation (`dodder.go:52-55,102-104`), and the build-pins
  manpage matches the code. Options: accept hard-fail and record it, or change
  the code to skip with `not ok`.
- 0012-1 (V): never implemented; liveness and the `sc list` count come from
  clown presence (`internal/clown/presence.go`), and 0016 says 0012 is folded
  in. Options: A. `superseded by FDR-0017` (+ pointer to RFC-0014 presence);
  B. keep `proposed`, rescope to the agent-beacon half only; C. withdrawn.
- 0016-2 (V): promotion criterion needs `chat-send`, which no longer exists.
  Options: A. `superseded by FDR-0017`; B. keep `proposed`, rewrite criteria
  around #169 Phase 1 and presence liveness; C. `accepted` for env hygiene +
  liveness join, chat content moved to history.
- 0017-ND3 (V/R): `experimental` vs `testing`; spinclass-side landings are
  complete, the multi-clown group-send exercise is clown-owned. Options: A.
  stay `experimental`; B. promote to `testing`, track the clown criterion in
  RFC-0013/0014.
- 0018-ND4 (V/I): implementation and `repair`/`disable-repair`/
  `repair-timeout` knobs are shipped and evidently used (#345, #401, #402).
  Options: A. `experimental` (record the live pass); B. `testing` if the soak
  is considered met; C. leave `proposed` until the checklist is signed off.
- 0019-1 (V): premise reversed: FDR says hand-written `conformist.toml` +
  `lib.mkToolchainHooks`, code uses `conformist.lib.evalModule` with
  `presets.eng`, `presets.eng-go`, `./conformist.nix` (`flake.nix:292-299`);
  `conformist-pre-commit` hook name survives. Options: (a) keep `testing`,
  add a "superseded in part" amendment; (b) mark superseded and write a new
  FDR for the module adoption; (c) rewrite the body.
- 0020-1 (V): implemented (`internal/run/run.go`, 16 Go tests,
  `zz-tests_bats/run.bats`). Options: promote to `experimental` with a DONE
  note, or hold until criterion (5)/bats contents are checked (I).
- 0027-1 (V): `accepted` with empty `promotion-criteria` and no bats coverage
  (Go tests only). Options: retroactive criteria note; demote to `testing`/
  `experimental` until bats exists; leave.
- 0032-1 (V): `proposed -> experimental` criterion (slice 0 landed) is met in
  code and tests; slices 1-4 are unbuilt. Options: (a) promote to
  `experimental` with an evidence line (the 0028 pattern); (b) keep
  `proposed` because the record is an umbrella. Recommendation in the part
  file: (a).
- 0034-3 (V/R): phase 1 landed; hello-with-real-profile is explicitly "Not
  exercised" (R). Options: (a) `experimental` with the caveat recorded; (b)
  hold at `proposed` until a real-profile spawn is observed; (c) evaluate
  against the `testing` criteria given phase 2 code landed (piggy dependency
  not checkable).
- 0035-1 (V/I): `proposed -> experimental` criteria met in code; the
  `testing` criteria need a passing live run. Options: (a) `experimental`;
  (b) promote and record a live `just debug-confinement-check` run; (c) hold.
- 0035-2 (I): "the check cannot pass today ... only F2 holds" (2026-10-19)
  is a dated measurement; other records describe units running. Options:
  date-stamp as historical, or re-measure and replace.
- 0036-3 (V): `sc moxin-export` and decision 6 are promised (section 7,
  step 6) but not built (only a comment at `grit_perms_cmd.go:43`). Options:
  (a) strike section 7 and decision 6 as deferred; (b) keep as an explicit
  open item with a tracking issue and fix the comment wording; (c) build it.
- 0036-4 (V): `proposed -> experimental` criterion says "behind a declared
  opt-in", but the tools are in the default catalog. Options: (a) promote to
  `experimental`, reword the criterion; (b) keep `proposed` until the moxy
  grit decision (moxy #482) settles; (c) leave and use later steps as
  `testing` criteria.

## Shipped but undocumented

- `sc revoke-orphans` (`cmd/spinclass/revoke_orphans_cmd.go:29`): in no FDR,
  manpage or CLAUDE.md. Candidate: FDR 0028 lifecycle step 5 and
  `spinclass-sweatfile(5)` `[auth]`.
- Handle commands `grant-session-handle`, `release-session-handle`,
  `list-handles`, `whoami`: FDR 0032 only; no `doc/*.scd` page.
- `profile-grant-bless`, `profile-grant-import` (FDR 0034 only),
  `confinement-check`, `confinement-bind` (FDR 0035 only), `grit-perms`
  (FDR 0036 step 6), `close-self-run`: no manpage.
- Merge autostash (#416; default on, `--no-autostash`, MCP `autostash`): only
  in `spinclass-grit(7)` lines 330-340; no FDR owns it, and it changes the
  reasoning in FDR 0013 and 0018 (notes added there only point at it).
- Merge-driver preflight (#324): CLAUDE.md only (I).
- Session-record warnings on the merge/check stream (#405): now a bullet in
  FDR 0015, no standalone record.
- `sc implicit-session-key`: FDR 0014 prose only; no `doc/*.scd`.
- `docs/epics` as an index genre (`EPIC`): the sweatfile manpage, and now
  FDR 0021 (this change).
- `sc fork` still registered (`commands_query.go:209`), create-only; documented
  only as a removal note in FDR 0006.
- Session states `running-detached` and `worktree-gone` (`listrow.go:13`)
  absent from FDR 0001's state machine (I).
- `sc run --allow-no-credential`, `--post-merge H` documented in FDR 0026
  prose, now pointed to from FDR 0020.

## Recommendations outside the allowed files

- `AGENTS.md`/`CLAUDE.md` "CLI Commands" omits `revoke-orphans`,
  `confinement-check`, `confinement-bind`, `profile-grant-bless`,
  `profile-grant-import`, `grit-perms`, `close-self-run`.
- Comments at `cmd/spinclass/commands_mcp_only.go:1092,1171` still say
  "chat-read cursor" / "chat-send" (cosmetic).
- Comment at `cmd/spinclass/grit_perms_cmd.go:43` mentions "the moxin export"
  (see 0036-3).
- `docs/plans/2026-10-04-188-sync-hook-cancellation.md`, cited by CLAUDE.md,
  was not found by one auditor's partial glob; it does exist (V, glob on
  review), so nothing to do.
- A short FDR (or an amendment to 0013) recording the autostash decision.
