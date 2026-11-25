# Per-session key holder (plan)

Status: in review (round 1 of 3). The switch is `[keyholder] mode` in a trusted
sweatfile layer, default off; reference text is in `spinclass-sweatfile(5)`.
This file holds the decisions the manpage points at; it grows as findings close.

## Driver versus worker (ruled for this build: rule A)

With the switch on, a session gets its own fibby and holder pair and its
`SSH_AUTH_SOCK` points at the holder in place of the operator's agent. That
applies to WORKERS. The operator's own launch, the DRIVER, keeps his agent and
gets no pair, because he still mints forge tokens and signs operator acts with
the card-backed agent until that is delegated (decision 4 of the operator's
five).

A `sc start` or `sc resume` launch is the driver's only when all three hold:

1. the session's record names no spawner (a spawn writes it; a session cannot);
2. neither `CLOWN_SESSION_ID` nor `SPINCLASS_SESSION_ID` is in the environment
   (the markers the card-act commands use to refuse an agent runtime);
3. stdin and stdout are both terminals.

Anything else is a worker: every spawned worker (the spawn path never asks who
is at the keyboard, so a worker cannot make itself a driver), a `sc start` or
`sc resume` that is piped or runs inside a session, and a session whose record
names a spawner even when the operator resumes it at his terminal.

The check is advisory in one direction only. A process that unsets the markers
and allocates a pty and runs `sc start` makes a session that inherits that
process's own environment, which is already the holder's (or none), so it gains
nothing. Nothing a session controls after launch makes it a driver.

Code: `cmd/spinclass/keyholder_driver.go`. The classification
(`classifyKeyholderLaunch`) and the decision ("this kind gets a pair", "its
environment is rewritten") (`keyholderPolicyFor`) are separate on purpose.

### Rule B, a fast follow

Rule B gives the driver a holder socket BESIDE his agent (a pair started, the
environment untouched, the holder socket offered under another name). It is one
additive change: the driver's policy flips `StartsPair` to true and keeps
`RewritesEnv` false; the classification and the worker path do not change.
`applyKeyholderPolicy` already carries the pair-without-rewrite case, with a
test.

### Not covered: implicit sessions

An implicit session is an agent the operator started in a repo's main checkout
OUTSIDE spinclass; spinclass learns of it from the SessionStart hook
(`MaterializeImplicit`) and never launches it, so it cannot set that process's
environment or start a pair before the agent runs. What stands in the way: the
hook runs inside the already-started agent, a pair started there would reach
the agent only through a file the `.envrc` loads (`WriteSpinclassEnv`), which
is not the agent's own environment. It is a separate item, not a small change.
Today an implicit session is treated as the operator's own (it keeps his agent).
