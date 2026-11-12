# Host queue management and priorities (spinclass#483): design note

Status: DESIGN ONLY, nothing built. It touches permissions, so it waits for the
operator's rulings on the questions at the end. Amended 2026-11-12 after
slim-myrtle's review (S1-S3, G1-G9). Builds on #482 (gates are admitted in the
order they asked, by a numbered place; landed b524df6).

## What exists

- Ledger `$XDG_RUNTIME_DIR/spinclass/host-queue/`: `gate-*.json` (admitted),
  `wait-*.json` (waiting, with `Seq`), `seq`.
- The operator's hold: `$XDG_STATE_HOME/spinclass/host-queue/hold`.
- Handle rights (FDR 0032 D13): observe, close, grant, instruct, cap, merge,
  check, restart.

## Where this sits (S1)

FDR 0032 D19 says scheduling is not that record's: queues and priorities are
ringmaster's. This note deviates knowingly: the host queue is spinclass's
interim scheduler (FDR 0038), and its management verbs live here until the
job platform owns scheduling. Priority is a scheduling POLICY owned by the
operator, not a capability a handle carries: no handle right grants it, and the
operator's choices are what the ledger applies.

## Trust model (S3)

The command files and the ledger markers are NOT a security boundary against
code running as the same uid. Today a forged `wait-*.json` with `Seq` 0 already
jumps the queue, and a flock-held fake `gate-*.json` takes a slot. The tool is
the check; the files are not. Mitigation that does hold: the ledger directory
and `$XDG_STATE_HOME/spinclass/host-queue` are added to what a confined unit
cannot write (the confinement write-deny list), so a confined worker cannot
forge either. An unconfined session is trusted at the level it already is.

## Proposal

### Verbs (`sc queue ...`, CLI first; MCP only the read verb)

- `list` / `status`: gates running, waiters in order with place, priority,
  reason and age, the hold, and the QUEUE BYPASS list. Read-only.
- `hold [reason]` / `release`: the whole queue (today's hold file).
- `hold-waiter <session>` / `release-waiter <session>`: a held waiter keeps its
  place but is skipped while held.
- `cancel <session>`: ends a waiter's wait; that gate fails with
  `host queue: cancelled by <who>`; its attestation stays buffered.
- `priority <session> high|normal|low`.

Every verb is a declared ajour atom (verb plus target, and priority or kind
where they apply), so operator-act profiles can admit or refuse it (G9).

### Who may do what (S2, G7)

- **v1: the operator at a terminal only** (stdin and stdout are terminals, the
  `handle-grant` guard). No handle is consulted.
- **Delegation is deferred** to a later step, with session-held verbs (Q6). When
  it comes it uses a narrow object right `spinclass:queue`, not in the default
  grant and below `close`. `merge` and `check` are execution-class (D13) and are
  NOT reused: delegating queue control through them would over-grant.
- **Assurance**: the journal records the actor as `tty`, meaning as strong as
  isatty (the #478 stopgap), not an authenticated act. The eventual authority is
  the card path (FDR 0032 D10/D11, "9C").

### Journal (G8)

`$XDG_STATE_HOME/spinclass/host-queue/journal.ndjson`, one record per verb
shaped like an FDR 0032 D8 record: time, actor (principal, or `tty`), act,
target session, seq, old and new value, assurance. Failure policy:
`priority`, `cancel`, `hold-waiter` FAIL CLOSED (no journal, no effect); the
whole-queue `hold` fails OPEN with a loud warning, because it is the brake.

### Command lifecycle (G1, G2)

The ledger is flock-based with no daemon, so a verb writes a command file
`cmd-<verb>-<n>.json` under the ledger lock. It names the target's `seq`, the
session and the marker's file identity (device and inode). The waiter applies it
at its next look (at most `poll`, 10 s) only if all three match. Locked readers
garbage-collect command files with no live target (so a deleted `seq` file, which
restarts low, cannot make an old command hit a new waiter).

A `cancel` that loses the race (the waiter was admitted inside its poll): the
command is ignored, journalled `too late` and reported. The verb waits for the
waiter's acknowledgement (poll plus a margin) and says what happened.

### Hold, aging and priority (G3-G6)

- `hold-waiter` pauses that waiter's `max-wait` clock; a hold expires at
  `max-wait` so a forgotten hold cannot wedge the line; every hold and expiry is
  journalled. A held waiter does not age while held.
- Order: priority (high, normal, low), then place. Priority never bypasses a
  term other than the queue position: the hold, the gate cap, memory, pressure,
  disk, load and settle all still apply.
- Raising to `high` is operator-only. Aging uses a FIXED interval (default 30
  min, not `max-wait / 2`, since `max-wait = "0"` would make that zero): a low
  waiter ages to normal and a normal one to high after each interval waited and
  not held. A `high` lasts for one admission and is per waiter, not per session.
- `Seq` 0 (a legacy marker from an old binary) sorts as normal, ahead of
  numbered normals, and only while old binaries exist.
- Idle-grace: checked in the code and pinned by a test
  (`fifo_test.go`, the "behind" case). A waiter that is BEHIND never carries
  `ProbeFailingSince`: `Decide` clears it on every refusal for position, hold or
  gate count. So a priority raise that makes a waiter head starts its failing
  clock fresh, and idle-grace needs a full `idle-grace` interval of failing probe
  terms as head; it cannot admit at once. The same holds for aging.

## Questions

1. Is v1 operator-tty only, with delegation (`spinclass:queue`) deferred, as
   proposed?
2. Aging: fixed interval, default 30 min, low to normal to high: wanted?
3. Hold expiry at `max-wait`: OK? Does `max-wait = "0"` then mean holds never
   expire?
4. Journal path and the fail-closed / fail-open split (G8): OK?
5. Confined units: deny writes to the ledger dir and the queue state dir: OK
   (a confinement change, so your ruling)?
6. MCP surface: only `queue-status`; mutations CLI/tty only until delegation.
