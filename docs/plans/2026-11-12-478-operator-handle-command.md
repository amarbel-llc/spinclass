# Operator command to add or move session handles (spinclass#478, plan, 2026-11-12)

Status: design note. Step 1 of the queue the coordinator set: this note, with
every point that needs an operator ruling as a numbered question and a
recommendation. Step 2 (below, section 5) builds only what needs no ruling.

Evidence tags: (R) read in the repo, (I) my inference or recommendation, (U)
unverified.

## 1. What exists (R)

- Handles are state on the session record (`internal/alfa/session/session.go`):
  `Holders` (accepted), `PendingHandles`, `HandleRights` (comma-joined names per
  principal, only for principals in either list), `HandleProvenance`
  (`GrantedBy`, `GrantedAt`, `AcceptedAt`), plus `SpawnedByPrincipal` (lineage)
  and `SpawnerReleased` (ends the legacy spawner-key authority path, FDR 0032
  D9). A principal is a bare key: the chat JID localpart.
- The mutators are `GrantHandleBy(to, rights, by, at)`, `AcceptHandle`,
  `ReleaseHandle` (removes from both lists and drops rights and provenance),
  `SeedSpawnerHandle`. A grant of a principal already holding or pending is a
  no-op (`GrantHandleBy` returns false): re-granting is deliberately not an
  escalation path.
- The MCP tool `grant-session-handle` (`cmd/spinclass/handles_cmd.go`) needs the
  caller to hold `grant` on the child, may only narrow (granted rights are a
  subset of the granter's), names outside the vocabulary are refused, the
  result is pending, and the caller is told to relay the child's key by chat
  because spinclass never messages for itself.
- Rights vocabulary (D13): `observe` (implied by any handle), `close`, `grant`,
  `instruct` and `cap` (vocabulary only), `merge`, `check`, `restart`. Default
  grant `observe,close`. Pending confers no authority and no exit wakes until
  first use; `list-handles accept:true` accepts every pending one at once.
- `sc restart` (`cmd/spinclass/restart_operator_cmd.go`, #473) is the pattern: a
  CLI-only verb (`RunCLI`, no MCP tool), refuses unless stdin and stdout are
  both terminals (seams `operatorRestartIsTerminal` and friends), judges each
  target on its own before writing, prints one line per session, one failure
  does not stop the rest, nonzero exit if any failed, one prompt for the whole
  batch before a widening (default no). The operator has no principal; the
  request records the shared marker `session.OperatorRestartRequester`.
- D11: rights only shrink without the card; "granting a right the granter does
  not hold" is one of the three v1 escalations, answered by a card-signed (slot
  9C) escalation grant. D14: records are meant to move to the D8 grammar in a
  system-scoped madder store, with `holders` a materialized view. Slice 0 (now)
  has no cryptographic binding and says so (D9).
- Candidates for "the operator is the authority": `sc spawn --unconfined`,
  `sc grit`, `sc restart` all state that under the same uid a terminal guard is
  a speed bump, not a boundary.

## 2. Command shape (I)

`sc handle grant SESSION... --to PRINCIPAL [--rights R]` and
`sc handle move SESSION... --to PRINCIPAL [--rights R]`, plus `--all-on-host`
instead of named sessions, and `--dry-run`. CLI-only. Names are provisional per
the issue; `sc handle` as a noun with `grant|move` matches `perms list|review|edit`.

- **Guard.** Identical to `sc restart`: refuse unless stdin and stdout are
  terminals, with the same wording about the operator's act and an agent asking
  with `grant-session-handle`. Reuse the seam pattern, not the vars.
- **Selection.** Positional sessions (any form `sc list` prints, resolved by
  `session.FindByTarget`) or `--all-on-host`; the two are mutually exclusive.
  `--all-on-host` covers live worktree sessions only: implicit (main-checkout)
  and closed sessions are skipped and reported, as `guardHandleTarget` does.
  The coordinator's own session is included in `--all-on-host` unless it is the
  recipient (a no-op line).
- **Judge first, write second.** Resolve every target and compute its
  before/after holders and rights without writing. `--dry-run` prints that and
  exits 0. Without it, one prompt for the whole batch (default no) lists every
  change; only `y`/`yes` proceeds. Then apply per session under
  `updateLoadedSession` (the locked read-modify-write), one TAP-style line per
  session, failures counted, nonzero exit if any failed.
- **Output.** One line per session, like `sc restart`:
  `<session>: <old holders> -> <new holders>: granted (pending)` or
  `...: refused: <reason>`. A TAP document is the house style for handle tools
  (`runForEachChild`), so the output should be TAP 14 with a subtest per
  session; the exact shape is a style question for step 2 (see Q9).

## 3. Semantics (I)

- **add (`grant`).** Keeps every existing holder; adds `--to` with the named
  rights (default `observe,close`, exactly the MCP default). `grant`, `merge`,
  `check`, `restart`, `instruct`, `cap` must be named explicitly, as in the MCP
  tool. A recipient that already holds or is pending is a reported no-op, not
  an upgrade, as today.
- **move.** Adds `--to` as `grant` does, then removes every previous holder and
  pending holder (their rights and provenance too, via `ReleaseHandle`),
  except the recipient. Whether it also ends the legacy spawner path
  (`SpawnerReleased = true`) is Q4.
- **Pending vs accepted.** Default pending, per D12: rights in transit are
  inert and the recipient exercises them on first use. The coordinator can
  accept every handle it was given in one call (`list-handles accept:true`).
  A flag for "accepted at once" is Q3.
- **Provenance.** `GrantedBy = "operator"` (a marker, not a principal),
  `GrantedAt = now`. Provenance is already per principal, so a second grant by
  the operator over a previous one has nothing to overwrite.
- **Narrowing only.** The operator is not holding rights on the session, so
  "subset of the granter's rights" has no meaning here. The command must not
  become a way to give a principal a right no spawner could ever have, so the
  vocabulary check stays (unknown names refused) and rights outside the
  default need to be named (Q2).

## 4. Cross-host readiness (I, U for the signing parts)

- A handle names the principal as `<localpart>` for a principal on this host
  (today's storage key, so nothing existing changes) and as
  `<localpart>@<host>` for any other host. `--to` accepts both from day one;
  a remote form is stored but not yet verified or used for authority until the
  D8 grammar lands, and the command says so in its output. The host part is the
  chat vhost (the JID domain), not the machine hostname (U: spinclass has no
  read of the vhost today; the local host's own vhost needs a source, Q7).
- The record the command writes must carry what a remote verifier would need
  without a format change: principal (qualified), session (`<repo>/<branch>`
  plus the granting host), rights, `GrantedBy`, `GrantedAt`, and a reserved
  empty `sig`/`record` slot (D8 shape: kind, subject, issuer, prev, body, sig).
  In slice 0 these live in `HandleProvenance` as extra optional JSON fields
  (`host`, `record`), omitted when empty, so older readers ignore them. No
  signing happens in this work.
- The card question (Q5): see below. My recommendation keeps slice 0's honest
  position (unsigned, attributable by the `operator` marker and the terminal
  guard) and reserves the slot.

## 5. What step 2 builds with no ruling (I)

Only this, once the note is merged: the tty-guarded command core
(`sc handle grant|move`), session selection (named sessions and
`--all-on-host`), `--dry-run`, the batch prompt, per-session judgement and
application with `updateLoadedSession`, default pending, the existing rights
vocabulary and default, `GrantedBy = "operator"`, one output line per session
with nonzero exit on failure, and tests with the injected terminal seams. It
does NOT build: accepted-at-once grants, ending the spawner path on move,
remote principal verification, the signed record, the card act, or any
permission-semantics change (no handle gains anything an existing grant could
not give).

## 6. Questions needing an operator ruling

1. **Command name.** `sc handle grant|move` (recommended), or `sc grant-handle`
   / `sc handles add|move`? Recommend `sc handle grant|move`.
2. **Dangerous rights.** Should `grant`, `merge`, `check`, `restart` need
   `--rights` named AND an extra confirmation line in the batch prompt ("these
   sessions would give X the right to land code / restart / pass handles on")?
   Recommend yes: name them explicitly (as the MCP tool does), and list them in
   the prompt, since `merge` and `check` run the pre-merge hook, which is
   arbitrary code execution.
3. **Pending or accepted.** Default pending (D12). Should there be an
   `--accepted` flag because the operator is present? Recommend no flag in v1:
   the coordinator accepts all in one `list-handles accept:true`, and accepted
   handles on a principal that never runs would suppress orphan detection
   (a holder that is not alive does not count, but the record would claim
   consent nobody gave).
4. **Does `move` end the spawner's authority?** The spawner keeps a legacy key
   path (`SpawnedBy` equals the caller's session key) until `SpawnerReleased`
   is set, even after its handle is removed. For `move` to mean "the
   coordinator now owns these", it must set `SpawnerReleased = true`.
   Recommend yes for `move`, and a note in the dry-run output when it applies.
5. **Card-signed?** Should the operator's act be a card-signed piggy act (RFC
   0013, slot 9C)? D11 treats "granting a right the granter does not hold" as a
   card escalation, but slice 0 has no signer and `sc restart`,
   `sc spawn --unconfined` and `sc grit` rely on the terminal guard. Recommend:
   not in this work. Record `GrantedBy = "operator"` and reserve the `record`
   slot, so a later slice can sign the same fact without a format change; make
   card-signing mandatory only for the rights that D11 lists as escalations
   once the signer exists.
6. **Recipient checking.** Check `--to` against a live session by the frontend
   principal mapping (the same one `ownerPrincipal` uses). On a miss, refuse,
   or warn and proceed? Recommend refuse unless `--force-unknown` is given,
   because a typo in a principal silently hands authority to nobody and
   leaves the sessions without the holder the operator wanted. (U: I have not
   confirmed what the mapping lists for the coordinator session; step 2 must.)
7. **Local host identity.** Where does the local vhost come from, for
   deciding that `localpart@host` is "this host" and normalizing it to the bare
   key? Options: a trusted sweatfile key, the clown/troupe environment, or the
   chat presence listing. Recommend the trusted-layer sweatfile key (read live,
   never pinned) with the vhost from the environment as a fallback; until then
   a qualified `--to` is stored as given and flagged unverified.
8. **`--all-on-host` scope.** All live worktree sessions, including the
   operator's other coordinators and confined workers? Recommend yes for all
   live worktree sessions, with confined sessions reported on their own line
   (a handle is only a right on the child's record, but the operator should see
   that the confined session is included), and the recipient's own session
   excluded.
9. **Output format.** `sc restart`'s plain per-session line, or TAP 14 with a
   subtest per session like `sc close`? Recommend TAP 14 (house style for
   handle tools), keeping the line text from `sc restart` as the subtest name.
10. **Revoking.** Is there a later `sc handle release` / `revoke` for the
    operator (remove a principal's handle everywhere)? `move` covers the
    common case; I recommend deferring revoke until the operator asks for it.

## 7. Risks and what this does not change

- It adds no authority beyond what the spawner already has: a handle with the
  default `observe,close` is exactly what `grant-session-handle` can give.
  Anything beyond that is named, listed in the prompt, and an operator act.
- An unconfined agent under the same uid can edit state files; the guard is a
  speed bump, as for the other operator verbs. Records written by this command
  are no more trustworthy than any other slice-0 handle record, and the
  `operator` marker is not a proof of origin (D9).
- Moving every handle onto one coordinator makes it the single holder: if it
  dies, the sessions are orphaned (D12: no accepted holder alive). `sc list`
  and `sc clean` already report that; an operator who uses `move` for the
  coordinator pattern accepts it.
