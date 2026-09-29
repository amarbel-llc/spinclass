---
status: proposed
date: 2026-09-29
promotion-criteria: |
  proposed -> experimental: slice 0 lands (principal split, `holders`,
  grant/release/accept-on-first-use, exit wakes) and closes spinclass#332 and
  spinclass#321. A `spawn-session` from a cwd outside any git repository
  succeeds, and a sibling reaps a handle it was granted.
  experimental -> testing: slice 1 runs on one host end to end: a
  card-blessed root session (one 9C touch) spawns a child whose certificate
  arrives over the troupe exchange, `close-child-session` verifies the chain,
  and `sc whoami` shows the walk to the card. The three companion records
  (troupe, piggy, clown) exist and cite this FDR.
  testing -> accepted: slice 2's tee-signed transcript has run for two weeks
  with no chain gap; a spawned child has acted on a retrieved operator quote
  without re-asking the operator; the security review named in "Security
  posture" has been done and its findings recorded here; at least one
  pre-merge gate has refused on a missing signed attestation (D16); and the
  confinement companion record exists (not necessarily implemented).
---

# Principals, handles, and provenance

> **Proposed** — designed 2026-09-28/29 by the operator with
> spinclass/swift-elder/pennywise, as the umbrella record for a family of
> changes that were being filed as unrelated bugs: spinclass#332 (spawn refuses
> from a non-repo cwd), spinclass#321 (session ownership as a passed
> capability), spinclass#293 (a child trusting its parent's orders), and
> spinclass#220 (parent-mediated permissions). It borrows one thing from each of
> three systems and says so: the **lifecycle vocabulary** of Erlang/OTP
> supervision trees, the **capability discipline** of Unix fd-passing
> (`SCM_RIGHTS`) and Capsicum, and the **trust chain** of PKI, rooted in the
> operator's PIV card.
>
> **This is the parent record.** Every companion record listed under
> "Companion records" is downstream of the decisions here and MUST cite FDR 0032
> as its parent. Where a companion and this record disagree, the disagreement
> is a bug to reconcile here first, not a fork.

## Problem Statement

spinclass has no notion of a **principal**. Two stand-ins carry the load:

- **A location stands in for "who is calling".** `currentSessionKey()` derives
  identity from the cwd: a worktree yields `<repo>/<branch>`, a main checkout
  yields a lazily materialized implicit session (FDR 0014). A cwd outside any
  git repository yields an error, so `spawn-session` and `close-child-session`
  refuse there even when the target repo is explicit (#332). The identity is
  only ever used as an opaque address (the hello rendezvous, the `spawned_by`
  record), neither of which needs a repository. A pid is not derived from a
  process's cwd.
- **A name stands in for "who is allowed".** `authorizeChildReap` authorizes a
  reap iff `child.SpawnedBy == callerKey`. Authority *is* the string equality,
  so there is nothing to pass, delegate, or refcount (#321). Lineage and
  ownership are one field.

Both are the same gap. Fixing #332 narrowly (a `bare/<rand>` fallback key)
would have added a third ad-hoc identity scheme next to `<repo>/<branch>` and
`<repo>/<rand>`. Fixing #321 as an ACL edit on `spawned_by` would have kept
authority ambient. This record names the layers instead.

A third problem sits behind both. A spawned worker gets only its brief.
Nothing tells it which session is its parent, that the operator sanctioned that
parent, or how to verify that a later chat message came from it (#293). troupe
records carry a sender-set `from` with no authentication (troupe#1). So a
worker either trusts every DM or, correctly, treats parent instructions as
untrusted data and stops to ask the operator, which defeats delegation.

## The model

Kernel concepts, mapped onto what exists on this host today and onto the
target architecture (juggler as the backing platform for every agent;
trapeze/clown and troupe as frontends).

| Kernel | Today | Target |
|---|---|---|
| kernel / scheduler | none (every `spinclass serve` is equally privileged) | juggler |
| process, pid | `CLOWN_SESSION_ID`, the claude `--session-id` UUID | the agent principal, minted by juggler |
| process group / session | `SPINCLASS_SESSION_ID` (`<repo>/<branch>` or `<repo>/<rand>`), the group decoration | unchanged |
| credentials (uid) | nothing | the certificate chain to the card |
| capability set (bounding set) | harness allow-lists, perms tiers | the principal's **ambient rights** |
| file descriptor | nothing; `spawned_by` is ambient authority | a **handle** on a session |
| fork / exec | `spawn-session` | unchanged |
| wait / `SIGCHLD` / OTP link | hello handshake only | exit wakes to handle holders |
| tty | the Claude Code TTY under clown | a frontend: clown/trapeze, or an XMPP client via troupe |
| the process's journal | Claude Code's JSONL, not ours | the agent's XMPP archive (MAM) as a signed DAG |

Three layers, each with one job:

1. **Principal (who).** The per-instance identity of the agent, minted by the
   platform that hosts it. Today that is clown's `CLOWN_SESSION_ID`, which
   `spinclass serve` already sees in its environment (verified 2026-09-28 with
   `just debug-session-env-map`) and which is already the JID localpart every
   brief tells a worker to message back. Never derived from cwd; never touches
   git. Named abstractly so the juggler cutover changes no record format.
2. **Certificate (who vouched).** A content-addressed record binding a
   principal to its public key and its issuer, chaining to the operator's
   card. Identity only; rights never live in it.
3. **Rights (what).** Two carriers. **Ambient rights** on the principal: what
   it may do at all, including every execution right. **Object rights** on a
   **handle**: what it may do to one session. Both form a lattice that only
   shrinks without the card.

## Decisions

Numbered in dependency order. Each was put to the operator and accepted on
2026-09-28/29; the alternatives rejected are recorded so they are not
re-litigated.

### D1. The principal is the platform's per-instance key

`currentPrincipal()` returns `CLOWN_SESSION_ID` when set, else a
per-`serve`-process random UUID cached in memory, in the same shape so nothing
downstream can tell them apart. `currentSessionKey()` remains for the
operations that genuinely need a worktree: merge, check, attestation,
description.

Rejected: a spinclass-minted id (a fourth identity chat cannot address); the
session key with a `bare/<rand>` fallback (the conflation being escaped).
Consequence accepted: a principal's lifetime is the harness instance's, and
`clown --resume` preserves it because the resume reuses the claude session id.

### D2. Root of trust: the card, once per human-started tree

The operator's PIV card certifies a **human-started root session's**
principal key exactly once, at session start. Every further link is signed by
a software key certified under that root. A root started with no card or agent
gets an **uncertified** principal: it still works (spawn, reap, chat) but every
verifier treats its whole tree as untrusted data and `sc list` shows it as
`unverified`. There is no silent fallback to a software root.

Rejected: the card signing every principal certificate or every message. Both
need a live card agent deep into a session, exactly where FDR 0028 observed the
forwarded agent dying, and the second adds touch fatigue that pushes toward
cached PINs.

### D3. Keys sit behind the SSH-agent seam; holders improve in tiers

spinclass, troupe and clown never touch private key bytes. They ask an agent
to sign, and signatures are sshsig blobs. The seam is fixed now so the record
grammar and every verifier never change while the holder improves:

- **Tier 1:** a software ed25519 key per principal, loaded into the session's
  agent. Same uid, so the property is only "not in a file the agent reads".
- **Tier 2:** a separate-uid signing service (a piggy-agent `--proxy-only`
  style front as a NixOS system service) that mints keys on request and binds
  each to the requesting session's systemd scope via `SO_PEERCRED` plus the
  peer's cgroup. A session can neither read nor exfiltrate its key nor sign as
  a sibling.
- **Tier 3:** the service holds each intermediate in a **fibby** virtual PIV
  card (piggy's pure-Rust pcsc-lite server), so the key gets PIV semantics and a
  virtual touch policy becomes an operator-approval prompt. fibby's PIV applet
  is unimplemented today (phase 5 of its plan); tier 3 is a dependency on piggy,
  not a prerequisite.

### D4. A certificate binds identity, never rights

One content-addressed record per principal, named by its markl-id digest:
subject principal and public key; issuer (parent principal, or the card for a
root link); the issuer's certificate digest (the merkle edge, absent on a
root); scope (the spinclass session key launched into, informational, absent
for a `~/eng` coordinator); the brief digest for spawned children (what #293
wanted sealed); issued-at; the issuer's signature over the canonical bytes.
Not in it: the worktree path (changes across hosts and on resurrect), and any
right.

### D5. Expiry lives at the root only, inherited transitively

Only the card-signed root link carries a hard expiry, the operator's
"unattended budget" (a sweatfile knob, default on the order of a day). A link
is valid iff its parent is valid, so there is exactly one clock per tree and
**renewal is one card touch at the root**, renewing everything below. A parent
may optionally **cap** a child shorter; the effective expiry is min(own cap,
parent's effective expiry); extending a cap is the parent re-signing that edge
with its software key, local, never reaching the card. Validity is checked at
use time by walking to the root.

Consequence: when a root's link expires, everything under it dies unless a
successor **cross-certifies** the children under its own chain (a new link for
the same key; verification accepts any valid path, so the structure is a DAG).
An unattended fleet whose human root is gone should not run indefinitely.

Rejected: per-link expiry with delegated renewal. It produces the renewal
cascade (a child's extension needing its parent's, and so on up to the card)
and gives every intermediate a way to keep a tree alive without a human.

### D6. Exit signals in v1, spinclass-emitted, no restart policy

A handle without an exit wake is a handle you must poll. spinclass emits one
reason-tagged wake to every accepted holder on the child's `SessionEnd`, on
`close-child-session`, on hello timeout, and from the dead-PID sweep. Reasons
map to OTP's: `normal` (merged and closed), `shutdown` (reaped by a holder),
crash (hello timeout, presence-stale), `killed` (force-reaped). The wake
carries the child's certificate digest so a holder wanting `one_for_one`
re-spawns from the brief itself. Restart strategies, `MaxR`/`MaxT` and
`temporary`/`transient`/`permanent` are holder-side policy, deferred.

Departures from OTP, deliberate: a supervisor's death does not kill children
immediately (a session holds unmerged work; orphans survive to root expiry or
a successor, which is "reparent to init" with the human as init); and OTP has
no capabilities, so the handle layer is the addition on top of it.

### D7. Ownership across repos

- **piggy** owns keys: the agent seam, the card root signature, tiers 2 and 3,
  the markl-id purposes for record digests and sshsig signatures, the 9C slot
  (D11) and its attestation.
- **clown** owns the instance: mints the UUID, launches troupe's connection
  owner and the plugins, passes the parent's JID into a spawned child's
  environment, and puts `claude` and its tree in an **agent scope** distinct
  from a **frontend scope** it and troupe's owner run in. No keypair, no
  certificate.
- **troupe** owns identity and provenance: generates the principal keypair at
  its existing per-session mint and loads it into the agent, performs the root
  bootstrap when no parent is present (one card touch), issues child
  certificates in response to a certificate request, stores and walks chains,
  publishes the certificate in presence, signs and verifies `<prov>` (its
  RFC-0001 already reserves that slot), renews the root. Chosen over clown for
  one decisive reason: troupe **federates**, and a certificate that cannot be
  fetched from another host is useless.
- **spinclass** owns objects and authority: session state, the handle table,
  certificate issuance being *requested* on spawn, `close-child-session`
  verification (ask troupe "is this principal certified and its chain valid",
  then check the handle table), exit-wake emission, `sc list` and `sc whoami`,
  and the root-session bootstrap trigger.
- **ringmaster** owns the event log: exit wakes are wake records; issuance,
  grants, accepts, releases, revocations and escalations are RFC-0019
  non-waking annotation records.

Consequence: the child's certificate cannot be issued at spawn time (its UUID
and key do not exist until it boots). **The hello becomes a troupe certificate
exchange**: the child's troupe sends a certificate request to the parent JID,
the parent's troupe signs it because spinclass registered the spawn intent
(child session key, brief digest, expected parent) before launch, and
`spawn.WaitHello` waits on troupe reporting "child certified". This deletes
`internal/spawnhandshake` in slice 1 and unifies the two handshakes the system
has today. **No troupe means an uncertified session**: D2's degrade, applied
consistently.

### D8. One signed-record grammar, owned by troupe

Every record is one shape: `kind`, `subject`, `issuer`, `prev` (a **set** of
parent digests), `body`, `sig`, markl-addressed. Certificates, grants,
releases, escalations, exit records, operator input and transcript `<prov>` are
instances. One canonicalization, one verifier, one markl purpose for the digest
and one for the signature.

- **The signed thing is never XML.** It is the canonical byte encoding of the
  record struct, carried as an opaque blob inside `<prov>`. The stanza is an
  envelope. XML-DSig canonicalization is where signed-XML systems die.
- **`prev` is a parent set**, so a transcript is a DAG, not a chain: a rewind
  appends a stanza whose `prev` is an earlier one (the abandoned tail stays
  valid history); a fork is two stanzas with the same `prev`; each frontend
  holds a signed **head ref** record. **Compaction** is a `summary` record
  over a range (first and last digest), the one place a merkle *tree* rather
  than a chain is needed. This is dodder's model (content-addressed objects,
  signed inventory lists, history never rewritten); the transcript could be a
  dodder repository per agent.
- **Cost:** one ed25519 signature per record (microseconds); large bodies are
  signed by digest and live in the RFC-0010 spool; verified certificates are
  memoized to root expiry so per-record verification is one signature check
  and one cached lookup; a hundred or two bytes per stanza in MAM. Tier 3 PIV
  signing at 10 to 50 ms per operation is acceptable per stanza, and touch
  never gates a stanza.
- **Deniability is a non-goal.** Signal's double ratchet solves
  confidentiality, forward secrecy and post-compromise security for a
  two-party channel and is deliberately deniable; a provenance transcript needs
  non-repudiation and signatures that stay verifiable forever. Two things are
  borrowed: post-compromise recovery as a *certificate-layer* ratchet (a leaked
  session key is replaced by a fresh certificate and a revocation), and OMEMO
  composing orthogonally later (sign, then encrypt; troupe#1 v2).

### D9. Slices

Each useful on its own, each tightening the previous rather than replacing it.

- **Slice 0 (spinclass only, no cryptography).** See "Interface". Fixes #332
  and #321. Authority is by principal membership, which is already unguessable;
  the record states plainly that this slice has no cryptographic binding.
- **Slice 1 (troupe + piggy tier 1).** Keypair and certificate in troupe's
  mint, card-touch root bootstrap, the hello as a troupe certificate exchange,
  `close-child-session` additionally requiring a valid chain, records move to
  the D8 grammar, `internal/spawnhandshake` deleted.
- **Slice 2 (the tee-signed transcript).** `clown-hook-tee`, already a Stop
  and SessionEnd hook with a persisted byte cursor over the Claude Code JSONL,
  becomes the signer for the transcript we do not own: each Stop produces a
  `transcript-checkpoint` record over the byte range since the last cursor,
  `prev`-linked, signed via the agent, **appended durably locally first**
  (journal or blob) and only then mirrored to MAM as a detached post. A missed
  post is a gap in the mirror, re-postable, never a gap in the chain. The tee
  stays clown's Claude-Code-specific extractor and hands the range to a new
  `troupe transcript append` verb. Rewinds and forks stay cheap because the
  JSONL is append-only with `parentUuid` links (a fact to verify, not assumed).
  This exercises the DAG transcript, the checkpoint grammar and MAM-as-durable
  on the harness actually in use, before any of the transcript is ours.
- **Slice 3 (the subagent platform).** A juggler-backed subagent spawned
  under a certified principal, with its transcript in XMPP as a `<prov>` DAG
  and the parent's instructions signed under the operator chain. The first
  agent whose transcript is ours, one level below Claude Code, which is exactly
  the delegation boundary where prompt injection crosses agents.
- **Slice 4 (frontends).** trapeze/clown on juggler, troupe as an alternate
  frontend. Named, not designed here.

### D10. Operator input is signed at observation; quotes are retrieval

The thing that sees the operator's real input signs it as it arrives, producing
an `operator-input` record per prompt. A **quote** is the retrieval of an
existing signed record by digest, never a new signature over agent-supplied
text: there is no path where an agent chooses the words. Sharing a quote is
passing a digest. The consumer is a `verify-quote` in troupe and #293's
system-prompt fragment for spawned children: an order carrying a verified
quote may be acted on within the quoted words; the parent's framing around it
is still data until slice 1 signs the parent's order under its own chain.

The observation point must be in the **frontend scope** (D7) and the signer
must refuse agent-scope callers, because the natural hook (`UserPromptSubmit`)
runs as a child of `claude`, in the same tree as the agent's tool calls, and an
injected agent can invoke it with a forged payload. Whether clown or posh can
observe keystrokes *above* `claude` is a fact to verify; if the only
observation point is the hook, the hook hands the payload to the frontend-scope
signer, which still authenticates the caller by cgroup.

- **Tier 2 and 3:** an operator session key, blessed by the card once at root
  start, held by the separate-uid signer with cgroup attribution, fibby as the
  holder when it exists.
- **Tier 1 stand-in, until the separate-uid signer exists:** signing at
  observation is impossible without a touch per prompt, so tier 1 signs on
  request with the 9C key (D11), showing the text at PIN time via the piggy
  askpass helper (a runtime sidecar it renders above the prompt). What you see
  is what you sign. Same record, `assurance: touch`. A child treats both as
  authentic; the record shows which one is unforgeable without infrastructure.

### D11. The rights lattice is monotone; 9C is the single attested escalation slot

Rights only shrink without the card: a holder may release a handle, drop
rights from one it keeps, cap a child shorter, or pass a handle carrying a
**subset** of what it holds (Capsicum's `cap_rights_limit`, pledge's "promises
can only be dropped"). Such records are signed by the principal's own software
key for the audit trail, silently.

Any step upward is an **escalation request** (a record by the requester naming
rights and target) answered by an **escalation grant** (a record by the card
over the request's digest), PIN and touch, request shown at PIN time.

The slot is **9C**, the NIST Digital Signature slot whose defined role is a
deliberate human signature, enrolled by papi with PIN-always and touch-always,
with its **F9 attestation certificate** published alongside the public key as
the trust anchor so a verifier can check the key was generated on that YubiKey
and cannot have left it. 9A stays cached, for ssh: papi enrolls it with PIN
`once` and touch `cached`, so a 9A signature inside the window needs no human
and 9A can never be an operator-act key. D2's root certificate, D10's tier-1
quote fallback and escalation all sign with 9C: one deliberate-act key, one
anchor. A card and slot cleanup plus a papi/piggy UX pass for the 9C defaults
is a dependency of tier 1, owned by piggy.

The v1 escalation set: force-reap of a child holding unintegrated work (today
the always-ask flag), root TTL renewal (D5), and granting a right the granter
does not hold. **Spawn is not escalation**: passing a subset to a new principal
is monotone. The #151 always-ask prompt stays as a harness-level speed bump for
cost, not an authority boundary; in tier 2 and above the touch can replace the
prompt for the three escalations.

### D12. Handles pass like `SCM_RIGHTS`

- **Grant is a message.** `grant-session-handle` produces a signed grant
  record and sends it to the recipient as a chat message carrying the digest.
  The sender keeps its copy: delegate is the default, transfer is grant then
  release.
- **Accept is first use.** The handle sits in the recipient's `pending` set,
  conferring no authority and no exit wakes, until the recipient first
  exercises it. `recvmsg` semantics: rights in transit are inert, unclaimed
  ones drop with the message. #321's "gains no authority it didn't ask for"
  holds without a second tool every handoff would forget.
- **Release is local.** Bulk is a list. A child is **orphaned** when no
  accepted holder is alive per the presence index; `sc list` marks it, `sc
  clean` reports it, only the human closes it.

### D13. Rights: five object rights, ambient execution rights, open namespace

Object rights on a handle, v1: `observe` (exit wakes, listing; implied by any
handle), `close`, `grant` (may pass onward; without it the handle is
non-transferable), `instruct` (the child treats this holder's messages as
orders within its brief; the bit a parent would not pass to a janitor
sibling), `cap` (D5). `merge` and `check` also exist as object rights, meaning
"land or gate *that* session from outside", off by default, and require the
matching ambient right: a handle never lets you do to another what you could
not do to yourself. `force` is not a right you hold; it is a D11 escalation.

Spawning mints the spawner all five. A grant defaults to `observe,close`
unless rights are named, so the lazy handoff is the conservative one.

**Ambient rights** on the principal are what it may do at all, granted at
spawn as a subset of the parent's, drop-only, raised only by escalation. Every
execution right lives here, because merge and check run the pre-merge hook,
which is "execute arbitrary code on this host", the same class as
`chix:develop-run`, `just-us:run`, `moxy:load-moxin`, `mcp:load`. A research
worker spawned with none of them physically cannot land or run code, whatever
its brief says. The root session's ambient set is the operator's policy,
blessed once by the card.

Rights are **namespaced strings, an open set** (`spinclass:close`,
`chix:develop-run`); the enforcing tool owns its names and declares them in its
manifest, the way moxins declare `[perms: …]` today. Verifiers only check
subset. The enforcement point already exists: `internal/perms` and the
PreToolUse hook gain one check, "does the caller's ambient set contain the
right this tool declares". The sweatfile's `claude-allow` and perms tiers stop
being the authority and become the *initial* ambient set the card blesses for
a root: permissions and capabilities become one lattice with one verifier, and
"always-ask" becomes "not in your ambient set, request escalation".

### D14. Storage: a system-scoped madder store, the journal as index

Records live in a **system-scoped** madder store written only by the
system-scoped signer service and read-only to the session uid, so an agent can
read every record but cannot tamper with one: the store is the integrity
boundary for records, as the tier-2 signer is for keys, and they share one
service account. Not FDR 0003's per-worktree store, which is the wrong scope
for identities that outlive worktrees. Content-addressed by markl-id, so a
record fetched from MAM or another host verifies identically. The ringmaster
journal is the index (one RFC-0019 annotation per lifecycle event). Session
state carries `holders` as a **materialized view** of accepted grant digests,
rebuildable from the journal, so a stale or hand-edited state file is a cache
bug, not an authority bug.

### D15. Remote operator acts

Renewal and escalation need the card, which is plugged into the workstation.
Three mechanisms, same records, an `assurance` field naming which:

- **Pre-authorization windows, first.** One touch signs a policy record
  ("auto-renew this tree until 06:00", "coordinator X may `force` its children
  for 6h"); troupe acts within the window without a touch. `sudo`'s timestamp,
  card-signed.
- **Enrolled operator devices.** The card signs once an enrollment blessing
  another identity of the operator's with an assurance level: the bare XMPP
  account over the s2s mesh (`account`, available now); the phone's OMEMO
  device identity key (`device`, when troupe#1 v2 lands; OMEMO authenticates
  the sender device, so no client-side signing is needed); an NFC YubiKey on
  the phone (`hardware`, a later app).
- **Per-operation minimum assurance.** Root renewal at `account`; `force` and
  handle-right escalation at `device`; raising a root's ambient set beyond the
  operator's policy at `hardware` only.

Later surfaces, flagged only: XEP-0050 ad-hoc commands as the admin UI in
Cheogram (the slidge-style pattern circus FDR 0019 already names); a Cheogram
fork with NFC YubiKey signing; a KeePassXC-Android fork speaking piggy pass
over NFC.

### D16. Gates become mechanical: attestations as signed records

FDR 0007's pre-merge attestation is self-reported prose: the merging agent
asserts it invoked each skill, and `nothing-but-the-truth` is "strict on
presence, lenient on content". With D8 and D13 in place a gate can demand
evidence instead of a claim. An **attestation is a signed record** whose issuer
is the principal that performed the step, `prev`-linked to the transcript
checkpoint (slice 2) or subagent transcript (slice 3) in which it happened. A
gate then checks, mechanically: was the required skill run by a **distinct
subagent principal** whose certificate chains to this session (so the merging
agent cannot attest for itself); did a required tool actually run (its
provider's signed execution record, the same `<kind>` the moxy manifest
declares its rights under); does the attested diff digest match the pinned
merge sha. `merge-this-session` refuses on a missing or unverifiable record, not
on missing prose. FDR 0031's exemption predicates stay as the policy layer on
top: they decide *which* records a merge base requires, and the D13 ambient
right `spinclass:merge` decides who may ask at all.

This is the same mechanism as D10's quotes (a claim is a retrieval of a record
someone else signed) applied to workflow rather than speech. It is specified
here, implemented no earlier than slice 2, and named as a revision target for
FDR 0007 and FDR 0031.

### D17. Security posture, stated plainly

The lattice governs the **sanctioned tool surface**: a principal cannot use a
spinclass, moxy or troupe tool for an operation outside its ambient set, cannot
act on a session without a handle, and cannot forge who said what. That
defeats confused-deputy misuse and injection-driven relaying through the
tools, which is the realistic threat with LLM agents.

It does **not** govern the uid. A `folio_write` to a shell rc, a git hook, a
`flake.nix` the pre-merge hook will evaluate, or anything under `~/.config` is
an escape from every ambient right at once, and a read-only tool with an
exploitable parser is the same escape with more steps. Until the agent scope
has a real boundary, the lattice is a policy the agent's tools enforce, not a
sandbox the agent lives in. **A security review is required before any right
here is treated as a containment guarantee**, and its findings are to be
recorded in this section. The write-tool escape is known and unaddressed by
this record.

D16's mechanical gates inherit this limit: a signed execution record proves
the tool ran under a principal, not that the principal's uid did nothing else.

**Where the boundary attaches: session confinement.** The agent scope (D7)
is the seam. That scope becomes a unit with `ProtectHome`, the worktree
bind-mounted in, a dynamic uid, the tool sockets passed in, clown's existing
`--tent` container as one realization, juggler as the eventual launcher; the
tier-2 signer already refuses callers outside the frontend scope. This is the
end vision and it needs coordination across spinclass (what the unit must
see), clown (launching it) and juggler (owning it). It is its own companion
record, not a footnote here.

## Interface (slice 0)

The only slice this record specifies at code level. Everything else is
specified by contract in "Companion records".

- **`currentPrincipal()`** as in D1. `currentSessionKey()` unchanged for
  merge, check, attestation, description.
- **`session.State` gains** `spawned_by_principal` (the authority link) and
  `holders` (a list of principals; slice 1 turns it into grant digests) and
  `pending_handles`. `spawned_by` keeps recording the driver's *session key*
  when there is one, purely for the human-readable `sc list` column, empty for
  a `~/eng` driver. All three new fields get the #147 carry-forward in `shop`,
  `resurrect` and the resume path.
- **`authorizeChildReap`** checks `holders` contains the caller's principal.
  Children spawned before this change, carrying only `spawned_by`, are treated
  as having an implicit holder equal to that session key, so nothing becomes
  unreapable.
- **Hello** keys on principals on both sides: spawn writes
  `spawned_by_principal` into the child's state, the child's SessionStart hook
  sends to it, the driver waits on its own principal. The "message me back at"
  line becomes the principal, which is the JID localpart, so it becomes more
  correct for chat, not less. The filesystem handshake file survives slice 0
  and is deleted in slice 1.
- **`grant-session-handle child=… to=<principal> [rights=…]`** and
  **`release-session-handle child=…`**, MCP and `sc` twins, always-ask,
  variadic in `child`. Rights are recorded but not enforced until D13's
  enforcement lands. Accept-on-first-use via `pending_handles`.
- **`list-handles`** (held, pending) and **`sc whoami`** (principal, session
  key, holders; grows the chain view in slice 1).
- **Exit wakes** from D6's four points, through the existing clown emit path.
- **`sc list`** gains `HOLDERS` (count, `orphan` when zero live) and a short
  principal column. `TRUST` arrives with slice 1.
- **Refusals name the missing thing:** "no handle to X (spawned by P; ask P to
  grant `close`)", never a bare "not authorized".
- **Tests:** `TestCurrentSessionKeyNoSessionStillErrors` and
  `TestHandleSpawnSessionNoDriverKey` flip (they pin the #332 defect); plus a
  non-repo-cwd spawn and close-child end to end, and a grant-then-reap by a
  sibling.

Rollback: slice 0 is additive on state and keeps `spawned_by`; removing the
principal fallback restores the #332 refusal exactly.

## Examples

A coordinator at `~/eng` (not a git repository) spawns and later reaps a
worker in a sibling repo:

    # spawn-session repo=just-us brief="…"
    session_key: just-us/calm-cypress
    worktree_path: ~/eng/repos/just-us/.worktrees/calm-cypress
    worker will message 4d56b43b-1b45-430d-9ed6-e3f2dc05ffe2 via chat

    # close-child-session child=just-us/calm-cypress
    ok 1 - close just-us/calm-cypress (holder 4d56b43b…, spawned_by_principal)

Handing the worker to a janitor sibling before exiting:

    # grant-session-handle child=just-us/calm-cypress to=9c2e…  rights=observe,close
    ok 1 - grant just-us/calm-cypress -> 9c2e… (pending until first use)
    # release-session-handle child=just-us/calm-cypress
    ok 1 - release just-us/calm-cypress (holders: 9c2e… pending)

A day in the life, once slice 1 exists: the operator opens a session at
`~/eng`; troupe finds no parent and asks the card once ("bless root session
`swift-elder` on `flic` for 24h", PIN, touch); every prompt typed is signed at
observation; `spawn-session` returns the child's certificate digest in the
wake; forty minutes before root expiry every session in the tree gets one wake
and the operator touches once; a worker needing `force` calls
`request-escalation`, the askpass shows the request, PIN, touch, and the grant
lands as a wake. Away from the keyboard, a Snikket DM "renew" from the enrolled
account renews the tree; a `force` waits for OMEMO.

## Companion records

This FDR is the parent. Each record below is a placeholder to be drafted in
its owning repo's session (research belongs to the owning repo), MUST cite
FDR 0032 as its parent, and MUST satisfy the contract stated for it. A tracking
issue is filed per record.

Every tracking issue carries the label `spinclass-fdr-0032` in its repo (the
label was created in each forge repo for this purpose; circus, still on
GitHub, lacks it until added by hand).

| Repo | Record | Issue | Owns | Contract it must satisfy |
|---|---|---|---|---|
| troupe | RFC (identity, certificates, the signed-record grammar, `<prov>`, the transcript DAG) | troupe#39 | D7 troupe row, D8, D10, D15 | the D8 grammar as normative; keypair at the existing mint; root bootstrap with one 9C touch; certificate request/ack over chat replacing the spinclass hello; presence carries the certificate; `verify-quote`, `transcript append`, enrolled devices and pre-auth windows; "no troupe = uncertified", never a software root |
| piggy | FDR (agent tiers, 9C, fibby as holder) | piggy#297 | D3, D11 | the SSH-agent seam with sshsig; tier 1 software keys; tier 2 separate-uid service with `SO_PEERCRED`+cgroup attribution; tier 3 fibby (phase 5 applet); 9C PIN-always/touch-always enrolled by papi with F9 attestation published; the askpass sidecar; the card/slot cleanup and defaults UX |
| clown | note or RFC (scopes, tee, parent JID) | clown#244 | D7 clown row, slice 2 | agent scope vs frontend scope as transient units; `clown-hook-tee` hands byte ranges to `troupe transcript append`, durable-local-first; parent JID in the child env; whether keystrokes are observable above `claude` |
| clown (juggler) | FDR (subagent platform) | clown#245 | slice 3 | a subagent under a certified principal with a `<prov>` transcript; parent instructions signed under the operator chain. juggler lives in clown (`cmd/juggler`) |
| spinclass + clown + juggler | FDR (session confinement) | spinclass#338 | D17 | the unit shape: `ProtectHome`, bind-mounted worktree, dynamic uid, sockets passed in; `--tent` as a realization; juggler as launcher |
| spinclass | security review | spinclass#339 | D17 | findings recorded in D17; `testing -> accepted` requires it |
| spinclass | revisions to FDR 0007 and FDR 0031 | spinclass#337 | D16 | attestations as signed records from distinct subagent principals; exemption predicates select required record kinds |
| ringmaster | note | ringmaster#25 | D7 ringmaster row | RFC-0019 annotation kinds for issuance, grant, accept, release, revoke, escalation; exit-wake reason tags |
| circus (GitHub) | note | amarbel-llc/circus#255 | D15 | XEP-0050 admin surface (the FDR 0019 addendum); enrolled-device provisioning on the operator's Snikket account |
| papi | change | papi#87 | D11 | 9C enrollment step and attestation publication |
| moxy | note | moxy#443 | D13, D16 | moxins declare the rights they enforce and emit signed execution records in the D8 grammar |
| purse-first | note | purse-first#194 | D13, D16 | manifest and `go-mcp` support for declared rights and execution records |

## Limitations

- **Slice 0 has no cryptographic binding.** Authority is principal membership
  in a same-uid file. It is unguessable, not unforgeable.
- **Same-uid escape** (D16) is unaddressed by any slice here.
- **Certification is asynchronous.** A child is a live but uncertified session
  between boot and the certificate ack; nothing it does before the ack needs a
  certificate.
- **Facts still to verify:** whether the Claude Code JSONL stays append-only
  across rewinds with `parentUuid` links (slice 2 depends on it); whether clown
  or posh can observe keystrokes above `claude` (D10's observation point);
  whether `piggy-agent` accepts an added software identity or tier 1 needs a
  small per-instance agent.
- **Tier 3 is blocked** on fibby's PIV applet.

## Non-goals

Confidentiality (OMEMO later, orthogonal); deniability; containment of the
same uid; restart policy; cross-host handle passing beyond what MAM carries;
any change to #317's implicit-session merge refusal or to #318.

## More Information

- spinclass#332, #321 (and its two design comments: the `SCM_RIGHTS` steer and
  the OTP addendum), #293, #220, #249, #147, #148, #151, #169, #317, #318.
- troupe#1 (signing chat messages), troupe RFC-0001 §11 (the `<prov>`
  reservation), troupe `internal/mint`.
- piggy: `piggy-agent(1)`, `piggy-piv-slots(7)`, `piggy-markl(7)`,
  `markl-id(7)`, `crates/fibby`, papi FDR 0001 (enrollment policies).
- clown RFC-0013/0014 (per-instance identity, the awareness seam),
  `cmd/clown-hook-tee`, clown#83 (`--tent`).
- ringmaster RFC-0016, RFC-0018, RFC-0019.
- circus FDR 0019 (clown sessions over federated XMPP).
- FDR 0006 (spawn), FDR 0014 (implicit sessions), FDR 0016/0017 (the clown
  seam), FDR 0028 (the card-login precedent and the forwarded-agent failure
  mode), FDR 0003 (per-worktree madder store, rejected as the scope here).
- Session: spinclass/swift-elder/pennywise, 2026-09-28/29;
  `just debug-session-env-map` output confirming `spinclass serve` carries
  `CLOWN_SESSION_ID`.

---

:clown: drafted by [Clown](https://code.linenisgreat.com/clown) 0.4.4+f724337
([f7243372](https://code.linenisgreat.com/clown/commit/f72433729bcca63491ed27ded1a5cddb1beee09c))
with the operator, 2026-09-29.
