---
status: proposed
date: 2026-10-19
promotion-criteria: |
  proposed -> experimental: phase 1 lands (the `profile` parameter, the
  `spawn-profile` default, fail-closed validation through the clown library,
  the `PROFILE` column, the profile and its digest in session state) and a
  worker spawned with a saved `--cheap-context` profile boots with the trimmed
  tool catalog and sends its hello.
  experimental -> testing: phase 2 lands (grant records, chain verification at
  spawn, card-signed root and widening grants) after piggy FDR 0006 phase (a)
  is pinned, and the "Where the card root is real" findings are recorded.
  testing -> accepted: no tuning lever has moved for two weeks; the unattended
  section is either specified or explicitly split into its own record.
---

# Spawn with a clown profile

> **Proposed.** Child of FDR 0006 (spawn) and FDR 0032 (principals, handles,
> provenance); parent epic EPIC 0001. Drafted 2026-10-19 from the operator's
> user stories (Google Doc "spawn-session profile names: user stories") and a
> review by circus/eager-holly/clarabell. Where this record and FDR 0032
> disagree, FDR 0032 wins and the disagreement is a bug to reconcile there.
>
> **Implementation status (2026-10-19):** phase 1 and the clown library wiring
> are on `master` (7b74d7c, 292309e); phase 2 (grant records, the card root) is
> not started. See "Implementation status" below for exactly what runs and what
> has not been exercised.

## Problem Statement

A worker spawned by `spawn-session` boots with whatever the sweatfile's
`spawn-entry` says plus an optional Claude `model` alias. There is no way to
give a worker a defined **role**: a named bundle of provider settings and, most
importantly, a trimmed MCP tool catalog, so a narrow task does not spend
context on roughly 170 tools it will never use. clown already owns such bundles
(named profiles, with a saved `--cheap-context` selection that replays without
a TTY); spinclass cannot name one, cannot tell whether one exists, and cannot
decide who may pick one. A typo today surfaces as a five-minute hello timeout,
and a coordinator agent could in principle move a worker onto a more permissive
profile than it was itself given.

## Interface

### Spawning

- **`spawn-session` gains an optional `profile` string** (and `sc spawn
  --profile <name>`), threaded through `Launch`/`LaunchDetached` to
  `renderSpawn`. `renderSpawn` places `--profile <name>` among clown's own
  flags, before the literal `--` separator (clown flags must precede it),
  replacing any `--profile` already in the entry.
- **A sweatfile default**: `[session-entry] spawn-profile = "cheap"`, settable
  at any layer, scalar override like the other `session-entry` fields. An
  explicit `profile` parameter overrides it per call.
- **Fail closed, before anything is created.** Before `shop.Create` (so no
  worktree is littered), spinclass resolves the profile through clown's
  library (below). An unknown profile, a profile the spawner does not hold
  (see Authority), a profile that drops a server the harness needs, or a
  library/registry version skew, each refuse the spawn with a message naming
  the missing thing. A `spawn-profile` default naming a profile the spawner
  does not hold also fails; there is no silent fallback to "no profile".
  Asking is allowed only in an interactive `sc spawn`; the MCP tool has no TTY.
  (Today's code refuses an unknown profile, a skewed registry and a dropped
  required server; the "does not hold" refusal needs the profile right and is
  phase 2.)
- **Hello is unchanged.** A worker that cannot start its hello is still a
  timeout; the checks above exist so that this is no longer the way a bad
  profile is discovered.

### Visibility and record

- `sc list` gains a `PROFILE` column. The profile name, the **content digest
  of the resolved profile**, and the authorizing grant digest (phase 2) are
  recorded in `session.State`, with the #147 carry-forward in `shop`,
  `resurrect` and the resume path.
- The FDR 0032 provenance view (`sc whoami`, the chain view) shows the same
  three fields.
- `sc resume` and `sc resurrect` re-apply the recorded profile. If the
  profile's current digest differs from the recorded one, they **refuse and ask
  an authority to continue** (operator decision, 2026-10-19). **Not implemented
  yet:** today the digest is recorded and carried forward, resume does not
  re-apply the profile, and nothing compares digests on resume.

### The clown library

clown publishes `code.linenisgreat.com/clown/pkg/profilelookup` (clown#264,
landed on clown `master` at 22f304a), which spinclass links in-process through a
`go.nix` bridge to clown's `go-pkgs` output. spinclass never reads
`profiles.toml` and never shells out to the `clown` CLI for the check. It is
clown's package and clown's design; this record states what spinclass relies on:

- `Resolve(name, Options{RegistryPath})` returns the resolved profile (name,
  provider, backend, model, source builtin or user), its **canonical JSON**, a
  **SHA-256 hex digest** of that JSON, and the registry stamp. A missing
  profile wraps `ErrNotFound`. Resolution uses the same merge as the binary.
- `Selection{Selective, Kept, Excluded}`: `Selective == false` means no saved
  cheap-context selection (keeps everything), which is distinct from a selection
  that kept nothing. `Missing(required)` and `CrippledTools(required)` report a
  required server dropped, or kept with some of its tools excluded.
- `RequiredServers()` returns `clown-builtin-jobs/ringmaster` and
  `clown-builtin-jobs/troupe`: the servers a worker needs to reach its driver.
  clown defines them, so spinclass carries no name list (this answers the
  earlier open question of who defines the required set).
- `*SkewError` for a registry that is unstamped, stamped under another
  protocol, or holding fields this build does not know. `ProtocolVersion` is 1.
- The canonical bytes include env values and the URL verbatim (only the token
  is withheld). They are **sensitive**: spinclass never puts them in an error or
  a log, only the digest.

**The digest spinclass records is a markl-id**, not the raw hex: spinclass
renders the library's SHA-256 as `clown-profile-digest-v1@sha256-<blech32>`
using piggy's public `pkgs/markl`. The purpose name is spinclass's choice (an
unregistered purpose is legal in markl); clown or piggy may want to own it.
clown does not emit a markl-id because markl-id is not a clown dependency; it
offered to take that dependency if wanted, and the operator declined.

**Version skew.** The operator chose the **registry version stamp** over a
`clown --version` probe. clown stamps `[registry] clown_version, protocol` into
the registry on every save; the library compares **protocol**, not clown
versions, since a linked library has no reliable clown version of its own. A
refusal names the registry's last-writing clown version and both protocol
numbers. A spawn with no profile is unaffected. Consequences accepted:
- An existing registry is unstamped until the next `clown profile` add, edit or
  remove, so the first lookup returns a `SkewError` with a restamp hint (no
  restamp command exists yet).
- A registry read from the legacy `~/.config/juggler` path is never stamped, so
  it always skews.
- Builtin profiles are compiled into the linked clown, so a pinned builtin
  digest detects drift only when spinclass is re-linked, not when the clown on
  `PATH` changes.

**Packaging.** The operator did not want a sub-flake. clown stayed one repo and
now publishes `go-pkgs` (via `mkGoPkgs`); spinclass takes `clown` as a root flake
input whose heavy inputs are redirected with `follows` to stand-ins (a scratch
lock test showed 27 nodes today, 50 for a plain input, 28 with the `follows`
block). Only clown's Go source is consumed, so nothing else may be built from
that input. `circus update` sorts clown before spinclass automatically once
spinclass declares clown as a direct input (verified by circus), and there is no
cycle. The old FDR 0010 reason for declining a clown input (its closure) no
longer applies for that reason.

## Authority: the profile right and the card root

This section builds on FDR 0032 D2, D8, D11, D13 and D15. It adds one right and
one record kind and changes nothing else in that model.

### The right

`spinclass:profile:<name>` is an **ambient right** in the FDR 0032 D13 sense:
it says which profiles a principal may spawn with. It is inherited at spawn as
a **subset** of the parent's ambient set, drop-only, and raised only by a D11
escalation. Passing the ability onward is governed by the existing `grant`
object right, not by a new mechanism: a worker spawned with `cheap` holds
use-of-`cheap` and not `grant`, unless the driver says so.

A worker's ambient set therefore never exceeds its parent's, and a worker
asking for a profile it does not hold is refused at spawn with a message that
names what it does hold and who holds the profile it asked for (the shape
`close-child-session` already uses).

**This is the first object-or-ambient right spinclass enforces.** FDR 0032
records rights today and enforces none (spinclass#355). Enforcing this one
changes the character of handles and is called out here on purpose.

### The root: the operator's card

Least privilege, with the operator's YubiKey as the root principal that grants
rights. The root session's profile set is part of its ambient set (D13), blessed
by the card at root start. Whether that blessing rides the same touch as the
root certificate (D2) or is a second touch is **open** (Open question 12);
FDR 0032 D4 says a certificate never carries rights, which favours a second
touch, and that is this record's recommendation. The human therefore holds
every profile they have blessed, with no further step per spawn; everything
below the root can only shrink that set.

- **Narrowing needs no card.** A grant that gives a child a subset of what the
  grantor holds (same or fewer profiles, same or earlier expiry, no `grant`
  unless the grantor has it) is signed by the grantor's own software key and is
  **derivable from the parent record**: a verifier recomputes the subset
  relation and needs no further act. This is FDR 0032 D11's "rights only shrink
  without the card".
- **Widening needs the card.** Any grant that gives a principal a profile it
  does not hold is a D11 **escalation**: an escalation request by the requester
  and an escalation grant by the card, PIN and touch, with the request shown
  verbatim on the PIN prompt by piggy's operator-act extension.
- **The default is not a grant.** `spawn-profile` selects among profiles the
  spawner already holds; it never confers one.

### The grant record

One record kind, `profile-grant`, in the FDR 0032 D8 grammar (`kind`,
`subject`, `issuer`, `prev` as a parent set, `body`, `sig`, markl-addressed).
The body is canonical text, one `key: value` per line. This record is
**spinclass's request record**: piggy defines no subject, scope or expiry fields
(the signed text is opaque bytes to it, piggy FDR 0006 "What the signed subject
is"), so the profile set, digests, grantee, session and expiry all live here, and
the card signs a short text that **names this record by markl-id**:

    spinclass profile-grant v1
    grantor: <principal, or "operator" for a card act>
    grantee: <principal>
    session: <repo>/<branch> of the grantee, or "-" for a service
    profiles: cheap=<digest> review=<digest>
    rights: spinclass:profile:cheap spinclass:profile:review
    onward: none | grant
    expires: 2026-10-20T14:00:00Z
    nonce: <random, 16 bytes hex>
    prev: <digest of the parent grant, or "-" for a card root>

Lines are in this fixed order; a verifier rejects reordering. Each profile is
bound to the **digest of its resolved definition at grant time**, so the grant
names a snapshot and not a name that can drift (a profile edited later does not
silently widen an existing grant).

**What the card signs.** Per piggy RFC 0012, the operator-act text is at most
4096 bytes of UTF-8 with no control characters other than newline and tab, is
shown verbatim on the PIN prompt, and must be readable whole. The classes
constrain it:

- `piggy-certificate-v1` (a card-signed certificate or enrollment): the text MUST
  name the **certified public key** and the **certificate request record** by
  markl-id. For a standing delegation the certified key is the delegate's.
- `piggy-escalation-v1` (a one-off widening): the text MUST name the **escalation
  request record** by markl-id and state the right and its bound **in words**
  (for example, "allow principal P to spawn with profile review until
  14:00Z").

So the prompt shows words and an id, and the id binds those words to the full
record above. Both signatures verify offline; no card is needed to verify.

One namespace per act, so a verifier can tell what kind of authority a signature
carries and a signature made for one purpose cannot be replayed as another:

| Act | sshsig namespace | Signer |
|---|---|---|
| Root blessing of the profile set (with the root certificate) | `piggy-certificate-v1` | card |
| Widening a principal beyond its set | `piggy-escalation-v1` | card |
| Standing delegation to a service principal | `piggy-certificate-v1` | card |
| Narrowing a grant to a child | none: the grantor's software key, D8 grammar | grantor |

Per piggy/free-larch (relayed by circus/eager-holly): a standing delegation that
certifies a delegate's key fits the certificate class and a one-off widening
fits the escalation class. The assignment of the **root blessing** to the
certificate class is spinclass's proposal, not piggy's statement.

### Verification and revocation

At spawn, spinclass walks `prev` from the grant presented to a card-signed
record, checking at every edge: the signature, that the child grant is a subset
of its parent (profiles, expiry, `onward`), and that no record is expired. Only
then does it proceed. A walk that fails is a refusal naming the broken edge.

piggy ships **no verifier** (piggy/free-larch, relayed). The card record is an
armored sshsig written by `piggy operator-sign --namespace NS`, verified with
`ssh-keygen -Y verify -n NS -f allowed_signers -I <principal> -s sig < text`.
The allowed-signers entry is the card's **9C** public key (not 9A, which is
cached and can never be an operator-act key). That PIN-always, touch-always
policy is proven by the 9C attestation chain from `piggy card export-attestation`
and checking that chain at enrollment is PAPI's job, not spinclass's. The
spawn-time verifier therefore only checks the sshsig and **pins the namespace**
for the act it expects. Verification needs no card.

Revocation is tied to **handle lifetime**: a grant is bound to the grantee's
session, so it ends when the grantee's handle is released or its session is
closed, with no separate revocation record in the common case. An explicit
revocation (a card-signed record naming the grant digest) exists only for
standing delegations, which outlive a session.

### Unattended spawns

Spawns that no human starts (a webhook, a roustabout graph) have no one to touch
a card. The mechanism is FDR 0032 D2's **service principal**: a long-lived key
enrolled by the card ahead of time, with D15's one policy record at enrollment
naming the ambient rights its trees may hold. For profiles, that policy record
is a standing `profile-grant` under `piggy-certificate-v1` whose `grantee` is
the service principal and whose `profiles` and `expires` bound every tree it
starts. The service is an issuer, not a root: each tree it starts gets a fresh
root principal under it, and narrowing below that point follows the same rule as
above.

**This section is deliberately incomplete.** Who may hold a standing
certificate, the lifetime bound, and what the holder tier must be depend on
piggy FDR 0006 phase (a) and on D2's open questions about service enrollment
lifetime. It is specified here only as far as the shape above; the rest is
marked open until the certificate semantics are pinned.

### Where the card root is real

The card root is only real if a worker cannot reach or paint the operator's
prompt. piggy-agent(1) SCOPES states the conditions and says piggy relies on
them without providing them; the session manager and terminal owner arrange
them (piggy FDR 0006, "Scopes: frontend and agent"). Per piggy/free-larch
(relayed by circus/eager-holly), for detached workers:

1. piggy-agent and its askpass run as a user service (a systemd user unit or
   launchd), **never in the cgroup or process tree of claude or any worker**,
   and the agent's cgroup is verified not to sit under a worker scope.
2. A worker has **no write access to the operator's prompt surface**: no
   inherited fd to the operator's tty or pty, no writable posh attach, no
   `WAYLAND_DISPLAY`/`DISPLAY` socket, no session D-Bus.
3. Nothing in a worker scope can **observe operator keystrokes** (FDR 0032 D10).

A worker **may** reach the agent socket. That is allowed: every act raises a
prompt the operator can cancel, so the guarantee rests on the prompt surface and
not on socket access. (An earlier draft of this record wrongly required hiding
the socket; it does not.)

**Where detached spinclass workers stand today.** Stated from what was read, with
the unverified parts marked:

| Line | Status today | Evidence |
|---|---|---|
| 1. Agent outside every worker cgroup | **unverified.** A worker inherits its launcher's cgroup: `startDetached` uses `setsid`, which starts a new session but does not move the process between cgroups. This session's own cgroup is the user's login-session scope (`user.slice/user-<uid>.slice/session-<n>.scope`), not a dedicated agent scope. Where piggy-agent runs relative to it was not determined | `internal/spawn/spawn.go` `startDetached`; `/proc/self/cgroup` |
| 2a. No inherited tty/pty fd | **holds for stdio.** The worker is started with stdin `/dev/null` and stdout/stderr on a log file | `startDetached` |
| 2b. No display socket | **not enforced.** `workerEnv` builds the worker environment from `os.Environ()` and strips only `CLOWN_SESSION_ID` and `CLAUDE_SESSION_ID`, so display variables pass through when set (unset in this session's environment). Removing a variable is hygiene, not a boundary: the sockets stay reachable by path under the same uid | `internal/session/env.go`; `env` of this session |
| 2c. No session D-Bus | **fails.** `DBUS_SESSION_BUS_ADDRESS` is inherited (`unix:path=/run/user/<uid>/bus` in this session), and that socket is reachable by path regardless of the variable | `env` of this session |
| 2d. No writable posh attach | **unverified.** posh's attach authorization (posh#224) is not shipped | not examined |
| 3. No keystroke observation | **unverified.** FDR 0032 already lists whether keystrokes are observable above `claude` as a fact to verify | FDR 0032 Limitations |

So **the card root is not real for detached workers today**: line 2c fails
outright and none of 1, 2d or 3 is established. Making it real needs a boundary,
not env hygiene: the agent scope as a unit with the D-Bus and display sockets
absent (FDR 0032 D17, spinclass#338, clown#244, with posh's observer
authorization). Until then the lattice is policy the tools enforce and not a
sandbox (FDR 0032 D17), and phase 2's signing and verification must not be
described as containment.

What spinclass can do without that boundary: stop inheriting the display and
D-Bus variables into workers (hygiene, with the limit stated above). This record
**proposes**, and the operator has not decided, that spinclass also refuses to
treat any grant as card-rooted until a confinement check passes.

## Examples

    # spawn-session repo=just-us profile=cheap brief="…"
    session_key: just-us/calm-cypress
    profile: cheap (digest 4f1c…)  # recorded in state, shown in sc list
    worker will message <principal> via chat

    # a worker running under cheap tries to launch with review
    refused: no right spinclass:profile:review
      you hold: spinclass:profile:cheap
      held by:  <driver principal>

    # sc resume just-us/calm-cypress, after cheap was edited
    warning: profile cheap changed since spawn (4f1c… -> 9ad2…)

## Limitations

- Profiles are about role and tool scope, not backend choice. A different
  provider or backend per worker is a non-goal (story 2, rejected).
- Subagents using the same framework through juggler are out of scope.
- Phase 1 has no cryptography: it is naming, validation and record. The grant
  records and the card root arrive in phase 2 and depend on piggy.
- A cheap selection that removes the harness's own plugin (troupe chat,
  ringmaster) yields a worker that cannot send its hello. The required-server
  check catches this only if the clown library exposes the kept-server set.
- The system-prompt notice that tool categories were disabled is clown's and
  is not built here. It should land before a repo-wide default ships, or every
  default worker is a confused one.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| Grant expiry default | one working day | matches D5's unattended budget order of magnitude | grants routinely expire mid-task |
| Resume on digest mismatch | warn | the worker already loaded the old definition; refusing a resume loses work | a changed profile causes a bad resumed session |
| Required-server set | troupe chat and ringmaster | the two a worker needs for hello and wake | a new harness dependency appears |

## Open questions

1. **Ambient versus object representation of a widening grant. Decided
   (operator, 2026-10-19, relayed by circus/eager-holly): ambient, bound to the
   grantee's session, to start.** The profile ability is an ambient right on the
   grantee and a widening grant ends with the grantee's session. The accepted
   user story's other reading, an object right on the handle to a particular
   child (which ends when that handle is released or the child closes), is
   **kept as a flagged future exploration, not rejected.** A reason to revisit
   it: it would scope a grant to one worker without a count limit.
2. **Root blessing's class. Decided (operator, same date): two touches.** One
   card act certifies the root (identity); a second, separate act blesses the
   initial profile set, because FDR 0032 D4 says a certificate binds identity,
   never rights. Per piggy, a standing delegation fits `piggy-certificate-v1`
   and a one-off widening `piggy-escalation-v1`. Still to settle with piggy:
   which class carries the initial profile-set blessing (the second touch is a
   rights act, so it is neither obviously a certificate nor an escalation).
3. **The verifier path. Decided (operator, same date): an in-tree sshsig parser
   over `golang.org/x/crypto/ssh`'s signature verification.** No third-party
   module and no `ssh-keygen` shell-out. The allowed-signers material holds the
   card's 9C public key with the namespace pinned. Still open: who maintains
   that file on a host (PAPI enrolls the key and checks the attestation).
   Hand-written signature framing needs a security review.
4. **Skew signal. Decided (operator, 2026-10-19): the registry-version stamp.**
   clown stamps its version into the registry on every write; the linked
   library compares against the last writer. The `clown --version` probe is
   dropped.
5. **`circus update` ordering. Decided as a requirement (operator): clown must
   sort before spinclass in the re-lock order.** Verified by circus/eager-holly
   (2026-10-19): `circus update` tsorts the direct flake inputs of first-party
   repos, so clown sorts first by construction once spinclass declares it as an
   input, and no circus change is needed. There is no cycle (clown does not
   input spinclass). The conflict with spinclass's old FDR 0010 note (a clown
   input "would drag its whole input closure in") was **resolved** by taking
   the input with a `follows` block that redirects clown's heavy inputs; see
   "Packaging" under The clown library.
6. **Precedence of `model` versus profile. Decided in direction (operator):**
   the permission scheme permits upgrading a model that a profile defines, if
   the spawner has been granted that. So an explicit `model` that differs from
   the profile's is refused unless the spawner holds a model-upgrade right.
   Still open: that right's name and shape (for example
   `spinclass:model:<alias>`), and whether it is granted per profile.
7. **Resume on a changed digest. Decided (operator): refuse, and ask an
   authority to continue.** A resume whose recorded profile digest no longer
   matches the current definition is refused until a principal holding the
   right to re-pin it approves.
8. **Required-server set. Resolved by clown#264:** clown's library defines it
   (`RequiredServers()`), so spinclass carries no list. See "The required-server
   check" below for what it is.
9. **Unattended spawns. Decided (operator, 2026-10-19): any card-enrolled
   service principal may hold a standing certificate, with the lifetime set per
   certificate.** Roustabout graphs still hold no authority of their own and are
   checked against the requester's chain. Still open: the holder tier a
   service's key needs (FDR 0032 D2) and how a graph's requester is identified
   at spawn.
10. **Escalation requests go to a principal, not only a human. Decided
    (operator): always ask a principal that holds the privilege needed to
    escalate, human or agent.** Which principal a given refusal asks, and how
    the request reaches it, is open and is where a handle to a privileged
    coordinator would be used.
11. **Ambient versus object right:** decided, see item 1.
12. **Root blessing:** decided, see item 2.
13. **The verifier:** decided, see item 3. For the record: neither the standard
    library nor `golang.org/x/crypto/ssh` is believed to provide sshsig
    verification (sshsig is a separate wire format, PROTOCOL.sshsig, and the Go
    implementations known to circus/eager-holly are third-party), which is why
    the parser is written in-tree.

## The required-server check

A profile can drop MCP servers. If it drops the plugin that carries the
worker's chat and job-wake tools, the worker starts but can never send its
hello or report back, and the driver waits out a five-minute timeout. The check
asks, before anything is created, "does this profile still keep the servers a
worker needs to talk to its driver, with their tools?", and refuses if not
(a required server dropped, or kept with some of its tools excluded). The set
is clown's: it owns those plugins and returns their names.

## Pinned profiles (operator, 2026-10-19)

The right is pinned to content: `spinclass:profile:<name>` is granted for one
**content digest** of the profile, not for a name. If the profile's digest
changes, that is a **hard error**: the user or an authorized agent must update
the pinned digest. This replaces the earlier idea that a name could drift under
a grant, and it is why a grant record already carries the digest.

This requires clown to render a profile into **canonicalized JSON** and derive
a markl-id from it. That is a requirement on clown's library (clown#264), and
it fixes the digest format that the FDR had left opaque.

## Operator direction on unattended spawns (operator, 2026-10-19)

- **Webhooks** are blessed with a delegated card privilege, so that they can act
  as "fuzzy" human operators: a standing delegation (the service principal
  above) whose bounds the operator sets once.
- **Roustabout graphs** are permitted on the privileges of their requesters: a
  graph holds no authority of its own, and each spawn it makes is checked
  against the grant chain of the principal that requested the graph.

## Implementation status

Landed on `master` on 2026-10-19 (7b74d7c: phase 1; 292309e: the clown library):
- `spawn-session` `profile` parameter and `sc spawn --profile`; `[session-entry]
  spawn-profile` default (scalar override); `--profile` spliced before the `--`.
- Resolution before `shop.Create` through clown's `profilelookup`, with refusals
  for an unknown profile, a skewed or unstamped registry, and a profile that
  drops a required server or excludes its tools. A requested profile that
  cannot resolve creates no worktree.
- `profile`, a markl-id `profile_digest` and `authorizing_grant` in session
  state, carried forward through attach and resurrect; `PROFILE` column in `sc
  list`.
- The detached worker harness no longer inherits `DISPLAY`, `WAYLAND_DISPLAY` or
  `DBUS_SESSION_BUS_ADDRESS` (hygiene, not a boundary; the agent socket is kept).
- `internal/profilegrant`: the pure grant canonical text, narrowing relation
  (chain bound by principal) and chain walk over an injected verifier. Unsigned
  and not called by anything yet.

**Not exercised.** The real library has only been run in tests against an
unstamped temporary registry and fakes; no `spawn-session` call has gone
through it end to end. On the machine where this was developed there is no user
`profiles.toml` at all, so there is no saved cheap-context profile to spawn with
yet.

**Not implemented:** the profile right and its enforcement (a spawner holding
the profile, "a spawn the spawner does not hold is refused"); grant records and
the card root; resume re-applying the recorded profile or refusing on a changed
digest; a way to clear `spawn-profile` once a lower layer sets it; the
model-upgrade right; `sc validate` checks for `spawn-profile`.

## Phase 2 design (2026-10-19)

The operator authorized Phase 2 on 2026-10-19 (decisions above). These are the
design choices made while turning the Authority section into code, each
spinclass's own, to be corrected if piggy or the operator disagrees.

1. **What the card signs.** The signed text is *derived*, never free: it is
   regenerated from the grant record by one function, `ActText(grant, digest)`,
   and the verifier requires the text the card signed to be byte-equal to that
   regeneration. So the prompt the operator read cannot differ from the record
   it authorizes. The text satisfies piggy RFC 0012 (UTF-8, at most 4096 bytes,
   no control characters other than newline and tab): a header line, the
   record's markl-id, the grantee, each profile name with the content digest it
   is pinned to, whether the grantee may pass the rights on, and the expiry, in
   words.
2. **Record digest is real.** A record's digest is the markl-id of the SHA-256
   of its canonical text, under piggy's `piggy-record-digest-v1` purpose, and
   the chain walk checks `Record.Digest` against it. This closes the review
   finding that digests were opaque and never checked against content.
3. **One class for both card acts, for now: `piggy-escalation-v1`.** The root
   blessing of the initial profile set and a later widening are both a human
   granting a rights set, stated in words, to a named principal. The certificate
   class (`piggy-certificate-v1`) requires naming the certified *public key*, and
   per-principal keys do not exist until FDR 0032 slice 1, so the standing
   delegation to a service principal (item 9) waits for them. **piggy confirmed
   this (piggy/free-larch, reading RFC 0012 §2.4):** the escalation class is
   defined by what the act does, granting a right with a bound, and is not
   limited to widening, so a first grant from no rights is the from-zero case;
   it is also the strongest askpass class (GRANTING AUTHORITY banner, the
   operator types ESCALATE). Once FDR 0032 slice 1 lands, standing delegation to
   a service principal's key becomes a certificate act while the profile-set
   grant itself stays an escalation. Because the namespace cannot tell an
   initial grant from a widening, the signed text says so itself: the record
   carries a `widens` field and the act text begins `initial grant` or
   `widening of <markl-id>`.
4. **Narrowing links are unsigned until principals have keys.** A narrowing grant
   is derivable from its parent (subset of profiles with equal digests, no later
   expiry, no wider `onward`, grantor equal to the parent's grantee), so authority
   still comes only from the card-signed root and the subset relation. What is
   missing without per-principal keys is *non-forgeability of the link*: anyone
   able to write spinclass's grant store can add a link. That is the same bar as
   FDR 0032 slice 0 (same-uid state, unguessable, not unforgeable), and it is
   stated rather than hidden. A link verifier that requires an empty signature
   documents this in code; it is replaced when per-principal keys arrive.
5. **The child's principal is unknown at spawn.** The worker's `CLOWN_SESSION_ID`
   is minted fresh when its clown boots, so a spawn-time narrowing grant cannot
   name it. The grant's grantee is therefore the child's **session key**, and
   spinclass records the worker's principal in its session state when its
   SessionStart hook runs (the hook sees `CLOWN_SESSION_ID`). A later link whose
   grantor is a *principal* is matched to a parent whose grantee is a *session
   key* through that recorded mapping. Until the hook has recorded it, the worker
   holds no spawnable profiles.
6. **Enforcement is behind a knob and a confinement check.** Enforcing the
   profile right changes every profile spawn from "refuse only on resolve
   failure" to "refuse unless a chain proves the spawner holds it", and nobody
   holds a chain yet. So: `[profile-rights] enforce` (default false) turns it
   on, and a grant is never treated as *card-rooted* unless the confinement
   check passes (item "Where the card root is real"), which today always
   fails. With enforcement on and the check failing, a spawn that names a
   profile is refused with the reason, so enabling it early cannot silently
   weaken anything. **The switch is a trusted-layer setting**, read like
   `[auth].url-resolver`: only sweatfiles above the repo are honoured, because
   an agent can edit the repo or worktree sweatfile and a repo-layer value must
   never be able to turn enforcement off. (This replaces the earlier plan of a
   `[session-entry] enforce-profile-rights` key.) When enforcement is on and a
   spawn names a profile, spawn authorizes the *spawner* against its grant chain
   before creating anything, records the authorizing grant digest in state, and,
   only when the spawner may pass the right on, mints the child's narrowing link
   (grantee = the child's session key, `onward: none`).
7. **Signing is injected.** The card acts go through an interface with the real
   implementation speaking piggy's `operator-act@piggy` agent extension; it
   stays unexercisable on a real card until piggy#303's acceptance pass and the
   agent-scope boundary (spinclass#338, clown#244, posh) land. piggy is pinned at
   eb98190 for the phase (a) code.
8. **Grant store.** Records are kept as files under spinclass's state directory,
   addressed by digest, with an index from grantee to its current record. The
   system-scoped store of FDR 0032 D14 is the eventual home and is not built.
9. **What `onward` means (found by independent review).** A holder always *uses*
   its own profiles to spawn workers; that is checked with "does the effective
   grant hold this profile", not by creating a link. A *link* exists only to
   give a child its own right to spawn or pass on, so it requires the parent's
   `onward: grant`. A parent with `onward: none` therefore has **no** children
   in the chain, and the card-signed text "may pass these on: no" is true:
   a worker spawned by such a holder runs under the profile but holds nothing
   to spawn with. (The earlier rule only stopped a child from *gaining*
   `grant`, which let a `none` holder still mint any number of `none` children
   with arbitrary grantees.)
10. **The text the operator reads is an allowlist, not "no control
    characters".** Every identity-like value (grantor, grantee, session, widens,
    prev, nonce, profile names and digests) is limited to `[A-Za-z0-9._:/@+-]`.
    The review showed that "no control characters" still let a grantee contain
    spaces and parentheses (so two different grantee/session pairs rendered the
    same `allow:` line) and Unicode line breaks or bidi overrides (so a prompt
    could be made to show a forged `until:` line). The canonical text is also
    required to be the exact re-render of what was parsed, which closes a
    fractional-second expiry the operator could not see. The act text shows the
    grantor (`from:`), which was otherwise visible only through the digest.
11. **A widening must widen.** The widened record must be a card-signed root,
    unexpired, for the same grantee, and a subset by profile name of the
    widening (digests may differ, since a widening may re-pin a profile).
12. **Resume keeps the profile.** `sc resume` on a session spawned with a
    profile splices `--profile <name>` into the resume entry (refusing, fail
    closed, when the entry has no `--`), resolves the recorded profile, and
    refuses when it no longer resolves or its digest differs from the recorded
    one. The operator's decision was "refuse, and ask an authority to continue";
    until the grant machinery can express an agent holding that authority, the
    only authority is the human at a terminal: `--repin-profile` is CLI-only,
    needs an interactive confirmation that shows the old and new digests, and is
    refused with `--no-attach` or without a terminal (found by review: without
    that gate an agent with a shell could rewrite the recorded digest).
13. **`sc profile-grant-bless` is not drivable by an agent.** It is CLI-only (no
    MCP tool), and it also refuses unless stdin and stderr are terminals and
    refuses when `CLOWN_SESSION_ID` or `SPINCLASS_SESSION_ID` is set. The
    environment check is advisory (an agent can unset variables) and the
    terminal check is defence in depth; the real boundary is the prompt surface
    (see "Where the card root is real"). An agent that can reach the card agent's
    socket can always raise a prompt, which RFC 0012 accepts because the operator
    reads the full text and can cancel.
14. **The card's signers file is explicit and owned.** The allowed-signers file
    that anchors trust must name the principal `operator` exactly (a `*`
    principal does not count) and list `piggy-escalation-v1` in an explicit
    `namespaces` option (an entry with no `namespaces` permits every namespace),
    and it must not be group- or world-writable or owned by another user. A
    shared signers file listing a PIN-less software key as `operator` would
    otherwise make that key a valid grant root.
15. **The client checks what the agent signed.** After a card act the client
    parses the returned sshsig and requires its namespace to be the requested
    one, its key to be the key it selected, and its hash to be sha256 or sha512,
    before the record is even verified against the signers file; and when the
    card's public keys are known it selects only an identity in that list, since
    the `PIV_slot_9C` comment prefix is settable by anything that can add a key
    to the agent. The wire framing was checked against golang.org/x/crypto
    v0.50.0 (`Extension` appends the contents raw after the name, so the RFC's
    single outer string is not double-framed), and a raw-connection test pins the
    request bytes independently of x/crypto's own server.
16. **Grant store semantics.** The record digest covers the text only, so the
    store treats a card-signed root as an *upgrade* of an unsigned copy and never
    the reverse, creates files atomically without clobbering, and refuses a
    symlinked store directory. The store holds records; it grants no authority.
17. **Assumption to verify: a worker's principal survives `sc resume`.** FDR 0032
    D1 says the principal survives a resume because the resume reuses the claude
    session id. The hook replaces a differing recorded principal only when the
    recorded process is dead, and `Attach` records the long-lived `sc` process as
    the PID, so if clown did mint a new principal on resume the mapping would keep
    the old one and a resumed session could not be the grantor of a link (it
    fails closed). Unverified. The hook also takes both the working directory and
    the principal from its caller, which any same-uid process can forge; that is
    the same bar as narrowing links being unsigned, and becomes real only when
    links are signed.

## Phasing

Everything that does not depend on piggy lands first; the signing and
verification call sites land after FDR 0006 phase (a) is pinned.

**Phase 1 (no dependency on piggy):**
1. This record and the user-stories doc.
2. The record schema (`profile`, `profile_digest`, `authorizing_grant` fields
   in `session.State`) and the carry-forward.
3. The chain rules as pure functions with tests, over fixture records, no
   signing (subset checking, expiry, prev-walk).
4. The clown library request, filed with clown's session, with the
   resolved-profile-plus-digest and kept-servers requirements.
5. The `profile` parameter, `spawn-profile` default, `renderSpawn` splice,
   `PROFILE` column, fail-closed validation behind an interface so the clown
   library plugs in when it exists.
6. `workerEnv` stops inheriting the display and session D-Bus variables
   (hygiene only; the agent socket is deliberately left, see "Where the card
   root is real").

**Phase 2 (after piggy phase (a) is pinned):** grant record signing through
the operator-act extension, the allowed-signers verifier, the card root
blessing at root start, widening escalation, revocation on handle release, and
the standing certificate for unattended spawns.

## More Information

- FDR 0006 (spawn), FDR 0032 (principals, handles, provenance; D2, D8, D11,
  D13, D15, D17), FDR 0028 (the per-session credential precedent), EPIC 0001.
- piggy: `piggy-agent(1)` OPERATOR ACTS and SCOPES; piggy FDR 0006.
- clown: `cmd/clown/cheapcontext.go` (replay of a saved selection),
  `internal/profile`; the clownfile and profile registry.
- clown (forge `linenisgreat/clown`): #264, the profile lookup library request
  (resolved profile, content digest, kept-server set, registry version stamp);
  #265, the system-prompt notice that tool categories were disabled; #266, an
  allowlist mode for saved cheap-context selections (exclusions are a denylist
  per kept server, so a restricted profile silently gains tools its server adds
  later; an allowlist makes the digest change exactly when the effective scope
  does, and is to be exposed through `pkg/profilelookup`); #176, the picker's
  collapse/expand.
- godyn(7), "cross-module composition", for the library bridge.
- The ringmaster `jobwake` bridge (`go.nix`) as the precedent.
- Review: circus/eager-holly/clarabell, 2026-10-19.
