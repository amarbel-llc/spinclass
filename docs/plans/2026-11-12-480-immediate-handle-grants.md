# Handle grants take effect at once, and the recipient is told (spinclass#480): design note

Status: DESIGN ONLY, 2026-11-12. Nothing built. It changes FDR 0032 D12
(accept-on-first-use), so it waits on the operator's rulings on §6.
Evidence labels: **V** read in code or a record named here, **I** inference,
**U** unknown.

## 1. The problem (from the issue)

On 2026-11-12 the operator granted a coordinator (circus/eager-holly, an
unattended dontAsk session) 16 handles with all rights, using `sc
handle-grant` at a terminal (#478). The grants sat in `pending_handles`. The
coordinator's `list-handles accept` was denied by the PreToolUse hook: "the
FDR 0032 handle-pass contract requires a human decision on every accept —
denied because this session cannot answer prompts". So the operator's
decision could not take effect without a second human step, in a session
that cannot take one.

## 2. What D12's acceptance step protects, and what it actually does today

FDR 0032 D12: "Accept is first use. The handle sits in the recipient's
`pending` set, conferring no authority and no exit wakes, until the
recipient first exercises it. `recvmsg` semantics: rights in transit are
inert, unclaimed ones drop with the message. #321's 'gains no authority it
didn't ask for' holds."

The protections it was meant to give:

- **P1. A misaddressed grant is inert.** A grant to a wrong principal key (a
  typo, a stale key from a restarted session, a key copied from the wrong
  `chat_list` row) confers nothing unless that principal acts on it.
- **P2. Nobody gains authority, or responsibility, they did not ask for.**
  A session is not silently made a holder (with exit wakes, and as the
  holder whose release can orphan a child) without doing something itself.
- **P3. No noise.** Pending holders get no exit wakes.

What the code does today (**V**, this worktree):

- Pending holders count for nothing that needs a holder. `IsHolder` reads
  only `Holders`, and `IsHolder`/`HoldersWith` gate close-child,
  session-health, close-self, the handle tools, the hook's #372 moxy read
  allow for a held child's worktree (`hooks/moxy_read.go`), the
  folio-evaluator roots (`folioeval/roots.go`) and `session/scoping.go`. So a
  coordinator with 16 pending handles also cannot read those children's
  worktrees or see their health.
- **Acceptance is inconsistent.** The explicit path, `list-handles` with
  `accept`, always asks a human (`perms/check.go`, `listHandlesTool`). The
  implicit path, first use, does not ask: `close-child-session` and
  `restart-child-session` call `AcceptHandle` before acting
  (`close_child_cmd.go`, `restart_cmd.go`). So an unattended session can
  accept today, but only by reaping or restarting a child. It cannot accept
  in order to merely observe.
- **The human decision already happens at grant time.** `grant-session-handle`
  is always-ask (`perms/check.go`: "granting a handle confers authority on
  another session; ... requires a human decision on every grant"), and `sc
  handle-grant` is operator-at-a-terminal only (#478). A session-to-session
  grant therefore already had a human decide; accept was a second decision,
  on the receiving side.

## 3. Proposal

1. **A grant is effective when it is recorded.** `GrantHandle` writes the
   principal to `Holders` directly, sets `HandleRights` as today, and records
   `HandleProvenance{GrantedBy, GrantedAt, AcceptedAt: GrantedAt}` (the field
   exists, **V**). `PendingHandles` stays readable for records written
   before the change, and is migrated by the next state write (move to
   `Holders`, stamp `AcceptedAt` = migration time with `granted_by`
   unchanged).
2. **Validate the recipient at grant time** (replaces P1). The grant is
   refused unless the recipient principal is live in the clown presence index
   (`internal/0/clown/presence.go`, **V**), with an error naming the
   principals that ARE live and close to it. A grant to an unknown or dead
   key is refused, not recorded. This turns D12's "inert until used" into
   "never recorded", which is stronger for the misaddressing case.
3. **Narrowing is unchanged.** The granter needs `grant` and may only name a
   subset of its own rights (D11, D13); the operator at a terminal may name
   any. Release is unchanged (issue item 4).
4. **Tell the recipient** (replaces P2 and P3). On every grant, spinclass
   sends one wake to the recipient through the existing
   `clown.NotifyPrincipalUnderClown` (`internal/0/clown/clown.go`, **V**: the
   path restart notices to holders already use, through the
   `notifyPrincipalFn` seam in `restart_cmd.go`), from the granter, with a
   one-line summary and the detail. A wake, not a chat message: spinclass
   already owns that path, the recipient is woken even when idle, and FDR
   0017 leaves chat to clown and troupe. The wake is best-effort (a missed
   wake does not undo the grant). The journal (§3.5) and `list-handles` are
   the durable record.

   ```
   handle granted: you now hold <child> with <rights> (granted by <who>)

   granted_by: <principal> | operator (tty)
   granted_at: <RFC3339>
   child: <repo>/<branch> (<principal>)
   rights: observe,close,...
   You receive this session's exit wakes from now on. To refuse, release it:
   release-session-handle child=<repo>/<branch>
   ```

   A batch grant (#342, several children in one call) sends one wake
   listing all children. The **other holders** of the child are told too:
   one wake each, "<recipient> now holds <child> with <rights>, granted by
   <who>". That is D12's intent that holders can see the handle set, made
   active.
5. **Audit line** (issue item 5): every grant, effective-at-once or
   migrated, appends one ndjson line to
   `$XDG_STATE_HOME/spinclass/handles/journal.ndjson` with `ts`, `act`
   (`grant`, `release`, `migrate`), `child`, `to`, `rights`, `by` (principal
   or `tty`) and `assurance` (`tty`, `session`, later `touch` per D10/D11).
   It is shaped like a D8 record so D14 can later index or sign it. A journal
   that cannot be written refuses the grant (fail-closed, as the spawn
   allow-list does). Release stays fail-open: refusing a release would keep
   authority alive because a disk was full.
6. **The hook.** `list-handles accept` becomes a no-op that reports "nothing
   pending" (and stops being always-ask once nothing can be pending).
   `grant-session-handle` stays always-ask.

## 4. Is notification plus release a sufficient replacement?

For P1, yes, and better (§3.2): a misaddressed grant is refused, not parked.

For P2: the recipient is told, and can release. What is lost is "inert until
the recipient chooses". **I**: the cases it mattered for are a session
handed authority it then misuses because it was injected, and an
unintended holder whose release orphans a child. Neither is stopped by D12
as built. Implicit accept-on-first-use means the first misuse is also the
accept, and acceptance is not where the injection is decided. The real
bound stays D11 narrowing at the granter, plus the always-ask grant.

For P3: a recipient now gets exit wakes at once, which is what a coordinator
granted handles wants.

## 5. Should an unaccepted grant lapse?

With §3 there is no unaccepted state, so the question becomes: should a
grant **lapse** when nobody exercises it? I recommend no time-based lapse in
v1. Grants already end when the recipient releases, when the child is
closed (the record is tombstoned), or when the recipient's principal is gone
(D12's orphan rule, `sc list`/`sc clean`). A time-based lapse would quietly
drop exit wakes from a coordinator that held a quiet child for a long time.
If the operator wants one, the shape I'd suggest is a per-grant optional
`expires` set by the granter (the D5 `cap` right's job, which is
"vocabulary only" today per D13), not a global timer.

## 6. Open questions for the operator

1. Effective at once for **every** grant (operator tty and session grants),
   or only the operator's tty grants, keeping D12 for session-to-session?
   Recommend every grant: the session grant is always-ask already, so a human
   decided either way.
2. Refuse a grant to a principal not live in the presence index (§3.2), or
   record it with a warning? Recommend refuse. It is the replacement for P1.
   Cost: you cannot grant to a session that is mid-restart; retry after it
   is back.
3. Notify the child's **other holders** too (§3.4), or only the recipient?
   Recommend both.
4. Time-based lapse (§5): none in v1? Or a granter-set `expires`?
5. Journal location and fail-closed grant / fail-open release (§3.5): OK?
6. FDR 0032 D12 text: amend in place ("Accept is first use" becomes "Grant is
   effective on record; the recipient is notified"), with the date and this
   issue? D12 is the record of the decision, so the amendment belongs there,
   not only here.

## 7. FDR 0032 D13 and D19

- **D13.** No right changes meaning; only when a granted set starts to
  count. Narrowing at the granter is the bound, as today.
- **D19.** Not engaged: nothing here schedules or throttles. The
  notification is a lifecycle event (D6's family), emitted by spinclass as
  the lifecycle owner of the handle table.

## 8. Out of scope

- Signing grants (D8 records) and escalation (D11 9C). The journal is
  shaped for it.
- #481's job-read right (separate note).
- Changing who may grant (`grant` right, operator tty).
