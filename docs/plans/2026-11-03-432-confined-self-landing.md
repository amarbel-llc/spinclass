# Letting a confined worker land its own session (design note for #432, 2026-11-03)

Design note, no code. A security-policy change: the operator approves the design
before anything is built, and any merge of code holds for their go. Extends
`2026-10-26-unattended-confined-merge-and-delegation.md` section 1 (pieces A-E),
which this note re-checks against master at 7db4137 and does not repeat.

Labels: **R** read in code or docs this session, **V** observed, **I** inferred
or proposed, **U** unverified. Nothing here was run.

Issue read as: spinclass#432, "After the trip: let a confined worker land its own
session safely (hook in a unit, own-session merge, enforced merge right, no
post-merge)". It is the issue behind "enable merge-this-session in confined
sessions".

## 1. What stands in the way today (R)

- `merge-this-session`, `-async`, `check-this-session`, `-async` and
  `nothing-but-the-truth` each call `refuseConfinedServeCaller` first
  (`cmd/spinclass/commands_mcp_only.go`, ten call sites across the serve). It is
  a policy refusal, not a missing mechanism: the merge already runs in the
  frontend serve.
- The caller is classified by this serve's own principal (`CLOWN_SESSION_ID`,
  `confinement.ClassifyServeCaller`, fail closed). The code's own comment says it
  cannot see a process that dials another session's serve over loopback; only
  per-session serve sockets (F12, clown's) close that.
- **The pre-merge hook runs the branch's own code in the frontend.** `PreMergeInDir`
  runs `[hooks].pre-merge` (`just`) in a detached build worktree at the branch
  sha, `direnv exec <session worktree>` wrapped, inside a `systemd-run --user
  --scope` (`internal/bravo/hookrun/hookrun.go`), outside any confinement unit.
  `just` runs the branch's justfile; `direnv exec` loads a devshell from the
  session worktree's `.envrc` and `flake.nix`. That is the escape: letting a
  confined worker start it runs code it wrote with the operator's authority.
- **The same exposure exists today when the operator runs `sc merge` on a
  confined branch** (the issue says so; the only guard is the operator reading
  the diff). So piece A below is not only a precondition for self-landing: it
  closes an existing hole.
- **Rights.** `merge` and `check` are in the handle vocabulary
  (`session.HandleRightNames`) and the spawner holds every right, but I found no
  use site that enforces either (only close, grant and restart are enforced,
  #429). A merge "right" is therefore a name today, not a check.
- **Post-merge** targets also run in the frontend; a changed `[[post-merge]]`
  already needs the operator's typed digest (1cb83c5). `merge-this-session`
  takes a `targets` list, so `[]` runs none.
- **The attestation is self-written** (#301): any non-empty reasoning passes.
- **spinclass#400's fix (c2a432f, 7db4137)** made the worktree's `.claude/`,
  `.mcp.json`, `.envrc` and `CLAUDE.md` read-only in new units. It helps here in
  one way: the hook's `direnv exec` devshell used a worker-writable `.envrc`; for
  a session spawned after the fix a worker can no longer rewrite that file after
  spawn. It does not help with `justfile`, `flake.nix`, hooks or any tracked
  script the gate reaches, which stay worker code. Old sessions keep their old
  spec and get none of it.

## 2. What "safely" has to mean

Four properties, each separately checkable:

- **S1, no code outside the unit.** Nothing the worker wrote runs with the
  frontend's authority or credentials. The gate (pre-merge hook, and `check`)
  runs in a unit no wider than the worker's own.
- **S2, own session only.** The caller can merge or check only the session whose
  serve it is, never a `target`, never a child.
- **S3, no landing it does not own.** See section 3: this is the real decision.
- **S4, no deploy.** A confined merge runs no `[[post-merge]]` target.

What it does **not** give, and the operator should hear plainly: the gate's
verdict is the exit status of the branch's own `just`. A worker that controls
the justfile can make the gate pass, in a unit or not. A green gate from a
self-landing confined worker is a statement by the worker, the same as the
self-written attestation. Safety here means containment, not quality.

## 3. The decision: what does "land" mean for a confined worker?

A merge rebases onto the default branch and pushes to `origin/<default>` with the
session credential (R: `internal/alfa/landing`; the merge lands on the default
remote's default branch). The default branch is a branch the worker does not own.
Landing there puts worker-written code into the tree every other session is cut
from, whose justfile and flake the operator and other sessions then run, and
which post-merge deploys read.

| Option | What the worker can do | Cost | Verdict |
|---|---|---|---|
| **L1, self-land on the default branch** | Gate in a unit, then push to master. | Needs A, B, C, D (section 5); worker code reaches master with only the worker's attestation behind it. | Only if the operator accepts that master takes unreviewed worker code. |
| **L2, publish for landing** (recommended first) | Run `check` (gate in a unit) on its own branch; push its own branch (grit already allows own refs); report ready. A holder with the `merge` right on that child (the coordinator, the operator) lands it. | Needs A and an enforced `merge` right on the holder's side (C). No self-merge switch, no change to S3. | Closes the existing `sc merge` exposure too and keeps landing with a reader. |
| L3, land on a branch the worker owns | Merge into a per-session integration branch. | A new landing target concept. | Not proposed; adds machinery for no new safety over L2. |

Recommendation: build A first and use it for **every** confined branch (operator-run
and coordinator-run merges included), then C, then offer L1 only behind a trusted,
off-by-default switch (section 4) if the operator wants self-landing.

## 4. The merge right: who holds it and how it is enforced

- **Holder-landed (L2): an enforced `merge` handle right.** The spawner already
  holds every right (R). Enforcement means `merge-this-session` and the CLI
  `sc merge <target>` check, for a target that is not the caller's own session,
  that the caller's principal holds `merge` on it (the way close, grant and
  restart are checked, #429 family). Default grants do not carry it
  (`DefaultHandleRights = observe,close`, R). This closes "by name only".
- **Self-landed (L1): a scope, not a handle**, mirroring `allow-self-close` (#389,
  R): a sweatfile `[permissions] allow-self-merge`, trusted layers only (a repo or
  worktree value is ignored), off when unset, never forces, refused while a merge
  or check job runs or the tree is dirty. The grant to a confined session is
  therefore the operator's sweatfile, which the unit cannot write (the worktree
  sweatfile is not a layer, R). A handle is the wrong object here for the reason
  #389 gave: the session acts on itself.
- **Enforced where.** In the serve handler before any parameter is read, after
  `serveCallerConfined` classifies the caller. The confined-caller refusal stays
  the default; the switch relaxes it for `merge-this-session*` and
  `check-this-session*` only, and only for the serve's own session. `target` and
  `branch`-style parameters are rejected for a confined caller, not ignored (S2).
- **Not delegable (unchanged):** force anything, unconfined spawn, widening grants,
  accepting a `[[post-merge]]` digest.

## 5. Where the gate runs: the hook in a unit (piece A)

Facts (R) are in section 1. Proposed shape (I):

- **A transient unit for the gate**, launched by clown from a spec derived from
  the worker's by narrowing: the build worktree bound read-write and nothing else
  of the session's state, the same private network and egress allowlist as the
  worker, no credentials, no sockets, no spinclass serve. The nix store and daemon
  the devshell needs are the one thing the worker's own unit already gets; the gate
  must not get more. The hook's `direnv exec` and `just` run inside it; the
  frontend reads back the exit status and the log.
- **The frontend keeps the credentialed steps**: fetch, rebase, the landing push,
  record keeping. Only the user-code step (`just` / `check`) moves into the unit.
  Rebase and `merge --ff-only` stay frontend git on a read-only view of the result.
- **Narrowing, not widening**: the gate unit is built by a pure function from the
  worker's spec and may only remove authority (the same shape as clown's twin-unit
  idea in the structural-moxy notes); spinclass probes the running gate unit the
  way F9-F17 probe the agent unit (so the probe set grows by one fact family).
- **Cancellation and the watchdog** (`hookrun` already has inactivity timeout and
  scope kill, R) must act on the gate unit, and the unit must die with the frontend
  (clown's deadman pattern, R for the agent unit, U for a second unit).
- **Dependencies on clown**: a launcher entry point for a non-pty, non-interactive
  gate unit with a result file; the capability listing; its teardown. This is
  clown's lane and I have not read its code for it (U); it must go to the owning
  session before any estimate is believed.

## 6. What a confined merge may and may not do

| May | May not |
|---|---|
| Merge or check **its own session** (S2) | Take a `target` or `branch` parameter; merge or check a child or sibling |
| Run the gate in a unit (S1) | Run the gate in the frontend |
| Land on the default remote's default branch only under the L1 switch | Land anywhere else; push other refs through the merge |
| Pass `targets: []` (forced) | Run any `[[post-merge]]` target; change `[[post-merge]]` (digest rule stands) |
| Record its attestation | Be admitted by an exemption predicate it can satisfy itself (FDR 0031) |

## 7. Needs from clown, and open items

- Gate-unit launcher and result channel (section 5); the frontend serve already
  exists, so nothing new is needed for the call path (R).
- A stable per-session serve socket for F12 so a process cannot dial another
  session's serve (R: clown's, already built per the F12 comment; a confined
  caller classification still depends on it).
- Attestation: #301/#337 (distinct principals) is the honest fix for a
  self-written attestation; until then L1 admits a merge on the worker's own word.
- Unverified (U): clown's ability to launch the gate unit from a derived spec; the
  cost and flakiness of a nix devshell inside a private-network unit; whether the
  gate's `just` needs anything the worker unit lacks (the nix daemon socket, a
  binary cache) and whether adding it widens the unit.

## 8. Order, if approved

1. **Design check with clown** (gate unit launcher, narrowing function) before any
   code. No estimate until clown answers.
2. **A**: gate in a unit, used for every confined branch, including `sc merge` by
   the operator. Test-first; a live confined canary round.
3. **C**: enforce `merge` and `check` on non-self targets. Test-first.
4. **D**: confined merge forces `targets: []`, refuses a non-empty list.
5. **B plus the `allow-self-merge` switch**: only if the operator wants L1.

## 9. Operator rulings (2026-11-03) and what they change

1. **L1 is wanted:** "land" means `origin/<default>`.
2. **A self-written attestation is acceptable for now**, no different from
   unconfined sessions; it will later be externally certified and possibly
   dynamic with the parent's input; constraints tighten incrementally. The
   operator also states that workers have `justfile` read/exec only, not write,
   unless additionally granted. **Check needed (R/U):** I found nothing in
   spinclass that enforces this: `buildUnitSpec` binds the worktree read-write
   as a whole, no spec bind makes `justfile` read-only, and a grep of non-test
   `internal/` code finds no `justfile` handling outside `apply`. If it is
   enforced it is by the harness permission rules (policy, which the worker
   could not edit after #400 for new sessions) or by clown, neither of which I
   read. Until it is a bind or a probe, section 2's "a worker that controls the
   justfile can make the gate pass" stays true for `flake.nix`, scripts the
   justfile calls, and any tracked file the recipes read. Proposal: make
   `justfile` (and `flake.nix`, `flake.lock`) read-only binds in the unit under
   the same trusted opt-in as #400, with an F17-style probe; that turns the
   operator's statement into something checked.
3. **Operator-run merge:** `sc merge <session-id>` is run from the operator's
   tty in the frontend, not in the session, and a merge made from inside an
   attached session needs no attestation. So question 3 reduces to: the gate
   still executes the branch's code in the frontend on the operator's authority
   (the operator reading the diff is the guard). Whether to move that gate into
   a unit is the operator's call; it is not required for self-landing.
4. **Merge right: both.** A handle right is a virtual sweatfile at the top of
   the chain. Design consequence (I): the right is stored as a grant record the
   serve consults as the highest-priority trusted layer, so
   `allow-self-merge` (sweatfile) and a `merge` handle right on a child
   resolve through one function and a nearer layer can only narrow. Which wins
   when they disagree follows the existing trusted-layer rule (the layer above
   the repo, last wins), with the handle as the top layer.

Remaining open questions: whether `justfile` read-only is enforced anywhere I did
not read (and if not, the read-only-bind proposal above), and clown's side of the
gate unit.

## 10. Questions that were asked

The four original questions, kept for the record:

1. L2 first (confined workers publish, a holder lands), L1 later or never?
2. If L1: is it acceptable that worker code reaches the default branch behind a
   self-written attestation, or does it wait for #301/#337?
3. Should the gate unit be required for operator-run `sc merge` of confined
   branches from day one (closes the existing exposure, changes the operator's
   workflow, may fail on hosts without the launcher)?
4. `allow-self-merge` as a sweatfile scope (my proposal), or a handle right?
