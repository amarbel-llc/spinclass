# Per-session key holder (plan)

Status: in review (round 1 of 3). The switch is `[keyholder] mode` in a trusted
sweatfile layer, default off; reference text is in `spinclass-sweatfile(5)`.
This file holds the decisions the manpage points at; it grows as findings close.

## Driver versus worker (ruled for this build: rule A)

With the switch on, spinclass gives a holder to the sessions it launches: such a
session gets its own fibby and holder pair and its `SSH_AUTH_SOCK` points at the
holder in place of the operator's agent. A session the operator starts himself at
his terminal, the DRIVER, keeps his agent and gets no pair, because he still mints
forge tokens and signs operator acts with the card-backed agent until that is
delegated (decision 4 of the operator's five).

A `sc start` or `sc resume` launch is the driver's only when all three hold
(`cmd/spinclass/keyholder_driver.go:47-58`, the one classification point):

1. the session's record names no spawner (a spawn writes it; a session cannot);
2. neither `CLOWN_SESSION_ID` nor `SPINCLASS_SESSION_ID` is in the environment
   (the markers the card-act commands use to refuse an agent runtime);
3. stdin and stdout are both terminals.

Anything else is a worker: every spawned worker (the spawn path never asks who is
at the keyboard, so a worker cannot make itself a driver), a `sc start` or `sc
resume` that is piped or runs inside a session, a session whose record names a
spawner even when the operator resumes it at his terminal, and a session started
again by the start hand-off (see 'Start hand-offs').

The call sites are `sc start` (`cmd/spinclass/commands_session.go:607`, passes
`spawned=false`) and `sc resume` (`:867`, passes whether the record names a
spawner); `TestStartAndResumeReportTheirLaunchKindToTheKeyHolder` pins both. The
classification (`classifyKeyholderLaunch`) and the decision "this kind gets a
pair / its environment is rewritten" (`keyholderPolicyFor`, `:73`) are separate on
purpose.

### Why a session that fakes a terminal gains nothing

- UNCONFINED session: a process that unsets the two markers, allocates a pty and
  runs `sc start` makes a session that inherits that process's OWN environment
  (`keyholder_driver.go:42-46`), which is already the holder's or none. And an
  unconfined session already reaches the operator's agent by path: the socket
  files sit in his home and runtime directory, same uid. The holder for an
  unconfined session is delegation hygiene, NOT a boundary; there is nothing to
  gain by posing as a driver that it could not do directly.
- CONFINED session: it cannot reach `sc start` outside its unit. Inside the unit
  `sc start` would run in the unit's own namespace with the unit's environment,
  whose only agent socket is the holder's (`internal/foxtrot/spawn/confine.go:304-316`,
  the spec binds the holder and nothing of the operator's); whatever it starts
  stays in the unit. Starting a process outside the unit would need the user
  manager, which clown's reviewers read as unreachable from a unit (private
  network, no bus variable; clown `unit.go`, findings of the bridge round A:
  seen in clown's code by them, not run by me).

### Fast-follow B (not this build)

Rule B gives the driver a holder socket BESIDE his agent. It is additive: the
driver's policy flips `StartsPair` to true and keeps `RewritesEnv` false; the
classification and the worker path do not change (`applyKeyholderPolicy` already
carries the pair-without-rewrite case, with a test). Open questions for B:

1. Which agent is `SSH_AUTH_SOCK` by default for a driver who has both a holder
   and his own agent? (The holder under another name, as written above, keeps
   today's behaviour; making the holder the default is the other direction.)
2. How does the driver choose the other one for one command or one session?
3. The question returns for drivers as a whole, implicit ones included (below):
   what a driver's session is allowed to hold is one decision, not one per
   launch path.

### Implicit sessions: OPEN

An implicit session is an agent the operator started in a repo's main checkout
OUTSIDE spinclass; spinclass learns of it from the SessionStart hook
(`MaterializeImplicit`) and never launches it. Do not read this as "implicit
sessions are drivers". The operator's direction, quoted:

> implicit sessions should have their own holders too, but there should be an
> escape hatch for me launching them that blesses them with my agent when
> necessary (hogtie maybe)

So the direction is a holder for EVERY session, including the ones he starts
himself, and his agent reaches a session only by an explicit blessing. Whether
that is this build or the fast-follow (with option B for drivers) is being asked.
Nothing is implemented for implicit sessions here, and the holder socket is not
written into the direnv file. What stands in the way today: the hook runs inside
the already-started agent, so a pair started there could reach the agent only
through the file the `.envrc` loads (`WriteSpinclassEnv`), which is not the
agent's own environment. Today an implicit session keeps the operator's agent
because spinclass sets nothing for it.

### What a blessing would be

- A session cannot bless itself: it is an operator act, at his terminal or with
  his card, never a tool a session can call.
- It is time-limited and says what it blesses (his agent, for that session).
- It is visible in `sc list` while it lasts, and logged.
- It replaces nothing permanently: when it lapses the session is back on its own
  holder.

## Resume rebuilds the stored unit spec (B-F2, A-H1)

A confined spawn writes its unit spec once. `sc resume` used to hand clown that
stored spec unchanged, so a spec written with the switch off kept the operator's
agent bound after the switch went on, and one written with it on named a holder
socket that is gone after it went off. Now (`spawn.RetargetAgentSocket`,
`internal/foxtrot/spawn/confine_retarget.go`, called from
`cmd/spinclass/commands_session.go:875`) a confined resume rewrites only the agent
socket and `SSH_AUTH_SOCK`, atomically:

- switch on and this launch has a pair: the holder's socket is the spec's only
  agent socket; the operator's bind and any earlier holder path are dropped; an
  unusable holder socket refuses the resume and stops a pair this resume started;
- switch off (or a driver) and the stored spec names a holder: the spec goes back
  to exactly what the builder writes with the switch off (byte-identical, tested);
- otherwise the stored spec is untouched.

Stored-spec reuse paths found: (1) `sc resume` via `confinedResumeScope`
(`cmd/spinclass/resume_confined.go:49`), now retargeted; (2) clown's restart in
place re-reads the spec file named by `CLOWN_AGENT_SCOPE` on relaunch (clown code,
not spinclass; it sees whatever the last `sc resume` or spawn wrote, and a restart
that keeps the same pair keeps the same socket path); (3) a respawn's
`stickyAgentSocket` (`internal/foxtrot/spawn/confine.go:469`), read only when the
holder is off; (4) the start hand-off (next section); (5) read-only readers
(`internal/bravo/confinement/probes.go`, `caller.go`).

## Start hand-offs (A-M1)

`start-child-session` and the operator's `sc start-stopped` run the trusted
`[session-entry].start-handoff` (`cmd/spinclass/start_cmd.go`). Until K2 it ran
with the CALLER's whole environment.

Can a confined session be started this way? By spinclass's code: yes, when a
frontend principal mapping names it (`ownerPrincipal`,
`cmd/spinclass/close_self_cmd.go:267-289`; a confined spawn leaves one). The
hand-off relaunches clown with `--resume <principal>`; it does NOT pass through
`sc resume`, so `confinedResumeScope` does not run. Whether clown then relaunches
the child confined is decided by clown (from the scope in the environment it is
given, or from its own record), which I have not read; this is the one thing to
verify with a real stop and start.

With the switch on the hand-off now runs with an environment spinclass builds
(`cmd/spinclass/keyholder_handoff.go`): the child is always a worker (a detached
relaunch has no terminal of the operator's), gets its own pair, the operator-agent
variables are emptied, the caller's holder socket never reaches it, and the
caller's `CLOWN_AGENT_SCOPE` is replaced by the child's own stored spec (itself
retargeted as above) or removed for an unconfined child. With the switch off the
hand-off is run exactly as before (inherited environment). A failing hand-off
stops a pair this start launched.

## Later

His holder is a software card with slots 9A-9C and 82/83: per-purpose keys AND
intermediate signing authorities in one holder. That sits beside the one-key rule
of this build. `sc session-key` refuses any key the pair did not mint (K6), and
that check stays: several keys later means naming them by purpose, not loosening
the check.
