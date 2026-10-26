# Unattended spawn without the operator's agent (#390, #411)

Question: with the operator away two weeks and the forwarded/card ssh-agent
down, what can a coordinator do to spawn or respawn a worker, what exists, and
what is the smallest safe change. Design note; no code.

Labels: R = read in the cited file/issue, I = inferred, V = verified by a run
(none; this note ran nothing). Line numbers are at the firm-mulberry worktree
(HEAD 41d6fa5). `internal/` paths are relative to the repo root.

## Short answer

Today a coordinator can spawn nothing without the agent. A new spawn needs the
card twice (mint, then the base-branch fetch), and the retry that exists (#411)
waits about 10 s. Existing sessions keep working on the token they already
hold, and that token stays valid unless papi's TTL sweep revokes it (today it does not: see "Token expiry" below; papi#91).
The smallest safe change that makes the trip workable is a pre-minted token
pool (papi#80) plus a spinclass hook that takes a token from the pool instead
of running the mint-command, with a sticky socket list so respawns do not trip
clown's boundary check. The spawn-grant (#390/papi#88) is the right long-term
answer and is the larger build.

## 1. What needs the agent today

### Spawn (new worktree), in order

1. `shop.Create` -> `createWorktree`, only when the worktree path does not exist
   (`internal/echo/shop/shop.go:97`). `spawn.Launch` calls it
   (`internal/foxtrot/spawn/spawn.go:212`), passing the repo's
   `AllowStaleBase` / `AllowNoCredential` (R).
2. **Base-branch fetch, over ssh, from the root checkout.**
   `basebranch.Freshen(repoPath, allowStale, required=true)`
   (`shop.go:115`) runs `target.Fetch(fctx, repoPath)`
   (`internal/bravo/basebranch/basebranch.go:176-181`). It runs before the
   worktree exists, so no per-session https rewrite applies; origin is ssh, so
   this uses the agent (FDR 0028 "Creation-time fetches stay on SSH", line 87,
   R). On creation it is wrapped in `agentretry.Do` (`basebranch.go:184-185`);
   a persistent failure is fatal unless `allow-stale-base` (`basebranch.go:155-168`).
   `--allow-stale-base` deliberately has no MCP parameter (#250, `spawn.go:208-211`),
   so only the repo/global sweatfile can set it (R).
3. **Credential mint.** `ProvisionCredential` (`shop.go:138`, `168`) -> orphan
   sweep (`auth.SweepOrphans`, `shop.go:175`; each revoke runs the pinned
   revoke-command, which per FDR 0028 line 74 is also `--card-login`, so the
   sweep needs the card too, but a failure is only reported,
   `auth.go:701-703`) -> `auth.Mint` (`shop.go:212`) -> mint-command
   `papi forge token mint ... --card-login ... --ttl 12h` (FDR 0028 line 73,
   R). `--card-login` signs a login nonce over `$SSH_AUTH_SOCK`: the failure
   text in the tests is "papi: sign the login nonce: ... no slot-9A ecdsa key
   in the agent ($SSH_AUTH_SOCK)" (`internal/charlie/auth/mint_retry_test.go:28`,
   R). So yes: card-login needs the agent, once per mint. A failed mint tears
   the worktree and branch down (`shop.go:215-220`) unless
   `allow-no-credential`, in which case the worker "pushes over the inherited
   ssh-agent" (`shop.go:222`), which is useless with the agent down.
4. **Unit spec (confined spawns).** `writeUnitSpec`
   (`internal/foxtrot/spawn/confine.go:298`, called `spawn.go:234`) reads
   `os.Getenv("SSH_AUTH_SOCK")` (`confine.go:320`) and
   `buildUnitSpec` adds `Sockets = [{path, "piggy-agent"}]` only when it is
   non-empty (`confine.go:275-277`); `confine_test.go:138,147` pin both
   branches (R). No agent need here beyond the env var being set; it does not
   dial the socket.
5. The confinement check's F2 fact fails with "no agent socket
   (SSH_AUTH_SOCK) to identify the agent by" when the var is empty
   (`internal/bravo/confinement/probes.go:329`; `driver.go:72` fills it from
   the env). Whether a failed F2 blocks a spawn or only `sc check` was not
   traced; treat as open (I).
6. Unconfined workers inherit `SSH_AUTH_SOCK` on purpose (`spawn.go:528-533`,
   R); nothing else in spawn dials it.

Everything else in the grep of `SSH_AUTH_SOCK|ssh-agent|piggy|pivy` is not on
the spawn path: `operatoract` (card-signed acts for `sc profile-grant`,
`internal/alfa/operatoract/operatoract.go:73-77`), grit remote verbs strip the
agent env (`internal/alfa/store/git/remote.go:34`), `dodder.go` is unrelated.
Verifying an existing profile grant is offline (`internal/bravo/grantverify`:
sshsig check against an allowed-signers file, R), so a spawn **with a profile
whose grant was card-signed before the trip** needs no agent for that part.
Grant TTL is capped at 168 h (`internal/0/sweatfile/sweatfile.go:183`), i.e.
one week: a two-week trip needs grants re-issued or two sequential grants (I).

### Resume / restart / respawn

- **Resume** (`shop.go:374-377`): `Freshen(..., required=false)` degrades to a
  skip on a failed fetch and never errors (`basebranch.go:112-116`). No mint:
  "`sc rebuild` and resume auto-rebuild do not re-run the lane" (FDR 0028
  line 60). The confined spec is only checked, never rewritten
  (`cmd/spinclass/resume_confined.go:21-27,33-53`), so the socket list stays
  what the first launch recorded. **Resume works with the agent down** (R).
- **Restart** (FDR 0037): spinclass authorizes and records, clown relaunches the
  same harness; it goes through the same resume path and spec (R for the
  split; the relaunch internals are clown's, I).
- **Respawn of the same worktree.** There is no such spinclass verb today:
  `spawn-session` always creates a fresh worktree. If a respawn re-ran
  `writeUnitSpec` for the same worktree (resurrect does re-run the credential
  lane, `internal/foxtrot/resurrect/resurrect.go:84`, so resurrect needs the
  card; whether it also rewrites the spec was not traced, I) with no
  `SSH_AUTH_SOCK`, the sockets list loses `piggy-agent`, and per the
  coordinator's constraint clown records the launch boundary and refuses a
  changed one. That constraint is not in this repo; taken as given.
- **Stale socket path (I).** A forwarded agent's socket path changes per ssh
  connection, and the recorded spec holds the path from spawn time. If clown
  binds a path that no longer exists the unit may fail to start. Needs
  confirming with clown; irrelevant if the agent is the local user-service
  socket (stable path, as in `confine_test.go:98`).

## 2. What a coordinator can do today with the agent down

| Action | Works? | Why |
|---|---|---|
| Resume / restart an existing session | Yes | no mint, spec reused (above) |
| Worker pushes/fetches in its worktree | Yes until the token expires | credential helper + insteadOf in the worktree config (`auth.Inject`, `auth.go:515-537`), token in frontend state (`auth.go:485`) |
| `merge-this-session` of an existing session | Yes until the token expires | fetch from the session worktree (`merge.go:773-790`, comment: "a dropped ssh-agent cannot fail it"); the landing worktree is wired from the session's credential (`MirrorInto`, `merge.go:615`, `auth.go:545`, R). Needs the session to have minted; a session created with `allow-no-credential` still uses ssh |
| Close / clean | Yes, with a warning | revoke failure is recorded and retried by session id (`auth.go:586-587,702`); the token then lives to its TTL or the papi sweep |
| Spawn a new worker | No | steps 2 and 3 above |
| Resurrect | No | re-runs the credential lane |
| Any card act (`sc profile-grant`, escalation) | No | `operatoract.go:77` |

Token expiry is a conditional risk, not a 12 h deadline. The mint-command passes
`--ttl 12h` (spinclass's own sweatfile, R), but Forgejo tokens have no native
expiry (FDR 0028 line 95): the TTL is papi's sweep. V:
spinclass/firm-mulberry minted 2026-10-22T02:09Z and its merges pushed through
that token on 2026-10-26, about 4.4 days later
(docs/plans/2026-10-26-unattended-confined-merge-and-delegation.md section 3).
So today the sweep does not revoke live sessions' tokens. Whether it is meant
to is the open question papi#91 asks. If papi ever enforces it, every session
older than the TTL loses push. `merge-this-session` then fails at the
fetch/push with a 401, and re-minting needs the card. #411's retry would not
help: it only retries agent-shaped failures (`agentretry.go:31-38`), and a 401
is not one.

## 3. #411 and #390 state

- **#411** (open, 0 comments, R): asked for (1) retry ~3 tries over ~30 s, (2)
  name the agent as the cause, keep `--allow-no-credential` hint separate, (3)
  merges distinguish "no key" from "forge rejected key". Landed (8ec4808, per
  the brief; `internal/0/agentretry`): delays 2 s and 8 s, 3 attempts, ~10 s
  total (`agentretry.go:16`), applied to the mint (`auth.go:427`) and the
  creation fetch (`basebranch.go:185`) only; the cause hint (`agentretry.go:53`).
  Open: the window is a third of the ask; merges are not retried or
  distinguished (the merge fetch uses the token, so mostly moot until the
  token expires); `allow-no-credential` text at `shop.go:141` still reads as a
  fallback rather than a downgrade. A longer retry only covers flaps, never a
  two-week outage.
- **#390** (open, 0 comments, R): proposes `[auth].spawn-grant` (repo list,
  max-children, max-ttl), honoured only from global/above-repo layers (the
  `url-resolver` trust rule, `shop.go:188`), mint-command gets the parent id
  and a delegation ticket path, child gets only its own per-session per-repo
  authorization, the manager's handle (FDR 0032) is the revoke authority.
  Cross-repo grants "later via papi delegation tickets". No design comments,
  no code. It says outright it widens what a manager can push through its
  workers.
- **papi#88** (open, 0 comments, R): after one card unlock, papi issues a
  bounded delegation grant (repo list, expiry, max credentials) that a
  manager process presents to mint per-session per-repo tokens without the
  agent; tokens stay revocable by session id and under the TTL sweep. Asks
  whether papi can do it and what the format/verification looks like; no
  answer yet. Papi's lane.
- **papi#80** (open, 0 comments, R): batch mint, one card interaction mints a
  set of per-repo tokens (`--batch repo:session:ttl rows --out <dir>`), each
  named by session id so revoke/sweep address exactly one; says the spinclass
  side ("an env or file the `[auth]` lifecycle accepts instead of invoking
  mint-command") is "spinclass#TBD". Also notes pre-minting does not remove the
  creation-time ssh fetch (FDR 0028 limitation). Filed 2026-09-09 after a
  cascade lost 8 of 32 repos when the card dropped.
- FDR 0032 (R): handles pass like SCM_RIGHTS, rights are enforced
  (`observe,close` default), card-signed escalation grants for widening;
  budgets (spawn count) are ambient rights (D19, line 877) but no mechanism
  exists. FDR 0034: card-signed profile grants, expiry default one working day,
  cap 168 h (`sweatfile.go:175-184`). Those verify offline and are the existing
  precedent for #390: a card-signed, time-bounded record verified without the
  agent.

## 4. Options

### (a) Pre-mint a token pool before the trip (papi#80 + spinclass intake)

Operator mints N per-repo tokens in one card interaction; the coordinator's
spawn takes one from the pool instead of running mint-command.

- Spinclass change: new `[auth]` source, e.g. `[auth].pool-dir` (trusted layers
  only, like `url-resolver`): `auth.Mint` takes a token for the repo from the
  pool and writes it through the existing `writeCredential`/`Inject`/state
  record (`auth.go:439-474`). Pool files live in frontend state, mode 0600,
  outside any unit tree (the same place as `StateCredentialPath`, hidden by
  ProtectHome per `confine.go:256-260`). Revoke-command for pool tokens must be
  pinned too; it needs the card, so it will fail until the operator is back
  and must be left to the papi sweep.
- Papi change: batch mint (papi#80). Open design point for papi: its tokens are
  named by session id, but spawn branch names are not known in advance. Either
  spinclass pre-reserves session ids (branch names) for the pool, or papi
  supports pool tokens bound to repo with a late bind of the session id (which
  must not need the card). Describe-only here; papi decides.
- Risk: N bearer tokens at rest for the trip, per-repo write scope, bounded by
  TTL. They are readable by anything running as the operator uid, which
  includes unconfined agents (FDR 0035's banner: today an agent is its
  operator's uid). Size the pool to expected workers, TTL to the trip, repos
  to the list the coordinator actually works. The pool bypasses the
  spawn-count and repo checks nobody has written yet: any spawner with access
  to the pool dir can use a token for any repo in it, so the dir is
  coordinator-frontend-only.
- Does not fix the creation-time fetch (step 2). Pair with `allow-stale-base`
  for the repos involved (see c), or with an https-fetch fallback.
- Owner: papi (batch mint, papi#80, exists, unscheduled) + spinclass (intake,
  none yet). Operator must run it before leaving.

### (b) Card-signed spawn-grant for the coordinator (#390 + papi#88)

One card act before the trip signs a grant (repos, max-children, max-ttl,
expiry, grantee principal), verified offline with the machinery of
`internal/0/profilegrant` + `internal/bravo/grantverify` (sshsig, allowed
signers). The mint-command in a spawn context presents the grant to papi, which
mints per-session tokens with no agent.

- Strongest model: authority is bounded and revocable, no standing bearer
  tokens, per-session ids preserved, handle-based revoke (#390).
- Cost: papi must accept a grant (papi#88 is an unanswered question), spinclass
  needs the key, grant store/bind (the grantstore pattern, FDR 0034) and the
  count/TTL accounting. Two repos, design first. Not shippable before a trip
  that starts soon (I).
- Risk: widens what a coordinator (an LLM reading worker-controlled text) can
  get pushed (#390 says so); the grant must be repo-pinned, short, and
  count-bounded. Same as the profile-grant caps: 168 h max means two grants
  for a two-week trip unless the cap is raised for this record type.

### (c) Make spawn tolerate no agent

Two separate things.

1. **Creation fetch without ssh.** Cannot use the token's https rewrite: it
   is worktree-scoped (`auth.Inject`, `auth.go:515`) and the fetch runs before
   the worktree exists (`shop.go:115`, `basebranch.go:180`, root checkout). Two
   ways: (i) set `allow-stale-base` for those repos (existing, operator-only,
   `spawn.go:213`); the base is then the last-fetched tip and the merge
   rebases onto the freshly fetched default through the token anyway
   (`merge.go:773-790`, FDR 0024 landing target) so a stale base costs rebase
   conflicts, not correctness (I); (ii) reorder so the fetch happens after the
   credential is wired, from the new worktree, then retarget the base: a real
   change to a funnel two FDRs rely on (0024, 0028), 2 batches (I). (i) needs
   no code.
2. **Socket boundary stable across respawn.** Today `confine.go:275` ties the
   boundary to the process env at spawn time. Make it independent of the env:
   the smallest form is *sticky*: if a unit spec already exists for the
   worktree (`confinement.UnitSpecPath`), carry its socket entries over instead
   of re-reading `SSH_AUTH_SOCK`, the same sticky-confined principle as #400
   (`resume_confined.go:21-27`). The alternative is a trusted-layer setting
   (`[confinement].agent-socket = "inherit" | "declared" | "none"` with a path
   for declared) so a fleet's boundary is the operator's choice, not the
   spawner's environment. Never-declare is the safe default for unconfined-
   style leakage but changes workers that today get the agent; always-declare
   with a dead path may fail unit start (I). Sticky is the smallest, with the
   setting as the follow-up. Owner: spinclass.

For unconfined workers the socket concern does not exist; the only agent need
is steps 2-3.

### (d) Longer #411 retry

`agentretry.Delays` from {2 s, 8 s} to roughly 30 s total as #411 asked, or an
env/setting override. One line, spinclass, covers flaps. Does not help a
multi-day outage, so it does not appear in the trip plan except as hygiene.

## 5. Smallest safe change, ordered

Estimates are in worker-batches (one spawned worker through merge), all I.

| # | Change | Issue | Owner | Batches |
|---|---|---|---|---|
| 1 | papi confirms its TTL sweep does not revoke live sessions' tokens (papi#91), or the operator sets the mint TTL to at least the trip length | papi#91 | papi, operator | 0 |
| 2 | Batch pre-mint, one card interaction, repo:session:ttl rows, `--out` | papi#80 (exists) | papi | papi's |
| 3 | `[auth]` pool intake: take a pre-minted token instead of running mint-command; trusted layers only; revoke pinned | none yet (papi#80 calls it spinclass#TBD) | spinclass | 1-2 |
| 4 | Sticky socket list on spec rewrite so a respawn keeps the first boundary | none yet | spinclass | 1 |
| 5 | Set `allow-stale-base` in the sweatfiles of the repos the coordinator may spawn into (config, not code) | none (operator action) | operator | 0 |
| 6 | #411 hygiene: ~30 s retry window; reword the allow-no-credential hint as a downgrade | #411 | spinclass | 1 |
| 7 | Spawn-grant (card-signed, bounded, offline-verified) replacing the pool | #390 + papi#88 | spinclass + papi | 3+ (design first) |
| 8 | Creation fetch after the credential is wired (no ssh at spawn) | none yet | spinclass | 2 |

Dependencies: 3 needs 2 (or a hand-written pool); 4 is independent; 5 is stopgap
for 8; 7 supersedes 2-3 later.

The minimum that makes a two-week unattended spawn possible is 1+2+3+5, with 4
if the coordinator ever respawns confined workers. If steps 2-3 will not land
before the operator leaves, the honest plan is: spawn the full set of workers
before leaving, with TTL at the trip length, and have the coordinator only
resume/restart/merge.

## 6. Decisions only the operator can make

1. Accept pre-minted bearer tokens at rest for the trip duration (per-repo
   write, readable by the operator uid), or insist on a card-signed grant (b)
   and a smaller trip plan.
2. Token TTL for the trip (the mint passes 12 h, but that is papi's sweep TTL, not enforced on live tokens today: V, papi#91): how long, and which repos
   in the pool. Longer TTL means a longer window for a leaked token; revoke
   needs the card.
3. `allow-stale-base` for the repos in play (loses the guarantee that new
   sessions start from the fetched default; the merge still rebases).
4. Whether a coordinator may spawn at all while the operator is away, how
   many workers (a spawn-count budget does not exist, FDR 0032 D19), and into
   which repos; this is the #390 widening decision.
5. Profile-grant expiry: 168 h cap means grants lapse mid-trip; sign a
   second one, or raise the cap for this use.
6. Confirm the agent is really a stable local socket (not a per-connection
   forwarded path) so a recorded spec's socket path stays valid for the trip;
   otherwise even resume may need a fresh spec (clown question).
