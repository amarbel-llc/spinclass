# XMPP-MUC per-session messaging — spinclass integration note

**Status:** design note (prototype follow-up) · **Date:** 2026-06-17

Companion to the circus prototype
(`circus/docs/reference/xmpp-muc-messaging-prototype.md`) and the trapeze
`xmpp-bridge`. The prototype's **vertical slice** runs a single bridge by hand;
this note sketches the **full session-lifecycle** integration that spinclass
would own — deliberately *not* built in the slice.

## Goal

Each spinclass session gets its own XMPP MUC room (one room per session) on the
krone Prosody, with the `trapeze xmpp-bridge` launched and torn down by the
session lifecycle, so a human can reach any live session's agent over XMPP
without manual setup.

## What already lines up

- A session is keyed by `SPINCLASS_SESSION_ID` (e.g. `repo/branch`), exported
  by the hooks (`internal/hooks/hooks.go`) and equal to clown's
  `CLOWN_GROUP_ID` — already the chat target the bridge needs (`--group`).
- clown's presence index (consumed via `internal/clown/presence.go`) already
  tracks per-session liveness for `sc list`; the room/bridge can hang off the
  same lifecycle signals.

## The three lifecycle concerns

### 1. Room naming (sanitization)

A session key contains `/`, illegal in a JID localpart. spinclass owns the
mapping `SPINCLASS_SESSION_ID → room JID`, e.g. replace `/` with `-`:

```
repo/feature-x  →  repo-feature-x@conference.krone
```

The bridge already takes `--group` (the raw key, for `clown chat`) and `--room`
(the sanitized JID) as **separate** flags precisely so spinclass owns this.

### 2. Provision / launch on session start

Candidate seams (in rough order of least-invasive):

- **A sweatfile `[[start-commands]]`-style hook or a new `[xmpp]` block.** A
  per-session background launch of `trapeze xmpp-bridge --room <derived>
  --group $SPINCLASS_SESSION_ID …` fits the existing config-driven launch
  surface. MUC rooms are created on first join, so no separate "create room"
  call is needed — the bridge joining provisions the room.
- **`SessionStart` plugin hook** (`internal/hooks`, where implicit sessions are
  materialized) — the natural place to also start the bridge, since it already
  fires per session and has the session key in hand.
- The bridge runs as a **detached helper** (like the spawn-window / job
  helpers), one process per session, logging to the session's state dir.

Credentials: the bridge needs an XMPP account. For the prototype, a shared
`agent@krone` account (seeded on krone) is enough; a per-session or per-user
account is a hardening follow-up.

### 3. Teardown on session close

On `sc close` / `SessionEnd`, stop the bridge process (and optionally have it
`Leave` the room / let Prosody reap an empty non-persistent room). Bridge
processes are one-per-room, so teardown is just killing the session's helper.

## `sc list` surfacing (optional)

Once rooms exist per session, `sc list` could show the room JID (or an
`xmpp:` reachability column) alongside the existing presence-derived liveness,
so you can see at a glance which sessions are reachable over XMPP and where.

## Explicitly out of scope here

- Replacing clown's file-backed chat with XMPP wholesale (the slice bridges
  instead, leaving clown unchanged).
- Remote sessions (`[[remotes]]`) — XMPP would actually generalize these, but
  that is a separate design.
- Multi-user rooms with more than one human / cross-session rooms — the model
  is one room per session for now.

## Pointers

- Bridge + MUC client: `trapeze/internal/xmppbridge`, `trapeze/internal/xmpp/muc.go`
- Server: `circus/zz-pocs/tent/hosts/krone/xmpp.nix`
- Local smoke: `circus/zz-pocs/xmpp-muc-dev/`
- Agent-side surface (unchanged): clown `clown chat` / RFC-0009, RFC-0013
