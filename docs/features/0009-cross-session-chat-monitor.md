---
status: proposed
date: 2026-06-03
promotion-criteria: |
  Promote to `experimental` once a spinclass-shipped plugin monitor is
  observed delivering a peer session's `chat-send` message into a second
  live session's context without that session polling — i.e. the
  monitor's stdout line lands as a `<channel>`-style notification and the
  receiving agent reacts on its next turn. Promote to `testing` once the
  open questions below have decisions: the per-session message-filtering
  predicate (how the monitor script selects "messages for THIS session"),
  the monitor restart/dedup story across `sc resume`, and whether the
  monitor watches via inotify/`tail -F` or a poll loop. The global/open
  chatroom storage model (FDR-less, tracked in issue #16) must remain
  expressible end-to-end with the chosen monitor shape.
---

# Cross-session chat via a plugin monitor

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
   session's key, writes a single human-readable line to stdout.

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
- **Min version.** Plugin-declared monitors require Claude Code
  v2.1.105+. Older clients silently ignore the `experimental.monitors`
  entry and get the `chat-read` polling fallback only.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| watch strategy | 1s poll loop (prototype) | dependency-free + portable across macOS/Linux, no cgo; adequate to prove the push loop | latency complaints, or session counts that make per-tick `readdir` costly. Candidates if so: `fsnotify` (pure-Go, no external binary — first choice), `fswatch` (streams change events on stdout, matches the line-consuming shape, but a pinned external dep), `watchexec` (cross-platform, debounced, fits the existing `lib.mkSpinclass` runtime-pin pattern, but rerun-on-change rather than long-lived stream) |
| emit format | one stdout line per message | cheapest signal the monitor mechanism consumes line-by-line | agents need structured fields (then emit a tagged/JSON line) |
| default scope | broadcast + DMs to this session | matches issue #16's "broadcast-global, opt-in filter" decision | firehose proves too noisy at higher session counts |

## More Information

- Issue [#16](https://github.com/amarbel-llc/spinclass/issues/16) —
  the global/open cross-session chatroom this feature is the receive
  half of. `chat-send` / `chat-read` / `chat-list-sessions` and the
  flat `$XDG_STATE_HOME/spinclass/chatroom/` storage model live there;
  this FDR replaces #16's polling receive with a push monitor.
- Issue [#97](https://github.com/amarbel-llc/spinclass/issues/97) —
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
- `.claude-plugin/plugin.json` — the manifest the `monitors` entry is
  added to; copied + version-substituted into
  `share/purse-first/spinclass/.claude-plugin/` by `flake.nix`
  postInstall.
- `internal/session/session.go` — `xdgStateBase()` / `indexDir()`
  establish the `$XDG_STATE_HOME/spinclass/` root the chatroom dir is a
  sibling of; `SessionKey` (= `$SPINCLASS_SESSION_ID`) is the address.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown).
