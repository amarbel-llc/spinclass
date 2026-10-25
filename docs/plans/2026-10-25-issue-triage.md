# Open-issue triage: linenisgreat/spinclass

- Date: 2026-10-25
- Author: spinclass/firm-mulberry/bozo, drafted by a subagent
- Method: smith issue-list (state open) returned 177 issues. Four parallel readers each
  fetched issue bodies (comments where the title was not enough) and checked this
  worktree with rg/folio and git log. Nothing was edited, built, commented or labelled.
  Packages are cited at their new `internal/<level>/<pkg>` paths.
- Evidence labels: V = observed in code/tool output, R = read in the issue or a doc,
  I = inference. Many bodies were only partly read; rows resting on titles are marked
  "title only". Fix sketches in (a) are unverified designs, not tested plans.
- Counts: (a) 45, (b) 92, (c) 13, (d) 27 = 177.
- **Verification caveat:** the drafting subagent split the 177 issues across four
  sub-readers and compiled their reports without re-checking them; I (bozo)
  have not re-checked them either. Treat every row as a lead: re-verify an
  issue's claim before acting on it, and never close a (d) row on this table
  alone (a (d) close needs the commit evidence named in a closing comment).

## (a) Fixable now, test-first, inside spinclass

Risk and first-red test are given in the next-step column.

| # | Title | Evidence | Next step |
|---|-------|----------|-----------|
| 423 | Other pkgs may hit #421 git-maintenance ENOTEMPTY | R: 7de573d fixed only the store/git pkg (TestMain sets maintenance.auto=false). I: merge/pull/shop/landing/clean likely share the hazard (issue says unchecked) | Shared testgit helper or TestMain env per git-running pkg. Low. Red: test asserting no detached `git maintenance` in internal/delta/merge, mirroring the store/git test |
| 413 | ProfileNarrowing dropped on resume/resurrect | V: echo/shop/shop.go:426-428 and foxtrot/resurrect/resurrect.go:102-104 copy ProfileDigest/ProfileRebinds only; V: alfa/session/record.go:305-306 authorityFields lacks it | Carry the field in both; decide the authorityFields entry. Low. Red: shop-carry and resurrect-carry tests asserting ProfileNarrowing survives |
| 412 | sweatfile(5) says enforce "last one wins" | V: doc/spinclass-sweatfile.5.scd:901-903. R: issue says code is sticky-on (trust.go not opened) | Doc-only edit to say sticky-on. Low. No natural red test |
| 411 | Retry ssh-agent mint failure, name the cause | V: no retry/backoff in charlie/auth/auth.go | Bounded retry (about 3 tries/30s) plus clearer message. Med. Red: mint stub failing twice then succeeding |
| 388 | Host-load MCP tool | V: no such tool. R: spec is clear | New read-only tool using internal/0/proccwd. Med. Red: handler test with fake /proc source. Threshold default shared with #387 |
| 378 | profile-grant-import atomicity/duplicate grantees | V: cmd/spinclass/profilegrant_import_cmd.go:179-210 binds in a loop, no rollback, no duplicate check | Reject duplicate grantees; bind with rollback. Low-med. Red: import of two unrelated roots with the same grantee must error |
| 377 | IssueChildLink not atomic | V: charlie/profilerights/profilerights.go:173-202 does Current then Bind, no lock | Create-if-absent Bind in alfa/grantstore. Low. Red: concurrent IssueChildLink, exactly one wins |
| 373 | Expose claimed attestation to post-merge | V: no SPINCLASS_ATTESTATION_BLOB. R: issue specifies the contract | Write a madder blob before Settle; add env in delta/merge/postmerge_options.go. Med (madder failure must not fail the gate, #349). Red: PostMergeEnv test expects the var |
| 371 | clown/presence.go reads pre-extraction path | V: internal/0/clown/presence.go:27-40 reads $XDG_STATE_HOME/clown/presence; no ListPresence call. I: jobwake.ListPresence existence not verified | Switch to jobwake.ListPresence. Low. Red: write an entry ringmaster-style, assert ReadPresence sees it |
| 368 | RFC-0018 lock for spawn-async / queue-notice producers | V: AcquireJobLock only at alfa/job/runner.go:215; cmd/spinclass/spawn_async.go:77 and merge_queue.go:179 StartJob without it | Acquire after StartJob, release before FinishJob. Low-med. Red: test like job/wake_test.go:190 expecting ErrAlreadyLocked |
| 365 | stack_wait/stack_depth for stacked merge queue | V: merge_queue.go:200 knows position, :224 restarts timer; no such metric | Add timer and gauge. Low. Red: metrics test (cf. delta/merge/metrics_test.go:170) |
| 353 | sc merge --target: expected commit id | R: issue. V: no expect-sha flag in *.go | Add `--expect-sha`, refuse if branch moved. Med. Red: branch gains a commit after sha supplied, expect refusal |
| 352 | Hook-scope fallback as severity=warn point | V: bravo/hookrun/hookrun.go:617 still plain Fprintf | Return notice from runHookInScope to check layer as warn point. Med (signatures widen). Red: passing gate with failed scope setup carries one warn point, exit 0 |
| 348 | Route every session read-modify-write via locked Update | V: Read-then-Write sites outside Update (hooks.go:383/452/549, auth.go:452, shop.go:474/520, close.go:478-491, handles_cmd.go, restart_cmd.go, close_self_cmd.go:158, close_child_cmd.go:246, commands_session.go:365, resurrect.go:108); Update used only at spawn.go:680, attestation.go:69/105, resume_profile.go:212. I: FDR 0035 records vs state.json mirror interplay unverified | Convert callers one at a time. Med. Red: concurrent writer test dropping a field (e.g. GrantHandle vs UpdateCredential) |
| 346 | REPAIR: amend only paths the tool rewrote | R: issue (post-#345). I: code not re-verified, no later commit cites #346 | Snapshot diff before/after repair; amend only changed paths. Med. Red: concurrent edit to another tracked file must not appear in amended commit. Related #402 (b) |
| 342 | grant/release-session-handle variadic | R: issue cites close.RunMany precedent. I: single child assumed from issue/test names, param not read | Variadic positional, one TAP subtest per child, one always-ask prompt. Low-med. Red: handles_cmd_test with two children |
| 340 | Post-merge runs in live session worktree | V: merge.go:1252-1261 runDir picks landDir, then wtPath, then repoPath | Run in detached checkout at landedSha or keep landing worktree. Med. Red: mid-merge edit to session worktree must not reach post-merge command |
| 334 | Surface merge-driver exit 127 behind rebase conflict | V: PreflightMergeDrivers only at merge.go:224/808, check.go:179; no post-failure 127 detection | Scan rebase stderr for `command not found` beside CONFLICT; warn. Low. Red: stderr fixture test |
| 333 | sc pull: pre-flight merge drivers | V: PreflightMergeDrivers not called from internal/delta/pull | Call per worktree before git.Rebase. Low-med. Red: bound-but-absent driver yields failing point, no rebase |
| 329 | spawn-session `model` description from sweatfile | V: spawn_cmd.go:272 static "sonnet, opus, haiku, fable" | Build description at registration from model-ids. Low. Red: description contains the overridden id |
| 320 | sc run / sc exec --repo-dir | V: commands_session.go "exec" (78) and "run" (105) have no such flag | Add flag to shared grammar. Low-med. Red: invoke from another cwd with --repo-dir |
| 308 | Move setup application out of internal/worktree | V: charlie/worktree/worktree.go defines Create:79, CreateFrom:129, Reapply:161. I: import list not checked | Move to shop or new pkg; repoint rebuild/resurrect. Med (layering). Red: layout/dependency test that worktree no longer imports sweatfileio/madder/dodder/claude |
| 305 | post-merge verify-failed diag: bounded output tail | V: merge.go:1417 postMergeFailDiag; R: its comment says output deliberately not duplicated | Capture last N lines in diag. Low. Red: postMergeFailDiag test expects tail |
| 283 | sc list: wrap DESCRIPTION column | V: list_view.go:325 `Col("DESCRIPTION", mesa.Flex)`, no mesa.Wrap. I: whether current purse-first pin has Wrap unverified | Use wrapped column; may need purse-first bump. Low-med. Red: narrow-width render test expects multiple lines, no ellipsis |
| 280 | Cover async job-log tail for named post-merge targets | R: test-coverage gap; V: runNamedPostMergeTargets at merge.go:1349 | Add test against runner_test.go/merge tests. Low. Red: named-target output must land in job.log/spool |
| 247 | merge loads sweatfile hierarchy repeatedly | V: trustedHierarchy at delta/merge/merge.go 278,326,465,1156; loadCommitHierarchy at 357,1220 | Resolve once, thread through. Low-med (loads read different refs). Red: loader-call-counting seam, one trusted load per merge |
| 240 | Failed merge hides succeeded prefix stages | R: issue. I: merge verdict emitter not read | Always emit prior ✓ stages. Low-med. Red: forced failure output contains "✓ rebase" |
| 236 | Host IP and Tailscale IP in sysprompt | V: no tailscale/net code in internal/; templates in bravo/sysprompt/templates | Deadline-capped probe (must not block, 2s). Med (IP privacy, latency). Red: render test with injected NetInfo seam |
| 234 | session-job-cancel never confirms | V: cancel-requested exists in alfa/job/job.go, runner.go. R: comment says cancel eventually won. I: whether status exposes it unchecked | Surface cancel-requested in session-job-status. Low. Red: status after cancel request contains "cancelling". Unverified |
| 231 | sc clean removes active/wanted sessions | V: echo/clean/clean.go plan actions have no active-state guard. R: issue proposes state-grouped preview | Skip active sessions; group preview. Low-med. Red: planClean with active session expects skip |
| 230 | Replace isatty checks with primordial.IsTTY | V: duplicates at echo/clean/clean.go:43, delta/merge/merge.go:42, delta/close/close.go:681, bravo/sessionpick:223, cmd/spinclass/resume_confirm.go:148, echo/shop/shop.go:539. I: primordial.IsTTY existence unverified (may be (c)) | Default seams to shared helper. Low. Red: source-scan test for go-isatty imports |
| 215 | sc validate: warn on inherited [hooks].create | V: no ancestor warning in bravo/validate/validate.go | Informational issue naming the ancestor. Low. Red: validate test with parent-set create |
| 211 | push race strands local merge | R: bug; V: docs/features/0022:7 lists it as watch item. I: merge flow reworked since (FDR 0029), not traced | Repro test of rejected push then re-run. High risk. Unverified |
| 205 | Tag-inclusive fetch/pull/push | V: `--tags` only in alfa/store/git/remote.go:125. I: merge/pull fetch code not found | Add --tags to fetch, --follow-tags to push. Low-med. Unverified |
| 192 | Shared IdentityEnv helper | V: bravo/sessionexec/exec.go:92 has IdentityEnv; foxtrot/spawn/spawn.go:445-488 workerEnv mirrors it | Move builder to alfa/session. Low. Red: all builders produce same var set |
| 177 | PreToolUse .spinclass deny misses MCP writers | V: echo/hooks/hooks.go:744 matches only Read/Write/Edit | Extend to moxy tools' path args. Low-med. Red: folio_write under .spinclass/ expects deny |
| 157 | Bridge tap/go and go-mcp/huh via go.nix | V: go.nix flakeInputs bridges clown, crap, piggy, dewey, ringmaster, tommy; tap/go, go-mcp, huh still plain requires. No nix build run | Add flakeInputs entries, drop hashed requires. Med (nix-only, unverified). No meaningful Go red test; godyn vet/build is the check |
| 138 | Say merge/check runs on isolated worktree | V: only async merge description says it (commands_mcp_only.go:875) | Add one sentence to sync merge and check descriptions. Low. Red: description-contains test |
| 105 | Docs drift: dodder MCP registration | V: examples/dodder-consumer/README.md:23-24,53 and e2e.sh:71-72 still claim `.mcp.json`; no such write in code; FDR 0008 has 16 mcp mentions | Edit README, delete assertions, scrub FDR 0008. Low. Red: `just explore-dodder-e2e` assertions (I) |
| 81 | justfile verb/group taxonomy | V: codemod-fmt, debug-dev-repo renamed; I: groups still single-quoted, several public recipes ungrouped; eng taxonomy doc unread | Normalize groups. Low. No red test (recipe-only) |
| 79 | sc clean abandoned-reap deletes tombstones early | V: echo/clean/clean.go:382-394, session.go:933-934. R: issue comment prescribes narrowing | Exclude tombstones in countAbandonedSessions. Med (delete-all may be intended; issue Q1 open). Red: fresh tombstone survives `sc clean --yes` with retention 999d |
| 78 | Adopt dewey analyzers | V: no defererr/repool/seqerror outside docs; dewey bridged. I: finding count unknown | Wire into lint lane, fix findings. Med. Red: the lint check itself |
| 52 | assert_session_state tombstone coverage | V: zz-tests_bats/common.bash:195-208 counts tombstones. I: closed-session test may be missing, unchecked | Add bats test. Low. Coverage-only, green immediately |
| 49 | Remove dead SessionExecutor.Detach | V: charlie/executor/session.go:170-172 returns nil; also in executor.go:9, shell.go:46, mocks in delta/merge/merge_test.go:30, echo/shop/shop_test.go:268; no production caller | Drop from interface, impls, mocks. Low. Red: remove from mock, build fails until interface drops it |
| 45 | Three pre-existing bats failures | V: skips at zz-tests_bats/hooks.bats:62 and session.bats:127. R: #100 says prompt hang fixed by --yes | Match session.bats:143 to sessionpick.go:194 string and unskip; investigate hooks.bats. Low. Red: unskipped test |

## (b) Needs a decision

| # | Title | Evidence | Decision / options |
|---|-------|----------|--------------------|
| 425 | Flaky TestHandleMergeThisSessionCancelReaps... | R: same test as #397. V: hookrun.go:427-433 documents no Setpgid for pre-merge; sync_cancel_test.go stub relies on sh TERM propagation. I: fits survivor ppid=1; root cause NOT confirmed | Collect samples (describeSurvivingProcess at sync_cancel_test.go:139) or add opt-in group kill (groupKillGrace). Treat #397 as duplicate |
| 424 | Resource/memory-aware merge queuing | R: explore; overlaps #387 comment | Fold into #387 (FDR 0022 amendment) or keep separate |
| 408 | Attestation claim lives while serve lives | R: issue. I: no lease in alfa/attestation (not read) | Lease+heartbeat, bounded timeout, or rely on #401 timeouts |
| 406 | Confined session receiving handle grants | R: explore; cause (dd1e022) fixed | Rights for confined holder: observe only / close of probes / none; needs real-unit probe test |
| 404 | Triage amarbel-llc refs | V: README.md:22, flake.nix:28,168,526 and more; many are upstream | Operator decides which are linenisgreat-owned; keep .github/workflows? |
| 402 | REPAIR amends concurrent writes | R: interim guard landed; structural fix is an unbuilt FDR 0018 amendment. V: FDR 0018:231-242 | Isolated-worktree repair with CAS branch move, or amend only rewritten paths (#346) |
| 400 | Confined worker plants .mcp.json / settings | V: internal/0/claude/mcp.go:23-61 merges into existing .mcp.json. Security-relevant | Regenerate-and-replace on resume, move files out of worktree, or refuse resume on diff |
| 398 | sc writes OSC11/CPR to stdout on tty | V: no OSC11/CPR/termenv strings in spinclass Go; grit_escape.bats:41-43 uses TERM=dumb. I: emitter likely a dependency. UNDETERMINED | Source hunt first; may become (c) |
| 397 | Flaky same test (older sighting) | R: same as #425 | Close as duplicate of #425 or merge |
| 394 | Credentials left after close | V: close.go:290,415 call revokeCredential; auth.SweepOrphans (charlie/auth/auth.go:689) runs at start (shop.go:173). R: about 300 leaked entries. I: leaking path unknown | Audit close paths (child, self-run, out-of-band, spawn failure) or run sweep fleet-side (papi#89) |
| 391 | Cross-host spawn under scoped grants | R: new feature; V: no code | FDR: grant shape, host scoping, trusted layers; depends on #390 |
| 390 | [auth].spawn-grant for managers | V: no SpawnGrant. R: widens manager push power | Operator decision; FDR 0028 amendment |
| 387 | Hierarchical resource budgets / admission queue | R: large design | Needs FDR; split confinement budgets / gate admission / load tool |
| 384 | Operator-card grant for unconfined children | R: stage 2 of #382 | FDR 0034 amendment, card-signed, principal-pinned |
| 383 | Add individual tools back to a profile | R: open points, partly clown-side | Where the list lives; composition with clown#276, #382 |
| 381 | spawn-session duplicates on retry | R: proposals A dedupe window, B request_id, C shorten sync part; cause of 16s unestablished | Pick one; request_id cleanest but client must send it |
| 375 | Skip SSH->HTTPS rewrite for non-push hosts | V: auth.go:412 gates only on forge-hosts membership | Per-host declaration vs probe vs fail-early message (related circus#343) |
| 374 | Always-run post-merge targets | V: merge.go:1478-1507 selectPostMergeTargets filters all by `targets`. R: open question whether targets=[] skips them | Settle that question; then small FDR 0026 amendment |
| 372 | Auto-allow folio reads on held-child worktrees | V: no observe->read mapping. R: folio is moxy's policy | Spinclass PreToolUse allow vs moxy-side |
| 370 | FDR 0032 amendment per ringmaster FDR-0001 | R: doc amendment, not blocking; depends on ringmaster step 4 | Operator decision |
| 369 | stats-me counter for background-agent prompts | R: empty body. I: hook verdict log exists (c029a54) | Ask filer what counts as a prompt and where to hook |
| 362 | FDR 0032 `pass` right level | R: design amendment, cross-repo (moxy FDR 0013, clown FDR 0019) | Operator decision |
| 361 | CLAUDE.md says dirtiness computed live | V: CLAUDE.md:52 says it; no dirty computation in session pkg | Delete phrase, or add dirty column to sc list (one git status per session) |
| 358 | Exit wake skips acting principal | V: FDR 0032:1002 says still so. R: issue names OtherHolders | Keep exclusion and reword D6, or wake every holder |
| 357 | Orphan ignores holder liveness | V: session.go:1080-1089 Orphan() uses SpawnedByPrincipal and Holders only. I: needs presence, depends on #371 | Decide liveness source after #371 |
| 356 | Pending grant never expires/delivered | V: session.go:1222-1238 GrantHandle only appends; audit row 32-14 SUPERSEDED | TTL/sweep rule; notify recipient? |
| 354 | Unit sessions v2 | title only (I); design-scale | New FDR, fold into FDR 0035, or defer |
| 344 | url-resolver runs on ambient PATH | V: auth.go:303 uses CommandCaptureAmbient. R: #335 Decision 4 chose ambient knowingly | Fixed baseline env; undo direnv via DIRENV_DIFF; or require absolute argv[0] plus validate warning |
| 343 | Verify rewritten origin resolves (ls-remote) | V: no ls-remote under internal/. R: issue offers refuse or warn | Refuse vs warn; probe at create (network cost)? |
| 341 | Pre-merge gate config read from live worktree | R: not a one-liner; no pinned commit with disable-merge-build-worktree; sc check has no pin | Default branch only, build-worktree HEAD, or document as accepted |
| 339 | Security review of FDR 0032 rights lattice | title only (I) | Who reviews and when (D17 gate) |
| 338 | FDR placeholder: session confinement | title only (I). I: FDR 0035 may cover part | Write the FDR or fold into 0035 |
| 337 | Revise FDR 0007/0031 for signed attestations | title only (I); overlaps #301 | FDR amendment |
| 336 | sc run approval pause (`--confirm`) | title only (I) | UX/semantics of the pause |
| 331 | Notify spawner when child reaped externally | V: no parent-notification code in internal/delta/close. R: issue proposes wake. #331 comments not fully read | Channel (ringmaster job-wakeup vs troupe DM), payload, silent cases |
| 322 | tommy codec header drift fails cascade | R: issue lists 3 options. Option 3 partly (c) eng | Restamp lane, pin tommy tag, or cascade regen recipe |
| 318 | Bootstrap main checkout as mergeable session | title only (I); conflicts with #317 | Pursue or not |
| 316 | Expose merge-queue state to agents | V: merge_queue.go:200 enqueued merge has no job id. R: issue suggests job at enqueue | Job at enqueue with queued progress, new MCP tool, or extend list |
| 312 | sc run as MCP tool with repo param | V: run/exec CLI-only in commands_session.go | Sync vs async, include exec, permission tier, repo resolution |
| 310 | Notify co-active siblings on merge | V: co-active info in sysprompt/coactive.go and merge.go. R: siblings never told | Channel, opt-out knob, notify on failure too? |
| 306 | Top-level posh session on create/resume | title only (I) | Hook design and naming of `resume` |
| 304 | Elastic post-merge-timeout | R: heartbeat-extended phase proposed | Heartbeat extend, per-target timeouts, or raise cap |
| 302 | Restricted in-session subagent tool | title only (I); explore | Pursue or not |
| 301 | Require pre-merge skills in subagents | V: attestation.go checks only presence and non-empty reasoning | Evidence model: per-entry subagent id, signed (#337), or description-only |
| 288 | sc run/exec escalate to spawned agent | R: from eng FDR 0015 | spawn-session with failing context, report only, or defer |
| 287 | Surface tagged recipe names in sysprompt | R: low priority, no design | Tag convention; build or not |
| 286 | Per-session throwaway justfile | R: low priority, no design | Build or defer; interplay with just-us plugin |
| 282 | Session tags and `sc close <tags>` | V: no --tag in *.go; sc close already variadic (commands_session.go:269) | Tag schema, list column, close-by-tag grammar |
| 279 | Retire legacy [hooks].post-merge string path | R: reporter-family asymmetry | Retire (deprecate/convert) vs converge reporters |
| 278 | Raw job-log tee for concurrent targets | R: issue | Tag lines per target or drop tee |
| 277 | merge --allow-empty | V: no allow_empty on merge. R: open question | Empty commit vs skip commit; queue/lock interplay |
| 272 | Persist stderr after posh launch | title only (I) | Session log, executor tee, or no capture |
| 270 | Reaping leaves clown running headless | R: comment says recorded pid fails provably-this-session check. V: 249eb17 and executor/close_signal.go exist; relates to #419 | Which pid/pgrp to record at launch; behavior when nothing signalled |
| 263 | Sync merge fails at 900s MCP ceiling | R: abort is clean (UX issue) | Auto-background past threshold, tool-text guidance, or leave |
| 261 | spawn/fork accept exact model ids | V: foxtrot/spawn/model.go:19-32 rejects non-alias for claude | Pass-through, registry validation (#223), or keep closed set |
| 256 | Sweatfile schema restructure + `schema` key | V: no schema key in 0/sweatfile/sweatfile.go; non-hook knobs in [hooks] (lines ~100-114) | Version indicator only; full split with migration; defer |
| 255 | Stale devShell after mid-session rebase | R: three candidate detection sites. V: no closure comparison in bravo/hookrun | After rebase in merge, at resume via setupfingerprint, or in pre-commit dispatcher |
| 254 | MCP elicitation for reap confirmation | R: go-mcp support unverified. V: no "elicit" in code. Partly (c) purse-first, clown | Explore support first; keep force; generalize |
| 252 | Subagent dispatch into sc run worktrees | R: cross-repo design questions. Partly (c) clown | Owner: clown Agent tool, eng wrapper, or sc run helper |
| 241 | Name posh sessions repo/worktree; cwd->key lookup | V: `sc whoami` is principal-based, not cwd. Template change lives in eng (c) | New plumbing subcommand, documented state read, or `<repo>/main` |
| 239 | sessions-for-repo MCP tool | V: list funcs exist (bravo/sysprompt/coactive.go, alfa/session). R: deliberately deferred | Build now, wait for demand, or won't-do |
| 223 | Validate model/profile names vs registries | R: needs registry APIs; c-leaning | clown/juggler list command; validate at spawn only; skip |
| 220 | Parent-mediated perms hook | R: exploration | Transport: troupe chat, ringmaster job, or socket |
| 218 | sc list per-clown names | V: alfa/session/listrow.go:46 ClownCount bare int; commands_query.go:363-368. R: clown#179 adds ClownName | Wire shape for text/JSON; wait for clown#179? |
| 217 | SPINCLASS_* contract for clown OSC title | V: 0/sweatfile/sweatfile.go:1121 mentions $SPINCLASS_GROUP. UNDETERMINED: issue's variable list not diffed against code | Diff the list against env builders |
| 216 | Bats lanes starve 10s timeout | V: flake.nix:547 BATS_TEST_TIMEOUT="10"; 333f1fb, 7de573d partial | Raise timeout, serialize lanes, or cap --jobs |
| 208 | Describe hooks in sysprompt fragment | R: exploration, open questions | Scope |
| 203 | Configurable session tmpdir root | V: no key in sweatfile.go. R: must keep CLAUDE_CODE_TMPDIR/perms coupling | Where the knob lives; hook path mapping |
| 202 | Worktree TMPDIR overflows posh 107-byte limit | R: related posh#105, clown#158 | Spinclass tmpdir-root (#203) or posh/clown defensiveness (c) |
| 191 | rebuild/Create read main-checkout sweatfile | V: 4fd1cbb, b6d58c6 deliberately avoid worktree sweatfile influence | Document contract (likely), read worktree for non-policy fields, or change nothing |
| 186 | Hook dependency resolution position | R: options a-d, FDR-worthy | nix-run pin, declared tools with min versions, document PATH assumption, hybrid |
| 182 | Pre-commit hook for implicit sessions | R: deliberately excluded (mutates main checkout) | Opt-in knob, stay out of scope, or document |
| 181 | Delete merge-time REPAIR phase | V: still wired (hookrun.Repair, merge.go); patched recently (1c1362d, 1eab1bb) | Promotion criterion and date |
| 178 | Presence in list text/JSON, presence liveness | V: session.go:969 ResolveDisplayState overlay exists; text/JSON presence absent | Wire format change |
| 175 | FDR-0017 remaining work | V: liveness-probe deprecated (sweatfile.go:1134). UNDETERMINED: default [session-entry] templates not checked | Diff issue checklist against defaults |
| 167 | Spawn hello gate heartbeat | V: no heartbeat in spawn.go; SpawnLogActiveWithin at spawn.go:696. R: default now 5m async | Keep longer fixed default, log-activity heuristic, or close |
| 164 | git_sync vs session branch during async merge | R: exploration. I: FDR 0013/0029 pinned worktrees may already answer | Write answer into docs or close |
| 160 | sc list --watch remote rows static | V: list_view.go:392-393,409-410 | Slower remote tick; opt-in `--watch-remotes`; accept static |
| 143 | Async create hooks | V: hookrun.go:167-172 only warns on backgrounded children | First-class `async = true`; document self-background; won't-do |
| 115 | Preserved design: [[mcps]] auto-allow | V: auto-allow only in grit code/docs; manpage section already deleted | Implement, redesign, or drop (likely drop) |
| 112 | Consolidate stub-clown test helper | V: only 2 copies remain (0/clown/clown_test.go, alfa/job/wake_test.go), not 4 | Shared non-_test package, or accept and close |
| 100 | sc merge bats hang via direct recipe | R: theory unverified. V: zz-tests_bats/justfile runs bare `bats` against on-PATH spinclass. Root cause unverified | Point SPINCLASS_BIN at fresh binary and retest; see #44 |
| 94 | Hide session-bound tools outside a session | V: tools still return "not inside a worktree session" at call time (commands_mcp_only.go:861,1089,1278); only disable-merge gating exists (1428). I: implicit sessions (FDR 0014) make "outside" ambiguous | Define "outside"; gate at serve start or per call |
| 93 | Hook events observing Claude's description | R: pure exploration | Run payload survey or not |
| 90 | dodder signing-key selection | V: alfa/dodder/dodder.go:105-107 takes first token | Sweatfile-pinned key id, `info-pivy_agent`, or prompt |
| 89 | WorktreeParticipant abstraction | V: embeds accessors with if-guards in charlie/worktree/worktree.go (8 hits) | Worth the refactor now? |
| 87 | Pre-merge hook chore commits | R: explore, not a feature request | Allow hook-authored commits or keep read-only |
| 56 | FDR 0004 direnv template plugins | V: status `exploring`, nothing implemented | Promote, keep exploring, or drop |
| 44 | Untangle bats infrastructure | V: BATS_LIB_PATH/batman/SPINCLASS_BIN refs in flake.nix and justfile | Unified toolchain (tap pivot) vs minimal local fix |
| 26 | Release/tag options on merge | V: no such feature | Post-merge release hook, or keep existing targets (FDR 0023/0026) and close |
| 24 | First-class Codex support | V: no codex code outside tests | Document-only vs code support |
| 3 | Sweatfile `[repo]` ownership key | V: no such key or isFork use | Schema; should sysprompt repository line consume it |

## (c) Blocked on another repo

| # | Title | Evidence | Needs |
|---|-------|----------|-------|
| 410 | build-tommy-codegen bumps go.nix x/text | R: issue says rewrite comes from igloo godyn-go, not the spinclass recipe | igloo godyn-go: do not re-resolve module graph on codegen runs |
| 386 | Complete agent census for F11 | R: items 1-2 need clown writing the `harness` field (clown FDR 0021); 3-4 live verification | clown: write `harness` in unit record; then spinclass drops comm list |
| 359 | Exit wake reason only in text, no digest | V: clown.go:269-287 EmitExitWakes text only. R: FDR 0032:1002 tracked gap; ringmaster#25 | ringmaster#25: annotation kinds/reason tags |
| 347 | sc observe / attach helper to live posh session | title only (I); R: consumer of posh#224 | posh#224 must ship first |
| 296 | Dormant sessions to clown presence | R: companion clown issue for presence side; V: internal/0/clown/presence.go only reads | clown: represent dormant state; agree bridge shape |
| 293 | Sysprompt declaring blessed parent | R: depends on troupe companion issue | troupe: signed messages / approval records |
| 251 | Retire session-job-* for ringmaster surface | R: ringmaster work gates cancel migration | ringmaster: cancel and result-by-reference wake |
| 212 | Tool-name variable expansion | R: blocked on clown#173. V: hard-coded names in echo/hooks/hooks.go:613-631 | clown: ship expansion |
| 201 | zmx->posh umbrella | R: remote needs posh#66/#67 open. V: liveness-probe deprecated (sweatfile.go:1134); local half looks done (I) | posh#66/#67 |
| 193 | sc exec --session completion | R: PassthroughArgs framework limitation | purse-first go-mcp: completion affordance for leading flag under passthrough |
| 190 | statix walks .direnv | V: conformist.nix:18 statix.enable, no ignore setting | conformist or statix ignore config |
| 165 | Migrate internal/job to clown job framework | R: converges with clown#117. V: alfa/job still exists but produces ringmaster jobs (FDR 0010). I: probably largely done via ringmaster | Check whether anything is left; else close |
| 144 | Merge-hook sudo breaks viewport | R: build-home-dry lives in eng; viewport hardening is crap#25 | eng: run sudo off TTY; crap: fail hard on TTY pierce |

## (d) Stale (done, duplicate or obsolete)

Candidates marked "confirm" rest on inference and need a quick check before closing.

| # | Title | Evidence | Next step |
|---|-------|----------|-----------|
| 422 | Forced CLI verbs need a gate? | R: operator ruling is no gate, deny agent Bash use of sc verbs. V: commit c743a1d (#422 Layer 1); FDR 0032:1111, FDR 0037:154 | Close |
| 419 | Self/child restart tooling | V: c942d87, 38f08f0, 249eb17; docs/features/0037-session-restart.md; cmd/spinclass/restart_cmd.go. I: live acceptance not verified | Operator runs the runbook, then close |
| 414 | godyn + dagnabit layout lane | V: e4766b5..22e43a0 (levels 1-7); justfile:5 lint includes lint-layout (:43). I: downstream flake bump unchecked | Close after operator confirms downstream bump |
| 407 | Re-pin as card-signed escalation | V: e9d4b69, c749aef, ae9efb7, a52df9a, a2af7f8. I: residual scope beyond items 1-4 unchecked | Close unless more wanted |
| 403 | Migrate build to dagnabit+godyn | R: duplicate of #414 | Close as duplicate |
| 392 | Record spawned worker's principal | V: echo/hooks/hooks.go:330-367 (principalForHook, recordOwnPrincipal); FDR 0032:120-129 | Close |
| 389 | close-this-session | V: cmd/spinclass/close_self_cmd.go (runCloseThisSession, runCloseSelfRun); FDR 0035:1067 | Close |
| 380 | Worktree-scoped stash/rebase | V: grit_stash (own shelf), grit_branch rebase/ownership; FDR 0036. I: rebase half unchecked | Confirm, then close |
| 379 | Move grit tools from moxy | V: nine grit_* tools in spinclass plugin (FDR 0036). R: moxy side tracked in moxy#482 | Close |
| 311 | core.bare flipped during nix develop | R: comment says root cause is a cmd/go bug elsewhere; nothing for spinclass to fix | Close |
| 264 | Canonical <repo>/<id> session refs | V: commands_session.go:372-417 uses s.Key(); sessionpick labels. Stage 2 (picker/confirm) not verified | Confirm internal/bravo/sessionpick, then close |
| 260 | Scope fallback to bare after systemd-run failure | V: bravo/hookrun/hookrun.go:583-619 runHookInScope with scopeStartMarker; scope_fallback_test.go | Close (#352 tracks warn-point follow-up) |
| 224 | Forge type and owner in session block | V: bravo/sysprompt/templates/worktree.md.tmpl:25 renders owner/forge; Forge workflow section at :33 | Close |
| 209 | spawn-session hangs behind background jobs | R: tool description says spawn is async. I: code not verified | Confirm, then close |
| 196 | Decoder drops [parent.sub] after scalar | V: regression tests in 0/sweatfile/dotted_header_test.go. R: comment says not reproducible on 0.1.30; left open for reporter; debug recipes in justfile say "delete once #196 closed" | Close on reporter confirmation; delete those recipes |
| 195 | devShell posture for hooks | V: bravo/hookrun/hookrun.go:454-472 runs hooks under `direnv exec` when .envrc exists. I: pre-commit/repair path unverified | Confirm, then close |
| 189 | Pre-commit hook not installed on live worktree | R: comment says rebuild and auto-rebuild-on-resume address it; V: AutoRebuildOnResume at sweatfile.go:114; remainder is #191 | Close |
| 188 | Kill pre-merge build tree on interrupt | V: Setpgid/cancel at hookrun.go:501-508; merge_cancel_test.go, hookrun/cancel_test.go | Close |
| 184 | FDR for per-commit pre-commit hook | R: CLAUDE.md cites FDR 0019. I: file not opened | Confirm docs/features/0019 exists, close |
| 180 | spawn-session opaque exit status, leaked worktree | V: foxtrot/spawn/spawn.go:404-425 logs to `.spinclass/spawn.log` (path only, not read); R: hello-timeout auto-reaps | Confirm error names log path, close |
| 172 | Restructure internal/ onto dagnabit levels | V: e4766b5..22e43a0; internal/0, alfa..foxtrot exist. R: facade-layer question remains | Close, or split facade question |
| 152 | Flaky clean_removes_merged | V: zz-tests_bats/lifecycle.bats:15 sets 30s. R: comment says mitigated 2026-08-09; broader case #216 | Close once CI green |
| 88 | operation_viewport on sc merge | V: commands_session.go:119 documents `--format auto/viewport/plain/ndjson`; FDR 0015 | Close |
| 83 | Token-conserve merge output via tap split routing | V: docs/features/0005 superseded by FDR 0015; merge/check use internal/present. I: Part 2 likely obsolete, not proven | Maintainer check, then close/retitle |
| 75 | Hook blob has only the tail | V: charlie/check/check.go:445 MultiWriter feeds full stream to madder in raw mode; structured path (481-518) stores parsed ndjson | Close or narrow to structured mode |
| 69 | Remove sc merge passthrough mode | V: no passthrough strings in internal/ (rg); FDR 0005 superseded | Close |
| 59 | FDR 0006 needs review | V: docs/features/0006 status `proposed`; spawn-session and close-child-session exist | Close or verify checklist |

## Recommended order for (a)

Smallest risk first; each should be done test-first. Unverified items last within their tier.

1. #412 (doc-only), #138, #105, #81 (docs/recipes; low)
2. #49, #45, #52 (test/dead-code cleanup; low)
3. #215, #230, #192, #371, #365, #423 (small, isolated; low)
4. #334, #329, #305, #280, #377, #413 (low, well-specified)
5. #342, #333, #320, #378, #368, #247, #231, #177, #240 (low-med)
6. #353, #352, #346, #340, #411, #373, #308, #283, #79, #78, #157, #236, #348, #388 (med; #348 and #308 are the widest)
7. Unverified, check before starting: #234, #205, #211 (high)

## Could not determine

- #398 (emitter of OSC11/CPR not found in spinclass Go), #217, #175 (checks not done).
- #425/#397 root cause; #394 leaking close path; #371 jobwake.ListPresence existence; #283 purse-first pin has mesa.Wrap; #211 push-race trace.
- (d) candidates resting on inference: #209, #195, #184, #180, #165, #83, #264, #380.
- Rows based on titles only: #354, #347, #339, #338, #337, #336, #306, #302, #272.
