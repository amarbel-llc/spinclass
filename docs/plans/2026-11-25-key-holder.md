# Per-session key holder (plan)

Status: in review (round 2 findings fixed; round 3 of 3 next). The switch is `[keyholder] mode` in a trusted
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
(`cmd/spinclass/keyholder_driver.go:48-59`, the one classification point):

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
`spawned=false`) and `sc resume` (`:878`, passes whether the record names a
spawner); `TestStartAndResumeReportTheirLaunchKindToTheKeyHolder` pins both. The
classification (`classifyKeyholderLaunch`) and the decision "this kind gets a
pair / its environment is rewritten" (`keyholderPolicyFor`,
`keyholder_driver.go:137`) are separate on
purpose.

### Why a session that fakes a terminal gains nothing

- UNCONFINED session: a process that unsets the two markers, allocates a pty and
  runs `sc start` makes a session that inherits that process's OWN environment
  (`keyholder_driver.go:43-47`), which is already the holder's or none. And an
  unconfined session already reaches the operator's agent by path: the socket
  files sit in his home and runtime directory, same uid. The holder for an
  unconfined session is delegation hygiene, NOT a boundary; there is nothing to
  gain by posing as a driver that it could not do directly.
- CONFINED session: it cannot reach `sc start` outside its unit. Inside the unit
  `sc start` would run in the unit's own namespace with the unit's environment,
  whose only agent socket is the holder's (`internal/foxtrot/spawn/confine.go:311-325`,
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
3. The question returns for every session the operator starts himself, implicit
   ones included (below, under 'Later'): what such a session is allowed to hold
   is one decision, not one per launch path.

### Not given a holder in this build

Ruled by the operator for this build: a session the operator starts himself,
explicit or implicit, is not given a holder; it keeps his agent. That is the
driver rule above, not a gap. An implicit session is an agent started in a repo's
main checkout OUTSIDE spinclass: spinclass learns of it from the SessionStart hook
(`MaterializeImplicit`) and never launches it, so there is no launch to give a
holder to. Nothing is implemented for it, and the holder socket is not written
into the direnv file. (The hook runs inside the already-started agent, so a pair
started there could reach the agent only through the file the `.envrc` loads
(`WriteSpinclassEnv`), which is not the agent's own environment.)

## A changed agent is a changed boundary: close and respawn (B-F2, A-H1)

A confined spawn writes its unit spec once, and clown re-reads that spec FILE on
every relaunch (a resume, a restart in place, a start hand-off). Clown's own gate
compares the file's boundary digest with the one it recorded at the last confined
launch, and the digest covers the sorted socket purposes. But the file does not
change by itself: an UNCHANGED stale spec has the recorded digest, so clown
relaunches it, silently, with whatever agent it was spawned with (verified by
reading `agentscope_record.go` `loadLaunchSpec` / `boundaryDigest` and the compare
at its line 348: the digest is taken from the file on disk now). Clown's gate
therefore refuses only a spec file REPLACED in place with other socket purposes
("a changed spec means a new session"). It is NOT a second wall against a changed
switch. **Spinclass's own check is the only wall.** A changed agent is a changed
boundary, and a session whose stored spec does not match what the builder would
write for the switch as it is NOW is not relaunched; spinclass says so first,
before clown is called and before any pair is started for the relaunch
(`spawn.CheckAgentBoundary`,
`internal/foxtrot/spawn/confine_agentcheck.go`; the callers are in
`cmd/spinclass/keyholder_boundary.go`). Nothing is rewritten, in either direction:

- switch on, stored spec binds the operator's agent (or no agent): refused;
- switch off, stored spec binds a holder socket: refused (the path names a pair
  that is gone or that nothing now uses);
- the stored spec matches the switch: relaunched exactly as before.

The refusal names the change (the agent socket in the stored spec against the
`[keyholder] mode`) and says: close this session and spawn a new one, with what
`sc close` would discard. So **a confined session spawned before the switch went
on must be closed and respawned, and so must one spawned with a holder after the
switch goes off.** (This replaces the in-place rewrite the first version had; the
holder socket keeps its own purpose, `key-holder`, because a changed agent is a
changed boundary and a new session is the honest answer.)

The check runs at every relaunch of a confined session from a stored spec:
(1) `sc resume` (`relaunchBoundaryNow`, defined at
`cmd/spinclass/keyholder_boundary.go:61`, called at
`cmd/spinclass/commands_session.go:874`), before the pair is started; (2) the
restart preflight and its `--rearm` (`relaunchBoundaryBeforeRestartFn`, defined at
`keyholder_boundary.go:84`, called at `cmd/spinclass/restart_cmd.go:681` and
`:919`), before the pair check, which is shared by `restart-this-session`,
`restart-child-session`, `sc restart` and restart-hung-child, because clown
relaunches without `sc resume`; (3) the start hand-off (next section;
`checkRelaunchBoundary` at `cmd/spinclass/keyholder_handoff.go:60`). A respawn's
`stickyAgentSocket` (`internal/foxtrot/spawn/confine.go:504-523`, used at `:573`)
still reads an earlier spec for the switch-off builder only. Clown's own restart in place that never goes through the
preflight is NOT covered here, and clown does not cover it either: it re-reads the
unchanged spec, its digest matches the recorded one, and the session relaunches with
its OLD boundary (the old agent). That is known behaviour, not a refusal.

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
caller's `CLOWN_AGENT_SCOPE` is replaced by the child's own stored spec or removed
for an unconfined child. A child launched confined is checked against the switch
first, whatever the switch says (the section above): a stored spec that does not
match is refused BEFORE a pair is started and before clown is called, and the
child stays stopped.

clown confines a started child by its sidecar record under the child's
`XDG_STATE_HOME`, else by `CLOWN_AGENT_SCOPE`; with neither it starts the child
UNCONFINED (the "resuming UNCONFINED" notice is printed only for a named
`--resume` id). So the environment the hand-off builds must carry the state
directory this frontend keeps its records under (set explicitly, whatever the
caller's environment had) and, for a confined child, the child's own spec path;
`checkHandoffEnv` refuses an environment without either before clown is called.
With the switch off and a child that was not launched confined, the hand-off is
run exactly as before (inherited environment). A failing hand-off stops a pair this
start launched.

## A lost pair is told, not hidden (B-F3, A-M3)

The keys live in fibby's memory, so a pair that is lost (a reboot, a dead unit)
is replaced with a NEW key and every certificate for the old one is void. The
operator accepted that only if he is told. What happens (`internal/0/keyholder/
remint.go`, `cmd/spinclass/keyholder_remint.go`):

- The pair package records the key's SHA256 fingerprint at the mint, and keeps a
  key history `<state>/spinclass/keyholder/<hash>.last.json` NEXT TO the session
  directory, not inside it (replacing a lost pair removes the directory whole).
  The session's own end (`sc close`, a failed launch's undo) ends the history;
  a lost pair does not, and neither does the operator's `sc keyholder-stop --all`
  of a session that lives on (the history is kept, marked `stopped` with the time
  and the last fingerprint), so the next mint is known to be a change and is
  announced. The history of an orphan goes with the pair.
- Every path that replaces a pair (start, resume, spawn, the start hand-off, the
  restart preflight) comes through one function, which prints the old and new
  fingerprint on stderr, writes the lifecycle log, and wakes the session's
  holders and its spawner with both fingerprints, at most once per 10 minutes per
  session (the ones held back are counted in the next wake).
- `sc list` (text, and the `keyholder` field of `--format json`) and session-health
  show `keyholder: DEAD` for a recorded pair that is not running and `keyholder:
  RE-MINTED ...` while no certificate for the new key is installed
  (`<state>/spinclass/keyholder/<hash>/cert/cert.pub`, see "The certificate").
- clown relaunches a restarted session without `sc resume`, so the restart
  preflight, and `restart-preflight --rearm`, start a lost pair BEFORE they approve
  the relaunch; if the pair cannot be started they refuse the restart naming the
  cause (a relaunch on a holder socket nothing listens on would fail every
  signature). restart-this-session, restart-child-session, `sc restart` and
  restart-hung-child all end in that preflight. A session that never had a pair is
  not touched, and with the switch off nothing is started.

Not covered: a pair that dies while the session runs is noticed by `sc list` and
session-health, and fixed by the next resume or restart; nothing restarts it in
the background.

## The certificate (B-F4)

The key holder's key opens nothing until the operator's card signs a certificate
for it. Spinclass never signs and never holds a CA; it hands out the public key
and gives the certificate a place to live, a place the session can read and
cannot write.

- **One path, one place that computes it** (`keyholder.CertPathIn`):
  `<state>/spinclass/keyholder/<session-hash>/cert/cert.pub`. The `cert/`
  DIRECTORY is created by the pair launch (and by `Start` for a live pair that
  predates it), exactly 0700 whatever the umask, owned by the user, never a link;
  the state root and the session directory are exactly 0700 too. The signer
  writes `cert.pub` (0600) by write-then-rename into that directory only.
- **Why a directory:** a bind mount of one file keeps the old inode after a rename
  over it, so a bound `cert.pub` would never show the new certificate (and cannot
  be bound before one exists). A confined unit gets the DIRECTORY, read-only, as
  the only bind the key holder adds (`binds_ro`; fibby's, the agent's and the work
  directory stay out), and `SPINCLASS_KEYHOLDER_CERT=<cert dir>/cert.pub` in its
  environment. An unconfined session gets the same variable pointing at the same
  file in the state directory. `CheckAgentBoundary` treats a stored holder spec
  without that bind or variable as a changed boundary (close the session and spawn
  a new one): clown's boundary digest covers `binds_ro`.
- **`sc session-key SESSION`** prints exactly two lines on stdout: the key line as
  the holder lists it (type, key, the holder's own comment) and its SHA256
  fingerprint. `--json`: `{session, public_key, fingerprint, cert_path,
  cert_expires, cert_matches_key, cert_problem, vouched}`; `session` is the
  canonical `<repo>/<branch>` key whatever form was passed, and `vouched` is the
  stated limit below (the golden has eight fields since round 2; text mode prints
  the same sentence on stderr, and stdout stays the two lines). A real capture of both forms is
  in `cmd/spinclass/testdata/session-key.{txt,json}.golden`
  (`just debug-session-key-capture` regenerates them from a real pair); a test
  pins the field set and that the command still writes them byte for byte.
- **Reading the certificate** (`keyholder.InspectCert`): no link followed
  (`O_NOFOLLOW`), regular file, at most 16 KiB, one line, an OpenSSH certificate
  type, then `ssh-keygen -L` on the bytes just read (stdin, so a swapped file is
  never what ssh-keygen sees). `cert_matches_key` is true when the certified key's
  fingerprint is the holder's, `cert_expires` is the end of the validity in UTC;
  both are null with no certificate, and `cert_problem` says why an installed file
  is unusable. A certificate for another key, an expired one or an unusable one is
  a warning on stderr; stdout stays the two lines. `sc list` and session-health
  show `certificate valid until ...`, `certificate is for ANOTHER key`,
  `certificate EXPIRED` or `certificate unusable`; session-health also says `no
  certificate`.

## Locks, leftovers and what off does (B-F6, B-F7, A-M4)

- **Lock.** Start and Stop of one session take an exclusive flock on
  `<state>/spinclass/keyholder/<hash>.lock`, in the keyholder root beside the
  session directory (Stop removes the directory, so a lock inside it would vanish
  under its holder). 0600, opened with O_NOFOLLOW. A second start
  of the same session waits, then finds the live pair and returns it: one pair,
  one mint, whatever races. A lock held for more than a minute fails the second
  caller with a message naming the file. Stop removes the file as its very last
  step, under the lock: it unlinks only if the path still names the inode it
  locked, and every acquire checks the same after the flock (a waiter that held
  the removed inode closes it and locks the new file), so two holders are never
  inside across the unlink. A stop that fails keeps the pair and its lock. A lock
  file with no pair and no directory is found by `sc keyholder-stop --all` and
  removed by it and by `sc clean` unless a process holds the lock (a try-lock:
  the sweep never waits). Stopping a pair, and switching off, leave no file.
  A `<hash>.kind` record left with no pair, no directory, no lock and no key
  history (a session removed by something other than `sc close`) is swept the
  same way; the kind record of a LIVE session is kept (a driver has no pair, and
  its kind file is the one trace of it), and a kind record beside a key history
  is a stopped session's, kept on purpose. A key history (and its kind record)
  with nothing left of its pair, that NO session record maps to, is swept too
  (`Found.HistoryOnly`): `sc clean` reaps a session's worktree without going
  through the key holder, so an operator stop's kept history would otherwise stay
  for ever, tell a later session with the same key that its first key REPLACED
  one, and keep the state root non-empty (which is evidence for the "never
  switched it on" rule). One whose session record exists is the live session's,
  reported and never swept, and `sc keyholder-stop --all` does not list it as a
  pair to stop. `sc clean` surveys before it reaps, so a history left by the
  worktrees it reaps in one run is swept by the next. `ForgetSession` (close)
  looks for a pair directory AGAIN under the lock: a start that held the lock
  while it waited has made a pair since, and it is stopped the proper way, not
  left running with its records deleted.
  **Evidence for the lock.** `TestAStopRacingAStartLeavesAWholePairOrNone` is NOT
  evidence that the lock works: it passes with the flock taken out (checked by
  mutation, with the fake runner slowed to widen the window), because with the
  fakes Stop and Start leave a consistent result in either order. It stays as a
  check that nothing half-removed is left behind. The evidence is
  `TestAStopRacingAStartAcrossTheLockUnlinkNeverLetsTwoHoldersIn` (200 rounds, a
  real flock; red when the same-inode check is dropped) and
  `TestConcurrentStartsOfOneSessionMakeOnePairAndMintOnce` (red when the lock is
  made shared).
- **Switching `mode` off** starts nothing and leaves nothing for NEW sessions.
  The pairs of existing sessions run until their session closes; `sc close`
  stops a pair whatever the switch says. A confined session spawned with a holder
  is refused on resume (a changed agent is a changed boundary: close and respawn).
- **`sc keyholder-stop --all [--dry-run] [--including-unowned]`** is the remedy:
  it stops every pair THIS STATE ROOT recorded (see "Whose pairs these are"),
  by unit name and by directory, holder first, directories only
  after both units are inactive, journalled to
  `<state>/spinclass/keyholder-journal.ndjson` with actor `tty` (a journal that
  cannot be written warns and the stop still happens: stopping is the safe
  direction). A real stop needs a terminal; `--dry-run` lists and marks orphans
  and needs none. It needs neither the switch nor the trusted paths, so it works
  after the host's configuration is gone.
- **Orphans** are pairs with no live session: a directory with no readable
  record, units with no directory but a key history or lock file under this
  state root, a record naming a closed or vanished session (units with NO trace
  under this state root are another state root's, not orphans).
  A pair with no live session that is younger than ten minutes is a launch in
  flight (the pair starts before the session record is written) and is not an
  orphan. `sc clean` stops orphans; `sc keyholder-stop --all --dry-run` marks
  them.
- **Narrowing-only (A-M4).** The target repo of a spawn is chosen by the caller,
  so its hierarchy alone cannot decide the switch (the same reason `[confinement]`
  and `[profile-rights]` read the driver's context too). The key holder is ON when
  EITHER the target's trusted layers OR the driver's own context (the repo that
  holds the working directory, else home) say on; neither can turn the other off.
  Driver on + target off = on; driver off + target on = on; both off = off. A repo
  or worktree layer is never honoured in either. The programs come from the
  target's trusted layers, else the driver's; when both set a program and they
  differ the launch is refused naming `[keyholder] fibby|piggy`, because the
  directory the caller chose must not pick the program the pair runs. An
  unreadable hierarchy or an invalid mode in either context refuses the launch
  (never the operator's agent). The boundary check on a relaunch reads the same
  resolved switch (`cmd/spinclass/keyholder_switch.go`).
- **What "trusted" rests on (round 2, A-M1).** A layer is trusted when it is the
  global file under the home directory or a sweatfile in a directory ABOVE the
  repo (`sweatfileio.LayerAboveRepo`): files of the operator's own account,
  written by the host's configuration. Two things the caller used to choose and
  now does not: (1) the mode is ON when ANY trusted layer says on (a nearer
  layer's off never undoes a farther on; `sweatfileio.TrustedKeyholderMode`);
  (2) the home is not taken from `$HOME` alone: `resolveKeyholderSwitch` reads
  the passwd home's layers too (`internal/0/trustedhome`), the switch is on if
  they say on, and the programs `fibby` and `piggy` from them win over the ones
  the caller's own home names; an account database that cannot answer refuses
  the launch. What the caller still chooses: which directories lie above the repo
  it names, and which repo is the driver's context. So a switch the host wrote
  only into a parent-directory layer (`~/eng/sweatfile`) binds a launch whose
  target or driver context is under that directory and no other; the host must
  write `mode = "on"` into the GLOBAL file under the passwd home to bind every
  launch. And every file here is writable by any process of the same account: the
  switch is the operator's setting, not a boundary against a session that can
  write his home.
- **Whose pairs these are (round 2, B-H1).** Units are per user, the state root
  follows `$XDG_STATE_HOME`. `Find` marks a hash `Unowned` when this root has no
  directory, key history or lock file for it; an unowned pair is never an orphan,
  `StopFound` refuses it (`ErrNotMine`), `sc clean` names it and leaves it, and
  `sc keyholder-stop --all` stops it only with `--including-unowned`, after
  listing it (journalled `stop-unowned`). A hand-removed directory AND history
  AND lock leave the units "not mine" too: `--including-unowned` is the way out.

## Stated limits, and what is visible (A-H1, A-M2, B-M2, B-M6, B-M7)

These are limits of this build, said plainly; none is a pretend fix.

- **A-H1: between unconfined sessions of one account there is no wall.** The
  fingerprint `sc session-key` checks the socket against is a file
  (`record.json`, `<hash>.last.json`) beside the holder's socket; `Start` adopts a
  live pair as it stands; `sc keyholder-pair` starts and stops pairs. Any
  unconfined session of the account can replace ANY session's holder, confined or
  not, and write a record that matches, so the key `sc session-key` prints is
  vouched for only against CONFINED sessions (which cannot reach the user manager
  or the state directory: seen in clown's code by the reviewers, not run by me).
  The operator certifies a key, so this matters at the moment he does: say so
  where he reads it. `sc session-key` prints "vouched only against confined
  sessions: an unconfined session of this account could have written this record"
  on stderr in text mode and as the `vouched` field of `--json`. `sc keyholder-pair
  start|stop` now needs stdin and stdout on a terminal, or the variable
  `SPINCLASS_KEYHOLDER_PAIR_RECIPE=1` the live recipes set (status only reads).
  The variable is NOT a boundary: any process of the account can set it. It keeps
  the plain call, a driven script and an accident out; the wall is the Sunday item.
  *For Sunday: where a record a session cannot write could live.* (1) The user
  manager's own unit properties, set at start (`systemd-run --property=` a
  description or an environment carrying the minted fingerprint, read back with
  `systemctl --user show`): a same-uid session can still `systemctl --user stop`
  and start units, so this stops only the quiet edit, not the replacement; (2) a
  record in a root-owned directory written by a small helper, or signed by the
  operator's card at the moment he certifies (the card then vouches for the key it
  signed, which is what the certificate already is); (3) the real fix is the
  per-session identity epic: sessions of one account in separate users or
  confined units.
- **A-M2: switching on does not convert sessions that already run.** An unconfined
  worker launched before the switch keeps the operator's agent across a restart,
  a watchdog relaunch and a re-arm: they reuse the environment it had. `sc list`
  (text, JSON and the table), session-health and `sc validate` now say
  "holder: none, operator agent ... a restart does not convert it; close and
  respawn" for every live worker with no pair while the switch is on. Only a
  resume of an unconfined session, or a close and a new spawn, puts it on its own
  key; a confined session is refused on resume until it is closed and respawned
  (above).
- **B-M2: the two units are tied one way.** The holder's unit starts with
  `BindsTo=` and `After=` fibby's, so fibby going away stops the agent and the
  half-dead state (an agent that still lists the key but can no longer sign)
  cannot exist; the live pair recipe stops fibby alone and asserts the agent unit
  goes with it. NOT done, for Sunday: the agent dying does not stop fibby (the
  next start tears the pair down whole); nothing watches a pair that dies while
  the session runs (a heartbeat or an `ExecStopPost` that wakes), and a RUNNING
  confined unit never sees a replaced pair, because the spec binds the agent
  socket FILE and a replaced pair is a new inode the old bind does not show (the
  certificate has the directory-bind answer; the socket does not yet).
- **B-M6: a worker's jobs inherit the holder socket.** The merge, check,
  post-merge and hook jobs of a worker run from its frontend, whose
  `SSH_AUTH_SOCK` is the holder once the switch is on; nothing in the hook runner
  or the job runner handles it. That is correct for this build (they are the
  session's own work and the holder is its identity), with this consequence: a
  worker's `merge-this-session` whose post-merge target needs the operator's agent
  (a deploy, an rsync, anything over ssh to a host) fails AFTER the merge has
  landed. Either a worker merges with `targets=[]` and the coordinator deploys, or
  the operator names an exception; nothing here chooses for him.
- **B-M7: implicit sessions: no holder in this build** (operator ruling: this
  build keeps rule A; implicit sessions get holders on Sunday). See "Not given a
  holder in this build" above and "Later".

## A host that never switched it on launches as before (round 3, A-M1 / B-M2)

The key holder added places a launch can fail that did not exist before it: the
account database's home lookup, the sweatfiles read under that home, the
driver's own context, and the home and state directories the relaunch paths
resolve. A host with no `[keyholder]` table must launch exactly as it did, so
(`cmd/spinclass/keyholder_newsource.go`, one function for every site):

- The account database's home not resolving (the one source left that is not a
  layer) refuses the launch only when there is evidence the switch is, or once
  was, on for this host: a layer that did load says `on`, or the key holder has
  left state (any entry, whatever its name: a pair directory, a key history, a
  kind record, a lock file or an unrelated file; an unreadable state root counts
  as evidence, since not knowing is not "no") under EITHER state root: the one the
  caller's environment names (`XDG_STATE_HOME`, else `$HOME`) AND the account
  database's own (`<passwd home>/.local/state/spinclass/keyholder`), so a process
  started with another `XDG_STATE_HOME` or an empty `HOME` cannot hide what the
  host holds. A root that is not an absolute path says nothing (an empty `HOME`
  makes the environment's root relative to the working directory). Otherwise it
  is warned once per process (per distinct message; two soft failures in one
  launch are one message of two lines) on stderr and the launch proceeds with the
  switch off.
- The sites that resolve `HOME` or the state directory themselves (the
  relaunch boundary check and the start hand-off) cannot trust either variable:
  when they fail they take "a layer says on" from the account database's own home
  layers (`keyholderLoadedOnViaPasswd`), not from a hard-coded false.
- A refusal by evidence says why and the way out: it names the state root it
  looked at ("the key holder is or was on for this host (it has left state under
  ...)") and what the operator runs to launch as before with no key holder in use
  (`sc keyholder-stop --all`, `sc clean`, remove that directory), or, for a layer
  that did load, to fix what failed.
- Still refused with no evidence at all: the TARGET repo's own sweatfile
  hierarchy not loading (every launch read it before the key holder existed:
  `attachSession` and the spawn path); a layer that EXISTS but cannot be loaded
  (the driver's own context, the layers under the account's home): an unloadable
  file may be the one that switches the key holder on, and only an ABSENT layer
  is "never on" (note: this reads two places a launch did not read before, so a
  broken sweatfile above the driver's repo now refuses a launch that used to
  ignore it); and a `[keyholder]` table that does not parse or validate (a layer
  that names the key holder is evidence in itself).

## Deploy order, and what breaks if it is violated (B-M5, B-M4)

A host takes the key holder in this order. Each step names what breaks when it is
done out of order; every one fails closed and none exposes the operator's agent.

1. **A spinclass that parses `[keyholder]`** is on the host. Without it,
   `sc validate` rejects the `[keyholder]` table circus's configuration writes
   (an unknown table), in every gate that runs it. Circus's own check
   (`infra/hosts/lib/spinclass-parses-keyholder.nix`) makes this order mechanical.
2. **Clown f502df21 or later** is the host's clown. Its spec validation lets a
   confined spec set `SSH_AUTH_SOCK` to a socket it binds (the holder's). An OLDER
   clown refuses every confined spawn with "env key SSH_AUTH_SOCK is scrubbed from
   the agent scope and may not be set by the spec". SEEN, in the PARENT of that
   commit (`git diff f502df21ae7d^..f502df21ae7d -- internal/alfa/agentscope/`,
   read in the clown checkout): `spec.go` already held the scrubbed-key branch
   (`if scrubbedKeys[k] { return fmt.Errorf("env key %q is scrubbed from the agent
   scope and may not be set by the spec", k) }`, at about lines 156-158 of the
   parent, counted from the hunk header) and `SSH_AUTH_SOCK` was already in
   `scrubbedKeys` (`unit.go`, outside the commit's one hunk, which only changed a
   comment there); f502df21 added only the exception. An older clown therefore
   refuses the spec by that branch. I did not RUN an older clown. That is fail
   closed and loud, but the text does not point at the
   key holder. **There is no way for spinclass to detect it ahead of time:**
   `clown agent-scope-capabilities` lists `agent-unit`, `private-network`,
   `frontend-mcp-sockets` and the like, and carries no marker for this rule, and
   clown has no validate-only command to ask. So the order is a HARD requirement:
   clown f502df21 first, then the switch.
3. **Lingering is enabled** for the user: `loginctl enable-linger <user>`. The pair
   is two transient USER units; a user manager that exits at the last logout takes
   every key with it, announced at the next resume of each session (K3's notice). A
   launch with no user manager to talk to is refused with a message that names
   lingering (`userManagerHint`, `internal/0/keyholder/systemd.go`); a user manager
   that exists but will not outlive the login is not detectable ahead of time, so
   this step is also a requirement, not a check.
4. **Circus's `[keyholder]` configuration** (`mode = "off"`, `fibby`, `piggy`) is
   deployed. With the mode off nothing starts and nothing is validated beyond parse.
5. **Every long-running session is restarted after the bump, BEFORE the switch**
   (restart it, or close it and spawn a new one). A running `spinclass serve`
   keeps the binary it started with, and an old serve spawns workers on the OLD
   path: no pair, the operator's agent, and it ignores `[keyholder]` silently (an
   unknown table fails only `sc validate`). The new binary cannot reach into it.
   Detection: `sc host-load` (text and `--format json`) lists every live serve
   whose binary lacks the key holder's build marker (`keyholder.BuildMarker`) under
   "KEY HOLDER STALE SERVE" (field `keyholder_stale_serve`), by the same scan as the
   host queue's QUEUE BYPASS (`cmd/spinclass/hostload_keyholder_stale.go`). It is a
   fact about binaries, so it is shown WHETHER THE SWITCH IS ON OR OFF, from any
   working directory, with no sweatfile read: check it at this step, BEFORE step 6,
   and restart until it is empty. (Right after the deploy every serve that started
   before it is listed, on a host that has not switched anything on: that is the
   list to restart.) A serve whose binary cannot be read is skipped silently, as
   QUEUE BYPASS skips it. `sc list` flags a worker that has no pair ("holder: none,
   operator agent") while the switch is on. That the marker really is in the
   SHIPPED binary is checked by the bats lane
   (`keyholder_build_marker_is_in_the_shipped_binary`, in
   `zz-tests_bats/serve.bats`), not by a Go test (a Go test binary holds the text
   for any reference a test makes). The marker is not bumped for every change: it
   must be bumped (here and in the bats test) whenever a later change makes serves
   that carry the older marker unsafe at switch-on, so they are listed as stale.
   It was NOT bumped for the evidence rule that followed the merge of 07f71750,
   because 07f71750 was never deployed to any host (the bump was held for that
   batch), so no live serve carries the marker without the rule.
6. **The operator switches `mode` on** (his act). Before this, a launch with the
   mode on and the paths unset is refused naming the setting.

A host's builds also matter: `.#spinclass-native` carries none of the tool pins and
refuses to start a pair (the launcher never looks a program up on PATH); the shipped
builds (`packages.default`) carry them.

## Later

For Sunday's review, not this build. Every session gets a holder, the ones the
operator starts himself included, and his agent reaches a session only by an
explicit blessing: an operator act (never a tool a session can call; at his
terminal or with his card), time-limited, saying what it blesses (his agent, for
that one session), visible in `sc list` while it lasts and logged, and when it
lapses the session is back on its own holder. His holder is also a software card
with slots 9A-9C and 82/83: per-purpose keys AND intermediate signing authorities
in one holder. That sits beside the one-key rule of this build: `sc session-key`
refuses any key the pair did not mint (K6), and that check stays; several keys
later means naming them by purpose, not loosening the check.
