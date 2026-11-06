# Open-issue grooming: spinclass (2026-11-06)

- Date: 2026-11-06. Author: spinclass/light-cedar/bozo, on the operator's
  idle-time ask relayed by circus/eager-holly. Read-only: no issue was closed,
  edited, labelled or commented; no build, test or nix evaluation was run
  beyond this docs commit's pre-commit hook. No new issue was filed (§8 says
  why).
- Input: `smith issue-list` (state open) returned 166 issues on origin/master
  544cf1e. The prior record is `2026-10-25-issue-triage.md` (177 issues,
  self-described as unverified); this note builds on it and corrects it (§7).
- Method: four read-only passes classified by range from the commit log, the
  code and the design records; three further passes read the bodies (and
  comments where needed) of 66 issues: every issue filed since 2026-10-25 and
  every row the first passes marked uncertain. Rows that still rest on the log
  and code alone, without the body, are marked **(no body)**. I re-verified
  the three code findings of §8 myself; the per-issue rows are compiled from
  the passes and spot-checked, not re-derived one by one.
- Labels: **R** read in code, docs or the issue; **V** observed in tool output;
  **I** inferred; **(?)** unsettled, with what would settle it.

## 1. What changed since 2026-10-25

- Closed since: 49 of the prior record's rows. From its "fixable now" tier:
  423 413 412 388 378 377 373 371 368 365 353 352 348 342 340 334 333 329 320
  308 305 283 280 247 240 234 231 230 215 211 192 177 138 81 79 52 49. From
  "needs a decision": 398 372 369 361 341. From "stale": 392 389 380 379 311
  180 152. (V: absent from today's open list.)
- Filed since: 32 issues numbered 427 and up (465 464 462 460 459 458 457 456
  453 452 451 450 449 448 447 446 444 442 441 440 439 438 437 436 435 434 433
  432 430 429 428 427). Every one is classified below with its body read.
- Still open although the prior record marked them done or stale (its tier
  d): 422 419 414 407 403 264 260 224 209 196 195 189 188 184 172 88 83 75 69
  59. Each is re-judged below; most are proposed for close with evidence.
- Arithmetic check: 177 − 49 + 32 = 160, not 166, so six open issues were
  missing from the prior record's tables (I; its own counts were "a 45, b 92,
  c 13, d 27"). Nothing here depends on which six.

## 2. Proposed closes (operator decides; nothing was closed)

Fixed on master, with the landing and what was seen in code or docs:

| # | title (short) | evidence |
|---|---|---|
| 462 | confined spawns gown at the door | 468df88 (spawn env carries `[direnv.dotenv]`), 65debdd (resume half); pilot result 77b79c1 |
| 459 | pre-commit re-eval fetches unlocked inputs | 868aabb: the dispatcher's formatter is a store path frozen from the landed base; FDR 0019 amendment; #464 tracks the deferred re-review |
| 457 | in-unit moxy from the profile moxy table | 3496a0a (`MoxyTable`, `checkConfinedToolSurface`), 31814be (stand-in dropped, clown relinked) |
| 453 | confined session with no resume entry | 900aec6: relaunches confined through the clown default resume |
| 446 | `restart-preflight --rearm` | 11c2c12: `runRestartRearm` (restart_cmd.go:594), bounds `maxRestartRearms`=2 and the 2 m window, tests in restart_rearm_test.go. "Deploy" is outside the repo (?): check the installed version on the hosts |
| 422 | gate on forced CLI verbs | c743a1d: the hook denies an agent's Bash use of sc session verbs (`internal/delta/hooks/agent_sc_guard.go`); c628262 shape tests; operator ruling recorded in FDR 0037 Limitations |
| 414 | godyn and dagnabit layout lane | e4766b5..22e43a0 (seven levels, `internal/0`..`internal/foxtrot`), lint-layout in the gate |
| 411 | ride out a short ssh-agent outage | 8ec4808; `internal/charlie/auth/auth.go:454` calls `agentretry.Do` |
| 407 | `--repin-profile` as a card-signed act | ae9efb7, e9d4b69, c749aef, a52df9a, a2af7f8; FDR 0034 "Visibility and record" documents the `piggy-escalation-v1` transition |
| 392 | record a worker's own principal | 389d597; `hooks.go:237` `recordOwnPrincipal` |
| 389 | close-this-session | 51fd193; `cmd/spinclass/close_self_cmd.go`; FDR 0035 |
| 380 | worktree-scoped stash and rebase | 84825e9, 8f339f9: per-worktree stash shelf, ownership-based rebase verdicts (`store/git/classify.go:247-250, 510-536`); FDR 0036 §4 |
| 338 | FDR placeholder: session confinement | FDR 0035 exists and names #338 as its companion (0035:24-26). (?) Confirm the issue tracked the record, not the whole programme |
| 322 | tommy header drift fails the cascade | codegen-repair lane (flake.nix:98-100, 283-288; fac9139, f67d6fc, a0c3305); 81e3395 bumped tommy with no manual regen. (?) Confirm that commit touched `sweatfile_tommy.go` |
| 260 | scope fallback after systemd-run failure | 66a0112 (fall back with a warn), 75ed258 (`require-hook-scope` opt-in); #352 was the follow-up and is closed |
| 224 | forge type and owner in the session block | `internal/bravo/sysprompt/templates/worktree.md.tmpl:24-39` renders owner, forge and the forge-workflow section (no body) |
| 209 | spawn-session hangs behind background jobs | a26bac0 (#266): `LaunchDetached` returns at once, `WaitHello` runs as a background job (spawn.go:52-77, 662-706). (?) The historic hang's cause was never diagnosed; the blocking call is gone |
| 195 | devShell posture for hooks | pre-merge and repair run under `direnv exec <worktree>` when `.envrc` exists (hookrun.go:94, 100, 520-524); pre-commit is frozen to a store path (868aabb); each documented in sweatfile(5):311, :589, :1739. (?) No single FDR as asked |
| 189 | pre-commit hook not installed into a live worktree | fe81bd7 `sc rebuild` with the staleness fingerprint; `auto-rebuild-on-resume` (shop.go:493-502). Remainder is #191 |
| 188 | kill the pre-merge tree on interrupt | 613d00e, 295122f, f7836fe, c3ba6d2, 2222425 (task series); hookrun.go:563 sets Setpgid on the cancel path |
| 172 | restructure internal/ onto dagnabit levels | same landing as #414; no `pkgs/` facade exists, so that question resolved by omission |
| 164 | git_sync vs the session branch during an async merge | FDR 0013 (hook in a pinned build worktree, 1520353) and the landing target (#315, `internal/alfa/landing/landing.go:4-11`) answer both halves |
| 88 | `operation_viewport` on `sc merge` | commands_session.go:221 documents `--format auto|viewport|plain|ndjson`; FDR 0015 shipped |
| 75 | hook blob holds only the tail | check.go:531-550: raw mode feeds the full stream to madder (`MultiWriter(madderStdin, ring, lw)`); the ring is the diagnostic tail only. (?) No fixing sha; if it recurs it is madder's writer |
| 69 | remove `sc merge` passthrough mode | d78536f dropped TAP and passthrough; no `PassthroughArgs` on merge (only exec and run) |

Duplicates: **403** of #414 (godyn is the backend, dagnabit the layout lane,
both landed); **397** of #425 (same test, see §8).

Stale or superseded: **460** (body: "revisit once #459 has a position"; #459's
frozen hook removed the commit-time `nix develop`, so the issue shrinks to
"what else copies into the store", a re-read against 868aabb is due);
**311** (root cause a cmd/go `git init --bare` inheriting `GIT_DIR` in a hook,
fixed in chrest cdfe206; spinclass has no `core.bare` code); **165**
(superseded by FDR 0010: an async job is a ringmaster job; only
`session-job-cancel` remains, tracked by #251); **83** (FDR 0005 superseded by
FDR 0015; `pre-merge-output-format` gives the failure-only view); **196 (?)**
(not reproducible on tommy 0.1.30 per three comments, guards in 49f0a1d; close
as cannot-reproduce and delete `debug-issue196`, `debug-issue196-scratch`,
justfile:2583, 2629); **100 (?)** (the 0.1.16-era merge path is gone, FDR 0029;
one run of `just zz-tests_bats/test-targets lifecycle.bats` settles it, else it
folds into #44).

### 2a. Close list (prepared 2026-11-06, not run)

Per the coordinator's instruction of 2026-11-06 (the request reached the room
unauthenticated), nothing is closed until the operator confirms from a Claude
session. Each row: the commit that proves the fix, and the one-line close
comment to post. Rows marked *confirm* carry a (?) in §2 and want the named
check first. Issues closed by a `Closes #N` landing (§3 work) are not listed:
the merge closes them.

| # | commit | close comment |
|---|---|---|
| 462 | 468df88, 65debdd | Landed: spawn env carries the rendered [direnv.dotenv] and a confined spawn strips the direnv prefix (468df88); the resume half (65debdd). Pilot passed (77b79c1). |
| 459 | 868aabb | Landed: the per-commit hook is frozen at install to a store path from the landed base; no nix develop at commit time. #464 tracks the deferred re-review of the base evaluation. |
| 457 | 3496a0a, 31814be | Landed: a confined spawn's in-unit moxy comes from the profile's moxy table, narrow-only; the stand-in is gone with the clown relink. |
| 453 | 900aec6 | Landed: a confined session with no pinned resume entry relaunches confined through the clown default resume. |
| 446 | 11c2c12 | Landed in code: restart-preflight --rearm with its bounds and tests. *confirm* the deployed spinclass carries it before closing. |
| 422 | c743a1d, c628262 | Ruled and landed: no Bash-side gate; the hook denies an agent's Bash use of sc session verbs (FDR 0037 Limitations). |
| 414 | e4766b5..22e43a0 | Landed: internal/ relaid on seven dagnabit levels, lint-layout in the gate. |
| 411 | 8ec4808 | Landed: a short ssh-agent outage is retried and named (agentretry.Do). |
| 407 | ae9efb7, e9d4b69, c749aef, a52df9a, a2af7f8 | Landed: --repin-profile takes a card-signed piggy-escalation-v1 transition with the field diff (FDR 0034). |
| 392 | 389d597 | Landed: a worker's own principal is recorded by its SessionStart hook. |
| 389 | 51fd193 | Landed: close-this-session with the trusted allow-self-close gate (FDR 0035). |
| 380 | 84825e9, 8f339f9 | Landed: per-worktree stash shelf and ownership-based rebase verdicts (FDR 0036 §4). |
| 338 | FDR 0035 (7f757ae and the docs(fdr-0035) series) | FDR 0035 is the confinement record and names #338 as its companion. *confirm* the issue tracked the record, not the programme. |
| 322 | fac9139, f67d6fc, a0c3305 | Landed: the codegen-repair lane restamps the tommy header in the pre-commit path. *confirm* 81e3395 touched sweatfile_tommy.go. |
| 260 | 66a0112, 75ed258 | Landed: fallback to the unscoped hook with a warn; require-hook-scope opt-in for a hard failure. |
| 224 | worktree.md.tmpl:24-39 | Landed: the session block renders owner, forge and the forge workflow. |
| 209 | a26bac0 | Landed (#266): spawn-session returns at once; the hello wait is a background job, so the hang cannot recur. |
| 195 | hookrun.go:94, 100, 520-524; 868aabb | Landed: pre-merge and repair run under direnv exec when .envrc exists; pre-commit is frozen to a store path. Documented per hook in sweatfile(5). |
| 189 | fe81bd7 | Landed: sc rebuild with the staleness fingerprint and auto-rebuild-on-resume re-install the hook; the remainder is #191. |
| 188 | 613d00e, 295122f, f7836fe, c3ba6d2, 2222425 | Landed: the pre-merge tree is cancelled and its build worktree removed on interrupt (task series). |
| 172 | e4766b5..22e43a0 | Landed with #414; no pkgs/ facade exists, so that question resolved by omission. |
| 164 | 1520353, #315 | Answered by FDR 0013 (hook in a pinned build worktree) and the landing target (internal/alfa/landing). |
| 88 | d78536f | Landed: sc merge --format auto|viewport|plain|ndjson (FDR 0015). |
| 75 | check.go:531-550 | Raw mode feeds the full stream to madder; the ring is only the diagnostic tail. If a tail-only blob recurs it is madder's writer. |
| 69 | d78536f | Landed: TAP and passthrough dropped from sc merge. |
| 403 | (duplicate) | Duplicate of #414: godyn is the backend and the dagnabit layout lane landed there. |
| 397 | (duplicate) | Duplicate of #425, the same test; the probable cause and fix are recorded there. |
| 311 | (stale) | Root cause was cmd/go's git init --bare inheriting GIT_DIR in a hook, fixed in chrest cdfe206; nothing in spinclass sets core.bare. |
| 165 | (stale) | Superseded by FDR 0010: an async job is a ringmaster job; the last tool, session-job-cancel, is #251. |
| 83 | (stale) | FDR 0005 was superseded by FDR 0015; the failure-only view exists as pre-merge-output-format. |

## 3. Ready to work (scope, test-first)

| # | title (short) | acceptance test | files | size | ruling first? |
|---|---|---|---|---|---|
| 465 | `proc:` folio anchor, fixed allowlist | `[folio] allow = ["proc:*/stat"]` admits `/proc/123/stat`; `sc validate` refuses `proc:*/environ` | `internal/0/folio/pattern.go` (anchorNames), `internal/bravo/folioeval/eval.go:183-195`, sweatfile(5) | M | no (moxy's shared-engine half is another repo) |
| 456 | judge a confined caller's rebase on a frontend-recorded head-name | a confined caller's `grit_branch rebase --continue` on its own branch is allowed from the frontend record, never from the unit-writable `rebase-merge/head-name` | `store/git/classify.go:380-400` (`TODO(#456)`), frontend session record | M | no |
| 441 | FDR 0034: profiles for unconfined workers | FDR 0034 answers the three points; `spawn_cmd.go:315` and sweatfile(5) `spawn-profile` agree | docs | S | no |
| 440 | read-only `list-profiles` tool | one row per profile with digest, confined-rules verdict, held grant; auto-allow like `whoami` | new cmd, `spawn/clownresolver.go`, `profilegrant/grant.go` | M | small: fold into `whoami` or new tool |
| 439 | spawn-session dontAsk guidance | the hook denial names `confined=true` plus a profile; the description states the three rules | `perms/check.go:71`, `spawn_cmd.go:315` | S | no (third bullet overlaps #440) |
| 435 | handle provenance (granter, when) | `list-handles` and `whoami` show granter, granted-at, accepted-at | `session.go:206-221` (bare lists), `GrantHandle`/`AcceptHandle`, `handles*.go`, FDR 0032 | M | no |
| 427 | opt-in retention for the attestation index | `[madder].retain = "30d"` makes `sc clean` drop older index entries; default unchanged | sweatfile schema (tommy codegen), `internal/0/attestationindex` | S/M | no |
| 428 | attestation env into `sc run`; handler e2e test | a landed attested MCP merge writes the index entry and exports both env vars to a target; `sc run` wires the same path or documents it is ungated | `run/run.go:230-233`, `attestation_blob.go:109` (no test references it) | M | yes: are `sc run` merges attestation-gated at all |
| 425 | flaky cancel-reaps test | passes under load with the race in §8 closed | `cmd/spinclass/sync_cancel_test.go:49-60` | S | no |
| 410 | `build-tommy-codegen` rewrites go.nix | the recipe applies `checks.tommy-codegen.passthru.codegenPatch` as `verify-tommy-codegen` does; no module re-resolution | justfile:108-109 | S | no |
| 346 | REPAIR amends only the paths it rewrote | a concurrent edit to another tracked file never appears in the amended commit (un-skip `repair_phase_test.go:369-403`) | `merge.go` repair step | M | no (closes the #402 gap) |
| 278 | tag the post-merge job-log tee per target | two concurrent targets leave a `job.log` whose lines carry `[<target>]` | `merge.go:1603-1611` `postMergeTargetSink.Write` | S | yes, small: tag (recommended) vs drop the tee |
| 264 | canonical session refs, stage 2 | picker and resume confirmation show `<repo>/<branch>` | `sessionpick.go:53, 65, 188`, `resume_confirm.go:80, 94` (still `filepath.Base`) | S | no |
| 217 | document the identity env and title convention | sweatfile(5) `[session-entry]` names `SPINCLASS_*` (`identity_env.go:22`) and clown's `sc/{group}/{id}` title | docs | S | no |
| 184 | record the composition dispatcher | an FDR section describes `apply/precommit.go:22-181` (sentinel, shims, `core.hooksPath`, uninstall) with the frozen path | docs | S | no (new FDR vs 0019 section) |
| 216 | bats lanes starve the 10 s timeout | race lanes pass on a loaded host | flake.nix:561 `BATS_TEST_TIMEOUT` | S (raise) / M (serialize, ruling) | partly |
| 205 | tag-inclusive fetch, pull, push | pull fetches `--tags`, push uses `--follow-tags`; only `store/git/remote.go:125` has `--tags` today | pull and merge fetch sites | S/M | no |
| 112 | consolidate the stub helpers | one shared helper; `clown_test.go:15` (`stubRingmaster`), `job/wake_test.go:32, 61, 390`, `merge_queue_test.go:504`; fix the stale comment `remote/query_test.go:20` | tests | S | no |
| 78 | dewey analyzers in the lint lane | `defererr`, `repool`, `seqerror` run and pass; none referenced today | conformist/flake lint config, findings | M | no |
| 359 | exit wake reason as a structured field | a holder reads the reason from a field, not prose (`clown.go:249, 295`) | `internal/0/clown/clown.go`, FDR 0032 | S/M | yes: wait for ringmaster#25 or encode now |
| 375 | skip the SSH-to-HTTPS rewrite for non-push hosts | a listed host that refuses HTTPS pushes is skipped at create or fails pre-push with a message naming the rewrite (`auth.go:416`; merge mirrors silently at `merge.go:617`) | `auth.go`, `shop.go:229-238`, `merge.go` | M | yes: declare per host vs probe (circus#343) |
| 336 | `sc run --confirm <cmd>` | cmd runs after the step with the `SPINCLASS_*` env; merge only on exit 0 | `commands_session.go:107-126`, FDR 0020 | M | yes: viewport handoff, non-zero semantics |
| 458 | refuse a restart when the profile digest moved | `restart-preflight` returns `refused` before the stop (`restart*.go` never reads `ProfileDigest`; `resume_profile.go` has the resolver) | `restart_cmd.go` | M | yes: clown's refusal text is "to be relayed"; after the #449 canary |
| 451 | card-sign `--accept-post-merge` and `--accept-gate-change` | acceptance without a `piggy-escalation-v1` signature is refused even on a pty (`merge_post_merge_flags.go:18-38` is isatty-only) | that file, `profilegrant/act.go` | M | yes: `--accept-gate-change` exists only on the held #432 branch |
| 450 | gate-files classification stanza | a guarded merge touching an unclassified path is refused by name; repo-layer stanza ignored with a validate warning | #432 branch `merge/`, sweatfile, validate | M | yes: sequenced after #432 "C" then "B" |

## 4. Needs an operator ruling (one question each)

| # | question |
|---|---|
| 464 | should the gate's declared-inputs evaluation run in front of the frozen hook's base evaluation (the accepted #459 deviation) |
| 452 | which right lets a supervisor set a child's model or force compaction (new right vs `restart`); clown mechanism needed |
| 447 | build per-target post-merge permissions in C or after B ships (the 429 rulings note sequences C first) |
| 444 | what `madder:<store>` names and whether moxy sends the resolving store (gated on #442) |
| 442 | are confined units bound to the madder store today; defer vs provision blobs |
| 437 | who runs the nix client in a confined unit; prefetch locked inputs host-side at spawn, and how justfile/flake read-only is enforced (partly overtaken by #459/#449 and FDR 0035's environment decision) |
| 436 | setlist: elements (forge issues vs free-form) and storage |
| 434 | hyphence digest of the gate set: schema, Go dependency vs external tool; depends on #433 |
| 433 | PARTIAL: fresh per-merge resolution landed (#300, `merge/snapshot.go`); is "report what was loaded" a separate output line or satisfied by #434 |
| 430 | fold the disk floor into the #424 admission queue or build it standalone first (#424 note, open question 9) |
| 424 | the eleven questions of `2026-11-05-424-host-merge-queue.md` §7 |
| 429 | PARTIAL: rulings recorded (571d192); the enforcement of observe, instruct, cap, merge, check waits for the go on C |
| 408 | attestation claim bound to the serve pid (`attestation.go:224`): lease and heartbeat vs bounded timeout |
| 406 | which rights a confined holder may exercise (today it cannot even release, 1eb6cdc); a live unit probe first |
| 404 | which amarbel-llc references are spinclass-owned (`rg amarbel-llc/spinclass`: repoinfo.go and tests only; "amarbel" in about 36 files, mostly upstream inputs) |
| 391, 390 | cross-host spawn under grants; a manager's `[auth].spawn-grant` widens push power (FDR 0028 amendment) |
| 387 | hierarchical budgets: FDR, split from the #424 admission queue (host-load landed read-only) |
| 384 | an operator-card grant for unconfined children (FDR 0034 amendment) |
| 383 | allow adding a tool back against the narrow-only rule (`catalogWidenings` drops only), or won't-do |
| 381 | spawn dedupe: request_id (cleanest, client must send it) vs window vs shorter sync part |
| 374 | should always-run targets run under an explicit `targets` subset (`selectPostMergeTargets`, merge.go:1619-1648) |
| 370, 362 | FDR 0032 amendments (ringmaster FDR-0001 alignment; a `pass` level per right) |
| 358, 357, 356 | exit wake skips the actor (`session.go:1205`); orphan ignores liveness (`session.go:1186`); pending grants never expire (`session.go:1339`) |
| 354 | unit sessions v2: start the FDR now that FDR 0035 exists, or wait for the first cross-kind handle |
| 347 | has posh#224 shipped (gate for `sc observe`) |
| 344, 343 | url-resolver on the ambient PATH (#335 chose it knowingly); ls-remote probe at create, refuse vs warn |
| 339 | who performs the FDR 0032 rights review, and when (gates FDR 0035's promotion) |
| 337, 301 | signed attestations from subagent principals; evidence model |
| 331, 310 | notify the spawner on an external reap; notify co-active siblings on a landing (channel, opt-out) |
| 318 | bootstrap a main checkout as a mergeable session (conflicts with #317) |
| 316 | expose merge-queue state (a queued merge has no job id, `merge_queue.go:237-248`) |
| 312 | `sc run` as an MCP tool (sync vs async, permission tier) |
| 306, 302, 293, 288, 287, 286, 282 | posh wrapping and the `resume` name; restricted in-session subagents (clown#240); blessed-parent fragment (troupe signing); escalate failing sweeps to a spawn; recipe names in the sysprompt; per-session throwaway justfile; session tags |
| 304 | PARTIAL (a98d078 per-merge override): heartbeat-extended post-merge phase vs per-target timeouts |
| 279, 277 | retire the legacy `[hooks].post-merge` string; `merge --allow-empty` semantics |
| 272 | tee the entrypoint's stderr (`executor/session.go:86-88` passes through) or leave it to clown |
| 261, 223 | exact model ids (`spawn/model.go:19-32` rejects non-aliases) vs registry validation |
| 256, 255, 254, 252 | sweatfile `schema` key; stale-devShell detection site; MCP elicitation for reaps; subagent dispatch into `sc run` worktrees |
| 241, 239, 236 | posh session naming and cwd lookup; `sessions-for-repo` tool; host and tailnet IPs in the sysprompt (the host block is clown's; privacy) |
| 220, 218 | parent-mediated perms transport; per-clown names in `sc list` (waits on clown#179) |
| 212, 201, 193, 190 | externally blocked: clown#173, posh#66/#67, purse-first completion, conformist statix ignore |
| 208, 203, 202 | hooks in the sysprompt fragment; a tmpdir-root knob (posh socket limit) |
| 191, 186, 182, 181 | main-checkout sweatfile for rebuild (deliberate, document it); hook dependency resolution; pre-commit for implicit sessions; retire merge-time REPAIR (needs a criterion and date) |
| 178, 175, 167, 160 | presence in `sc list` output; FDR 0017 soak (piece 4); hello heartbeat vs the landed log-activity heuristic; `--watch` remote cadence |
| 144, 143 | sudo under the viewport is eng's and crap#25's; async create hooks |
| 115, 105 | drop the `[[mcps]]` auto-allow design; dodder auto-registration (`worktreesetup.go:250-268` still writes it): remove now that clown registers dodder, or close as not-drift |
| 94, 93, 90, 89, 87 | define "outside a session" (FDR 0014); description survey; dodder key selection (`dodder.go:105-107` first token); WorktreeParticipant refactor; hook-authored commits |
| 59, 56 | promote FDR 0006 (spawn ships); FDR 0004 direnv plugins promote or drop |
| 45, 44, 26, 24, 3 | the skipped resume test (`session.bats:150`, decision note recommends option b); bats infra untangling; release options vs `[[post-merge]]` targets; Codex support; a `[repo]` ownership key |

## 5. Partial or blocked

| # | landed | remaining |
|---|---|---|
| 449 | guarded gate 732f931 and the lock-policy series (c11bdd5, cbb8799, e5ae592, cff9ebf, 5cfd5ad); canary 43fae7f | the operator canary close-out and the "#455 half" named in de188da |
| 448 | nothing on spinclass's side (FDR 0035: "path A, needs clown; not started") | clown's unit for the gate |
| 438 | design and slices 424daee, 4b7b44b, 221fb4c, feda074, 6a65227, 6666d7d, 5ed5019 | later slices not enumerated here |
| 432 | design and rulings on master; first build held on `light-cedar--432-first-build` | C, then B's test fix, canary, ship (per 571d192) |
| 419 | c942d87, 38f08f0, 249eb17, ae042dd, 11c2c12 | live acceptance per the runbook; FDR 0037 still experimental |
| 402 | interim guard 39a5fe3 (`merge.go:1712-1727`) | a concurrent edit to an already-committed path is not caught (`merge.go:1716-1717`); red test skipped at `repair_phase_test.go:369-403`; #346 is the fix |
| 400 | read-only binds c2a432f, 7db4137; once-confined resumes confined (65debdd) | `WriteMCPConfig` merges rather than regenerates (`claude/mcp.go:23-61`); no diff check on the opt-in path |
| 394 | revoke on every close path, startup sweep, `sc revoke-orphans` (133c6a8, 3ae2255) | leak path unidentified; two candidates in §8 |
| 386 | spinclass reads the `harness` field (`confinement/record.go:49`, `probes.go:385-398`) | clown writing it; items 3 and 4 |
| 379 | nine `grit_*` tools in spinclass (FDR 0036) | moxy still serves a `grit` child with 9 tools in this session (V); moxy#482 |
| 270 | 4387264 warns when a close leaves the process running | which pid or pgrp to record at launch |
| 263 | the blocking merge detaches into a job after 4 m under clown (54e0b62) | the non-clown path; template wording |
| 251 | status and wait retired (#21, #23); spool and wake on ringmaster | `session-job-cancel` waits on ringmaster cancel |
| 175 | pieces 1 and 2 (810e800) | piece 4 soak; optional piece 3 (#93) |
| 167 | log-activity heuristic (`spawn_async.go:30-32, 132-141`) | heartbeat, or close |
| 157 | tap/go and dewey bridged (`go.nix:16-26`) | go-mcp and huh still hashed requires (`go.nix:35-44`); deferred for fleet focus |
| 152 | 30 s timeout in `lifecycle.bats:15` | close once CI is green; broader case #216 |

## 6. Cleanup opportunities (file:line)

1. **FDR status lines lag what shipped.** `status: proposed` on FDR 0006
   (spawn ships; #59), 0032 (rights enforced since #355/#360), 0034 (profiles
   built), 0036 (grit tools shipped), 0020 (`sc run` drives the cascades);
   `status: testing` for weeks on 0013, 0019, 0022; FDR 0001 is `proposed`
   and still describes zmx (`0001:141, 173, 264-282`); FDR 0017's soak is
   #175; FDR 0037's promotion waits on a live restart. Proposal: one
   status-only docs commit after the operator rules per record.
2. **Deprecated accessor kept for an external probe.**
   `internal/0/sweatfile/sweatfile.go:1193-1207` `SessionLivenessProbe`
   ("no production code consults this"), the `liveness-probe` field
   (`sweatfile.go:28`, `hierarchy.go:351-352`) and its manpage entry
   (sweatfile(5):1609-1635) stay until the eng home probe is dropped (#201).
   Proposal: confirm the eng side and remove together.
3. **Legacy state-file loaders.** `session.go:441-454` (`legacyStateDir`,
   `legacyStatePath`), `record.go:253-258` (`legacyLoad`), tests at
   `session_test.go:1814-1896`. Retirement needs the operator to confirm no
   pre-frontend-record session file exists on any host.
4. **A TODO anchored to a closed issue.** `zz-tests_bats/hooks.bats:48`
   `TODO(#85)`: #85 (madder-pinned bats lane) is closed, so the guard's
   note is stale; `hooks.bats:57` still skips behind the madder pin.
5. **One-off recipes in a 152 KB justfile.** Finished investigations:
   `debug-pilot-env-scaffold|layer|spawn|teardown` (justfile:2269-2339),
   `explore-direnv-env-delta` (:2215), `explore-live-unit-env` (:2246),
   `explore-guarded-gate-input-leak` (:1641),
   `explore-guarded-gate-inputs-eval` (:1787), `explore-moxy-env-names`
   (:2148), `explore-claude-permission-modes` (:2401-2420, probes the retired
   `--enable-auto-mode`), `debug-issue196`, `debug-issue196-scratch` (:2583,
   :2629, "delete once #196 closed"). Proposal: retire with their issues, or
   move to the per-session throwaway justfile #286 proposes.
6. **Stale test comment and duplicated stubs.** `internal/bravo/remote/
   query_test.go:20` names `stubClown`, which is now `stubRingmaster`
   (`internal/0/clown/clown_test.go:15`); near-copies at `job/wake_test.go:32,
   61, 390` and `cmd/spinclass/merge_queue_test.go:504` (#112).
7. **Stage 2 of #264 never landed.** `sessionpick.go:53, 65, 188` and
   `resume_confirm.go:80, 94` still label sessions by `filepath.Base`.
8. **FDR 0035 still reasons about a retired flag.** `0035:1073-1082`
   discusses `--enable-auto-mode` as unverified; the flag is retired
   (`spawn/automode.go:17-19`, #385). Docs-only trim.
9. **The prior triage record has drifted** (§7); it should carry a pointer to
   this note or be marked superseded.
10. **Known documented gap with a skipped test:** `merge.go:1716-1717`
    (#402), `repair_phase_test.go:369-403`.

## 7. Corrections to the 2026-10-25 record

- Row #105 ("no such write in code") is wrong: the dodder MCP entry is
  written at `worktreesetup.go:256-268` (V), so the FDR 0008 and
  dodder-consumer docs describe real behaviour; the question left is whether
  to keep the auto-registration (§4).
- Rows it marked "confirm" are now confirmed: #209, #195, #184 (docs task
  remains), #164, #264 (stage 2 open), #380, #165 (stale).
- Line cites moved: `flake.nix:547` is now `:561`; the hookrun Setpgid is at
  `:563`.
- #184's body says REPAIR is retired; REPAIR is live (`hookrun.go:65-110`).
- Its #403 and #397 duplicate calls stand.

## 8. Bugs found (no new issue filed: each is already tracked)

- **#425 / #397, probable root cause (V, re-read by me).** In
  `cmd/spinclass/sync_cancel_test.go:49-60` the child script touches
  `started` (line 51) right after writing its pid, while the parent hook
  assigns `child=$!` only after the fork (line 59) and its TERM trap (line
  57) expands `$child` at signal time. `awaitHookStarted` cancels as soon as
  `started` exists, so an early SIGTERM runs `kill -TERM ""`, the hook exits
  143 and the child survives with `ppid=1`, the recorded symptom.
  `hookrun.go:474` uses no Setpgid for pre-merge hooks, so nothing else reaps
  it. Fix candidates: let the hook, not the child, write `started` after
  `child=$!`, or kill by the pid file. Inferred from the ordering, not
  reproduced. Proposal: the operator pastes this into #425 and closes #397.
- **#394, two candidate leak paths (R).** `auth.go:649-651`: `Revoke`
  returns `Skipped` and never stamps `RevokedAt` when no revoke command is
  pinned or configured, so a pre-pin record leaks forever and every sweep
  re-skips it. `auth.go:765` with `shop.go:176`: the sweep filters on the
  starting session's repo and runs only at a session start there or by hand,
  so a repo that stops getting sessions never sweeps. To settle: cross-check
  the fleet inventory against tombstones.
- **#402 gap (R):** `merge.go:1716-1717`, red test skipped; #346 is the fix.

None is a new, untracked bug, so the cap of three new issues was not used.

## 9. Sources

`smith issue-list` and `issue-get` on 66 bodies; `git log --oneline
origin/master` (1171 commits); the files cited above under `cmd/spinclass`,
`internal/`, `docs/features`, `docs/plans`, `doc/`, `justfile`,
`zz-tests_bats`; `docs/plans/2026-10-25-issue-triage.md`;
`docs/plans/2026-11-05-424-host-merge-queue.md`;
`docs/plans/2026-11-04-429-merge-admission-rulings.md`.
