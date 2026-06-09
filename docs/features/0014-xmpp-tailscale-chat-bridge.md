---
status: proposed
date: 2026-06-09
promotion-criteria: |
  Working `spinclass xmpp-bridge` behind an unset-by-default `[xmpp]`
  sweatfile block. `experimental` gate: a human XMPP client on the
  tailnet joins the main MUC and sees one occupant per live session with
  a correct nick, and a groupchat message the human sends reaches ≥2
  live sessions (via the chatroom broadcast + clown job-watch push) —
  observed end-to-end. `testing` gate: session state transitions
  (active → detached → abandoned) drive the corresponding occupant
  presence/status changes in the human's client within the probe
  interval, and a session's own `chat-send` surfaces in the MUC under
  that session's nick — both verified over ~1 week of real fleet use
  with no occupant-desync or missed-broadcast reports.
---

# XMPP-over-tailscale bridge for cross-session chat

> **Draft.** Design proposed, not yet implemented. Sections describe
> intended behavior.

## Problem Statement

Cross-session chat (FDR 0009, issue #16) gives sessions a shared,
file-backed chatroom; clown's job-watch (FDR 0010) pushes messages to
the *agents*. But there is no **human-facing** surface: a person who
wants to watch the fleet — see which sessions are live, what each is
doing, and broadcast a directive to all of them — has nothing but
`sc list` and a shell. They cannot do it from a phone, cannot get
presence at a glance, and cannot type one line that lands in every
session.

XMPP already solves exactly this shape: a multi-user chat (MUC) room
with per-occupant presence, a mature mobile/desktop client ecosystem
(Conversations, Gajim, Monal, …), offline delivery, and push. This
feature **binds the existing chatroom to an XMPP service reachable over
tailscale**, modelling each spinclass session as a MUC occupant with a
nick and live presence, so a human XMPP client can watch sessions and
broadcast to all of them. It is complementary to clown job-watch: that
remains the agent-facing receive path; XMPP is the human-facing
receive **and** send path. The file-backed chatroom stays the system of
record — XMPP is a bridge, not a new store.

## Interface

### Configuration (`[xmpp]`, unset = dormant)

A new sweatfile block, scalar/override merge like `[session]`/`[hooks]`.
Unset (the default) means the bridge never runs and spinclass is not
coupled to any XMPP server — exactly the dormancy posture of the
presence probe (FDR 0012), `[[remotes]]` (FDR 0011), and the clown emit.

```toml
[xmpp]
# Component connection to the tailnet XMPP server (recommended; see
# "Identity model" for the multi-client alternative).
component-jid    = "sessions.host.tailnet.ts.net"
server           = "host.tailnet.ts.net:5347"   # MagicDNS name : component port
secret-file      = "~/.config/spinclass/xmpp-secret"  # never committed
muc              = "fleet@conference.host.tailnet.ts.net"  # the "main channel"
# Presence is derived from FDR-0012 session presence; this bounds how
# often the bridge re-derives and re-pushes occupant presence.
presence-interval = "10s"
```

- **`secret-file`** holds the XEP-0114 component shared secret (or, in
  multi-client mode, a provisioning credential). It is read at startup,
  is user-local, and is never written to the repo — same handling as
  `~/.config/circus/*.toml` tokens in clown.
- `sc validate` checks the block is well-formed (parseable JIDs, a
  resolvable `secret-file`, a positive `presence-interval`) but does not
  network.

### The bridge daemon (`spinclass xmpp-bridge`)

One **host-global** long-lived process (not per-session): it watches the
whole session index, not a single worktree. Started out-of-band — a
LaunchAgent / systemd user unit / `[[start-commands]]`-style launcher —
and supervised like any daemon. It is the single point that holds the
XMPP connection; sessions never speak XMPP themselves.

On start it:

1. Reads `[xmpp]` from the global sweatfile (`~/.config/spinclass/`),
   resolves the secret, and connects to the XMPP service.
2. Joins/creates the configured **MUC** — this is "the main channel."
3. Reconciles MUC occupants against the live session index on a
   `presence-interval` tick, and tails the chatroom for messages to
   relay outward. Both directions below.

### Identity model — one occupant per session

**Recommended: an XMPP component (XEP-0114).** The bridge connects once
as a server component owning a subdomain (`sessions.<host>`), and
puppeteers one virtual JID per session — `<branch>@sessions.<host>` —
joining the MUC from each. A component is the canonical XMPP *gateway*
shape (the same mechanism IRC/Slack transports use): one TCP
connection, N independent occupants with independent presence, no
per-session account provisioning. Cost: the XMPP server must be
configured to trust the component (a `Component` block + shared secret
in prosody/ejabberd).

- **Nick** = the session branch. On collision across repos the nick
  disambiguates to the full session key `<repo>/<branch>` (the same
  string `sc list` and `$SPINCLASS_SESSION_ID` use).
- A reserved occupant (`fleet-bridge`, the bridge's own JID) represents
  the bridge itself and is the author of system notices.

**Alternative (zero server-config): multi-client puppets.** Where the
XMPP server cannot host a component, the bridge instead opens one
ordinary **client** connection per live session, each authenticating as
a per-session account (in-band registration, XEP-0077, or a pool of
pre-provisioned accounts) and joining the MUC with its own nick. Same
external behavior, N connections instead of one, and it works against
any stock XMPP server. The component vs multi-client choice is the
**one server-coupling decision** an operator makes; the bridge supports
both behind the same `[xmpp]` config (the presence of `component-jid`
selects component mode). See Open Questions.

### Presence + status ← session state (FDR 0012)

The bridge maps each session's state to its occupant's XMPP presence on
every `presence-interval` tick (push only on change). The data source is
FDR 0012's derived `Presence` (`Clients`, `Agent ∈ {"","running",
"busy"}`, `AgentActivity`) layered over the FDR-0001 session state — so
this feature **consumes** FDR 0012 rather than re-deriving liveness:

| session state | agent (FDR 0012) | XMPP `<show>` | status text |
|---|---|---|---|
| `active` | `busy` | `chat` (or available) | `busy · <description>` |
| `active` / `running-detached` | `running` | available | `<description>` |
| `inactive` (PID dead, tree exists) | — | `away` / `xa` | `detached` |
| `abandoned` (worktree gone) | — | *unavailable* (occupant leaves) | — |

Dirty working tree and attached-client count (FDR 0012 `Clients`) append
flags to the status line (e.g. `· ✎ dirty · 1 client`). Where FDR 0012
is not yet implemented/configured, presence degrades to the coarse
three-state liveness only (agent/client segments omitted) — never an
error.

### Message bridging (both directions)

The existing `internal/chat` store is the pivot; nothing about it
changes.

- **Human → all sessions (the core ask).** A *groupchat* message the
  human sends into the MUC is ingested by the bridge and written to the
  chatroom as a **broadcast** (`to = "*"`, `from = "xmpp/<muc-nick>"`,
  subject = first line, body = full text), then `chat.EmitWake` fires —
  so clown's job-watch pushes it to **every** live session exactly as a
  peer `chat-send` broadcast would. "Messages sent to the channel are
  broadcast to all of the sessions."
- **Human → one session (DM, natural extension).** A MUC *private*
  message to a session's occupant becomes a directed chatroom write
  (`to = <that session key>`), so only that session wakes.
- **Session → MUC.** The bridge tails the chatroom (the watch loop the
  store already supports) and relays each new message **out through that
  session's puppet occupant**, so it appears in the room under the
  session's own nick — preserving sender identity. Directed
  (`to=<key>`) messages relay as a MUC private message to the addressed
  occupant; broadcasts go to the room. The bridge never echoes a
  message it itself injected (loop-break on the `xmpp/*` sender prefix).

Chatroom contents are **untrusted** (a compromised session can inject):
the bridge relays bodies as plain text, never as XMPP markup/commands,
mirroring FDR 0009's monitor trust note.

### Transport — over tailscale

spinclass does **not** ship or operate the XMPP server; it binds to one
the operator points it at (prosody recommended — lightweight, built-in
`mod_muc` and component support). "Available over tailscale" means that
server's listeners bind to the tailnet interface and are addressed by
MagicDNS name; the human's client connects to `<host>.tailnet.ts.net`
over tailscale, and tailnet ACLs gate who may reach the port. TLS uses a
tailscale-issued cert (`tailscale cert <magicdns-name>`). A reference
prosody + nix deployment is a follow-up, out of scope here. The Go XMPP
client library is `mellium.im/xmpp` (idiomatic, maintained, component +
c2s + MUC support).

## Examples

A human on their phone (Conversations, over tailscale) joins
`fleet@conference.host.tailnet.ts.net` and sees the roster of occupants:

```
fleet  (3 occupants)
  ● sleek-locust      busy · fix login bug · ✎ dirty · 1 client
  ● light-birch       bump tap dep
  ◐ clear-cherry      detached
  ⦿ fleet-bridge      (bridge)
```

They broadcast one line to the whole fleet:

```
me → fleet:  rebase on main, the schema migration just landed
```

The bridge writes it to the chatroom as `to="*"`, `from="xmpp/me"`, and
`EmitWake` pushes it; every live session's agent receives the
`[clown-job] message` notification and reacts — no `chat-read` needed.

A session replies via its MCP `chat-send`; the bridge relays it out and
the human sees, in-room under the session's nick:

```
sleek-locust → fleet:  rebased clean, build green
```

A directed nudge — the human opens a private chat with `light-birch`:

```
me → light-birch (private):  this one's blocked on you
```

→ only `light-birch`'s session wakes (`to=<repo>/light-birch`).

## Limitations

- **Bridge must be running.** Like FDR 0009's monitor, push only works
  while the daemon is up; a broadcast sent while the bridge is down
  still lands in the chatroom (history) but is not delivered to XMPP or
  pushed until the bridge reconnects and replays from its cursor.
- **Push to agents still requires clown.** Human → session delivery
  rides `EmitWake`, which is a no-op without `CLOWN_BIN` (FDR 0010). On
  a clown-less host the message is written to the chatroom but only
  picked up by a session's `chat-read` poll. The XMPP → store write is
  unconditional; only the agent *push* is clown-gated.
- **Presence is probe-shaped (FDR 0012).** Occupant presence is only as
  accurate as the session-presence derivation; sessions whose attach
  path the probe can't see, or hosts without FDR 0012, show coarse
  liveness only.
- **Closed sessions vanish.** An `abandoned` session's occupant leaves
  the MUC; there is no persistent per-session JID/roster contact across
  close→reopen in v1 (the MUC occupant is ephemeral). A roster-contact
  model (presence subscriptions independent of the MUC) is a possible
  later addition for durable per-session identity.
- **Untrusted bodies.** A compromised session can post arbitrary text
  into the MUC under its nick; the bridge does not authenticate message
  *content*, only relays it. The human must treat in-room text as they
  would any chat.
- **Server coupling.** Component mode requires editing the XMPP server
  config (shared secret + component block); multi-client mode requires N
  accounts. Either is a one-time operator step, but spinclass cannot
  bootstrap it.
- **Single host / single MUC (v1).** The bridge watches one host's
  session index and one room. Multi-host fleets (cf. FDR 0011 remotes)
  would run a bridge per host into a shared MUC — occupant nicks already
  carry `<repo>/<branch>`, so rooms compose, but cross-host presence
  reconciliation is unspecified here.

## Tuning Levers

| Lever | Proposed | Rationale | Change signal |
|---|---|---|---|
| identity model | XEP-0114 component | one connection, native per-occupant presence, canonical gateway shape | server can't host a component → multi-client puppets |
| presence interval | 10s | matches FDR 0012 render cadence; presence is low-churn | occupants lag real state, or stanza churn shows in the server |
| nick | branch, `<repo>/<branch>` on collision | shortest stable handle a human reads | cross-repo collisions common enough to always qualify |
| broadcast sender label | `xmpp/<muc-nick>` | distinguishes human-origin from peer-session messages; loop-break key | need richer human identity (map to roster JID) |
| relay scope (session→MUC) | broadcasts to room, DMs as MUC-PM | mirrors chatroom addressing into MUC semantics | firehose too noisy → in-room filter or per-repo sub-rooms |

## More Information

- FDR 0009 (`docs/features/0009-cross-session-chat-monitor.md`) — the
  chatroom store (`internal/chat`), `chat-send`/`chat-read`, and
  `EmitWake`; this bridge is a second consumer of the same store, on the
  human-facing side.
- FDR 0010 (`docs/features/0010-clown-job-wakeup-producer.md`) /
  clown RFC-0009 — the job-wakeup push that carries human → session
  broadcasts to agents; the XMPP bridge reuses `chat.EmitWake` verbatim.
- FDR 0012 (`docs/features/0012-session-presence.md`) — the `Presence`
  derivation (agent beacon + client probe) this feature maps onto XMPP
  presence/status; a soft dependency (degrades to coarse liveness
  without it).
- FDR 0011 (`docs/features/0011-remote-sessions.md`) — the tailnet /
  ssh remote-session patterns and the multi-host shape a future
  bridge-per-host would extend.
- `internal/chat/chat.go` — `Message`, `Broadcast`, `Send`,
  `DisplaySubject`/`RecoveryHint`; `internal/chat/wake.go` `EmitWake`;
  `internal/clown/clown.go` `SendMessage`/`Enabled` — the exact APIs the
  bridge calls.
- `internal/session/session.go` — session states (`StateActive`,
  `StateInactive`, `StateRunningDetached`, `StateAbandoned`) and the
  index the bridge reconciles occupants against.
- XEP-0114 (Jabber Component Protocol), XEP-0045 (MUC), XEP-0077
  (In-Band Registration), XEP-0357 (Push) — the XMPP mechanisms;
  `mellium.im/xmpp` — the proposed Go library; prosody `mod_muc` /
  `Component` — the recommended reference server.

## Open Questions

1. **Component vs multi-client as the shipped default.** Component is
   the cleaner architecture but couples to server config; multi-client
   works anywhere but is N connections + account provisioning. Ship
   both behind `[xmpp]` (recommended) or pick one for v1?
2. **Account/secret provisioning.** Who creates the component secret or
   the per-session accounts, and where does the bridge read it — a
   single `secret-file`, or per-session credentials derived from the
   session key?
3. **Daemon supervision.** Is the bridge a hand-run `spinclass
   xmpp-bridge`, a shipped LaunchAgent/systemd unit (cf. clown's
   `homeManagerModules`), or folded into an existing always-on
   spinclass process?
4. **Durable per-session identity.** Is the ephemeral MUC occupant
   enough for v1, or do sessions also need persistent roster JIDs
   (presence subscriptions surviving close→reopen)?

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown).
