# FDR divergence audit: FDR 0035, 0036, 0032, 0034 (2026-10-22)

A strict audit of four FDRs against the code at worktree HEAD 757de26. Every
normative statement got one row, with a status, evidence, a severity and a
fix. The per-FDR tables are in `2026-10-22-fdr-divergence-audit/`:

- `fdr-0035.md` (session confinement)
- `fdr-0036.md` (grit store tools)
- `fdr-0032-0034.md` (principals and handles; spawn with a clown profile)

## Method

- Statuses:
  - **MET**: the code read at the cited `file:line` does what the statement says. A test is named where one exists. "Live X" cites the FDR's live-evidence round that confirms it.
  - **DIVERGES**: the code, or a recorded live round, does something else.
  - **UNTESTED**: the FDR flags the statement as unverified, or the owner is another repo (clown, posh) whose code was not read here, or no test was found.
  - **SUPERSEDED**: a later section of the FDR replaces the statement (the section is cited).
- No row is written from memory. Each one cites code or a live round.
- The audit is read-only. Fixes landed afterwards as separate commits; the two security fixes are noted on their rows.
- The rows were drafted by subagents and then spot-checked. The rows verified
  by hand are listed below. Any other row's citation is the drafter's and
  should be re-read before it is acted on.

## Counts

| FDR | MET | DIVERGES | UNTESTED | SUPERSEDED |
|---|---|---|---|---|
| 0035 | 153 | 23 | 27 | 5 |
| 0036 | 76 | 15 | 11 | 4 |
| 0032 | 18 | 5 | 3 | 3 |
| 0034 | 37 | 7 | 5 | 5 |

## Top five by severity

All five are "security". The first two were found by the audit, not by a live
round. They are inferred from the code, were not exploited, and are now fixed.

1. **0035 row 183: the `unconfined` ask could be bypassed.** The ask lived
   only in the agent-side PreToolUse hook, and `handleSpawnSession` did not
   classify its caller. So a confined worker's call to its own frontend serve
   could spawn an unconfined worker as the operator.
   - Fixed in 029c3c8 for that case (vantage A): the serve refuses any spawn
     from a confined or unclassifiable caller.
   - **Correction.** This entry first said the fix covered "a process that
     reached the serve's MCP (F12)". It does not: the classification sees the
     serve's own principal, so a process dialling another session's serve
     (vantage B) is classified as that session. B is inferred, not observed;
     see `2026-10-22-fdr-divergence-audit/serve-handler-risk.md`.
2. **0035 row 195: close-self-run trusted a forged principal.**
   `close_self_cmd.go:210` took the principal from `CloseRequest.Principal`
   in agent-writable session state, so a forged value skipped the orphan
   check.
   - Fixed in 8446b92: the principal comes from the frontend mapping, and a
     confined session with no mapping is refused.
3. **0035 rows 82/125 (F12): no per-session serve socket.** F12 fails
   because the unit record names no `serve_socket` (`probes.go:1224`). It
   never connected to anything, so "the serve is reachable from the unit" was
   an inference, and F13 (private network namespace) passing argues against
   it. Until a round measures it (FDR 0035 lists the probes), cross-serve
   reachability (vantage B) is open and unproven. The per-handler gates cover
   vantage A; ranks and gates are in `serve-handler-risk.md`. Per-session
   sockets are clown's.
4. **0032 row 32-23 (D13): handle rights are recorded, not enforced.** An
   `observe`-only holder can reap, grant and release.
   - Evidence: `close_child_cmd.go:59-73`, `handles_cmd.go:118-122`.
   - Already documented as #355 and #360.
5. **0034 row 34-31: the trust anchors follow the caller's environment.**
   The signers file and the grant store resolve from `XDG_CONFIG_HOME` and
   `XDG_STATE_HOME` (`grantverify.go:66-70`, `grantstore.go:36-39`), so the
   CLI path can be pointed at an attacker-owned signers file.

Next by severity:
- **0032 row 32-12: the `SpawnedBy` session-key path.** The key is derived
  from the cwd, and it keeps reap authority after a transfer.
  `close_child_cmd.go:69-71`; the FDR records it in part as deliberate.
- **0035 row 159: close-this-session and the hook verdict resolve the caller
  through worktree state.**
- **0035 rows 16/149/150: the troupe account exposure.** It was found live in
  rounds N and O and closed by clown in round P.

## Rows verified by hand

- 0035 row 183: `spawn_cmd.go:50-85`, `:175-188`; `check.go:53-55`.
- 0035 row 195: `close_self_cmd.go:210`.
- 0036 row 28: `git.go:144` against `confined.go:51-53` and `:87-90`. For
  confined callers the explicit `GIT_DIR` mitigates it.
- 0036 row 85: `host_other.go:19`.
- 0032 row 32-12: `close_child_cmd.go:69-71`.
- 0034 row 34-31: `grantverify.go:66-69`.

## Live-round entries

The round P entry in FDR 0035 records that clown's troupe fix closed the
account-file exposure. It also records F10's zombie count growing from 2 to 6
as a clown hygiene item. F11 and F12 remain open.
