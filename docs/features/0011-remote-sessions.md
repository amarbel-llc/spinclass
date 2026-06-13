---
status: experimental
date: 2026-06-06
promotion-criteria: |
  experimental -> testing: exercised against >= 1 real remote host —
  (1) `sc list` shows the remote's rows under the `<name>:` prefix,
  (2) tab completion offers `<name>:<id>` entries from the cache, and
  (3) `sc resume <name>:<id>` reattaches over ssh end-to-end. The flip
  happens on that live pass.
  testing -> accepted: routine multi-host use with no tuning-lever
  adjustments (timeout, staleness, fan-out) for ~2 weeks.
---

# Remote sessions (`host:` routing)

## Problem Statement

Spinclass sessions exist on more than one machine, but `sc list`, tab
completion, and `sc resume` only see the local state directory.
Reattaching to a session on another host means remembering it exists,
ssh'ing manually, and running `sc resume` there. The sweatfile should
declare remote hosts so `host:`-prefixed sessions show up locally and
reattach with one command.

## Interface

**`[[remotes]]` sweatfile config** (dedup-by-name merge across the
hierarchy, like `[[mcps]]`/`[[start-commands]]`; remotes normally live
in the global sweatfile):

```toml
[[remotes]]
name   = "devbox"                  # the <host>: prefix users type
ssh    = "sasha@devbox.lan"        # optional; Dest() falls back to name
attach = ["ssh", "-t", "{ssh}", "spinclass", "resume", "{id}"]  # optional; shown value is the default
```

- A **name-only entry is a valid all-defaults remote**: the ssh
  destination defaults to the name (`~/.ssh/config` does the work) and
  the attach template defaults to the ssh argv above. Removing an
  inherited remote therefore requires an explicit `remove = true`
  entry — a deliberate divergence from `[[mcps]]`'s name-only-removes
  sentinel (user-ruled): name-only is the most natural way to declare
  a remote, so it cannot double as the removal sentinel.
- `{ssh}`/`{id}` are substituted literally in every attach argv
  element (the `exec-start` `{arg}` mechanism); the argv is exec'd
  directly, no shell.
- `sc validate`: name required, must not contain `:` or `/`; empty
  `attach` elements are errors; `remove = true` alongside ssh/attach
  fields warns (the fields are ignored).

**Target grammar**: `^([^:/]+):(.+)$` — a prefix naming a configured
remote routes remotely; everything else (plain names, paths, prefixes
matching no remote) resolves locally exactly as before.

**Listing**: `sc list --format json` emits a `session.ListRow` array
(`id`, `session_key`, `state`, `description`, `repo`) — both the local
machine format and the remote wire format. For each configured remote,
`sc list` runs `ssh <dest> spinclass list --format json` in parallel
with a 3s per-host timeout, renders remote rows prefixed
(`devbox:crisp-catalpa`) after local rows, and writes each healthy
host's rows to `$XDG_STATE_HOME/spinclass/remotes/<name>.json`. An
unreachable host yields one diagnostic line (`lab: unreachable (...)`)
in text formats; in json mode diagnostics go to the debug log so the
output stays a machine-clean array (merged remote rows carry a
`"remote":"<name>"` field). List never fails because a host is down.

**Completion**: `host:`-prefixed entries come from the cache files
ONLY — instant, possibly stale, never networks. The cache is refreshed
by each `sc list`.

**Resume picker**: cached remote sessions also appear in the
interactive resume picker after the local rows (cache-only, same
source as completion), marked `remote(<name>) · <state> · cached`;
selecting one routes over the remote's attach template — selection is
the confirmation, no dialog. Remote rows never count toward the
single-match auto-resume shortcut, and the close picker stays
local-only.

**Resume**: `sc resume host:id` builds the attach argv from the
remote's template and execs it with full stdio/TTY passthrough; the
remote spinclass owns sweatfile/entrypoint semantics from there, and
failures pass through the exec'd command's error. `sc close` and
`sc merge` reject `host:` targets explicitly: "remote targets support
resume only (v1)".

## Examples

List across hosts (one remote healthy, one down):

    $ sc list
    spinclass/crisp-catalpa  active   ...   remote-sessions docs
    devbox:sleek-sumac       active        chat self-echo fix
    lab: unreachable (ssh lab: exit status 255: connection refused)

Reattach to the remote session (completion offers `devbox:sleek-sumac`
from the cache):

    $ sc resume devbox:sleek-sumac
    # execs: ssh -t sasha@devbox.lan spinclass resume sleek-sumac

Drop an inherited remote in a child sweatfile:

    [[remotes]]
    name   = "devbox"
    remove = true

## Limitations

- **Resume-only v1.** `close` and `merge` reject `host:` targets
  rather than mis-resolving them locally; remote lifecycle management
  stays on the remote host.
- **No remote detached workers yet.** This `host:` story is ssh-resume
  of sessions that already exist remotely; it does not spawn a detached
  worker (FDR 0006) on another host. The posh multiplexer substrate
  (see `eng-spinclass(7)`) is the candidate native-remote path — posh
  treats `host:group/session` as a first-class roaming attach — but
  remote-worker parity is blocked on two posh gaps: **posh#66** (`posh
  list host:` ignores `-g/--group`, so remote liveness can't be
  group-scoped) and **posh#67** (no remote `attach --detach`, so a
  remote spawn can't return non-blocking). When both land, spinclass's
  spawn/liveness templates could target a `host:` worker the same way
  they target a local one.
- **Remote needs a json-capable spinclass.** The remote host must run
  a spinclass that supports `list --format json`; older binaries
  surface as a per-host unreachable/parse-error row, not a crash.
- **Completion can be stale.** The cache is refreshed only by
  `sc list`; sessions closed remotely linger in completion until the
  next list, and resuming one fails with the remote's own error.
- **Remote rows reflect the remote's own states.** State, description,
  and filtering (non-abandoned only) are whatever the remote spinclass
  reports about itself; the local side adds only the `<name>:` prefix.
- **Custom attach templates own their argv safety.** The default
  template places `{id}` after ssh's option boundary, so an id from a
  compromised remote's list output cannot become an ssh flag. A custom
  `attach` template that puts `{id}` where its command parses options
  (e.g. before a `--`) takes on that risk itself — ids in the
  completion cache are remote-controlled data.

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| per-host list timeout | 3s | LAN/tailnet ssh typically <500ms; tolerates wake-from-sleep | list feels slow / WAN timeouts misfire |
| completion staleness | unbounded cache, refreshed by each `sc list` | completion must never network | stale entries mislead → TTL or `(stale)` label |
| parallel host fan-out | unbounded | configs hold 1–5 remotes | someone configures dozens |
| remote list scope | non-abandoned sessions | matches local default | demand for closed/abandoned visibility |

## More Information

- `docs/plans/2026-06-06-remote-sessions-design.md` — the approved
  design (decision trail: configurable attach with ssh default, live
  list + cached completion, prefix routing over state replication).
  Note the one divergence above: removal is `remove = true`, not the
  design's name-only sentinel.
- `internal/remote` — target grammar, attach argv, host query, cache;
  `internal/session/listrow.go` — the wire contract;
  `cmd/spinclass/commands_query.go` — list fan-out;
  `cmd/spinclass/commands_session.go` — completion, resume routing,
  close/merge rejection.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown) 0.3.10+e8ff9ee ([build commit](https://github.com/amarbel-llc/clown/commit/e8ff9ee351c67cfdf06e9e61ebe262ec3aaa247d)).
