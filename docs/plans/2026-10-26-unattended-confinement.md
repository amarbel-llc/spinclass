# Unattended operation: confinement status

Date: 2026-10-26. Author: spinclass/firm-mulberry research pass (read-only; nothing
was run, built or committed).

Status: **confined sessions have been proven live for the isolation cut and the
frontend git path, in canary rounds J to P (2026-10-22). Nothing records a live run
of an unattended confined cycle (spawn, hours of work, land, close) or of any change
made after 2026-10-22. FDR 0035 is still `proposed`.**

Labels: **R** read in code, docs or an issue. **V per <ref>** a commit, FDR or issue
records a verified live run (I ran nothing, so I cite and do not re-verify). **I**
inferred. A capability with no recorded live run is not marked proven.

Siblings (not repeated here): restart `docs/plans/2026-10-26-unattended-restart.md`;
permissions `docs/plans/2026-10-26-unattended-permissions.md`.

Lane note: clown (unit launch, idle trigger, egress proxy), papi (token mint, TTL
sweep), posh and troupe are other lanes. Only the interface spinclass depends on is
named, and marked "dependency".

## 1. Proven live / tested against fakes / unverified

Live evidence is one host class (the "nikulin canary", spinclass 55b46c8 to e8bede1,
clown c0ec56b or earlier), recorded by the driver in FDR 0035 "Live evidence". A
round is secondhand to this page: R that it is recorded, V per FDR 0035 that it ran.

### Proven live

| Capability | Evidence |
|---|---|
| Confined spawn launches a worker in a transient unit; hello returns | V per FDR 0035 rounds J3, K (swift-teak, 4bd1590), L (41ce553), N (sunny-oak, e8bede1). The first spawn also timed out at the hello and left a live unit with no mapping (FDR 0035 "principal-to-session mapping"), fixed by 0a86f2c |
| Git cut: refs/logs read-only, raw `commit`/`checkout -b`/`update-ref`/https push fail; `status`/`log`/`diff` work | V per rounds J3, L |
| Nested read-only file binds (`.git` pointer, `config.worktree`) hold: EBUSY on mv/rm/ln | V per round L |
| Minted credential lives in frontend state, ENOENT in the unit before and after a grit push | V per round N (3ae2255, e8bede1) |
| grit add, commit, push through the frontend with the minted credential | V per round K ("first credential-store push"), L, M |
| grit force deny and stash refusal arrive as written | V per round K (FDR 0036 behaviours) |
| `sc spawn --unconfined` refuses with no terminal; logs; creates nothing | V per round N; also bats `zz-tests_bats/spawn_unconfined.bats` (R, runs a pty) |
| Probes F1-F8b, F13, F15 pass with the worker's principal; F9 ok before and after a push | V per round K, L, N |
| F10 passes with a zombie note; F11 no longer flags the worker's own agent; `sc list` shows the worker `active` | V per round N (2be2527, e8500c3) |
| `nix flake metadata` and `nix eval` run in the unit with read-only refs | V per round N, against a **fixture** flake only |
| Troupe account exposure (33 of 33 password files readable) found, then closed in clown | found V per round O; closed "per the round P report" (R, secondhand) |
| A stale serve wrote a stale unit spec (14 h old) | found V per FDR 0035 "A stale serve refuses" (#399) |
| Per-session forge credential mint, push, revoke for **unconfined** sessions | V per FDR 0028 promotion evidence (2026-09-03, "live round-trip, operator present") |
| Gate code runs in a real serve (in the wrong direction) | V per #406: on flic 2026-10-23 unconfined sessions were refused merge/spawn/attestation as "confined" (fixed dd1e022) |

### Tested against fakes or stubs only

| Capability | Test, and what is faked |
|---|---|
| Serve refuses a confined caller on every gated tool (spawn-session, resurrect, close-child, grant, merge, check, release, update-description, job-cancel, list, perms-list, host-load, restart-child, cancel-restart child form) | `cmd/spinclass/serve_caller_gate_test.go`: `asConfinedServeCaller` writes a fake unit record and serve record into a temp state dir. `TestEveryServeToolIsGatedOrDeclaredOpen` walks the registry (R). Vantage B (another session's serve) is **inferred, never measured** (R, FDR 0035) |
| Open tools for a confined caller: whoami, version, validate, list-handles (no accept), close-this-session, restart-this-session, cancel-restart (own) | same file, `openServeTools` (R) |
| Self-close (#389): request, refusals, runner re-check, principal from frontend mapping | `cmd/spinclass/close_self_cmd_test.go`. FDR 0035: "read from code, not from a live round". Trigger is clown's (dependency). #392 on a canary at 51fd193: `close-this-session` failed "could not tie this call to a spinclass session" (V per #392 comment); the fixes (7000be2, dd1e022) are tests only |
| Unit stop on close | `internal/bravo/confinement/stop_test.go`, `internal/delta/close/close_test.go` with a fake `systemctl` seam (`stopUnit`). Clown side: "frontend exits and removes the agent dir once the unit is gone (confirmed by the operator on smart-hemlock)" (V per FDR 0035, 2026-10-20). That the spinclass stop itself ran live is not recorded |
| Credential revoke on every close path | `internal/charlie/auth` tests, forged-record tests. Contrary live signal: ~300 leaked credentials for closed sessions (V per #394, 2026-10-21, open) |
| Ride out an ssh-agent outage in mint and base fetch (#411) | 8ec4808: fake mint-command and fake `ssh` that fail twice. Not live |
| `dontAsk` splice for a confined spawn | `internal/foxtrot/spawn/dontask.go` tests (R). Live: the first confined worker hung 30+ min on a prompt under `--enable-auto-mode` (V per FDR 0035). The round-K force deny is consistent with dontAsk being active (I). FDR still says "Unverified that the installed claude accepts dontAsk" |
| Profile rules R1-R4, tier-1 narrowing, unit-spec shape, F9/F10/F14 probe logic | `confine_test.go`, `confineprofile_test.go`, `confinement_test.go`: fake filesystem, fake `/proc`. FDR 0035: "A full confined spawn past the preflight is not tested; it would need a faked systemd and clown" (R) |
| Hook verdicts: `CLOWN_UNIT` defer, dontAsk ask-to-deny | `internal/delta/hooks/*_test.go`; permission_mode field "not verified in this repo" (R, 3b1ff43) |
| Agent Bash guard for `sc`/`spinclass` verbs (#422) | c743a1d tests. Commit says "a guard, not a boundary" (R) |
| F11 census by unit | tests (R). #386 items 1-3 open (clown writes no `harness` field; juggler comm unverified) |

### Unverified (no recorded live run, or only a doubt)

- Any confined session running for hours or days. Longest recorded run is one canary
  round (I). No soak. R: no recipe exists; `justfile` has only `debug-confinement-check`
  (the confinement harness and conformance recipe in
  `docs/plans/2026-10-22-confinement-harness-and-conformance.md` are plan only).
- `sc confinement-check` end to end on a host with the current builds. Last recorded:
  round P. F11 (an unconfined driver exists) and F12 (record names no `serve_socket`,
  clown) were the open failures (V per FDR 0035 round P).
- direnv in the unit (blocked: `.envrc` not allowed); `nix flake` on a real flake with
  git inputs; `just` runtime dir (round K: read-only, P2-P6 did not run; later rounds
  ran probe recipes, so likely fixed, I).
- nix fetch through the egress proxy. `egress_allow` is omitted from the spec unless a
  trusted sweatfile sets it, so clown's default list applies (R, `confine_test.go:149`).
  The default list's contents are clown's; no run fetches a real flake input from a unit.
- Vantage B (cross-session serve reach) probes: planned, not run (R, FDR 0035).
- Ringmaster/troupe state dirs bound rw into the unit: writability "not tested" (R, round O).
- Confined resume and confined restart: not built (R, FDR 0035 "Not covered: ... confined
  resume"; runbook "Confined workers: restart in the unit. clown owns this, untested").
- Stale-serve refusal resolving the right `spinclass` on PATH (R, FDR 0035 "Unverified").
- Anything deployed after 2026-10-22 (dd1e022, 8ec4808, 7000be2, c743a1d, 967a0db) in a
  confined unit.

## 2. What is missing for a confined session to work for days unattended

1. **Nothing lands a confined worker's branch without an operator terminal.** R: every
   merge/check/attest tool refuses a confined caller and names `sc merge <target>`
   (`commands_mcp_only.go:482-486`); R (c743a1d, #422 ruling 2026-10-25): an agent's
   Bash `sc`/`spinclass` use is denied except list, version, validate, whoami,
   list-handles, perms list. There is no MCP tool that merges another session. I: so a
   coordinator agent cannot land a child either. A changed `[[post-merge]]` section also
   needs a terminal-typed digest (1cb83c5, R). Permission aspects: permissions page.
2. **Credential lifetime** (corrected 2026-10-26; the first version of this page
   wrongly treated 12 h as the token's lifetime). R: the token is minted once, at
   session creation (`shop.Create` is `auth.Mint`'s only caller). The merge neither
   mints nor refreshes; `auth.MirrorInto` points the landing worktree at the same
   credential file. R: `--ttl 12h` in the mint-command (spinclass's own sweatfile sets
   it) is papi's sweep TTL, and FDR 0028:95 says "Forgejo tokens have no native expiry
   today". The token stays valid until revoke or a papi sweep. V: spinclass/firm-mulberry
   minted 2026-10-22T02:09Z and its merges pushed through that credential on 2026-10-26
   (bdb34e6 through 913484b), about 4.4 days later. So 12 h is not a limit in practice
   today. I: it becomes one if papi's TTL sweep (papi#73, papi's lane) is enforced, and
   then a re-mint is refused while the unit runs (3ae2255).
3. **Spawn needs a live ssh-agent at mint time.** R: FDR 0028 ("session creation needs a
   live agent at mint time"). V per #411 (flic, 2026-10-23): forwarded agent flapped,
   spawn failed twice, merge fetch failed `Permission denied (publickey)`. 8ec4808
   retries 2 s then 8 s (about 10 s total; the issue asked for ~30 s; R) and does not
   tell "no key" from "forge rejected" in a merge fetch (ask 3, R). Fakes only. With
   nobody at the keyboard the forwarded agent can be gone for hours (I). #390 (manager
   spawn-grant without a live agent) is open.
4. **Credential leak.** V per #394: ~300 leaked per-session tokens, source not found,
   open. A two-week run multiplies it (I).
5. **No resume or restart story for a confined session.** R: confined resume not built;
   #400 (open): worker can plant `.mcp.json`, `.claude/settings.local.json` that an
   unconfined resume loads; `docs/plans/2026-10-26-400-planted-harness-config-options.md`
   recommends sticky-confined resume, no decision (R). Restart arc: restart page.
6. **Self-close trigger not in spinclass.** R: #389 open, "the trigger is clown's
   frontend and is not built here" (FDR 0035); the runbook shows clown now writes
   `close-self-run.log` for restart (R), whether idle also fires `close-self-run` is
   clown's and unconfirmed (I). #392 (open) shows a spawned worker could not self-close
   on 51fd193.
7. **Unit lifecycle gaps.** R: only `close.RunResolved` stops a unit; `sc clean`'s
   abandoned-worktree removal does not (FDR 0035; `StopUnit` has one non-test caller,
   `internal/delta/close/close.go:534`). A host reboot leaves stale unit records (I).
   #270 open (closed session leaves a clown process); 4387264 only warns.
8. **No resource limits.** R: #387 open; only the read-only `host-load` tool exists
   (967a0db). No TasksMax/MemoryMax in the spec (R, `confine.go` fields). F10 zombies in
   a unit grew 2, 3, 5, 6 across rounds (V per FDR 0035), a clown hygiene item; whether
   they accumulate to a task limit over days is I.
9. **Unconfined spawns still ask; a confined worker cannot spawn.** R: `unconfined` always
   asks until #384 (open); `confinedPreflight` and the serve gate refuse spawn from a
   confined caller. So fan-out under confinement exists only from an unconfined
   coordinator. Permissions page covers the prompt side.
10. **`dontAsk` converts surprise into denial.** R: a confined worker never prompts, so a
    call outside its allow list or hook approvals is auto-denied (FDR 0035). A worker that
    needs something unforeseen will loop or give up; no human sees it (I).
11. **Check state.** R: F11 fails while any agent on the host is unconfined; F12 fails
    until clown records `serve_socket`. I: if `[profile-rights] enforce` is on, a
    card-rooted grant for a profiled spawn cannot pass (FDR 0034 gate); the setting on the
    target host was not read.
12. **Egress and nix fetch** unverified (section 1). **direnv** unverified.
13. **Stale serve** guards only the confined spawn (R); other surfaces need restart
    discipline after each deploy (restart page).
14. **Handles for confined holders** undecided (#406 open); orphan marker ignores holder
    liveness and grants never expire (#357, #356, open): a restarted spawner can leave
    workers held by a dead principal (I).
15. **Review gate.** R: #339 (D17 security review) open; required for `accepted`.

## 3. Smallest ordered list that closes the gaps

1. Operator decisions in section 4 (items 1-4); nothing below is safe to build first.
2. Live soak recipe for one confined worker: spawn, 2+ h of grit commits and pushes,
   idle, land by operator, close; run `sc confinement-check` at start and end. None yet.
   spinclass (recipe) with clown (unit); doubles as the first live check of dd1e022,
   8ec4808, 7000be2, c743a1d. Also settles items 4, 7, 12 of section 2.
3. Credential longevity: confirm with papi whether its TTL sweep is enforced (V above:
   a 4.4-day-old token still pushes). If it is, either a sweep TTL covering the run or a
   frontend re-mint that does not touch the unit's `config.worktree` (the reason a
   re-mint is refused). None yet. papi (sweep), spinclass (re-mint). Related #390 (open).
4. Finish #411: a longer bounded retry window and the "no key" versus "rejected" split.
   #411 (open). spinclass.
5. Find the credential leak: #394 (open). spinclass, with papi#89 sweep.
6. A landing path for confined branches that does not need a terminal, if the operator
   wants one. None yet. spinclass.
7. #400 fix per the operator's pick (sticky confined resume is the recommended option).
   #400 (open). spinclass; harness flags would be clown's.
8. Unit lifecycle: `sc clean` stops units; sweep stale unit records after reboot; close
   leaves no process (#270). Stop-in-clean: none yet. spinclass. #270: spinclass.
9. Per-unit TasksMax and MemoryMax from sweatfile keys (smallest slice of #387). #387
   (open). spinclass writes the spec; clown must accept the fields.
10. Confirm the self-close and restart idle triggers fire for a confined session. #389,
    #392 (open). clown (trigger) and spinclass (resolution).
11. Close F12 and #386 items 1-3: clown writes `serve_socket` and `harness`. #386
    (open). clown.
12. Egress: record clown's default allow list; run one real flake-input fetch and one
    direnv load in a unit. None yet. clown (list), spinclass (test).
13. Vantage B probes (cross-session serve reach). None yet. spinclass with clown.
14. #406 rights for confined holders; #357/#356 orphan and expiry. spinclass.
15. #339 review, then FDR 0035 promotion. spinclass.

## 4. Decisions only the operator can make

1. **Landing.** Must a confined worker's branch always be landed by you at a terminal
   (current state, EPIC 0001 "engineer-builder cannot merge"), or may an unconfined
   coordinator land children through an MCP tool (needs a new tool and a ruling)?
2. **#400.** Sticky confined resume (refuse or always-ask an unconfined resume of a
   once-confined session), or one of the other four options? Does it also cover `sc exec`?
3. **Credentials.** Whether papi's TTL sweep stays unenforced for the run (tokens then
   live until revoke), whether a re-mint while the unit runs is allowed, and whether an unattended spawn may depend on your forwarded agent
   (#411) or needs the #390 spawn-grant (which widens what a manager can push).
4. **Scope of the run.** Which hosts run confined sessions for the two weeks (the canary
   host had it enabled; flic's setting was not read), and whether `[confinement]
   enabled` and `default-spawn` stay on there.
5. **Enforcement.** Stay with `[profile-rights] enforce` off for the run, accepting that
   F11 and F12 fail and no grant is card-rooted? (I: the check cannot pass while any
   interactive agent runs unconfined.)
6. **Fan-out.** Is an all-confined fleet wanted (then workers cannot spawn), or does an
   unconfined coordinator stay, and does it need #384's blessing so its unconfined spawns
   stop asking?
7. **Limits.** Per-unit CPU, memory and task numbers, and the load threshold and units
   for admission (#387 thread: left as a parameter pending a policy ruling).
8. **Confined holders** (#406): observe only, close of probes, or none.
9. **Accepted residuals**, restated for the run: operator uid kept in the unit (no
   per-session uid), abstract unix sockets reachable, `ssh` key files not blocked for
   remote verbs (ruled 2026-10-21), grit commits unsigned (ruled 2026-10-25, #426
   closed).

## 5. Update 2026-10-26 (spinclass/light-cedar): clown's limits and dead-man socket

Read from clown's docs (R; clown FDR 0020 and its unattended confinement note,
which record host tests as V on clown's side; not re-run here):

- **Decision 7 (limits) is answered clown-side, partly closing #387.** Every agent
  unit carries `MemoryHigh=6G`, `MemoryMax=8G`, `MemorySwapMax=2G`,
  `TasksMax=4096`, `CPUQuota=400%`, `IOWeight=50`, `OOMPolicy=stop`, `Restart=no`;
  an unattended unit (`CLOWN_UNATTENDED=1`) also `RuntimeMaxSec=604800`. The
  admission threshold and load units (#387) stay open; spinclass's `host-load`
  is the read-only view.
- **7 days is shorter than the trip.** An unattended unit ends at 7 days and
  nothing relaunches a stopped unit (I, from "nothing restarts a hung unit
  (ruled)" and the transient-unit note). A two-week run needs each confined
  session restarted (FDR 0037) at least weekly, or the operator's ruling on a
  longer `RuntimeMaxSec`. Raised with the coordinator.
- **Dead-man socket (clown#319).** The frontend's death by any signal ends the
  unit within about 10 s (plus up to 5 s); the result file then has no
  `exit_code`. Residual: a hung frontend that stays alive is not frontend death
  (spinclass's `session-health` and `restart-hung-child` are the answer, FDR
  0037), and a host reboot or logout ends the unit with nothing relaunching it.
- **Not done yet (deliberately):** requiring the `frontend-deadman` capability in
  `confinement.RequiredCapabilities`. It must wait until the deployed clown
  reports it, or every confined spawn is refused; check with the clown on PATH
  before turning it on.
