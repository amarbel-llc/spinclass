---
status: superseded by FDR-0017
date: 2026-06-06
promotion-criteria: |
  PROMOTED to `experimental` on 2026-06-04: the spinclass-shipped plugin
  monitor was observed arming and running on a Linux host with the real
  merged build (`chat-watch` monitor present on launch). The push delivery
  of a peer `chat-send` into a second live session is the remaining
  `experimental → testing` gate — verify the streamed line lands as a
  notification and the receiving agent reacts unprompted. NOTE: monitor
  arming is gated by the Claude Code GrowthBook flag `tengu_amber_sentinel`,
  which currently resolves true on Linux and false on macOS (see
  Limitations → Platform-gated rollout); `experimental → testing`
  verification must be done on a host where the flag is enabled. Also
  decide the open questions below before `testing`: the
  open questions below have decisions: the per-session message-filtering
  predicate (how the monitor script selects "messages for THIS session"),
  the monitor restart/dedup story across `sc resume`, and whether the
  monitor watches via inotify/`tail -F` or a poll loop. The global/open
  chatroom storage model (FDR-less, tracked in issue #16) must remain
  expressible end-to-end with the chosen monitor shape.
---

# Cross-session chat via a plugin monitor

> **Superseded by [FDR-0017](0017-clown-session-attach-grouping-chat-rescope.md)
> (2026-06-14):** cross-session chat left spinclass entirely and is now a clown
> construct. `internal/chat`, the `chat-send`/`chat-read`/`chat-list-sessions`
> MCP tools, and the `chatroom/` store have all been removed; clown owns chat
> (store + transport + addressing). Retained for history. (Earlier, 2026-06-06:
> the plugin-monitor receive path was removed in favor of clown's job-watch
> push; the store + chat-* tools then lived on until this supersession.)

## Problem Statement

Spinclass sessions cannot talk to each other. Issue #16 proposes a
global, open file-backed chatroom with `chat-send` / `chat-read` MCP
tools, but its receive path is **polling**: a session only learns of a
new message when its agent decides to call `chat-read`. That is the
wrong shape for "a peer messaged you" — the receiving agent has no
trigger to poll, so messages sit unseen until something unrelated
prompts a read. This feature replaces the polling receive path with a
**push**: a Claude Code *plugin monitor* (shipped in spinclass's own
plugin manifest) tails the chatroom and streams new messages addressed
to the current session straight into its context, so the agent reacts on
its next turn without being asked to look.

## Interface

### Receive — a plugin monitor (push)

Claude Code's **Monitor** mechanism runs a shell command as a persistent
background process for the lifetime of an interactive session and
delivers every stdout line to the agent as a notification. Plugins can
declare monitors that start automatically when the plugin is active
(Claude Code v2.1.105+), via either an `experimental.monitors` array in
`.claude-plugin/plugin.json` or a sibling `monitors/monitors.json`.

Spinclass ships exactly one such monitor in its existing plugin manifest
(the same `.claude-plugin/plugin.json` that already declares the
`spinclass serve` MCP server). The monitor runs a spinclass-owned
subcommand — `sc chat-watch` — that:

1. Resolves the current session key from `$SPINCLASS_SESSION_ID`
   (which is the session key, `<repo-dirname>/<branch>`).
2. Watches the global chatroom directory
   `$XDG_STATE_HOME/spinclass/chatroom/` for new message files.
3. For each new message whose `to` is `"*"` (broadcast) or this
   session's key, writes a single human-readable line to stdout —
   except the session's own messages (`from` == its key), which are
   never echoed back at their sender (#108): the push surface is for
   peers' messages, while `chat-read` still returns own messages as
   history.

Because the monitor declares `"when": "always"`, it starts at session
start and on plugin reload. Its stdout lines arrive in the agent's
context the same way a `<channel>` event would — the agent sees
"new chat message from `madder/rare-buckeye`: …" and can act or reply.

### Send — the existing MCP tool (unchanged)

Sending is a normal MCP tool call, not part of the monitor. `chat-send`
(per issue #16) writes one JSON message file into the chatroom
directory. The two halves compose: **send = `chat-send` MCP tool,
receive = `sc chat-watch` plugin monitor.** No reply-tool transport is
needed because send is already a first-class tool.

### Subjects and bodies (#103)

Messages are split into a **subject** (one line, ≤200 runes — rejected
over-cap when explicit) and an optional **body** of any length. Push
notification lines (chat-watch and the clown wake `--message`) carry
ONLY the subject — the harness truncates long notification events, which
is how bodies used to get lost (#103) — plus a
`· full body: chat-read from=<sender> peek=true` hint when a body
exists. `chat-read` renders the subject line followed by the full body;
the store keeps full fidelity. The pre-subject `message` parameter
remains as a deprecated alias for `body`, with the subject derived from
its first line (clipped, never rejected) so in-flight senders keep
working; stored pre-subject messages render the same way.

### Plugin manifest shape

```json
{
  "name": "spinclass",
  "mcpServers": {
    "spinclass": { "type": "stdio", "command": "spinclass", "args": ["serve"] }
  },
  "experimental": {
    "monitors": [
      {
        "name": "chat-watch",
        "command": "spinclass chat-watch",
        "description": "Cross-session chat messages addressed to this session"
      }
    ]
  }
}
```

`name` must be unique within the plugin (prevents duplicate processes on
reload). `command` runs unsandboxed at hook trust level, in the session
working directory, and inherits the session env — so
`$SPINCLASS_SESSION_ID` and `$XDG_STATE_HOME` are available without
substitution. The monitor is skipped on hosts where the Monitor tool is
unavailable (Bedrock / Vertex / Foundry, or when `DISABLE_TELEMETRY` /
`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` is set), which degrades
gracefully to issue #16's `chat-read` polling fallback.

## Examples

### A broadcast reaches a sibling session unprompted

```
# session A (madder/rare-buckeye), via its agent:
chat-send  message="schema migration landed, you can rebase"

# session B (dodder/deft-sequoia) is idle. Its chat-watch monitor,
# running since session start, sees the new file and emits:
new chat message from madder/rare-buckeye: schema migration landed, you can rebase

# B's agent receives that line as a notification and reacts on its
# next turn — no chat-read call was made.
```

### A directed message

```
# session A:
chat-send  to="dodder/deft-sequoia"  message="can you bump the tap dep?"

# only session B's monitor emits the line (A's own monitor and any
# third session's monitor filter it out, since to != their key and
# to != "*").
```

## Polling fallback and cleanup

The push monitor is the preferred receive path, but it is not always
available (see the platform-gated-rollout limitation below — notably it
is off on macOS today). For those hosts, and as a general pull surface,
spinclass also ships the polling half from issue #16 (implemented in
#98):

- **`chat-read`** — returns messages new since this session's last read,
  defaulting to the full firehose with opt-in `to_me` / `from` / `repo`
  filters. A per-session cursor file
  (`$XDG_STATE_HOME/spinclass/chatroom/.cursor-<hash>.json`) advances
  read-through (past everything scanned, filtered-out included) unless
  `peek` is set, which reads without advancing.
- **`chat-list-sessions`** — lists active sessions across all repos (the
  candidate recipients for a directed `chat-send`).

**Cleanup.** `sc clean` reaps the chatroom using the same retention
window as session tombstones (`[session-entry].tombstone-retention`,
default 30d): message files whose timestamp is older than the window are
removed, and per-session cursor files are reaped by mtime once they have
gone untouched for that long. Retention is keyed off age, not the
session index — cursor files are hashed by session key and not reversible
back to it, so age is the robust proxy for "no live reader." Implemented
in #99.

## Clown job-wakeup migration (`SPINCLASS_CHAT_WAKE`)

The push half of chat is migrating onto clown's **job-wakeup channel**
(clown RFC-0009 / FDR-0013): a chat message addressed to a session is
exactly a non-terminal *waking* event with addressable targeting, so the
shared channel replaces this FDR's bespoke monitor + 1s poll. The
factoring is **store = spinclass, wake = clown**: the chatroom file
store stays the system of record (history, the `chat-read` firehose,
the pull/macOS fallback) in both modes — only the push path changes.

The two halves are gated on DIFFERENT axes (changed after a live
incident — see below):

- **Send/emit — gated on capability (`CLOWN_BIN` present).** Whenever
  the session runs under clown, `chat-send` dual-writes: the store
  first (the message), then a wake emit via
  `${CLOWN_BIN:-clown} job message --target <key> --from <sender>
  --source spinclass --message <subject> --result-ref "chat-read
  from=<sender> peek=true"`. Broadcasts emit once to clown's reserved
  broadcast key `*` (condvar-style channel broadcast — clown's
  job-watch scans its own channel plus the broadcast channel; a
  monitor's first attach starts at journal end, so pre-existence
  broadcasts are never replayed). The chat wake MODE does not gate the
  emit.
- **Receive — gated on `SPINCLASS_CHAT_WAKE` (default `legacy`;
  unrecognized values resolve to `legacy`).** `legacy`: the
  `chat-watch` monitor polls the chatroom as in this FDR. `clown`:
  `sc chat-watch` exits immediately and clown's job-watch owns the
  push path.

**Why the axes split (mixed-window incident, 2026-06-06):** emit and
stand-down were originally both gated on the mode, which opened a
delivery hole during the fleet rollout — a pre-flip legacy-mode SENDER
emitted nothing while a post-flip clown-mode RECEIVER had already stood
its chat-watch down, so a directed message sat in the store with no
push of any kind (observed live: a directive to a freshly-restarted
session went unseen until a manual `clown job message` wake). Sender
capability and receiver mode are different axes; gating the emit on
presence closes the hole. The cost is that a legacy-mode receiver of
an always-emitted wake sees a duplicate notification (chat-watch +
job-watch) until pre-flip sessions retire — the transitional trade-off
already accepted above.

A wake-emit failure is surfaced in the `chat-send` result but never
fails the send — the store write already succeeded, so the recipient
still gets the message via `chat-read` or a legacy monitor. The emit is
bounded by a 10s timeout so a wedged clown binary cannot hang the tool.

Rollout is fleet-global via the shared profile env; both modes always
write the store, so flipping either direction loses nothing. During a
mixed window a clown-mode sender + legacy-mode receiver produces a
duplicate notification (chat-watch + clown job-watch both fire) —
tolerated, and consumers dedupe per the channel's at-least-once posture.

Promotion criteria for flipping the default to `clown` and deleting
`chat-watch` + `internal/chat/watch.go` + the monitor manifest entry:

1. clown `message` waking class merged + conformance-tested (clown side).
2. Directed push observed end-to-end between two live Linux sessions
   via clown job-watch.
3. Broadcast observed reaching ≥2 sessions via the broadcast channel.
4. Replay verified: a message emitted while the target's monitor is
   down is delivered on its next monitor start (an upgrade over
   `chat-watch`, which starts at current end and never replays).
5. macOS pull path re-verified (trivially — `chat-read` is untouched).
6. ~1 week of real use with no missed-message reports.

**Executed 2026-06-06 (user-directed promotion).** Criteria 1–5
satisfied; the ~1-week soak (criterion 6) was deliberately waived by
user direction on the strength of: the raw-CLI directed and broadcast
legs, producer wakes observed in both terminal flavors, the
spinclass-plumbing sender self-echo (msg-44d10e6a), peer broadcast
confirmations from tommy/solid-mulberry (likely a replay — it was
inactive at send) and madder/clear-larch, and the mixed-window
incident fix verified live. `chat-watch`, `internal/chat/watch.go`,
the monitor manifest entry, and `SPINCLASS_CHAT_WAKE` were deleted;
clown's job-watch is the sole push path. NEW LIMITATION: push requires
running under clown (`CLOWN_BIN` set); without it (bare spinclass,
macOS-without-monitors) `chat-read` polling is the only receive path.

Retention skew is deliberate: the store keeps 30d (system of record);
clown's journal GC keeps ~7d (push layer only).

## Limitations

- **Receive only while the session is open.** Monitors run only in live
  interactive CLI sessions. A message sent while the target session is
  closed is never pushed; it is read later only if that session reopens
  and something calls `chat-read`. This is inherent to the Monitor
  mechanism, not a spinclass choice.
- **One-way.** A monitor streams stdout into the session; it cannot
  carry a reply back out. That is fine here — `chat-send` is the reply
  path — but it means the monitor is strictly a receive channel.
- **Trust level.** The monitor command runs unsandboxed at the same
  trust level as hooks. `sc chat-watch` must treat chatroom file
  contents as untrusted input (a peer session's message is attacker-
  controllable if any session is compromised) and must not interpret
  message bodies as commands.
- **Not Channels.** This deliberately does NOT use Claude Code's
  `claude/channel` MCP capability. Channels are a research-preview
  feature gated behind an Anthropic-curated allowlist — a custom
  spinclass channel would require `--dangerously-load-development-channels`
  or an org `allowedChannelPlugins` entry to run, which is a non-starter
  for a paved-path tool. Plugin monitors have no allowlist. Channels
  would only win if two-way push (Claude replying *out* through the same
  transport, like a Telegram bridge) were needed; it isn't. See More
  Information.
- **Host availability.** No push on Bedrock / Vertex / Foundry or with
  non-essential traffic disabled; those sessions fall back to polling.
- **Platform-gated rollout (`tengu_amber_sentinel`).** Plugin-monitor
  arming is gated behind a Claude Code GrowthBook feature flag,
  `tengu_amber_sentinel` (verified from the CC 2.1.161 binary: the
  arming entrypoint begins `if (!Wu()) return;` where
  `Wu() = D_("tengu_amber_sentinel", false)`). When the flag resolves
  false the monitor is skipped **silently** — no log line, no error,
  nothing in the task panel. As of 2026-06-04 the flag resolves **true
  on Linux and false on macOS** for the same account and identical
  `~/eng` config (confirmed by a matched-control test: same build, same
  CLI `Bash`/`Write` deny, same plugin manifest — only the OS differs,
  and only Linux arms the monitor). This is an Anthropic-side rollout
  decision; **no client env var overrides it** — the only GrowthBook
  env vars (`DISABLE_GROWTHBOOK`, `DISABLE_TELEMETRY`) push the flag
  toward its `false` default, and the value is otherwise served by
  GrowthBook based on platform/account targeting. Consequence: on macOS
  hosts today the monitor receive-path is unavailable and the polling
  `chat-read` path (issue #98) is the only working receive mechanism;
  on Linux the monitor works. Re-check when the flag reaches macOS.
- **Min version.** Plugin-declared monitors require Claude Code
  v2.1.105+. Older clients silently ignore the `experimental.monitors`
  entry and get the `chat-read` polling fallback only. (The
  `experimental.monitors` placement under `experimental` is required as
  of v2.1.129; top-level `monitors` still works but warns.)

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| watch strategy | 1s poll loop (prototype) | dependency-free + portable across macOS/Linux, no cgo; adequate to prove the push loop | latency complaints, or session counts that make per-tick `readdir` costly. Candidates if so: `fsnotify` (pure-Go, no external binary — first choice), `fswatch` (streams change events on stdout, matches the line-consuming shape, but a pinned external dep), `watchexec` (cross-platform, debounced, fits the existing `lib.mkSpinclass` runtime-pin pattern, but rerun-on-change rather than long-lived stream) |
| emit format | one stdout line per message: subject + chat-read hint (#103 fix) | cheapest signal the monitor mechanism consumes line-by-line; subject survives harness truncation | agents need structured fields (then emit a tagged/JSON line) |
| subject cap | 200 runes | comfortably under every observed harness truncation (~500+) after prefix overhead; reads as a one-line summary | observed truncation below the cap, or senders chafing at the limit |
| default scope | broadcast + DMs to this session | matches issue #16's "broadcast-global, opt-in filter" decision | firehose proves too noisy at higher session counts |

## More Information

- Issue [#16](https://code.linenisgreat.com/spinclass/issues/16) —
  the global/open cross-session chatroom this feature is the receive
  half of. `chat-send` / `chat-read` / `chat-list-sessions` and the
  flat `$XDG_STATE_HOME/spinclass/chatroom/` storage model live there;
  this FDR replaces #16's polling receive with a push monitor.
- Issue [#97](https://code.linenisgreat.com/spinclass/issues/97) —
  expose merge/check-this-session as MCP tasks; shares the broader
  "session reacts to background/async work" surface.
- FDR 0006 (`docs/features/0006-spawn-sibling-sessions.md`) — the
  driver/worker coordination pattern whose `Monitor` usage (a
  background script streaming GitHub-issue state) is the same Monitor
  mechanism this feature builds on. That FDR coordinates via GitHub
  issues; this one adds a direct in-session channel.
- Claude Code **Monitor tool** reference
  (`docs.claude.com/en/docs/claude-code/tools-reference#monitor-tool`,
  v2.1.98+) and **plugin monitors**
  (`docs.claude.com/en/docs/claude-code/plugins-reference#monitors`,
  v2.1.105+) — the mechanism and the plugin-declaration shape.
- Claude Code **Channels** (`/en/channels`, `/en/channels-reference`) —
  the `claude/channel` push capability deliberately NOT used here;
  research-preview + Anthropic allowlist make it unsuitable for a
  paved-path tool. Recorded as the two-way alternative.
- **`tengu_amber_sentinel` investigation (2026-06-04).** The monitor not
  arming on macOS was root-caused to this GrowthBook flag via binary
  inspection of the CC 2.1.161 bundle, after eliminating (each by
  evidence): CC version, manifest/clown-compile correctness, telemetry
  on/off, clown/moxy MCP proxying, inline-vs-installed plugin loading,
  `policySettings.disableAllHooks` (hooks demonstrably run, so this gate
  is false), and the CLI `Bash`/`Write` deny (present identically on the
  working Linux host). The decisive control was the Linux host: same
  `~/eng`, same deny, monitor works — isolating OS platform as the only
  differing variable and the `tengu_amber_sentinel` GrowthBook gate as
  the mechanism. Env-var override was researched and ruled out against
  the official env-vars reference and the binary's gate functions
  (`D_`, `Wu`, `gJ_`/`QJ_`).
- `.claude-plugin/plugin.json` — the manifest the `monitors` entry is
  added to; copied + version-substituted into
  `share/purse-first/spinclass/.claude-plugin/` by `flake.nix`
  postInstall.
- `internal/session/session.go` — `xdgStateBase()` / `indexDir()`
  establish the `$XDG_STATE_HOME/spinclass/` root the chatroom dir is a
  sibling of; `SessionKey` (= `$SPINCLASS_SESSION_ID`) is the address.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown).
