# Unattended operation: permissions status

Date: 2026-10-26. Status: draft status page, docs only, nothing changed in code.
Goal: sessions run two weeks with no human answering a prompt. A prompt nobody
answers is a stuck session.

Sibling pages (cross-reference, not duplicated):
`docs/plans/2026-10-26-unattended-confinement.md`,
`docs/plans/2026-10-26-unattended-restart.md`.

Evidence tags: R = read in code/docs/issue; V = freud output I ran (or "V per
<ref>"); I = inferred. Dependencies owned by other lanes (Claude Code, clown,
moxy, piggy) are marked DEP.

## 1. Proven live / tested against fakes / unverified

| Mechanism | State | How |
|---|---|---|
| PreToolUse hook verdicts (allow / ask / deny / rewrite) | Tested against fakes (R: `internal/delta/hooks/hooks_test.go`, `dontask_test.go`); live for allow/ask (V per freud_interruptions 2026-10-26: spinclass-owned tools appear only as `[auto]`, never prompted) | Hook output format `permissionDecision` is the Claude Code interface (DEP, R `hooks.go:827`) |
| Perms tiers (`~/.config/spinclass/permissions/{global,repos/*}.json`, PermissionRequest hook `RunCheck`) | Tested against fakes (R: `perms/check_test.go`, `tiers_test.go`). Live effect unverified/null: `perms-list` shows empty global and repo tiers (R #372 body) | I: tiers do nothing for the tools that actually prompt (see 2) |
| Always-ask floor (`perms.AlwaysAsk`: unconfined spawn, non-carved spawn, force close, force restart, every grant, every release, list-handles accept) | Tested against fakes (R: `alwaysask_test.go`, fail-closed on non-bool `force`/`unconfined`/`accept`). Ordering ahead of the auto-allow switch is called "verified" in a code comment (R `hooks.go:688`); no live prompt observed (V: freud shows 0 prompts on any spinclass tool, 7 days) | |
| Confined spawn auto-allow (#382) | Tested against fakes (R `alwaysask_test.go` carve-out tests; commit f61f51d "closes #382"). Unverified live in this window (I: no freud row for spawn-session) | `SpawnConfined` is a CLI-installed seam; nil means always ask (R `check.go:27`) |
| Auto-allowed spinclass tools (list, validate, update-description, session-job-cancel, close-child w/o force, version, whoami, list-handles w/o accept, perms-list, host-load, close-this-session, restart-*, cancel-restart, merge/check/nothing-but-the-truth when the sweatfile declares the hook/skills) | Tested against fakes (R `hooks.go:703-759`). Live: V per freud_interruptions 2026-10-26, none of these appear as prompted in 15 sessions / 7 days | Merge/check allow only if `hasPreMergeHook`; otherwise falls through to no decision, i.e. Claude Code's own default (R) |
| dontAsk conversion (every `ask` becomes `deny` in a dontAsk session) | Tested against fakes (R `writeDontAskVerdict`, `dontask_test.go`). The `permission_mode: "dontAsk"` value is "unverified in this repo" per the code (R `hooks.go:114`, grit(7)) | DEP: Claude Code hook input. Confined workers get `--permission-mode dontAsk` (R commit 24618df); unconfined spawns start in auto mode (R commit 12aa4a1) |
| Agent Bash `sc` guard (#422 Layer 1) | Tested against fakes (R `agent_sc_guard_test.go`, commit c628262 "more shapes"). Not a boundary, by its own comment: alias, `bash -c`, copied binary bypass it (R). No freud row shows it firing (V: the 6 rejections on 2026-10-21 are `run_recipe`, not Bash) | Operator ruling 2026-10-25 on #422: no Bash-side ask gate, Bash path should not exist for agents (R, issue comment). Read-only verbs allowed: list, version, validate, whoami, list-handles (not `--accept`), perms-list, host-load, implicit-session-key, attestation (R `agent_sc_guard.go:24`) |
| Subagent merge/attestation deny | Tested against fakes (R `hooks.go:655`, hooks_test) | Keyed on hook `agent_id` (DEP: Claude Code) |
| Sweatfile/.spinclass guards (deny `.spinclass` paths; ask or deny sweatfile Write/Edit) | Tested against fakes (R `hooks.go:765-781`, commit 3280659). Sweatfile edit defaults to ASK, i.e. a prompt (R sweatfile(5)); `deny-sweatfile-edits` is the opt-in non-prompt form | |
| grit store verdicts (FDR 0036) | Tested against fakes (R `grit_policy_test.go`, `TestGritDefaultPermissionsForAnUnconfinedSession`, spinclass-grit(7) table). Live: V per freud_interruptions 2026-10-26, only one `spinclass...grit_stage [auto]` row (interrupted, 1, not prompted); zero prompts on the spinclass grit tools | Read verbs allow; writes allow only on owned refs in the own worktree; force, default branch, foreign refs, unknown verb ask (R grit(7)). Outside any session every write asks (R) |
| Handle-right enforcement (FDR 0032 D13) | Enforced for 3 of 8 rights: `close` (close-child-session), `grant` (grant-session-handle), `restart` (restart-child, cancel-restart) (R `close_child_cmd.go:172`, `handles_cmd.go:216`, `restart_cmd.go:329`; pinned by `handle_rights_table_test.go`, fakes). `observe`, `instruct`, `cap`, `merge`, `check` are recorded, enforced by nothing (R test + #429) | Not security-reviewed: #339 (D17 review before any right is a containment guarantee) and #362 (`pass` level) open |
| Operator-card grants (`sc profile-grant-bless`, `profile-grant-import`, `resume --repin-profile`) | Tested against fakes (R `profilegrant_cmd_test.go`; fake signer seam). Commits record "live evidence" for confinement rounds, but no recorded live card run found in what I read (I) | CLI-only, TTY-only, refuses when `CLOWN_SESSION_ID`/`SPINCLASS_SESSION_ID` set (R `profilegrant_cmd.go:57,81`). Needs a physical card tap + PIN (R). DEP: piggy `operator-act`, SSH agent holding the card. TTL 1m to 168h, default 24h (R) |
| Merge attestation gate (FDR 0007 / 0031) | Tested against fakes (R `merge/policy.go`; GateTerminal exempt by operator decision #326, GateAttested, GateNeedsExemption runs predicates from the merge-base tree, 5 min cap) | Agent self-attests via `nothing-but-the-truth`; "any non-empty reasoning accepted" and `used:false` allowed (R #301, open). So the gate needs no human today, and also proves little |
| Post-merge section change gate | Tested against fakes (R sweatfile(5) l.313-326, commit 1cb83c5) | Agent merges can never pass it; needs an operator-run `sc merge --accept-post-merge <digest>` at a terminal (R) |
| Exemption predicates | Tested against fakes (R policy.go) | Definition read from the merge-base commit, not the branch (R). Only helps diffs a predicate vouches for |

Freud evidence (V, `freud_interruptions` project spinclass, 7 days, run 2026-10-26):
60 prompted, 54 approved, 6 rejected, 6 interrupted, over 15 sessions.

## 2. What is missing for days-long runs without a human answering prompts

Where the real prompts come from (V, freud 2026-10-26; all are moxy or
just-us tools, none are spinclass tools):

| Tool | Prompted | Note |
|---|---|---|
| `mcp__plugin_moxy_moxy__folio_read` | 28 | path policy outside CWD/siblings (R moxy tool doc) |
| `mcp__plugin_moxy_moxy__folio_ls` | 15 | same |
| `mcp__plugin_moxy_moxy__grit_branch` (moxy grit, not spinclass grit) | 9 | probably `repo_path` outside own worktree (I) |
| `mcp__plugin_moxy_moxy__folio_glob` | 6 | same |
| `folio_rm`, `folio_file-type` | 1 each | |
| `mcp__plugin_just-us_just-us__run_recipe` | 0 prompted, 6 rejected (all 2026-10-21) | operator rejected recipes; I: a rejection in an unattended session is a stall |

Reading: 59 of 60 prompts are read-style filesystem or git tools on paths the
session reaches outside its own worktree (I: matches #372, a coordinator
reading child worktrees). Spinclass's own permission surface prompted nothing.
Caveat: 15 sessions, one week, one project; interactive sessions with a human
present (I), so absence of spinclass prompts is not proof for unattended runs.
Per-day prompts: 10, 4, 11, 17, 1, 17 (V). There is no counter that separates
background-agent prompts from main-agent prompts (R #369 open, no body).

Prompts that remain by design (R `AlwaysAsk`, unchanged for unattended runs):

- `spawn-session` unless confined; any `unconfined` spawn (until blessing, #384).
- `close-child-session` with `force`; `restart-this/child-session` with `force`.
- every `grant-session-handle`, every `release-session-handle`,
  `list-handles` with `accept`. A coordinator that must hand a child a handle
  stalls here; a pending grant is also never delivered or expired (R #356).
- grit: force, default branch, foreign ref, anything outside a session, any
  `repo_path` into another session's worktree without a handle.
- sweatfile Write/Edit (ask by default).
- In a `dontAsk` session each of these becomes a hard deny, so the session
  does not hang but the action is impossible (R). Confined workers are in this
  regime (R). Unconfined detached workers start in auto mode (R commit 12aa4a1);
  what auto mode does with an `ask` is DEP (Claude Code).

Needs a human regardless (R):

- Post-merge section change: operator terminal, `--accept-post-merge`.
- Card acts: `profile-grant-bless`, `resume --repin-profile` (#407 is the
  proposal for a signed escalation instead of a typed command, open); needs
  PIN and touch, TTY, not from an agent runtime.
- `sc resume` confirmation (`active` session warns, `-y` does not skip) (R CLAUDE.md).
- Merges with no pre-merge hook in the sweatfile fall through to Claude Code
  default, not an allow (R `hooks.go:747-754`); I: that is a prompt in a repo
  lacking `[hooks].pre-merge`.

Structural gaps:

- Tiers are empty and cannot express MCP argument conditions (R `check.go`
  comment: `MatchingRule` discards MCP tool input). So allow-listing the moxy
  read tools is only possible whole-tool, not path-scoped.
- No parent-mediated approval path for a worker's prompts (R #220 explore, open);
  no MCP elicitation (R #254 explore, open). Unattended means neither a parent
  nor a human can answer.
- Handle rights `observe`, `merge`, `check`, `instruct`, `cap` are decoration
  (R #429): a delegation of "may merge on my behalf" is not enforceable.
- Rights lattice has not had its security review (R #339) and cannot confer a
  right the holder cannot use (R #362).
- Attestation is self-attested (R #301); D16 signed records from distinct
  subagent principals not built (R #337).
- No audit counter for prompts (R #369). Only the per-session
  `.spinclass/hook-verdicts.ndjson` exists (R commit c029a54); it logs
  spinclass tools only, so it would not have seen the 59 moxy prompts (I).

## 3. Smallest ordered list of work

1. Allow-list read-only moxy tools for the paths a coordinator reads (own worktree, held children's worktrees): #372 (spinclass; folio path policy itself is moxy lane, DEP). Status: implemented in the PreToolUse hook (`internal/delta/hooks/moxy_read.go`, tests `moxy_read_test.go`), uncommitted at time of writing; tested against fakes, unverified live. See `spinclass-internals(7)` READ-ONLY MOXY AUTO-ALLOW.
2. Count prompts by origin (main vs background agent) so the two-week run can be measured: #369 (spinclass + stats-me).
3. Decide and write down what the fleet does at an `ask`: dontAsk-deny vs park; document auto-mode behaviour for unconfined workers: none yet (spinclass docs; DEP Claude Code behaviour).
4. Confirm `permission_mode: "dontAsk"` value against a live Claude Code hook payload (currently "unverified in this repo"): none yet (spinclass test; DEP Claude Code).
5. Card-rooted grant letting a named session spawn unconfined children without a prompt: #384 (spinclass; DEP piggy).
6. Signed escalation for profile re-pin instead of a typed terminal command: #407 (spinclass + piggy).
7. Enforce or retire the unenforced handle rights (`merge`, `check`, `observe`, `instruct`, `cap`): #429 (spinclass).
8. Security review of the rights lattice; `pass` level: #339, #362 (spinclass).
9. Make attestation require subagent-run skills / signed records: #301, #337 (spinclass).
10. Pending grant delivery and expiry so a grant does not strand a session: #356 (spinclass).
11. Parent-mediated or elicited approval for worker prompts: #220, #254 (spinclass + clown; explore only).
12. Make agent merges able to accept an operator-vetted post-merge digest ahead of time, or pre-authorize it with a card-signed record: none yet (spinclass; operator decision, see 4).
13. Sibling pages cover confinement and restart prerequisites: see the two sibling pages above.

## 4. Decisions only the operator can make

- Which always-ask gates may be relaxed for the two weeks: `grant-session-handle` (every grant asks today), `release-session-handle`, `spawn-session` outside confinement, force close, force restart. Each is a decision about token spend, orphaning, or killing a running job (R reasons in `check.go`). Not a code question.
- Whether to issue a time-bounded card-signed grant (max 168h in the code, R) before leaving, and for what: unconfined spawn (needs #384 built first), profile re-pin, post-merge digest. A 168h cap means two weeks needs two card taps; confirm that is acceptable (R/I).
- What an unanswerable `ask` should do for unattended sessions: deny (dontAsk), park until a human returns, or ask a parent session. The code only does deny (R). Decide before relying on unconfined workers (I).
- Whether tiers (empty today) should carry whole-tool allows for folio_read/ls/glob and moxy grit_branch, accepting that argument-level scoping is not available (R). Alternatively fund #372.
- Whether self-attestation (#301) is acceptable for two weeks, given terminal merges are already exempt by operator decision (#326, R) and agent merges only need a self-written attestation.
- Whether post-merge section changes are frozen for the window, since no agent can land one (R).
- Whether to accept that the Bash `sc` guard is a guard, not a boundary (operator ruling 2026-10-25 R), and treat confinement as the boundary for unattended agents.
