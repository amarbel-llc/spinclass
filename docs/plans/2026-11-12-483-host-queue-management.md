# Host queue management and priorities (spinclass#483): design note

Status: DESIGN ONLY, nothing built. It touches permissions, so it waits for
the coordinator's and the operator's rulings on the numbered questions.
Builds on #482 (gates are admitted in the order they asked, by a numbered
place).

## What exists

- Ledger `$XDG_RUNTIME_DIR/spinclass/host-queue/`: `gate-*.json` (admitted),
  `wait-*.json` (waiting, with `Seq`), `seq`.
- The operator's hold: `$XDG_STATE_HOME/spinclass/host-queue/hold`.
- Handle rights (FDR 0032): observe, close, grant, instruct, cap, merge,
  check, restart. A session holds rights over the sessions it holds handles on.

## Proposal

### Verbs (`sc queue ...`, CLI first; MCP tools only for the read verbs)

- `list` / `status`: gates running, waiters in order with place, priority,
  reason and age, the hold, and the QUEUE BYPASS list. Read-only (same data as
  `host-load`'s queue section; `host-load` stays).
- `hold [reason]` / `release`: the whole queue (today's hold file).
- `hold-waiter <session>` / `release-waiter <session>`: a waiter stays in line
  but is skipped while held (its place is kept; it does not block those behind).
- `cancel <session>`: ends a waiter's wait; that gate fails with
  `host queue: cancelled by <who>`, attestation stays buffered.
- `priority <session> high|normal|low`: sets a waiter's priority.
- Every mutating verb appends a line to an audit journal
  `$XDG_STATE_HOME/spinclass/host-queue.log` (time, verb, target, who: operator
  or the caller's principal and session, old and new value). A journal that
  cannot be written refuses the verb (as the spawn allow-list does).

### Who may do what

- **Operator at a terminal** (stdin and stdout are terminals, the existing
  `handle-grant` guard): every verb, no handle needed.
- **A session** acting on another session's waiter: needs a handle on that
  session carrying the existing `merge` or `check` right (matching the gate's
  kind) for `hold-waiter`, `release-waiter`, `cancel`; for `priority` see Q2.
  Rationale: whoever may start a gate for a session may stop it waiting.
- **A session acting on its own waiter**: `cancel` and `hold-waiter` of its
  own are allowed (it can already abandon its gate); no priority change.
- **Whole-queue `hold`/`release`**: operator only (it delays everyone).
- A new right `queue` would be needed only if the operator wants delegation
  without `merge`/`check` (Q1).

### Priority

- Three levels, ordering by priority then place (FIFO within a level):
  high before normal before low. Default normal.
- Priority never bypasses a term other than the queue position: the hold, the
  gate cap, memory, pressure, disk, load and settle all still apply, and a high
  waiter does not run beside a running gate beyond `max-gates`.
- Bound against a runaway high loop: raising to high is operator-only by
  default (Q2); a session may only LOWER its own or a held child's priority.
  Even then a normal waiter that has waited longer than `max-wait / 2` is
  treated as high ("aging") so a stream of high gates cannot starve it. A gate
  is high for at most one admission; a `high` is per waiter, not per session.
- Where it lives: the waiter marker gains `Priority`; `Decide`'s queue term
  compares (priority, Seq) instead of Seq; `position` counts waiters ahead on
  that order.

### Held waiter and cancel mechanics

The ledger is flock-based with no daemon, so a verb writes a small command file
`cmd-<verb>-<seq>.json` under the ledger lock; the waiter's own poll applies it
at its next look (at most `poll`, 10 s). `cancel` makes the waiter return
an error, `hold-waiter` makes `Decide` skip it. Nothing kills a process.

## Questions

1. Delegation: reuse `merge`/`check` rights for waiter verbs (recommended; no
   new right), or add a `queue` right (more precise, more surface)?
2. Who may raise priority: operator only (recommended), or any holder of a
   `queue`/`merge` handle up to normal and the operator for high?
3. Is the aging rule (normal waits past `max-wait / 2` become high) wanted, or
   is operator-only high enough?
4. Should `cancel` of another session's waiter be always-ask in the permission
   hook (recommended), like `close-child-session --force`?
5. Audit journal location and the fail-closed rule: as above, OK?
6. MCP surface: only `queue-status` as a tool (read), mutating verbs CLI-only
   and operator-tty (recommended, like `handle-grant`), with session-held
   delegation via the `merge`/`check` handle deferred to a later step?
