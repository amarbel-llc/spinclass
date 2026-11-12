# Spawn allow-list: a TEMPORARY stopgap for unattended spawners (plan, 2026-11-12)

Status: design note (step 1). Operator ruling 2026-11-12 (relayed by the
coordinator): build a trusted-sweatfile allow-list of principals that may make
UNCONFINED, dontAsk-worker spawns through `spawn-session` without a prompt even
when the calling session is itself unattended. It is a stopgap for the "blessing"
of spinclass#384 and must be easy to delete once ajour plus profiles enforce
this. Evidence tags: (R) read in the repo, (I) my inference or recommendation,
(U) unverified.

## 1. What exists (R)

- `perms.AlwaysAsk` (`internal/charlie/perms/check.go`) is the one always-ask
  floor, shared by the PreToolUse hook and `RunCheck`. For `spawn-session` it asks
  unless: (1) `unconfined` is requested -> always asks; (2) `SpawnConfined`
  (confined spawn, #382) -> passes; (3) `SpawnUnattended` -> passes (added for
  #476). Anything that does not parse cleanly, an absent seam or a "no" asks.
- `SpawnUnattended` is installed as `spawnWillBeUnattended`
  (`cmd/spinclass/spawn_confined_perms.go`): it returns false when
  `spawn.DriverUnattended()` (any non-empty `CLOWN_UNATTENDED` in the hook's
  environment, which clown also puts in claude's own env), then parses `repo` and
  `confined` strictly and asks `spawn.WillBeUnattended` (the trusted
  `[session-entry].unattended-permission-mode = "dontAsk"` key plus a spawn-entry
  that takes the dontAsk splice). That driver test is the "bound 4" the operator
  names: a dontAsk worker's own spawns still ask, so fan-out stays bounded.
- The hook allows the passing case positively (`hooks.go`, `spawnSessionToolName`
  case); `RunCheck` refuses to auto-approve an always-ask call from a perms tier.
- Trusted-layer plumbing: `sweatfileio.Trusted*` accessors read each source file
  of the hierarchy and honour only layers above the repo (global, parents),
  never the repo or worktree sweatfile; the schema in
  `internal/0/sweatfile/sweatfile.go` is tommy-generated (`verify-tommy-codegen`
  fails if the committed codec differs). `Permissions` (`allow-self-close`,
  `deny-sweatfile-edits`) is the existing trusted-only table, "a stopgap for the
  hook ... which replaces this".
- A principal is the per-instance key (the chat JID localpart); the hook sees the
  caller's through `currentPrincipal()` (`CLOWN_SESSION_ID`), and
  `confinement.SessionKeyOfPrincipal` resolves it to a live session through the
  frontend principal mapping the agent cannot write. A session's workers carry
  `SpawnedByPrincipal` in their records.
- Existing journals (R): `$XDG_STATE_HOME/spinclass/spawn-unconfined.log` (0600 in
  0700 dir, JSONL, written by the `sc spawn --unconfined` CLI gate) and the
  per-worker `<worktree>/.spinclass/spawn.log` (the worker's stdout/stderr,
  worker-writable, deleted with the worktree). `sc validate` has
  `CheckPermissions(sf, trusted)` that warns on a repo-layer trusted-only key.

## 2. The rule (I)

Bound 4 is relaxed for exactly one case. The spawn passes the floor without a
prompt when ALL of these hold; any doubt asks:

1. `unconfined` is not requested (explicit `unconfined=true` stays always-ask),
   and `repo` and `confined` parse cleanly (unchanged).
2. The resolved worker is unconfined and will start in dontAsk (unchanged:
   `spawn.WillBeUnattended`). Attended, auto or unset-mode workers still ask.
3. The calling session is attended (unchanged, already passes), OR it is
   unattended and its principal is LISTED:
   a. the hook's principal resolves to a live session through the frontend
      mapping (fail closed otherwise);
   b. a trusted-layer entry names that principal;
   c. the entry has not expired (RFC 3339 UTC, unparseable = invalid = ignored);
   d. the principal's live workers are under the entry's cap (default 12).
4. The use is journaled; if the journal cannot be written, the spawn asks.

Nothing else relaxes: widening, confinement opt-outs, auto/attended workers and
the force/close/grant paths are untouched, and a listed principal's own dontAsk
workers are not listed, so THEIR spawns still ask (no recursion).

## 3. Configuration (I)

Trusted layers only (global or a parent above the repo); a repo or worktree
entry is ignored and `sc validate` warns. Proposed shape, an array of tables
under `[permissions]` (the table that already holds the other operator-only
switches):

    # TEMPORARY: replaced by ajour + profiles. See docs/plans/2026-11-12-spawn-allowlist-stopgap.md.
    [[permissions.spawn-allow]]
    principal   = "<chat JID localpart of the session>"
    max-workers = 12                        # optional, default 12
    expires     = "2026-11-19T00:00:00Z"    # optional RFC 3339 UTC
    note        = "coordinator, nightly"    # optional, journaled

The circus-side change (a separate circus change the coordinator routes) is one
such table in the global sweatfile naming the coordinator's principal. I add no
principal to any file. Because a principal is per-instance, a coordinator that
restarts keeps its principal (restart keeps identity), but a newly started
coordinator session has a new one and needs a new entry or the session-key form
of Q2.

## 4. Mechanics (I)

- **Evaluation.** New trusted accessor `sweatfileio.TrustedSpawnAllow` (all
  trusted sources, entries appended, invalid ones dropped with a reason for
  validate), a pure `spawn.DriverMaySpawn(home, cwd, principal, now, liveCount)`
  used by `spawnWillBeUnattended` in place of the bare `DriverUnattended` return
  false. The driver's trusted layers are read from the repo containing the hook's
  cwd (or just the global layers outside a repo), as `WillBeUnattended` does.
- **Cap.** Count of the principal's live workers = session records with
  `SpawnedByPrincipal == principal` that are neither tombstoned nor abandoned,
  across all repos (dangling and pre-hello ones count: they hold a worktree).
  Fail closed on a listing error. The check is a read, not a lock: two spawns
  racing past it can exceed the cap by the number of concurrent calls (U: the
  tool is serialized per session by the harness, so in practice by one).
- **Journal.** A new JSONL `$XDG_STATE_HOME/spinclass/spawn-allowlist.log`
  (0600 in a 0700 dir, like `spawn-unconfined.log`): time, principal, session
  key, repo argument, model argument, entry note, live count and cap. Written by
  the hook at the point it allows the call (one record per allowed call, not per
  evaluation: `AlwaysAsk` stays pure and `RunCheck` never journals). A write
  failure turns the allow into an ask. The per-worktree `spawn.log` is not used:
  it is the worker's output and worker-writable.
- **Validate.** `sc validate` warns on a repo-layer or worktree-layer
  `spawn-allow` (ignored), on an expired entry, on an unparseable `expires`, a
  non-positive `max-workers` and a duplicate principal.
- **Deletion plan.** Everything is TEMPORARY-marked and confined to: one schema
  type (`SpawnAllow`), one trusted accessor, one function in the spawn package,
  one branch in `spawnWillBeUnattended`, one hook journaling call, one validate
  check and this note. Removing the stopgap deletes those and restores bound 4
  exactly as in 036b9e2.

## 5. Questions needing an operator ruling

1. **Syntax.** `[[permissions.spawn-allow]]` (recommended, with the other operator
   switches) or a top-level `[[spawn-allow]]`? (U: tommy's support for an array of
   tables inside a table is unverified; if it fails, I fall back to the top-level
   form, still trusted-layer-only, and say so.)
2. **Identity.** A principal is per-instance, so a freshly started coordinator
   needs a new entry. Also accept `session = "<repo>/<branch>"` (the unforgeable
   frontend mapping resolves the caller's principal to its session key; the
   entry then follows the coordinator's worktree, not one run of it)? Recommend
   yes, an entry must give exactly one of `principal` or `session`.
3. **Host qualification.** The operator wants entries host-qualified for future
   cross-host handles. Recommend the same interim rule as the `sc handle-*`
   commands (spinclass#478 Q7): `principal` may be `<key>` or `<key>@<host>`; a
   qualified entry matches a caller only when the key resolves to a live local
   session; a `<key>@<other-host>` entry never matches a local caller (it is
   inert, and validate says so). No new vhost config.
4. **Cap meaning.** Cap on live workers per listed principal across all repos,
   counting dangling and pre-hello ones, default 12, per-entry `max-workers`;
   reaching it asks (the prompt path), it does not deny. Agreed?
5. **Journal file.** A new `spawn-allowlist.log` beside `spawn-unconfined.log`
   (not the per-worktree `spawn.log`). Agreed? Record the `model` argument as
   passed (the alias, not a resolved id).
6. **Journal failure.** Fail closed (the spawn asks)? Recommend yes.
7. **Expired entries.** Ignored (so the prompt returns) and `sc validate` warns;
   a clock set wrong does not matter beyond the entry's window. Agreed?
8. **Scope of the relaxation.** Only dontAsk, unconfined workers from a listed
   principal. Should a listed principal also be allowed to start CONFINED
   spawns? They already pass without a prompt (#382), so nothing to add.
9. **Entry limits beyond cap and expiry** (a model allow-list, a repo allow-list)?
   Recommend none now: the operator said the nikulin repo exclusion list is out
   of scope, and anything more makes this harder to delete.

## 5a. Operator answers and as built (2026-11-12)

Q1 to Q9 accepted as recommended. Q1: the nested `[[permissions.spawn-allow]]`
works with the tommy codec (verified by `TestTrustedSpawnAllow`), so no
top-level fallback. Built: `sweatfile.SpawnAllow` / `ParseSpawnAllow`,
`sweatfileio.TrustedSpawnAllow`, `spawn.DriverMaySpawn`, the seam branch in
`spawnWillBeUnattended`, `perms.SpawnAllowJournal` called by the hook at the
allow point (an error becomes the prompt), `cmd/spinclass/spawn_allowlist.go`
(the journal), `validate.checkSpawnAllow`, docs (sweatfile(5), FDR 0035,
AGENTS.md). Two small deviations to name: a driver outside any repo fails
closed (no trusted layers are read for it) instead of reading only the global
layers; and `RunCheck` does not journal (it never auto-approves this call; only
the hook allows it).

## 6. Step 2 (what I build once the note is merged and the questions answered)

The schema type and accessor, `DriverMaySpawn` and the cap count, the seam
change, the hook journaling with fail-closed, the validate checks, tests (listed
vs unlisted principal, repo-layer entry ignored, cap reached asks, expiry,
attended driver unchanged, `unconfined=true` unchanged, journal failure asks,
unlisted dontAsk worker spawn still asks), sweatfile(5), the FDR 0035 note and an
AGENTS.md line, each marked TEMPORARY with a pointer to ajour.
