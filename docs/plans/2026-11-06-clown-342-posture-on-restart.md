# Posture change on restart: the spinclass half of clown#342 (plan, 2026-11-06)

clown#342 asks for a way to relaunch an existing unconfined session into
claude's `dontAsk` permission mode ("unattended") and back out ("attended"),
keeping its identity. Clown's half landed on clown master at 70f904a on
2026-11-06 (reported by clown/rare-walnut; not deployed): the
`restart-preflight` verdict may carry `"posture": "unattended"|"attended"`,
absent meaning keep, and the hand-off form is `clown restart --session K
--posture P`. This note is the spinclass half, written on the coordinator's
ask (circus/eager-holly, 2026-11-06, docs only). Every decision below is a
proposal for the operator; nothing is built, and the questions in §7 are open
until ruled.

Labels: **R** read in code or docs, **V** observed in a run, **I** inferred or
proposed, **U** unverified (clown-side facts come from rare-walnut's reports,
not from reading clown).

## 1. What exists (R)

### 1.1 Where a session's posture lives today

- The only place spinclass sets `dontAsk` is a detached spawn. The trusted
  key `[session-entry].unattended-permission-mode` (global sweatfile or a layer
  above the repo; `sweatfileio.TrustedUnattendedPermissionMode`, never in
  `Hierarchy.Merged`) decides an UNCONFINED worker's mode; a CONFINED worker is
  always `dontAsk` whatever the key says
  (`internal/foxtrot/spawn/unattended.go`, `applyUnattendedMode`).
- Applying it does two things (`internal/foxtrot/spawn/spawn.go:168-171`,
  `:256-259`): `SpliceDontAskMode` places exactly one `--permission-mode
  dontAsk` after the first `--` of the **spawn argv**, dropping any other
  permission mode (`dontask.go`), and `setUnattendedMarker` puts
  `CLOWN_UNATTENDED=1` into the spawn environment AND into the pinned entry's
  environment, `session.State.Entry.Env`.
- The pinned entry (`session.PinnedEntry{Start, Resume, Env, ...}`,
  `shop.PinEntry`) is the sweatfile's `[session-entry]` start, resume and env
  as resolved at creation. Its `Resume` argv is **not** spliced: only the env
  marker records the posture. `sc resume` launches the pinned resume entry
  with `entry.Env` (`cmd/spinclass/commands_session.go:839`), and clown reads
  the marker from that environment to apply its dontAsk settings
  (spinclass-sweatfile(5), *unattended-permission-mode*). A resume carries the
  existing pin forward unchanged (`internal/echo/shop/shop.go:464-466`); a
  session created before pinning has `Entry == nil`.
- The splice refuses an entry that cannot take the flag: no `--` separator, or
  a provider other than claude (`dontask.go:48-59`). An unconfined spawn whose
  trusted key says `dontAsk` is refused the same way (`unattended.go:32-44`).
- A confined worker's `grit_commit` and its whole posture come from the
  confinement profile (FDR 0035); its SessionStart hook reaches only the
  display copy of the record, never the frontend record.

### 1.2 The restart (FDR 0037)

- Request: `restart-this-session` and `restart-child-session` share
  `queueRestart` (`cmd/spinclass/restart_cmd.go:149-193`): `restartPlan`
  checks the obstacles (implicit session, principal shape, a trusted hand-off
  configured, no running job unless forced), records
  `session.RestartRequest{RequestedAt, Requester, Reason, When}`, expands the
  hand-off argv per element (`expandRestartHandoff`, `:93-105`; placeholders
  `{principal}`, `{session-key}`, `{when}`, `{reason}`) and execs it in the
  worktree with a 15 s bound. A failed hand-off clears the request.
- Authority: restarting yourself needs no right (D6); a child needs a handle
  with the `restart` right (`authorizeRestartChild`, `:310-337`); a confined
  serve caller is refused before anything (`:343`). `force` is always-ask in
  `perms.AlwaysAsk` (`internal/charlie/perms/check.go:92-102`), read
  fail-closed by `forceRequested` (`:148-158`: any non-boolean counts as
  force); it records `when: now` and skips only the running-job obstacle.
- Preflight: `runRestartPreflight` (`:472-551`) re-checks the obstacles,
  resolves the principal from the frontend mapping, and on success consumes
  the request and stamps `RestartingAt`, `RestartingBy`, `RestartingReason`
  in one locked `session.Update` that re-checks the closing mark (`:523-537`).
  It prints one JSON line, `restartVerdict{Verdict, Reason, When, State}`
  (`:385-392`). `--peek` is read-only; `--rearm` restores a dropped verdict
  from the in-flight stamp and re-checks the requester's `restart` right
  (`rearmRequesterAllowed`, `:568-579`), the one place the right is checked
  after request time.
- The plain preflight does not re-check rights: they are checked once, when
  the request is made. FDR 0037 Limitations names the consequence: the request
  lives in state an unconfined agent can write, so a forged `when: now` skips
  the running-job check; the always-ask guards the tool, not the state file.
- Rights (FDR 0032 D13): `restart` is the eighth object right, held by the
  spawner, not in the default `observe,close` grant. Granting may only narrow
  (EPIC 0001 principle 3). `force` is not a right but a D11 escalation.
  FDR 0035's rule for confinement is the same shape: narrowing needs no
  prompt, widening (an unconfined spawn) always needs the human.

### 1.3 Clown's half at 70f904a (U, per clown/rare-walnut)

- The restart relaunches by exec of the clown on PATH with the frontend's own
  resolved flags (`--resume <principal>`) and its own launch environment; it
  never re-reads `session.State.Entry` (`cmd/clown/restart_relaunch.go`). So a
  rewrite of the pinned entry reaches the next `sc resume`, and only the
  verdict reaches the restart's own relaunch. Both halves are needed.
- On `posture: unattended` clown sets exactly one `CLOWN_UNATTENDED=1` in the
  relaunch env, drops every forwarded permission-mode flag and adds one
  `--permission-mode dontAsk`. On `attended` it removes the marker and drops
  only the dontAsk flags, leaving claude's default mode. An unknown posture
  value in the verdict means "do not restart". The `[attach]` carrier follows.
- `clown restart --posture P` is gated at request time (unattended refused for
  `--naked`, a non-claude provider, a forwarded `--settings`) and again before
  the stop (refusal kind `posture` in clown's status file); clown never acts on
  the flag by itself, only on the verdict. The status file gains an
  `unattended` bool.
- Compatibility: an older spinclass sends no posture, which is keep; an older
  clown ignores the key.

## 2. The decisions proposed (I)

1. **Narrowing is free.** `restart-this-session` may request
   `posture: unattended` for the caller's own session with no right and no
   prompt (D6: the session's own act, gated by the operator's hand-off).
   `restart-child-session` may request it with the `restart` right, auto-
   allowed by the hook as the unforced restart is today. A session that
   narrows itself cannot widen itself back (3).
2. **Widening is an operator-gated escalation, not a right.**
   `posture: attended` on `restart-child-session` needs the `restart` right
   AND is always-ask in `perms.AlwaysAsk`, the same shape as `force`
   (FDR 0037 D8: no allow-list can approve it silently). At a terminal
   (`sc restart-child-session --posture attended`) the caller is the operator
   and nothing prompts, as for `--force`. `restart-this-session` refuses
   `attended` outright: a session may not widen itself. No new D13 right in
   v1: a right would let a holder delegate the widening without a human, which
   is the thing to avoid; if delegated widening is wanted later, a new right
   is the place, never `restart`. Reasoning: a `dontAsk` worker is detached by
   definition and attended means a human answers prompts, so widening
   presupposes the operator anyway; and FDR 0035 already draws the line at
   narrowing free, widening human.
3. **Confined sessions are out of scope and refused, not ignored.** A posture
   request naming a confined session (`restart-child-session`, or a confined
   session naming itself) is refused with the reason: its posture is the
   confinement profile's, always `dontAsk` with the marker. The existing
   confined-caller refusal stays in front of it.
4. **The request records the posture; the preflight applies it.**
   `session.RestartRequest` gains `Posture string` (`""` keep, `"unattended"`,
   `"attended"`). On a `restart` verdict, the same locked `session.Update`
   that stamps `RestartingAt` rewrites the pinned entry: set
   `Entry.Env["CLOWN_UNATTENDED"]="1"` for unattended, delete it for attended
   (the `setUnattendedMarker` rule, moved where both can reach it). Nothing in
   `Entry.Resume` changes, since the pinned resume argv is not spliced today
   (§1.1); the next `sc resume` then agrees with the restart. The verdict
   gains `Posture string` (`json:"posture,omitempty"`), printed only on
   `restart`. `RestartingPosture` is kept beside `RestartingBy` so `--rearm`
   can re-emit the same posture; `cancel-restart` drops the posture with the
   request. One update, so a crash between stamping and rewriting cannot
   leave them apart.
5. **Refusals on the request, mirrored from the spawn.** Unattended is refused
   with nothing recorded when the pinned resume entry's provider is not claude
   (`effectiveProvider` of the clown flags before `--`, `dontask.go:23-39`;
   the `--` requirement itself does not apply, since nothing is spliced into
   the pinned argv) or when the session has no pinned entry (`Entry == nil`,
   a session from before pinning: there is nothing to rewrite, and a resume
   would read the sweatfile live). Clown's own request-time gate (`--naked`,
   forwarded `--settings`) is reached through the hand-off's exit status, and
   a failing hand-off already clears the request (`:176-184`).
6. **The hand-off carries the posture as a placeholder.** `{posture}` joins
   the four placeholders of FDR 0037 D2 and `restart_contract_test.go` pins
   the new shape against a fake of clown's verb. Because substitution is per
   element and cannot drop an element, "keep" needs a spelling clown accepts
   on `--posture`, or spinclass must strip the flag and its value when the
   posture is empty. Preferred: clown accepts an explicit keep value (one
   rule, no argv surgery); open for clown in §7.
7. **The preflight re-checks the right for a widening.** A `posture:
   attended` request is honoured at the preflight only if the recorded
   requester is not the session's own principal and holds the handle with the
   `restart` right now, exactly the `rearmRequesterAllowed` check (`:568-579`).
   This costs one read and narrows the forged-state hole (§3) to a request that
   also forges a real holder's principal. Narrowing requests are not re-checked
   (a forged narrowing only takes authority away).
8. **Display.** `sc list` gains a posture column derived from the pinned
   entry's marker (unattended when `Entry.Env` carries `CLOWN_UNATTENDED=1`,
   attended otherwise, blank for an unpinned session), in `session.ListRow`.
   Clown's `unattended` bool is clown's view of the running agent; a
   disagreement between the two is information, not an error. The `--peek`
   verdict may also print the recorded posture, read-only, so clown can show
   it before relaunching (open, §7).
9. **Attended mode value.** Spinclass sets nothing for attended: the resume
   replays the operator's resume entry with no marker, and clown's relaunch
   drops only the dontAsk flags. There is no third knob.
10. **Record type.** An FDR 0037 amendment (Decisions: widening is operator-
    gated, narrowing free; Interface: `posture` on both tools, the verdict and
    `--rearm`; the `{posture}` placeholder) and one line on FDR 0032 D13 that
    `restart` does not carry widening. Not a new FDR.

## 3. The limitation, stated plainly (I)

The request lives in `session.State`, which an unconfined agent can write, and
the plain preflight checks rights at request time only. A hand-edited
`posture: attended` would widen the session on its next idle restart, the same
class of hole FDR 0037 Limitations names for a forged `when: now`. Decision 7
narrows it to a forgery that also names a real holder with the `restart`
right; it does not close it. The honest residual mitigation is that an attended
relaunch has a human at it. Closing it fully means the preflight honouring a
widening only when a trusted-layer sweatfile key allows posture widening at all
(the key would be code-shaped like `[session-entry].restart`, read live, never
pinned). That is the operator's open question (§7), left open here; it also
needs a decision on whether the key is per repo hierarchy or global only.

## 4. Sequence of the arc (R)

Nothing here moves until the operator rules on §7 and gives a go. Held items
are unchanged: the #432 build branch, C (#429), B's canary, the #424 host-queue
questions. When built, the live check is the unattended runbook's shape
(`docs/plans/2026-10-26-first-live-restart-unattended-runbook.md`): a worker
restarted into unattended shows `CLOWN_UNATTENDED=1` and one
`--permission-mode dontAsk` on the NEW agent pid, and back out shows neither.

## 5. Tests (I, test-first)

- `queueRestart` records `Posture` and expands `{posture}`; empty posture
  expands to the agreed keep spelling (or strips the pair, per §7).
- `restart-this-session` with `attended` is refused, nothing recorded; with
  `unattended` on a pinned claude entry it records and hands off.
- `restart-child-session` with `attended`: refused without the `restart`
  right; `perms.AlwaysAsk` asks on `attended` and on any non-string or unknown
  posture value (fail-closed like `forceRequested`), stays silent on
  `unattended` and absent; `RunCheck` mirrors it.
- A confined target is refused with the reason; an unpinned session is refused;
  a non-claude pinned resume provider is refused for unattended.
- The preflight's `restart` verdict rewrites `Entry.Env` in the same update as
  `RestartingAt` (set and delete), prints `posture`, and keeps
  `RestartingPosture`; a verdict with no posture leaves `Entry.Env` and the
  JSON line byte-identical to today (`restart_cmd_test.go:926`).
- A widening request whose recorded requester is the session itself, or lacks
  the right at preflight time, is `refused` with the holders told; a narrowing
  request is not re-checked.
- `--rearm` re-emits the posture; `cancel-restart` drops it.
- `restart_contract_test.go` pins `--posture` against the fake clown verb,
  including the keep case.
- `sc list` shows the derived column; `--format json` row carries it.

## 6. Build sequence (I)

1. `session.RestartRequest.Posture`, `RestartingPosture`, the marker helper
   shared by spawn and the preflight, with tests.
2. Request side: the `posture` parameter on both tools and CLI verbs, the
   refusals (2, 3, 5), `{posture}` in the hand-off, the contract test.
3. `perms.AlwaysAsk` case for `attended` and the hook table test.
4. Preflight: the re-check (7), the locked rewrite (4), the verdict field,
   `--rearm` and `cancel-restart`.
5. `sc list` column; sweatfile(5), FDR 0037 amendment, FDR 0032 D13 line,
   AGENTS.md one-liner.
6. Live check per §4, then the operator's deploy.

## 7. Open questions

For the operator:

- **Should widening also need a trusted-layer sweatfile key** (§3), and if so
  global-only or per hierarchy? Marked open on the coordinator's instruction.
- Always-ask on `attended` (decision 2) versus a new D13 right for delegated
  widening: which does the fleet want first?
- Is a self-narrowing without any prompt acceptable, given a narrowed session
  cannot come back on its own?
- Should `--peek` print the recorded posture, or only the `restart` verdict?
- The `sc list` column: wanted now, or wait for clown's `unattended` bool to be
  visible elsewhere?
- Unpinned (legacy) sessions: refuse (decision 5), or pin the entry at the
  request from the current trusted layers?

For clown (rare-walnut):

- The keep spelling of `--posture` on the hand-off (decision 6): does `clown
  restart` accept an explicit keep value, or must the flag be absent?
- An older clown that ignores the verdict key relaunches in the old posture
  while the pinned entry already says the new one, so the two paths disagree
  until the next `sc resume`. Should the verdict reason name it, or should
  spinclass refuse a posture request when the hand-off clown is too old, if
  clown exposes a version to check?

## 8. Sources

FDR 0037 (`docs/features/0037-session-restart.md`), FDR 0032 D13, FDR 0035;
`cmd/spinclass/restart_cmd.go`, `internal/charlie/perms/check.go`,
`internal/foxtrot/spawn/{unattended,dontask,spawn}.go`,
`internal/echo/shop/shop.go`, `cmd/spinclass/commands_session.go`,
`internal/alfa/session/session.go`; spinclass-sweatfile(5)
*unattended-permission-mode*; clown/rare-walnut's reports of 2026-11-05 and
2026-11-06 on clown 70f904a (U, not read here).
