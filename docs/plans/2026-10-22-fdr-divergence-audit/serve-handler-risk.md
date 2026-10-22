# Serve handlers: what a confined caller could do (2026-10-22)

Read from the code at 9923c6f; citations are repo-relative. Nothing here was
exploited. "Not determined" means the code was not read far enough to say.

## Two vantages, and what caller classification can see

`ClassifyServeCaller` classifies the **serve's own** principal (its
`CLOWN_SESSION_ID`), not the process that dialled it. So:

- **Vantage A, the worker's own frontend serve.** The worker's tool calls go
  through its own serve, whose principal the unit record names. The
  classification sees "confined", and a gate in the handler refuses. Before
  the gate, only the agent-side hook stood here, and principle 7 says that is
  no boundary.
- **Vantage B, another session's serve.** If a process in the unit could dial,
  say, the driver's serve, that serve's principal would be the driver's. It
  would classify as **unconfined** and every gate would pass. The gates below
  do **not** cover B.
  - **B is inferred, not observed.** F12 never connected to anything: it
    fails on a missing `serve_socket` in the unit record (`probes.go:1224`).
  - Units run `PrivateNetwork=yes`, and F13 passed, so the host's 127.0.0.1
    is not the unit's loopback.
  - The paths that remain are clown's: a peer-checked forward to the wrong
    target, or the egress proxy with a loopback port allowlisted.
  - FDR 0035 lists the probes for the next confined round.

Fix 1 (029c3c8, spawn) covers vantage A only. The earlier report said it
covered "the F12 case", which overclaimed; that is corrected here.

## Cross-cutting facts

- `wrapMCPHandler` (`commands_mcp_only.go:41-66`) does no caller check.
- The cwd-based handlers act on the **serve's cwd session**. Under vantage B
  that is the victim serve's session, and none of them takes a path or repo
  parameter.
- `Holders`, `SpawnedBy`, `RepoPath` and `WorktreePath`, which the handle and
  close handlers trust, come from worktree-local `state.json` that the agent
  can write (`session.go:286-305`). Whether a confined agent can write the
  central index directory is not determined.

## Ranked (worst first)

| # | Tool | Handler | Worst case, A (own serve) | Worst case, B (another serve) |
|---|---|---|---|---|
| 1 | `resurrect` | `resurrect_cmd.go:45`; `internal/resurrect/resurrect.go:38-114` | Resurrects **any** closed session, including another session's, from its tombstone. The new worktree gets a freshly minted forge credential (`shop.ProvisionCredential`, `:77`), and copies `Holders`, `AuthorizingGrant` and `Profile` from the tombstone (`:83-99`). No caller identity is checked at all. | Same. |
| 2 | `close-child-session` | `close_child_cmd.go:205`; auth `:59-122` | Reaps any session whose `Holders` names its principal, or whose agent-writable state it rewrote to name it. `force` discards unmerged work: its only prompt is the client hook that is being bypassed. | Acts as the victim's principal, so it can force-reap **every** session the victim holds (e.g. all of a driver's workers). |
| 3 | `grant-session-handle`, `list-handles accept` | `handles_cmd.go:150`, `:330` (`:296-314`) | Grants a handle on a session it holds to any principal (`to` is free text); rights are recorded, not enforced (row 32-23). `accept` promotes every pending grant naming it. | Grants the victim's handles to the attacker's own principal: lasting reap and grant authority. |
| 4 | `nothing-but-the-truth` + `merge-this-session(-async)` | `commands_mcp_only.go:1143`, `:411`, `:536` | Self-attests with any content (validation only checks the skill names, `:1184`), then lands and pushes its own branch onto the default branch and runs the post-merge targets. `default_branch` is unvalidated here (`:451-458`); what merge does with it is not determined. `post_merge_timeout = "0"` lifts the cap. | Lands and pushes the **victim's** branch with a forged attestation. |
| 5 | `check-this-session(-async)` | `:505`, `:690` | Runs the `[hooks].pre-merge` command in the frontend and burns a buffered attestation on a pass. | Same, for the victim's session. |
| 6 | `release-session-handle` | `handles_cmd.go:240` | Drops only its own handle; may orphan a child. | Drops the victim's handles and orphans its children (a denial of service). |
| 7 | `update-this-session-description` | `:971` | Sets any description string. `list` shows it to every session, so it is a prompt-injection carrier. It can also synthesize state for an untracked worktree. | Same, on the victim's session. |
| 8 | `session-job-cancel` | `:759` | Cancels its own session's running merge or check. | Cancels the victim's (denial of service). |

## Read-only

| Tool | Returns | Cross-session? |
|---|---|---|
| `list` | Every session in the index: key, state, branch, worktree path (text), description, `spawned_by`, holder count, profile | **yes**, so gate it |
| `perms-list` | The global and every repo's allow-rule tiers. `repo` is joined into a path (`internal/perms/cmd.go:50`), so `../` may reach another `*.json` (not tried) | **yes** (every repo's rules), so gate it and validate `repo` |
| `whoami` | The serve's principal and key, and its own held and pending handles | no |
| `version` | Build versions, pinned store paths, the serve binary path | no |
| `validate` | The serve's own sweatfile hierarchy: paths and parse errors | no |

## Row 34-31 (signers file and grant store follow XDG env)

- `grantverify.DefaultSignersPath` (`grantverify.go:66-77`) and
  `grantstore.DefaultDir` (`grantstore.go:36-45`) read `XDG_CONFIG_HOME` /
  `XDG_STATE_HOME` (falling back to `HOME`) **from the calling process at call
  time**. The signers trust check is "owned by our uid, not group- or
  world-writable" (`trust_unix.go:15-27`). Root ownership is not required.
- **Serve path** (the spawn-session MCP tool): it reads the serve's own launch
  env, which an MCP caller cannot change. It is still exposed if the agent can
  write the file that env points at (same uid). Whether the unit can write the
  operator's `XDG_CONFIG_HOME` is not determined.
- **`sc spawn` CLI path:** the env is whoever runs it. An agent running
  `sc spawn` from its own shell controls `XDG_CONFIG_HOME`, `XDG_STATE_HOME`,
  `HOME` and `CLOWN_SESSION_ID`, so it can point verification at its own
  signers and grant store. `productionAuthorizer` runs `conf.Check()` first
  (`spawn/rights.go:61-64`), but its inputs come from the same env-redirected
  state dir (`confinement/driver.go:40, 117-126`). Whether its probes stop
  this is not determined.
- **Answer: yes on the CLI path; not determined but plausible on the serve
  path.** By principle 2 this is a bug either way.
- **Fix:** resolve both from the passwd home (`os/user`), never from env.
  Refuse if an env override points elsewhere while enforcement is on.
