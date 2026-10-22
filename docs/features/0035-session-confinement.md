---
status: proposed
date: 2026-10-19
promotion-criteria: |
  proposed -> experimental: the confinement check (`internal/confinement`) is
  the production Confinement behind FDR 0034's profile-rights gate, every probe
  has a fake-backed unit test, and `sc confinement-check` prints the per-fact
  table on a real host. The check still fails today; that is expected.
  experimental -> testing: clown#244's agent unit lands, spinclass hands it the
  required view behind `[confinement].enabled`, and on a host with that
  switch on every probe passes for a spawned worker; posh's attach
  authorization lands and its probe is real rather than "unverifiable".
  testing -> accepted: the D17 security review (spinclass#339) has looked at
  the unit and the check and its findings are recorded here and in FDR 0032.
---

# Session confinement

> **Proposed.** Child of **FDR 0032** (principals, handles, provenance),
> decision **D17** ("Security posture, stated plainly"), which names this
> record as its companion (spinclass#338). Consumed by **FDR 0034** (spawn with
> a clown profile), whose profile-rights gate refuses to treat any grant as
> card-rooted until the confinement check in this record passes ("Where the
> card root is real"). Cross-repo: clown owns launching the unit (clown#244),
> posh owns attach authorization, piggy states the precondition
> (piggy-agent(1) SCOPES, piggy FDR 0006). Where this record and FDR 0032
> disagree, FDR 0032 wins.

## Problem Statement

```
  ┌──────────────────────────────────────────────────────────────────────┐
  │  TODAY AN AGENT IS ITS OPERATOR'S UID. EVERY RIGHT IN FDR 0032 IS A   │
  │  POLICY ITS OWN TOOLS ENFORCE, NOT A WALL IT LIVES BEHIND.            │
  └──────────────────────────────────────────────────────────────────────┘
```

FDR 0032's rights lattice governs the **sanctioned tool surface**. It does not
govern the uid, and every one of these is, today, an escape from every ambient
right at once:

- **A `folio_write` (or a Bash redirect) to a shell rc** (`~/.bashrc`,
  `~/.config/fish/config.fish`): the next interactive shell the operator opens
  runs the agent's code as the operator, outside every scope.
- **A write to a git hook or `core.hooksPath` target** in any checkout the uid
  can write, the main checkout included: the next `git commit` the operator
  (or another session) runs executes it.
- **An edit to a `flake.nix`, `justfile` or `.envrc`** that the pre-merge hook,
  direnv, or the operator's next `nix develop` evaluates: code execution in
  whatever context evaluates it, including the operator's.
- **Anything under `~/.config`, `~/.ssh`, `~/.local/state`**: sweatfiles (the
  global sweatfile turns `[profile-rights] enforce` on or off), the grant store,
  ssh config, clown/troupe config.
- **The operator's prompt surface.** A process that can reach the session
  D-Bus, a Wayland or X11 socket, or a writable posh attach can draw over the
  prompt piggy-agent raises for an operator act. piggy-agent(1) SCOPES: the
  prompt is evidence only if nothing an agent runtime controls can write to the
  surface the operator reads it from. Today that is false (FDR 0034's table:
  the session bus is inherited and reachable by path).

The last item is the one this record must close first: FDR 0034 roots profile
rights in card signatures, and a card signature is only evidence of operator
intent if a worker could not have painted the text the operator approved.

## The model

Three scopes, after FDR 0032 D7 and clown#244:

- **frontend scope**: clown, troupe's connection owner, the MCP plugins clown
  launches, piggy-agent and its askpass (as a user service, never a descendant
  of any of these). Has the operator's uid, home, bus and display.
- **agent scope (the unit)**: `claude` and everything under it, tool calls,
  Bash, hooks. A transient systemd unit launched by clown. Sees only what this
  record lists.
- **operator surface**: the terminal / posh session / graphical session the
  operator reads prompts from. No path from the unit to its write side.

spinclass decides **what the unit must see** and owns **the check** that the
unit sees no more. clown decides **how** (systemd properties, `--tent` as one
realization). The check verifies outcomes, never clown's choice of mechanism,
so a different launcher (juggler, a per-run OS user) satisfies it unchanged.

## The unit's required view

What a worker needs to do its job, and nothing else:

| Item | Access | Notes |
|---|---|---|
| The session worktree | read-write, bind-mounted at its own path | So paths in briefs, state and the hello stay valid |
| The repo's git common dir (`<repo>/.git`) | read-write for `objects` and this worktree's `worktrees/<name>`; `config`, `hooks`, `refs`, `logs`, the worktree's `config.worktree` and the worktree's `.git` pointer file **read-only**; the session credential **inaccessible** | Git reads refs but cannot write them, so raw `git commit`, `checkout -b` and `update-ref` fail in the unit by design: ref writes go through grit in the frontend (FDR 0036). See "The git view" below |
| `$TMPDIR` | read-write | Already `<worktree>/.tmp`, covered by the worktree bind |
| The madder store | **read-only** | Blobs are content-addressed; writes go through tools in the frontend |
| `/nix/store` | read-only | Toolchain |
| The agent socket (`SSH_AUTH_SOCK`) | connect | Allowed: every act raises a prompt the operator can cancel (FDR 0034) |
| troupe IPC socket, ringmaster socket/journal | connect / as the platforms require | Named by their owners; passed as single socket inodes, not their directories |
| `SPINCLASS_*` identity env, `TMPDIR`, `CLAUDE_CODE_TMPDIR`, the worker's `CLOWN_*` | env | Built by `spawn.workerEnv` today; unchanged |
| The operator's `$HOME` | **none** except the bind mounts above | `ProtectHome` or equivalent. The worker's own `$HOME` is a private tmpfs |
| Session D-Bus, Wayland, X11, posh attach socket | **none** (not reachable by path, not just unset) | The prompt surface |
| A tty or pty of the operator | **none** | clown gives the agent a pty of its own |

`DISPLAY`, `WAYLAND_DISPLAY` and `DBUS_SESSION_BUS_ADDRESS` stay scrubbed by
`spawn.workerEnv` regardless (hygiene, kept as defence in depth).

### The git view (round I findings, three commits)

A confined canary showed the unit could create refs in the shared common dir
(`refs/` and `logs/` were `binds_rw`) and `git push` with the session's minted
credential (`<worktree>/.spinclass/git-credentials`, wired by `credential.helper
store --file` and `url.<https>.insteadOf` in `<common>/worktrees/<name>/config.worktree`).
Separately, the frontend runs grit's git with the session worktree as its cwd,
so git discovers the repo through unit-writable files (the `.git` pointer file,
`config.worktree`) and would run config-defined commands (`core.fsmonitor`,
`core.sshCommand`, `credential.helper=!cmd`, `diff.external`, filter drivers,
pager, editor) in the frontend, outside the unit. The unit spec now says:

| Path | Spec field | Why |
|---|---|---|
| `<common>/objects`, `<common>/worktrees/<name>` | `binds_rw` | Git writes objects and this worktree's HEAD and index |
| `<common>/config`, `<common>/hooks`, `<worktree>/.spinclass/hooks` | `binds_ro` | As before (F9) |
| `<common>/refs`, `<common>/logs` | `binds_ro` (were `binds_rw`) | Git still resolves refs; only the frontend creates or moves them, so a raw push of a unit-made ref is no longer possible |
| `<common>/worktrees/<name>/config.worktree` | `binds_ro`, nested over the rw `worktrees/<name>` | Closes the frontend escape through the worktree-scoped config. `writeUnitSpec` creates it empty when absent: `auth.Inject` writes it only when a credential was minted, and a bind source that does not exist would fail the unit start |
| `<worktree>/.git` (the pointer file) | `binds_ro`, nested over the rw worktree bind | Keeps the unit from repointing the frontend's discovery |
| the minted credential | none: not in the unit's tree (since 2026-10-22; it was a `deny_paths` entry on `<worktree>/.spinclass/git-credentials`) | It lives in frontend state, `$XDG_STATE_HOME/spinclass/credentials/<hash>`, hidden by `ProtectHome`; grit (frontend) reads it through its get-only helper. See "Frontend rewrites detach file mounts" |

Raw `git commit`, `git checkout -b` and `git update-ref` inside a confined unit
now fail by design; use grit. This changes nothing for an **unconfined** worker:
it has the same raw-push capability it always had and keeps it. The frontend
side (the explicit repository and the config neutralisers, FDR 0036 section 9.5)
is the second half of the same fix.

`sc confinement-check` F9 probes each of those: the six write targets unwritable,
and the credential unreadable (or absent).

**Unit-tested only, not live.** The spec contents (`TestBuildUnitSpecShape`,
`TestBuildUnitSpecRefsLogsReadOnlyAndCredentialDenied`), the F9 probe logic
against a fake filesystem, and the creation of `config.worktree`. Not
exercised on a real unit, and so **unverified until a live canary**:

- that systemd honours `InaccessiblePaths=` for a file inside a
  `BindPaths=` read-write tree;
- that a nested read-only **file** bind (`config.worktree`, `.git`) over a
  read-write directory/bind takes effect, and the ordering of
  `BindReadOnlyPaths=` against `BindPaths=` for nested mounts;
- that `rename`/`unlink` of a read-only-bound file is refused (EBUSY) so the
  pointer file cannot be replaced from the rw directory above it (the F9 probe
  only tests writability of the file itself);
- that `git` inside the unit still works for read verbs with `refs`/`logs`
  read-only (`git status`, `diff`, `log`), including `nix flake` evaluation of
  git inputs and direnv hooks, which are **unverified** against this change;
- clown's spec parser accepting the fields: no new field was added, only
  existing `binds_ro`/`binds_rw`/`deny_paths` entries changed.

**Live evidence (rounds J3 and K, nikulin canary, 2026-10-22).**

- J3, worker canary/noble-hazel on spinclass 55b46c8:
  - a raw ref create failed with `Read-only file system`;
  - a raw https push found no credential;
  - all four `.git` marker paths were blocked.
- K, worker canary/swift-teak on spinclass 4bd1590:
  - the spec carried every row of the table above;
  - `sc confinement-check` passed F9 before any push; that run used the driver's principal, so its F1 and F15 failures came from the wrong principal, not the cut;
  - after the round, with the worker's principal, F1–F8b, F13 and F15 passed. F9 failed (below), and F10, F11, F12 and F14 failed as open items;
  - grit add, commit and push through the frontend worked with the minted credential (the first credential-store push);
  - the stash refusal and the force deny arrived as written (FDR 0036).
- Round K's P2–P6 (pointer and `config.worktree` replace/unlink, nix and direnv) did not run: `just` found its runtime dir read-only.
- L, worker canary/quick-magnolia on spinclass 41ce553 (the get-only helper):
  - **nested RO file binds hold**. On the `.git` pointer and `config.worktree`: writes fail with `Read-only file system`; `mv`, `rm` and `ln -sf` over them fail with `Device or resource busy`; `git config --worktree` fails with EBUSY. That settles the first three items of the list above.
  - the credential still stats (InaccessiblePaths mounts a mode-000 inode) but `cat` fails with `Permission denied`, before and after a push;
  - F9 stayed ok after a grit push with `set_upstream`;
  - raw `update-ref`, `checkout -b` and commit fail on the read-only refs, while `status`, `log` and `diff` work;
  - with the canary-grit profile the worker's tool list had no moxy `grit_*` and no `chix_build`. Absent, so never called.
  - Still unverified: `nix` flake evaluation and direnv in the unit. The canary fixture has no `flake.nix` and its `.envrc` is not allowed.
  - Still failing: F10, F11 (see below), F12, F14.
- **F10 and F14 were check gaps, not cut failures (inferred, 2026-10-22).**
  - F10 counted every numeric `/proc` entry in the unit against the unit's
    cgroup. The overcount grew over the rounds (+2, +3, +5), which fits
    zombies: an exited task leaves `cgroup.procs` at exit, but keeps its
    `/proc` entry until reaped. F10 now counts zombies (state `Z` in the
    unit's `/proc/<pid>/stat`) apart, passes with a note when they are the
    only extras, and names each live extra.
  - F14 did not know that clown binds the child's own troupe account file
    (`troupe/accounts/<principal>.pass`). It now accepts exactly that, and
    names any other account or entry under `troupe`.
  - Both readings wait on a live round: a probe listing the unit's runtime
    dir, and F10's note or failure detail.

### Frontend rewrites detach file mounts (round K, open)

After the worker's grit push, F9 failed: the credential file was readable from
the unit, though it had been unreadable before the push. The file had existed
since mint (`auth.writeCredential`) and the spec's `deny_paths` named exactly
that path. Cause, part measured and part theory:

- **Measured** (`TestHardenedCredentialHelperNeverRewritesTheFile`, run against
  the old helper). The frontend's pinned `store --file=<cred>` helper makes git
  run the `store` action after a successful auth and `erase` after a rejected
  one. `git-credential-store` rewrites the file as a new inode, so a rejected
  auth also drops the token from it.
- **Theory, not verified on a unit.** A mount whose mountpoint is renamed over in
  another mount namespace is detached (Linux 3.18 and later). The unit's
  `InaccessiblePaths=` mount sat on the old inode, so after the rewrite the unit
  sees the new, readable file.

Fixed for the credential: the confined frontend helper is get-only
(`getOnlyCredentialHelper`, FDR 0036 section 9.5), so no push or fetch rewrites
the file. A re-mint writes in place (`O_TRUNC`, same inode).

**Round M (workers canary/fast-hazel and its sibling, 41ce553), measured by
the driver.** The frontend's `<common>/config` got a new inode four times:
twice from spawns (`auth.Inject`'s `git config`, the content unchanged), once
from a sibling spawn, and once from a push with `set_upstream` (+70 bytes).
The unit's view stayed on the inode it started with at every step, with the
old content. It was never detached, never missing, and every write
(`append`, `git config`, `mv`) failed with `Read-only file system`.

So not every file mount detaches. What decides it is the mount point's
**parent directory**. This reading is inferred, and it is consistent with
rounds K and M:

- **The unit does not share the parent.** `<common>` in the unit is systemd's
  stub under `ProtectHome=tmpfs`; only its children are bound. A host rename
  in the real `<common>` never touches the unit's mount point, so the mount
  holds. Its content goes stale: the unit's git does not see the upstream
  config its own push wrote. That is functional, not a security issue.
- **The parent is an rw bind of the real directory**, i.e. the worktree or
  `worktrees/<name>`. Host and unit share the parent dentry, so a host rename
  replaces the mount point itself and the mount is lost. That is round K's
  readable credential.

Frontend operations that rewrite a file-mounted path while a unit runs:

| Bound path | Parent in the unit | Frontend writers | Status |
|---|---|---|---|
| `<worktree>/.spinclass/git-credentials` (was `deny_paths`) | rw worktree bind | credential-store `store`/`erase` on push/fetch | fixed twice: the get-only helper (F9 ok after a push in rounds L and M), then the file moved to frontend state (option (b), below) |
| `<common>/config` (`binds_ro`) | tmpfs stub | every common-scope `git config`: each mint in the repo (`extensions.worktreeConfig true`), `set_upstream` pushes, branch tracking setup | holds, but stale (round M) |
| `<common>/worktrees/<name>/config.worktree` (`binds_ro`) | rw `worktrees/<name>` bind | `git config --worktree` (`auth.Inject`, at mint only, before the unit) | safe today. A re-mint or re-inject while the unit runs would detach it: a frontend escape through per-name config hooks |
| `<worktree>/.git` (`binds_ro`) | rw worktree bind | `git worktree repair` and `move` | safe unless someone repairs a running worker. Close it instead |

**Design question.** File mounts inside an rw bind make the cut depend on no
frontend operation ever renaming over the bound file. There were two options
to remove that dependence:

- (a) bind directories instead of files, e.g. a dedicated credential directory
  that is not inside the worktree, made inaccessible as a directory;
- (b) keep secrets and config out of the unit's tree entirely. Put the credential
  in the frontend state dir. For config, use a repository config the frontend
  never rewrites, or an overlay.

**Decided (coordinator, 2026-10-22): option (b) for the credential.**
- The minted credential is written to `confinement.CredentialPath`:
  `$XDG_STATE_HOME/spinclass/credentials/<sha256(worktree)[:8]>`, 0600 in a
  0700 directory, beside the unit records the unit already cannot see.
- `auth.Inject` points the worktree's `credential.helper` there, so unconfined
  raw git keeps working.
- A confined caller's grit uses only that file. A copy in the worktree could
  have been planted by the unit, so it is never used: with the frontend file
  gone, the remote verb fails on git's own authentication error (`trustedRepoFor`).
- An unconfined session minted before the move keeps its legacy worktree file
  until it is closed.
- Close removes the frontend file whatever the revoke does, and so does the
  orphan sweep after a revoke.
- The spec no longer lists a credential `deny_paths` entry.
- F9 checks that neither the frontend file nor a copy in the unit's tree is
  readable from the unit.

Why not (a):
- a directory bind keeps the secret inside the unit's mount namespace, one
  bind away, and still needs an inaccessible mount to hold;
- the frontend already owns a state directory the unit cannot see, so moving
  the file there removes the mount entirely;
- nothing in the unit needs the credential: raw git in a confined unit is
  meant to have none.

**Not movable: `config.worktree` and the `.git` pointer.** git reads them from
the worktree layout, so they keep their read-only file mounts, under two
guards:
- `auth.Mint` refuses while the worktree's unit runs, since a mint rewrites
  `config.worktree`;
- F9 now checks that each of these is still a read-only mount point in the
  unit's mountinfo (`fileBindsInForce`), so a lost mount is reported rather
  than hidden behind a file that happens to be unwritable.

`git worktree repair` and `move` on a running worker remain the operator's to
avoid; close the worker instead.

### Session PIDs are untrusted (round L, 2026-10-22)

`State.PID` lives in `<worktree>/.spinclass/state.json`, which the session's
own agent can write, confined or not. A confined worker's spawn-hello hook ran
inside its unit and recorded `os.Getppid()` from the unit's private pid
namespace. That number is meaningless to the frontend. F11 then misjudged the
worker as dead and reported its agent "runs unconfined" inside its own unit.
Worse, `sc close` sent SIGHUP to that number on the host, and did so before it
had decided to close at all (a refused close still signalled). Like the
principal, the PID is now untrusted input:

- the hello records no PID when `CLOWN_UNIT` is set (`hooks.go`
  `maybeSendSpawnHello`);
- `executor.RequestClose` never signals a session with a unit record or spec
  (its unit is stopped instead). It signals any other session only when the
  process provably belongs to it: its cwd is inside the worktree, or its
  environment carries `SPINCLASS_SESSION_ID=<key>`. A PID it cannot prove is
  not signalled, and close says so. Tests cover a foreign PID, a reused PID,
  PID 0, a dead PID, and a confined record (valid or not);
- **a refusal precedes any signal.** Close signals only after the
  unintegrated/dirty/shelf refusal and the confirmation, so a refused or
  declined close sends nothing (`TestRefusedCloseSendsNoSignal`). Before this
  fix the signal came first. On flic, refused `close-child-session` calls on
  two unconfined sessions were followed by those sessions exiting (the
  coordinator's observation, 2026-10-22).

Audit of every reader of a recorded PID (file:line at the time of this change):

| Reader | Use | Needs |
|---|---|---|
| `internal/close/close.go` (RequestClose call, WaitForExit) | signal, then wait | fixed: signal only a proven PID, never for confined; wait only on the signalled PID |
| `internal/executor/close_signal.go` `RequestClose` (was `executor/session.go:174-183`) | SIGHUP | fixed |
| `internal/merge/merge.go:879` | RequestClose after an outside-session merge | fixed by RequestClose |
| `internal/confinement/driver.go:57` | `Live` = `IsAlive(st.PID)` for the check | fixed: a session whose PID is dead goes into `Inputs.Recorded`, and the probes count it live while `unitRunning` holds (its record loads and its cgroup holds a process). F11 no longer reports a worker's own agent as "runs unconfined" inside its own unit |
| `internal/session/session.go:844` `ResolveState` | active vs inactive in `sc list` | fixed: an active session with no live PID stays active while `session.ConfinedUnitRunning` (set by the binary to `confinement.UnitRunningForWorktree`) reports its unit running |
| `internal/session/session.go:605-613, 665` | implicit-session sweep and lookup | main checkouts are never confined; the PID is agent-writable, but used only for liveness, never signalled |
| `internal/hooks/hooks.go:365` `recordOwnPrincipal` | keeps a differing recorded principal while its PID is alive | for a confined session PID is 0, so a new principal is recorded. Acceptable: the frontend mapping, not this field, is trusted |
| `internal/clean/clean.go:437` | transient merge worktree PID, taken from the directory name | frontend-created, not from state.json; unaffected |
| `internal/executor/wait.go:22` `WaitForExit` | polls a PID | fed only by close's signalled PID now |
| `internal/attestation/attestation.go:220` | claim liveness | written by the serve, not the agent. Unaffected by namespaces; its storage location was not audited here |

## The confinement check

`profilerights.Confinement.Check()` gets a real implementation,
`internal/confinement`. It is a list of **facts**, each with one probe. The
check passes only if every fact holds. **Any probe error is a failure** (fail
closed): an unreadable `/proc` entry, an unparsable mountinfo, a missing unit
record all refuse. A fact whose probe cannot run on this platform (no cgroup v2,
no `/proc`) fails.

The check runs at authorize time, every time FDR 0034 needs a grant to be
card-rooted; it is not continuous.

**Vantage: the frontend, looking into the unit.** `spinclass serve` is started
by clown's pluginhost from the plugin's `clown.json` `stdioServers`, as clown's
child, and claude reaches it over loopback (clown/cool-fir/koko, 2026-10-19). So
`spawn-session` and the check run in the frontend, outside the unit they judge.
The **subject** is the driver's own agent unit: the unit named in the driver
session's unit record. Its processes are the `cgroup.procs` of the reported
cgroup **and every descendant cgroup**; per-process facts are probed for every
one of them. Path facts are probed **through the unit's own mount namespace**
under `/proc/<main_pid>/root`, after checking that every unit process shares
that mount namespace. Paths are resolved there one component at a time: each
symlink is read and re-resolved inside the unit's root, never followed by the
kernel against the frontend's root (an absolute symlink below
`/proc/<pid>/root` would otherwise escape it).

**The unit record lives outside the worktree.** The worktree is bound
read-write into the unit, so a record there could be forged by the agent it
describes (a `pty` naming the operator's terminal, a `serve_socket` naming
another session's). clown writes it from the frontend to
`$XDG_STATE_HOME/spinclass/units/<sha256(worktree)[:8]>.json`, which
`ProtectHome` hides from every unit, and refuses a `result_path` the unit can
write. A record is refused unless its cgroup is a `.service` under
`clown-agent.slice`, and two live sessions' records may not share a cgroup or
a serve socket.

Only the frontend vantage is implemented and only it authorizes. An
`--inside` diagnostic (the same probes with `/` and `self` as the vantage) is
possible later; it could never authorize, since a compromised agent controls
its own process.

**`sc spawn` from inside a unit.** The CLI runs agent-side. A spawn from there
would put the child's frontend inside the parent's agent unit (setsid does not
move cgroups), violating FDR 0032 D7. With a spawn that resolves to confined,
`sc spawn` refuses when it is inside an agent unit and names `spawn-session` as
the route; with enforcement on, only `spawn-session` can authorize a profile.

**"Came over MCP" is not "came from claude".** clown's bridge trusts loopback
(clown ADR 0006): any process in the unit can call serve's MCP directly. Same
principal, so acceptable for the unit's own serve. The cross-session case is
open question 5.

**The uid does not change.** Under the systemd user manager `DynamicUser=` is
unavailable and `PrivateUsers=` maps the operator's uid to itself (koko). The
unit keeps the operator's uid; separation is by mount namespace, a private pid
namespace and a private devpts, not by uid. Every fact below is written for
that: none relies on a uid difference. A real uid split needs a system-manager
unit and is deferred.

| # | Fact | Probe |
|---|---|---|
| F1 | The driver's agent runs inside an active confinement unit clown reported for its session | Read the driver session's unit record (written by clown, see Interface); `/sys/fs/cgroup/<cgroup>/cgroup.procs` is non-empty (and holds `main_pid` when reported); the record's `principal`, written by the frontend clown that launched the unit, equals the driver's principal (claude's environment never carries `CLOWN_SESSION_ID`, clown#136, so it cannot be read from the unit's processes). Bound by principal, not the recorded PID: an attached session records the `sc` process, which runs outside the unit. No record, an empty cgroup or no principal match ⇒ fail ("not launched confined") |
| F2 | piggy-agent is outside every agent unit | Connect to `$SSH_AUTH_SOCK` (from the frontend), `SO_PEERCRED` → peer pid; `/proc/<pid>/cgroup` must not equal or descend from the subject's cgroup nor from any other live session's reported cgroup. Peer pid 0 or unreadable ⇒ fail. (From the frontend the peer pid is real; the self-report extension in open question 3 is only needed for the `--inside` vantage) |
| F3 | No inherited tty | For every pid of the unit, every `/proc/<pid>/fd` entry that is a tty character device (major 4 or 136–143, or `/dev/tty`) must be the unit's own pty: the same device number AND devpts instance as the `pty` path clown reports, stat'd from the frontend (a private devpts reuses low minors). The controlling tty in `/proc/<pid>/stat` (tty_nr) must be its number. Any other tty fd ⇒ fail |
| F4 | No display or bus variables | `DISPLAY`, `WAYLAND_DISPLAY`, `DBUS_SESSION_BUS_ADDRESS` absent from `/proc/<pid>/environ` of every pid of the unit (hygiene; F5/F6 are the boundary) |
| F5 | The session bus and the user manager are unreachable | `connect(2)` in the unit to `/run/user/<uid>/bus`, to `/run/user/<uid>/systemd/private` (the user manager, which would let the agent start an unconfined unit) and to any `path=` in the operator's bus address fails with `ENOENT`/`EACCES`/`ECONNREFUSED`; success ⇒ fail |
| F6 | The display is unusable | Under the unit's root: no `wayland-*` socket in the operator's runtime dir connects; no `/tmp/.X11-unix/X*` connects; the operator's X authority (`$XAUTHORITY` as the frontend sees it, else `~/.Xauthority`) is unreadable; and the operator's runtime dir `/run/user/<uid>` cannot be listed. A reachable **abstract** X socket alone is not a failure (accepted residual, see below) |
| F7 | The operator's home is not writable | For each escape target under the operator's home (shell rc and profile files, `.gitconfig`, `.config` and `.config/git`, the global sweatfile, `.ssh`, `.gnupg`, `.local/bin`, `.local/state`, `.local/share`): skipped only when the mount covering it in the unit is `tmpfs` or read-only **and** is not the mount covering it on the host (same `major:minor` in both mountinfos, as on an impermanence host whose real home is a tmpfs, proves nothing); otherwise the write to the target, or its nearest existing ancestor, in the unit must be denied |
| F8a | No posh attach socket is reachable | `${POSH_DIR:-$XDG_RUNTIME_DIR/posh}` and the operator's `/run/user/<uid>/posh`: `stat` fails with `ENOENT`/`EACCES` (per posh/smooth-maple/bobo; today a same-uid worker reaches it and `posh run <session> -- cmd` writes to the operator's pty). When posh's own refusal (`POSH_CONFINE`) lands, the probe additionally checks posh reports the refusal; the message string is posh's to fix |
| F8b | No operator pty is writable | For every `/dev/pts/N` other than the unit's own `pty`: `faccessat(W_OK)` is denied (today the operator's slave is 0620 and owned by the uid, so a worker can write it and spoof the display). A private devpts instance in the unit satisfies this trivially |
| F9 | The repo's hooks, config, refs and logs are not writable, the worktree's git entry points are not writable, and the session credential is unreadable | In the unit, the write to `<git-common-dir>/hooks`, `<git-common-dir>/config`, `<git-common-dir>/refs`, `<git-common-dir>/logs`, `<git-common-dir>/worktrees/<name>/config.worktree`, the worktree's `.git` pointer file and the resolved `core.hooksPath` is denied (each, resolved inside the unit, so a hooks dir replaced by a symlink is judged by what the unit reaches; a writable file is also a replaceable one, so the writable test stands for both). `config.worktree` and the `.git` pointer are still read-only mount points in the unit's mountinfo (a frontend rename over either would remove the mount). Reading the frontend credential (`confinement.CredentialPath`) or `<worktree>/.spinclass/git-credentials` is refused, or the file is absent. Renaming `hooks` away needs the common dir itself writable: open question 2 |
| F10 | The unit cannot see or reach processes outside it | `/proc/<pid>/ns/pid` of a unit process differs from the frontend's `/proc/self/ns/pid`, and every numeric entry of the unit's own `/proc` (`/proc/<pid>/root/proc`) is a process in the unit's cgroup. Without this, a same-uid worker reads `/proc/<operator-shell>/root/…` and walks straight out of its mount namespace (that access is `PTRACE_MODE_READ`, which Yama does not restrict), and can `ptrace` or write `/proc/<pid>/mem` of frontend processes where Yama allows. `ProtectProc=invisible` does not help: it hides other uids, and the uid is the same |
| F11 | Every live agent on the host is confined | Every live session in spinclass state has a unit record whose unit is running now (a non-empty cgroup under `clown-agent.slice`; a gone or empty cgroup is a stale record and fails), and every process on the host whose `comm` names an agent runtime (`claude`, `.claude-wrapped` under nixpkgs' wrapper, `juggler`, or any `harness` a live record reports) sits in some live session's unit cgroup (docs/plans/2026-10-20-agent-census-by-unit.md); offenders are named by session (from their cwd). One unconfined agent anywhere could have painted the prompt the card root depends on, and an unconfined session's MCP servers are an escape hatch for every confined one |
| F15 | The frontend principal mapping matches | The mapping the grant walk trusts (`$XDG_STATE_HOME/spinclass/principals/<principal>.json`, see "The principal-to-session mapping") exists for the driver's principal and names the driver's session and unit, and the worktree's cached principal, if any, is the same. A disagreement fails with both values named |
| F14 | The operator's runtime dir holds only the bound sockets | Listing `/run/user/<uid>` in the unit shows nothing but the entries spinclass and clown bind there (`clown`, `ringmaster`, and the agent socket's directory if it lives there), or cannot be listed at all. It is where the bus, user manager, gpg-agent, pipewire and posh live; the allowlist catches what F5, F6 and F8a do not name |
| F13 | The unit has a private network namespace | `/proc/<pid>/ns/net` of every unit process differs from the frontend's `/proc/self/ns/net`. In the host namespace every loopback MCP bridge (another session's moxy `folio_write` included) and every abstract unix socket is reachable. Hard requirement, same tier as F10 |
| F12 | No serve endpoint but the unit's own is reachable | The unit record names exactly one `serve` socket (the per-session unix socket clown's bridge listens on for a confined session); for every other live session's `serve` socket, `connect(2)` under the unit's root fails. No `serve` socket in the record ⇒ fail. The bridge's own peer check (SO_PEERCRED + `/proc/<pid>/cgroup` against the bound unit) is clown's and is not probed here |

With no agent unit on any host yet, the check **cannot pass today**. Measured
on the operator's workstation 2026-10-19 (`just debug-confinement-check`): only F2 holds (piggy-agent
runs in `app.slice/piggy-agent.service`); every unit fact fails for want of a
unit record, and F11 names six live sessions whose agents run in login-session
scopes.
That is intended: FDR 0034's "no grant is card-rooted until the check passes"
stays true, and the check now names exactly which facts are missing.

Each probe returns `{fact, ok, detail}`. `Check()` joins the failures into one
error naming every failed fact, so a refusal says why. `sc confinement-check`
prints the same table (TAP), for the operator and for the integration test.

**What the check is not.** It is not the boundary; the unit is. Run from the
frontend it is outside the unit's reach, but it only holds as long as F10 and
F12 do: a unit that can reach frontend processes or another session's serve can
subvert the frontend too. The `--inside` vantage is diagnostic only, since a
compromised agent controls its own process. The check exists so spinclass never
claims a card-rooted guarantee where a worker could have obtained the card's
signature by painting the prompt.

## Interface with clown (clown#244)

Agreed in shape with clown/cool-fir/koko on 2026-10-19 (koko proposed the
spec-file/result-file transport; spinclass added `operator_home`,
`deny_paths`, per-inode `sockets`, and `uid`/`pty` in the result). Field names
may still move while clown#244 is implemented; this record is updated when they
do. Mechanism is clown's: user-manager `systemd-run` with `ProtectHome=tmpfs`,
bind mounts, `InaccessiblePaths`, a private devpts and pid namespace, host
network for loopback to serve.

spinclass → clown (opt-in; unset means today's unconfined launch):

- `CLOWN_AGENT_SCOPE=<path>` names a spec file spinclass writes beside the
  unit record, outside the worktree so the unit cannot edit it
  (`confinement.UnitSpecPath`:
  `$XDG_STATE_HOME/spinclass/units/<sha256(worktree)[:8]>.spec.json`; this
  example shows the original shape, see the current fields in
  `internal/spawn/confine.go` `unitSpec`):

  ```json
  {
    "version": 1,
    "session_key": "spinclass/smart-redwood",
    "unit_name_hint": "spinclass-agent-spinclass-smart-redwood",
    "worktree": "/home/op/eng/repos/spinclass/.worktrees/smart-redwood",
    "operator_home": "/home/op",
    "binds_rw": ["<worktree>", "<git-common-dir>/objects",
                 "<git-common-dir>/worktrees/<name>"],
    "binds_ro": ["<madder store>", "<git-common-dir>/{config,hooks,refs,logs}",
                 "<git-common-dir>/worktrees/<name>/config.worktree",
                 "<worktree>/.git"],
    "binds_rw_platform": ["$XDG_STATE_HOME/ringmaster",
                          "$XDG_RUNTIME_DIR/ringmaster"],
    "sockets": [{"path": "<SSH_AUTH_SOCK>", "purpose": "agent"},
                {"path": "<troupe ipc>", "purpose": "troupe"},
                {"path": "<ringmaster>", "purpose": "ringmaster"},
                {"path": "<frontend egress proxy>", "purpose": "egress"}],
    "egress_allow": ["api.anthropic.com:443", "<forges>:443", "..."],
    "deny_paths": ["$XDG_RUNTIME_DIR/bus", "$XDG_RUNTIME_DIR/wayland-*",
                   "/tmp/.X11-unix", "$XDG_RUNTIME_DIR/posh"],
    "env": {"SPINCLASS_SESSION_ID": "...", "SPINCLASS_WORKTREE": "..."},
    "result_path": "$XDG_STATE_HOME/spinclass/units/<sha256(worktree)[:8]>.json"
  }
  ```

clown → spinclass: once the unit is active, clown writes `result` atomically:
`{version, unit, cgroup, invocation_id, uid, pty, serve_socket, main_pid,
principal, capabilities}` (`principal` is the launching clown's
`CLOWN_SESSION_ID`, the only place it is read from; `main_pid` is in the
frontend's pid namespace and becomes the
probes' root; unknown fields are tolerated, `version` gates meaning; `pty` is
the host path of the slave clown allocated, `/dev/pts/N`, empty if none, and
the check stats it from the frontend for its device number and devpts
instance; `serve_socket` is the per-session unix socket the bridge serves this
unit's MCP on, F12; `capabilities` echoes the capability query below). The
reported unit name is authoritative; the hint is only a hint. F1 reads it.

**Capability query** (agreed with koko). Before a confined launch, from the
frontend and with the clown the spawn-entry will run, spinclass runs
`clown agent-scope-capabilities`, which prints
`{"version":1,"capabilities":[…]}`. spinclass requires `agent-unit`,
`private-network`, `frontend-mcp-sockets` and `in-unit-moxy`
(`confinement.RequiredCapabilities`). A missing command or a missing
capability refuses the confined launch and names what is missing, so clown's
landing flips the switch without a spinclass change.

### CLOWN_UNIT: the in-unit marker (agreed 2026-10-22; clown side 508839d)

Contract, agreed with the clown session and landed there in clown 508839d:
clown sets `CLOWN_UNIT=<full .service unit name>` in the environment of the
confined unit only. It is the last entry clown's launch environment applies, so
nothing inherited or configured can override it, and clown scrubs it from every
other source (a frontend process never carries one). It is a **hint, not a
boundary**: an unconfined process can set it on itself, and gains only a
different refusal (below).

spinclass reads it in one place, the PreToolUse hook's grit store verdict
(`hooks.storeDecision` → `perms.StoreVerdict`, FDR 0036 section 2):

| CLOWN_UNIT | session resolved from the principal | non-force verdict |
|------------|-------------------------------------|-------------------|
| set        | yes                                 | `Classify` decides |
| set        | no (known principal or none)        | `Allow`, deferred to the serve |
| empty      | yes                                 | `Classify` decides |
| empty      | no (unknown, ambiguous, conflicting, or no principal) | `Ask` |

`force: true` asks in every row; under `--permission-mode dontAsk` every `Ask`
becomes a deny. The deferred row is safe for the same reason the earlier
blanket deferral was: the serve resolves the caller itself, a confined or
unresolved caller is refused any write it does not own, and the hint grants
nothing the serve would refuse. Setting the marker on an unconfined process
turns a human prompt into that body refusal; it opens nothing.

**Not inherited.** `session.StripInheritedSessionIDs` drops `CLOWN_UNIT` from
every child spinclass launches (spawn, attach, `sc exec`), so a worker the
frontend serve spawns never inherits one even if the launching process carried
it (`TestLaunchExecEnvCarriesWorkerIdentity`). A confined worker gets its own
from its own unit's clown. Dropping it fails closed: an unmarked hook asks.

**Older clown.** A clown older than 508839d sets no marker inside a unit, so
its confined workers' hooks see `CLOWN_UNIT` empty and an unresolved session,
and ask; with `dontAsk` the call is denied. That is the fail-closed direction
and is accepted for now: the hosts running confined workers have clown
8a2b9fe or newer (coordinator, 2026-10-22).

## Spawn side

`spawn-session` and `sc spawn` write the spec and set `CLOWN_AGENT_SCOPE` in the
worker's env when the spawn resolves to confined (`[confinement].enabled` plus
`default-spawn` or an explicit `confined` request; `launch-unit` is the
deprecated alias for both; default **off**; see "A confined spawn is not
always-ask"). Off, spawn is unchanged byte for byte. The existing env
hygiene (`harnessScrubbedEnvVars`) is kept in both modes. The switch is read
from the same trusted layers as `[profile-rights]` so a repo cannot turn it off
for a driver that set it.

**A stale serve refuses (spinclass#399, 2026-10-22).** A session's `spinclass
serve` keeps the binary it started with; after a deploy it kept writing unit
specs with the old code (nikulin canary: a 14-hour-old serve wrote a spec
without the round-I binds). Before writing a spec a confined spawn resolves
its own running binary (`os.Executable`, symlinks resolved; a collected binary
keeps its ` (deleted)` marker) and the `spinclass` on its PATH (symlinks
resolved, so a profile link to the same store path is not a false positive),
and refuses when they differ, naming both, or when either cannot be resolved
(`refuseStaleServe`, `internal/spawn/servefresh.go`). The `version` MCP tool, run in a
serve, adds a `serve binary: current|STALE|unknown` line; the CLI table is
unchanged. Unverified: that the serve's PATH resolves `spinclass` to the
operator's profile rather than a plugin-private bin dir; if it does not, the
check passes a stale serve, and the live round's `version` line is the proof.

Until clown runs moxy inside the unit and forwards only frontend-class servers
(ringmaster, troupe's MCP, spinclass serve) over per-session sockets, the
switch **refuses** to launch a worker confined, naming the clown rev it waits
for: a confined worker without folio, grit or rg cannot work, and a forwarded
moxy is a `folio_write`-to-`~/.bashrc` escape (coordinator, 2026-10-19).

troupe's password file is bound read-only into the unit as the **child's own**
file, derived from the child's principal, never a path inherited from the
spawner's environment. Every child launch (spawn, attach, `sc exec`) already
drops the parent's `TROUPE_XMPP_PASSWORD_FILE`/`TROUPE_XMPP_PASSWORD`
(`session.StripInheritedSessionIDs`, spinclass#169): the same uid can read the
parent's file, so a child could otherwise authenticate as its parent.

## The principal-to-session mapping (decided 2026-10-19, coordinator)

The grant walk joins a link's grantor principal to the grantor's session
(FDR 0034 Phase 2). That join used `session.SessionKeyOfPrincipal`, which reads
the worktree-local session state (FDR 0001), so a confined agent could rewrite
its own principal and bind the walk to whatever it liked.

The mapping now lives in **frontend-owned state** beside the unit records:
`$XDG_STATE_HOME/spinclass/principals/<principal>.json`
(`{version, principal, session_key, worktree, unit}`), hidden from every unit
by `ProtectHome`. The frontend writes it from the **unit record**, whose
`principal` the launching frontend clown wrote (`RecordPrincipalFromUnit`;
claude's environment never carries `CLOWN_SESSION_ID`, clown#136), so the agent
never names itself. The spawn binds BEFORE it waits for the hello: the record
is all the bind needs, and the first real confined spawn timed out at the hello
and returned with a live unit and no mapping. The bind retries while clown is
still writing the record (it lands from a goroutine after the unit goes
active), ignores a record older than the launch, and stops at once on a record
carrying clown's `error`. `sc confinement-bind --session <key>` repairs an
already-running unit the same way (CLI-only). It survives `sc
resume`, which keeps the principal through `{principal}`; rewriting it at
resume, once a resumed session launches confined, is a follow-up.
spawn's authorizer joins through it (`confinement.SessionOfPrincipal`, live
sessions only); it is removed when the session ends. The worktree state keeps
its `principal` field as a display cache that nothing authorizing trusts. A
disagreement between the two is F15. A session that was never launched
confined has no mapping, so its session-bound grants do not join; its grants
could not be card-rooted anyway. Amends FDR 0001.

## A confined worker never blocks on a permission prompt (decided 2026-10-20, operator)

No operator sits at a confined unit's tty (F8a), so a question the agent asks is
never answered. The first real confined worker sat 30+ minutes on a Claude Code
permission prompt for a read outside its allow list, with `--enable-auto-mode`
in its argv. Claude's docs say auto mode pauses and resumes prompting after
repeated classifier blocks, and start it with `--permission-mode auto`; whether
`--enable-auto-mode` alone starts a session in it is unverified.

A confined spawn therefore gets `--permission-mode dontAsk` in its provider
args (`spawn.SpliceDontAskMode`): per the docs every call that would otherwise
prompt is auto-denied, the session never waits for input, and allow rules and
PreToolUse-hook approvals still run. The splice replaces any permission mode
and `--enable-auto-mode` the spawn-entry carried, and fails closed: an entry
with no `--` separator or a non-claude provider is refused before anything is
created. The agent sees refusals and adapts, so its allow list and hook
approvals must cover what it legitimately needs. Unconfined spawns are
unchanged. Unverified: that the installed claude (2.1.301) accepts `dontAsk`;
the docs read were current.

The spawn SessionStart hook leaves a trace in `<worktree>/.spinclass/
hook-trace.log` (the worktree is rw-bound and host-readable; the lifecycle log
in the state dir is not shared with the unit). It records that the hook ran and
where the hello stopped. The trace showed the hello failing in a unit with
`mkdir .../.local/state/spinclass: read-only file system`: the hello is a file
in the state dir, which a unit can neither create nor share with the driver.
A confined spawn therefore gets a per-session handshake directory
(`spawnhandshake.SessionDir`, a `session-<hash>` subdirectory of the shared
one), created by the frontend, listed in the launch spec's `binds_rw` and named
in the unit's env as `SPINCLASS_HANDSHAKE_DIR`. The worker's hello goes there and
the driver reads the same path; only that one directory is writable, so a unit
cannot write a sibling's hello. Unconfined spawns use the shared directory as
before.

## Closing a confined session stops its unit (2026-10-20)

`close-child-session` reaped a confined worker but left its agent unit running
with claude inside, its worktree removed from under it: nothing in the close
path touched the unit. `close.RunResolved` now calls `confinement.StopUnit`
before any teardown. It reads the unit name from the record clown wrote
(refusing one that is not a `spinclass-agent-*.service` whose cgroup ends in that
name, so a bad record cannot make it stop another unit), runs `systemctl --user
stop`, waits up to 15s for the unit's cgroup to empty, then removes the record
and the launch spec. Clown's frontend exits and removes the agent dir on its own
once the unit is gone (confirmed by the operator on `smart-hemlock`). A
worktree with no record was never confined and is untouched. A unit that will
not stop fails the close before anything is torn down; `--force` warns and
closes anyway. `sc clean`'s removal of abandoned worktrees does not stop units
yet.

The check also treats the record's `main_pid` as advisory: one outside the
unit's cgroup is ignored and noted in F1, since every process in the cgroup
must share one mount namespace for the path facts to hold anyway.

## A confined spawn is not always-ask (spinclass#382; two switches, 2026-10-20)

`spawn-session` was unconditionally always-ask (#151). That floor yields for a
spawn that will launch a unit, since confinement bounds what the worker can
reach and the worker never blocks on a prompt. This changes the tool's
permission class only; it does not touch confinement or the preflight, which
still refuses a launch that cannot be confined.

### Two switches

Both live in `[confinement]`, trusted layers only (global and strict-ancestor
sweatfiles; repo and worktree layers are ignored, `LayerAboveRepo` unchanged),
and are plain scalars: the **last trusted layer that sets each key wins**. They
are not sticky-on, so a nearer trusted layer can turn either off.

- `enabled` is the feature switch: confinement is available or not.
- `default-spawn` is, with the feature on, whether a spawn is confined unless it
  says otherwise. **Unset means true**, so setting `enabled = true` alone does
  what `launch-unit` did. `enabled = true, default-spawn = false` makes
  confinement opt-in per spawn, so a session can be allow-listed to spawn safe
  (confined) children while the default stays unconfined.
- `launch-unit` is a **deprecated alias** for `enabled = true` and
  `default-spawn = true` (`sc validate` warns). It stays sticky-on. Composition
  is per key: `launch-unit` supplies a key's value only when no trusted layer
  sets that key, so `launch-unit` alone behaves exactly as before,
  `launch-unit = true` plus `default-spawn = false` is enabled and opt-in, and
  `launch-unit = true` plus `enabled = false` is off.

### The decision

`spawn.resolveConfined` is the one decision, shared by `LaunchDetached` and the
permission layer's mirror `spawn.WillConfine`. With T the target repo's policy
(the `repo` argument, or the driver's own repo when omitted) and D the driver's
context policy:

    confined = (T.enabled || D.enabled) &&
               (request == confined ||
                (request != unconfined && (T.enabled && T.default-spawn ||
                                           D.enabled && D.default-spawn)))

The "driver context OR target" rule is kept for the default, so a confined
driver context makes its children confined. `spawn-session` and `sc spawn` take
two optional bool parameters: `confined` (request confinement; narrowing, always
honoured when the feature is on, an error when it is off) and `unconfined` (opt
out of the default). Passing both is an error. `unconfined` also opts out of a
default that came from the driver's context; it is gated by the ask below, not
by the sweatfile.

### Permission

`perms.AlwaysAsk` (shared by the PreToolUse hook and `RunCheck`) judges the
invocation:

1. `unconfined` passed (anything but absent, null or a real `false`: a non-bool
   fails closed) always asks, with a reason naming the opt-out. This is the
   operator's rule that the request needs the user unless the session is
   blessed to run unconfined children. **Until blessing exists (stage 2)** the
   ask is unconditional; the blessing, a card-rooted grant in the FDR 0034
   model, is not built or stubbed here.
2. Otherwise a spawn that resolves to confined is allowed (the hook returns
   `allow`). `perms.SpawnConfined` is the seam the CLI installs as
   `spawn.WillConfine`; any doubt (unresolvable target, unreadable hierarchy,
   non-string `repo`, non-bool `confined`, a request the launch would refuse
   such as `confined` with the feature off) keeps the ask.
3. Everything else asks.

**The CLI opt-out (2026-10-22).** That floor lives on the PreToolUse path, so
it covers only the `spawn-session` MCP tool. `sc spawn --unconfined` never meets
it: a recipe running `spinclass spawn --unconfined` with stdin `/dev/null` spawned
an unconfined worker with no prompt (nikulin canary, spinclass 8997df5). The
CLI therefore asks itself (`confirmUnconfinedSpawn`,
`cmd/spinclass/spawn_unconfined_gate.go`), before anything is resolved or
created. It refuses unless stdin and stdout are both terminals ("--unconfined
needs a human at a terminal"), then prompts on stderr and requires the operator
to type the word `unconfined` (a `y` or an empty line declines). It appends
every attempt (refused, declined, confirmed) as one JSON line to
`$XDG_STATE_HOME/spinclass/spawn-unconfined.log` (0600, in a 0700 directory),
and refuses when that log cannot be written. Under the same uid this is a speed
bump, not a boundary: a program that allocates a pty and types the word passes
it, and anything that can run `sc` could equally edit the sweatfile or exec
clown directly. What it stops is a paved recipe or a pipe opting out silently.
A CLI spawn that is unconfined only because no policy confines it is not gated;
it is the operator's ordinary spawn.

Fan-out stays bounded: `confinedPreflight` refuses a confined spawn from inside
an agent unit, so a confined worker's own spawns are unconfined and still ask.
`RunCheck` (perms tiers) still never auto-approves spawn; the allow comes from
the hook.

### Sweatfile self-edits

Separately, `[permissions] deny-sweatfile-edits` (trusted layers, last wins,
off by default) turns the hook's ask on a Write/Edit of a sweatfile into a
deny. It is kept small and isolated (`internal/hooks/sweatfile_guard.go`)
because the intended end state is all session IO served by a spinclass plugin
that enforces permissions at its own junction, which replaces it.

## A session closing itself (spinclass#389, 2026-10-20)

`close-this-session` lets a session end itself. The unit cannot stop itself
inside its own tool call, so it is two halves:

- **The tool**, run inside the session, checks and then only records a
  `CloseRequest` in the session's state; it removes nothing and returns "end
  your turn". Authority is a scope, `[permissions] allow-self-close` in a
  trusted layer (off by default; a repo or worktree layer is ignored), not a
  handle: the session closes only itself, so FDR 0032 D12 (a session only ever
  removes its own handle) is untouched. It is refused while a merge/check job
  runs, on unmerged commits, uncommitted changes or a non-empty stash shelf
  (`close.Blocker`, the read-only half of what `RunResolved` checks), and when
  the caller is the only accepted holder of another live session. It accepts no
  `force`, not even `false`.
- **The runner**, `spinclass close-self-run [target]`, is plumbing for the
  frontend, which must run it from OUTSIDE the agent unit once the session has
  finished its last turn. It re-checks every condition above (the worktree may
  have changed), then calls `close.RunResolved` (unit stop, credential revoke,
  nix gc, worktree and branch) and wakes the other holders with `shutdown`. A
  refusal clears the request (no retry loop), wakes the holders with `failed`,
  and leaves the session whole. With no target it uses `CLOWN_SESSION_ID`; with
  no session or no request it does nothing, so the frontend may run it on every
  idle transition.

The trigger is clown's frontend and is not built here; until it exists the
request just sits in the state and an operator can run the runner by hand.

## The principal across `sc resume` (FDR 0034 item 17)

The check and the grant chain bind on the worker's principal (its
`CLOWN_SESSION_ID`). Verified 2026-10-19 from code, not end to end: it does
**not** survive `sc resume` today. `SessionExecutor.Attach` strips
`CLOWN_SESSION_ID`/`CLAUDE_SESSION_ID` and passes no `--resume`; per clown
(clown/cool-fir/koko: `cmd/clown/main.go` `resolveSessionIdentity`,
`cmd/clown/resume_hint.go` `decideClaudeSession`) clown then mints a fresh key
unless relaunched with `--resume <id>`/`--session-id <id>`, and the SessionStart
hook replaces the recorded principal once the old process is dead. A resumed
worker's principal-bound grants and the links it granted stop resolving (fail
closed, wrongly).

Fix (implemented; keeps "spinclass never sets `CLOWN_SESSION_ID`"; clown
confirmed `clown -- --resume <uuid>` keeps channel key == id): a `{principal}`
placeholder in `[session-entry].resume`, substituted with the recorded
UUID-shaped principal, e.g. `clown -- --resume {principal}`. With profile rights
enforced, `sc resume` refuses a session that has a recorded principal when the
entry carries no `{principal}`; otherwise it warns.

## Hard requirements on the unit

- **A private pid namespace** (F10). Mandatory, not a tuning option: without it
  `/proc/<pid>/root` walks out of every mount namespace.
- **Every live agent confined** (F11). Turning enforcement on means every live
  session on the host is restarted under confinement first;
  `sc confinement-check` names the offending sessions.

## Deferred

- **A per-session uid** (FDR 0032 D17 named a dynamic uid). The stronger end
  state; it needs a system-manager unit and therefore a root-installed helper,
  since the user manager has no `DynamicUser=` and `PrivateUsers=` maps the
  operator to itself. This piece separates by mount, pid and devpts namespace.
- **Per-run OS users** (a real uid per session rather than a dynamic uid).
- **juggler as the platform** (FDR 0032 D7 v2: spinclass as the sole launcher
  of unit sessions). This record's spec is meant to be what a juggler-launched
  unit consumes too.
- **Budgets** (FDR 0032 D19) as unit CPU/memory limits.
- **Keystroke observation above `claude`** (FDR 0032 D10): the check does not
  probe it; it is clown's and posh's to establish, and F8 covers the writable
  half.
- **The pre-merge hook's scope** (#25) runs in the frontend's serve today; it
  evaluates agent-written `flake.nix`/`justfile` and is the remaining
  evaluation escape. Confining it is a follow-up.

## Open questions

1. *(Resolved 2026-10-19: serve runs in the frontend; see "Vantage".)*
2. The git common dir: `git commit` from a worktree writes objects and refs in
   the shared `.git`. Binding it rw lets the unit write other branches' refs and
   `config` (`core.hooksPath`). Options: rw bind of `objects/` and this
   worktree's `worktrees/<name>/` only, refs through a broker, or FDR 0032
   D18's broker for all git writes.
3. Only for the diagnostic `--inside` vantage: F2 from inside a private pid
   namespace sees peer pid 0, so it needs piggy-agent to report its own cgroup
   over a self-report extension. Not blocking; the authorizing check runs in
   the frontend.
4. F8a's final form waits on posh's `POSH_CONFINE` refusal message.
5. *(Decided 2026-10-19, coordinator.)* **Cross-session MCP.** Over loopback
   any unit could call another session's serve and spawn as it (serve takes the
   principal from its own environment): grant theft. Closed by authentication,
   not a private netns (the agent needs outbound HTTPS): a confined session's
   serve listens on a per-session unix socket only that unit receives, and
   clown's bridge authenticates the peer by `SO_PEERCRED` + cgroup against the
   bound unit. Loopback remains only for unconfined sessions. F12.
6. *(Decided 2026-10-19, coordinator.)* **Abstract unix sockets** stay
   reachable in the host netns. Accepted residual with compensating controls:
   X11 needs the cookie from the operator's X authority file and Wayland needs
   the socket under the operator's runtime dir, both hidden from the unit (F6).
   **A host running an X server without cookie authentication is out of
   policy.**
7. `.mcp.json` and `.claude-plugin/plugin.json` `mcpServers` would start a
   second `spinclass serve` as claude's child, inside the unit (koko).
   Resolved for `.mcp.json`: `internal/claude/mcp.go` no longer mirrors the
   spinclass server there (it writes only `[[mcps]]` entries and dodder).
   Resolved for the plugin manifest: clown's compile replaces a plugin's
   `.claude-plugin/plugin.json` `mcpServers` with its `clown.json` servers, and
   spinclass's `clown.json` marks serve `"scope": "frontend"`, so a confined
   clown runs it in the frontend (older clowns ignore the key).
8. The ringmaster monitor and `troupe receive` are claude's children today, so
   agent-side; the unit binds `$XDG_STATE_HOME/ringmaster` and
   `$XDG_RUNTIME_DIR/ringmaster` until clown moves them to the frontend (FDR 0032
   D7 wants troupe's connection owner there).

## Limitations

- The check is not the boundary (see "What the check is not").
- Root sessions (operator-started, not spawned) are confined only if clown
  launches them confined; this record's switch governs spawns only.
- **F11 finds agents by `comm`**, which a process can rename
  (`prctl(PR_SET_NAME)`), and an npm-installed claude runs as `node`. It
  detects honest unconfined sessions; it is no defence against a hostile
  unconfined process, which already has the operator's uid and needs none.
  Accepted until every agent is a unit (coordinator, 2026-10-19). Since
  2026-10-20 the session half is by unit (a record must name a running unit)
  and the name list is a deprecated fallback that also takes each record's
  `harness`; it goes when clown records every agent it launches, confined or
  not (docs/plans/2026-10-20-agent-census-by-unit.md).
- **F2 identifies the agent by `SO_PEERCRED`**, which names the listener: a
  socket-activated piggy-agent resolves to the user manager.
- **Session state is worktree-local** (`<worktree>/.spinclass/state.json`,
  FDR 0001), so a confined agent can rewrite its own session's recorded PID
  and principal. Resolved for what authority depends on: see "The
  principal-to-session mapping". The live-session list still comes from that
  state, but it only widens what F11 and F12 demand.
- Every path probe is check-then-use against a live unit; a unit that changes
  between the probe and the spawn is caught at the next spawn, not this one.
- Reviewed 2026-10-19 by an independent code-review pass; its findings (record
  in the worktree, unchecked runtime dir and user manager, sub-cgroups, symlink
  resolution against the frontend's root, devpts instance collision, tmpfs home
  on impermanence hosts, procfs of the host pid namespace, the non-Linux stub
  passing vacuously) are fixed above. The D17 security review (spinclass#339)
  is still required for `accepted`.

## More Information

- FDR 0032 D7, D10, D17; FDR 0034 "Where the card root is real", Phase 2
  item 6; spinclass#338, #339; clown#244, clown#83 (`--tent`);
  piggy-agent(1) SCOPES; piggy FDR 0006.
