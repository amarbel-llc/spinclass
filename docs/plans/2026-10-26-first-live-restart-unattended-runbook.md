# First live restart of an unattended throwaway worker (#419): runbook

Date: 2026-10-26. Author: spinclass/firm-mulberry. Status: **not run.**

This supersedes `2026-10-25-first-live-restart-runbook.md` for the unattended
case: an UNCONFINED, detached throwaway worker launched with the
`dontAsk` lever, driven by a coordinator agent, with the operator supervising
and not typing into the worker. The old page stays for the confined and
attached cases, the race check and the not-covered list. Where this page says
"as before" it means that page.

Claims are labelled V (observed), R (read in code, a doc or a peer's report),
or I (inferred).

## What was observed so far

- V: `just explore-live-restart-prereqs` parses and runs against this
  worktree's devshell. On this host it reported 4 FAIL, all genuine gaps and
  none a recipe fault: `clown restart --help` fails (the installed clown has
  no restart verb, and `clown --version` failed; the recipe now also tries
  `clown version`, which that run did not exercise),
  `spinclass restart-preflight --peek` prints
  `{"verdict":"none","reason":"no spinclass session --peek","when":""}` with
  no `state` key (installed spinclass 0.2.1+22e43a0 predates `--peek`), no
  `restart =` in the global sweatfile, and no `unattended-permission-mode`.
  The systemd user manager was `running`.
- V: nothing else in this page has been run. The three snapshot recipes
  (baseline, verify, moxy-allow) have **not** been executed against a session.
  They were written from the code and read-checked only.

## Roles

- **Operator** (supervises; owns config and installs): fixes prerequisite
  FAILs, approves the unconfined spawn if asked, changes the `spinclass` on PATH
  between baseline and restart (step 4), and does the manual checks. Does not
  type into the worker.
- **Coordinator agent** (runs the MCP steps): `spawn-session`, `chat_send`,
  `restart-child-session`, waits for wakes, `folio_read`/`folio_ls` in the
  worker's worktree, `close-child-session`. It may also run the recipes through
  the just-us tools (`run_recipe`), which are the paved path; the operator can
  run the same recipes with `just <recipe>` from this repo.
- **Worker**: a throwaway. Brief: "idle; remember any nonce you are sent;
  reply when asked."

## The lever (R)

`[session-entry] unattended-permission-mode = "dontAsk"` in the global sweatfile
or a layer above the repo (a repo-level value is ignored; `sc validate`
warns). For an unconfined detached spawn it puts `--permission-mode dontAsk` in
the worker's argv and `CLOWN_UNATTENDED=1` in its launch environment and in its
pinned resume environment. Clown reads `CLOWN_UNATTENDED` to add
`permissions.defaultMode dontAsk` to its `--settings` (clown#323, commit
1f5e57c5). It is read live at spawn, not pinned in state
(`doc/spinclass-sweatfile.5.scd`, `internal/foxtrot/spawn/unattended.go`). A
confined spawn is always dontAsk with the marker, whatever the key says. A
clown older than 1f5e57c5 leaves argv at dontAsk but ignores the marker. (R)

The restart hand-off is clown's `restart` verb, run by `restart-child-session`.
The relaunched agent is built by clown from the resume entry pinned at spawn,
so the lever surviving the restart is exactly what the verify recipe checks.
(I: the pinned resume env carrying the marker is documented, but no live
relaunch has proven it.)

## Step 0: prerequisites (operator)

```
just explore-live-restart-prereqs
```

Prints PASS / FAIL / INFO / MANUAL per line and a summary; exits non-zero on any
FAIL. Read-only. What each FAIL means and what to do:

| Line | On FAIL |
|---|---|
| `jq on PATH` | install jq; the other recipes need it |
| `clown restart --help exists` | install a clown with the restart verb (613bf9a or later) |
| `clown <commit> contains 613bf9a / 1f5e57c5` | install a clown that contains it. MANUAL instead of PASS means no clown checkout was found (set `CLOWN_REPO`); check by hand with `git merge-base --is-ancestor` |
| `restart-preflight --peek prints one JSON line` | install a spinclass with `--peek` (2a79497 or later); clown's pre-relaunch race check takes its unknown path otherwise |
| `restart hand-off is set in the global sweatfile` | add `[session-entry] restart = ["clown", "restart", "--session", "{principal}", "--when", "{when}", "--reason", "{reason}"]` to `~/.config/spinclass/sweatfile`. Operator's call: it lets every session's restart tools exec clown |
| `unattended-permission-mode = dontAsk` | add it to the global sweatfile (or a parent directory's `sweatfile`). Operator's call: it loosens every unconfined detached spawn |
| `systemd user manager reachable` | start a user manager; without one no `restart-watch` watchdog is armed (the restart still works, the "never came back" wake does not) |

The recipe prints only whether the hand-off key and the lever are set, never the
sweatfile. It cannot check the clown build against the commits without a clown
checkout.

Re-run after fixing until the summary has no FAIL. The MANUAL troupe line stays.

## Step 1: spawn the throwaway (coordinator)

`spawn-session` with `repo` a scratch repo (omitting it spawns in this repo),
`description` "first live restart throwaway", and `brief`:

> Idle. When you receive a DM containing "nonce:", remember it. When asked for
> the nonce, reply with it exactly. Message `<coordinator session key>` via
> chat when you have started.

- Pass `confined` omitted. If `[confinement] default-spawn` is on in this
  environment, the spawn needs `unconfined: true`, which always prompts the
  operator: approve it. (R)
- To test the profile pin (check 4 below), also pass `profile: <name>`; the
  name must exist in clown's profile library, or the spawn is refused. Skip if
  you do not want to test it; the recipes then print INFO and skip that check.
- Wait for the hello wake (async spawn, up to 5 minutes). Do not poll. Note the
  worker's session key.
- Tools: `chat_list` for the worker's JID; `whoami` in the worker is not
  needed, the baseline recipe reads the principal.

If the hello times out, `close-child-session` reaps it; fix and spawn again.

## Step 2: send the nonce (coordinator)

`chat_send` to the worker's JID: `nonce: <fresh random words>`. Wait for its
acknowledgement. Write the nonce where the operator can see it.

## Step 3: baseline (coordinator or operator)

```
just explore-live-restart-baseline <worker session key>
```

Writes `.tmp/live-restart/<key>/baseline.json` (pids, exe, flags, record
fields, the `--peek` line; no credential contents). Expected PASS lines:
principal UUID-shaped, `--peek` state `live`, an agent process found,
`CLOWN_UNATTENDED=1` in its environment, `--permission-mode dontAsk` in its
argv, a serve process found. Exits non-zero on any FAIL.

| FAIL | Meaning and action |
|---|---|
| no record | `target` is wrong (a key, a worktree basename or an absolute path; live sessions only) |
| principal missing | the worker's SessionStart has not recorded it; wait for the hello and retry. A restart is refused without a UUID principal |
| peek not `live` | the installed spinclass lacks `--peek`, or the session is closed/closing: do not restart |
| no agent / serve process | the worker is not running, or the principal/key is not in its environment; run `just debug-session-procs <key>` to look |
| no `CLOWN_UNATTENDED=1` / no dontAsk | the lever was not applied at spawn: wrong layer, wrong clown, or the spawn was confined/auto. Close the worker and respawn after fixing step 0 |

**Operator, make the STALE check meaningful (I).** A worker spawned just now
runs the `spinclass` on PATH, so "no STALE afterwards" would pass vacuously.
The baseline prints INFO saying which case you are in. To test it for real,
install a different spinclass build on PATH now (any rebuild whose store path
changes), then continue. Skip this and mark check 5 "vacuous" in the result.

Manual, before restarting: note that the worker's troupe credential is live
(see check 6).

## Step 4: restart (coordinator)

`restart-child-session` with `child: <worker session key>`, `reason: "first live
restart (unattended runbook)"`. Do **not** pass `force` (always-ask; the
operator would be prompted, and forcing is not what is under test). The
coordinator needs the `restart` right: the spawner holds it. (R)

Expected: the tool returns success, saying the request was recorded and the
hand-off ran. A refusal names the cause: no hand-off configured, a running
merge/check job, an implicit session, a non-UUID principal, or a missing right.
If the hand-off itself fails, the request is cleared and its output returned.
To withdraw a queued request: `cancel-restart` with `child`.

**Countdown (R, the old runbook).** A detached worker needs no posh opt-in and
gets no posh countdown; clown prints its notice inline or not at all. FDR 0037
decision 10 still says a 30 second countdown is owned by clown and posh. I:
treat the relaunch as happening within the worker's next idle plus clown's own
delay, and wait for the wakes rather than timing it.

## Step 5: wait for the wakes (coordinator)

End the turn; do not poll. Three outcomes arrive as wakes (R, FDR 0037):

1. Start wake: "`<key>` is restarting now (`<reason>`, requested by
   `<requester>`)". The preflight verdict was `restart`.
2. Back wake: "`<key>` is back after its restart". The relaunched session's
   SessionStart stamped `RestartedAt`.
3. Watchdog wake after 5 minutes (`RestartInFlightWindow` plus 3 minutes), only
   if no return was marked: "`<key>` has not come back ... check the session
   and its clown log".

| Outcome | Action |
|---|---|
| start wake, then back wake | continue to step 6 |
| start wake, no back wake, watchdog wake | go to step 6 anyway for the evidence; the verify recipe will FAIL on `RestartedAt` and the new agent pid. Read `$XDG_STATE_HOME/clown/close-self-run.log` for the preflight verdict and the relaunch |
| no start wake | the frontend did not call `restart-preflight` (the worker is not idle, or clown ignored the request). Check `clown restart` ran: `cancel-restart`, fix, retry |
| a refusal wake instead | the preflight refused (the obstacles failed at idle); the message says why |

## Step 6: verify (coordinator or operator)

```
just explore-live-restart-verify <worker session key>
```

Takes the `after.json` snapshot and compares it with the baseline. Exits
non-zero on any FAIL.

| FAIL line | Meaning and action |
|---|---|
| same session key / principal / worktree | the session was recreated or re-minted. Stop: the restart's whole point failed. Collect both snapshots and the clown log |
| `RestartingAt` unset or not moved | no `restart` verdict was consumed since the baseline |
| `RestartedAt` unset or older than `RestartingAt` | the relaunched session has not marked its return: not yet back, or SessionStart did not run. Wait and rerun; then clown log |
| `--peek` not `live` | the session is closed or closing |
| a NEW agent process | the old agent survived (restart did not kill it) or no agent is running |
| `CLOWN_UNATTENDED=1` / `dontAsk` on the new agent | the lever did not survive the relaunch: the resume entry lacks the marker or the flag, or clown is older than 1f5e57c5. This is the unattended-mode finding; file it and stop |
| serve process exists / serve binary is PATH's spinclass | stale or missing serve. If the exes differ only by path, ask the worker for its `version` MCP tool: "same build, different path" is fine (the recipe false-FAILs that case) |
| profile pin intact | the record's profile name or digest changed, or the relaunched argv lost/changed `--profile` |

## Step 7: conversation resumed (coordinator)

`chat_send` to the worker: "what was the nonce?" The worker must reply with the
exact nonce from step 2. A worker that cannot recall it restarted into a fresh
conversation: resume did not carry the conversation (the old runbook's "not
covered" note on `[session-entry].resume` and `{principal}` applies). Also ask
it to call its `version` tool: expect `serve binary: current` (or "same build,
different path") and no `STALE`.

## Step 8: #372 live (coordinator, then recipe)

The coordinator calls, on the worker's worktree (path from the baseline
snapshot's `worktree`, or `git worktree list`):

- `folio_read` with `file_path: <worker worktree>/<an existing file>`;
- `folio_ls` with `path: <worker worktree>`.

Expected, **supervised by the operator**: neither call prompts. Operator, tell
the coordinator whether a prompt appeared (this is the V for "allow honoured
live"). The coordinator holds an accepted handle on the worker (the spawner
does at spawn). (R)

Then:

```
just explore-live-moxy-allow <coordinator worktree or cwd> <worker session key>
```

Run it from the coordinator's session, so `CLOWN_SESSION_ID` is the coordinator's
principal (or export it first; `whoami` prints it). R: the hook's verdict log
`.spinclass/hook-verdicts.ndjson` records only spinclass's own tools
(`internal/delta/hooks/verdictlog.go`, `verdictToolPrefix`), so the coordinator's
moxy reads leave **no** line there, whatever the recipe's first section shows.
The recipe's first section therefore lists only log lines naming the child (the
spinclass tools, such as `restart-child-session`); an empty result is expected.
Its second section is the proof: it replays moxy PreToolUse payloads through the
installed `spinclass hook` as the coordinator's principal against the live
session store and prints the decision, the reason and the argument keys:

| Replayed call | Expected |
|---|---|
| `folio_ls` `{path}`; `folio_read` `{file_path}`; `folio_glob` `{pattern, path}` | allow, reason names "held child `<key>`" |
| `grit_branch` `{subcommand: log, repo_path}`; `grit_stage` `{subcommand: status, repo_path}` | allow, same reason |
| `folio_read` of the child's `.spinclass/state.json` | not allowed (another session's `.spinclass` is never readable) |
| `folio_ls` `/etc` | not allowed |
| `grit_branch` `reset-hard` | not allowed |

The argument names are R from `internal/delta/hooks/moxy_read.go`; the moxy
tool schemas in the catalog carry the same names (`file_path` for `folio_read`,
`path` for `folio_ls`/`folio_glob`/`folio_tree`/`folio_file-type`, `repo_path`
and `subcommand` for the grit tools). FAIL means the installed hook does not
allow what #372 promises, or the driver holds no accepted handle: run
`list-handles` with `accept: true` and retry. The replay judges the hook's
decision function live; only the coordinator's real calls in the first bullets
prove Claude Code honoured it.

## Step 9: tear down (coordinator)

`close-child-session` with `child: <worker session key>`, no `force`. Then,
manual, confirm the worker's troupe account is revoked on this real close (and
not before: check 6).

## The checks and what proves each

| # | Check asked for | Proof |
|---|---|---|
| 1 | same principal | verify: `same principal`, `same session key`, `same worktree` (record fields). V when it passes |
| 2 | conversation resumed | manual, step 7: the worker recalls the step-2 nonce. No recipe can see it |
| 3 | `CLOWN_UNATTENDED=1` and dontAsk survive the relaunch | verify: `relaunched agent environment has CLOWN_UNATTENDED=1` and `argv has --permission-mode dontAsk`, comparing a NEW agent pid with the baseline. The baseline recipe proves they were set before |
| 4 | profile pin intact | verify: `profile pin intact in the record` (name and digest unchanged) and, when a profile was passed at spawn, `same --profile` in the relaunched argv. Skipped with INFO if no profile was pinned |
| 5 | no STALE afterwards | verify: `serve binary is the spinclass on PATH`; authoritative: the worker's `version` tool in step 7. Vacuous unless the PATH binary changed after the baseline (step 3) |
| 6 | troupe credential not revoked | **manual**. Spinclass knows nothing about the troupe credential: it neither stores it nor reads it (`internal/0/clown/clown.go` states there is no troupe shell-out). Proxies only: the same principal (check 1) and the worker answering chat as the same JID (`chat_list`, step 7). The decisive observation is on the clown/troupe side (the mint-revoke state for the principal); never print credential contents. FDR 0037 requires that a restart exit does not run `mint-revoke`; clown 720ffd7 claims it does not (R, tested only against fakes) |
| 7 | #372 allow and moxy argument names live | step 8: supervised absence of a prompt on the real `folio_read`/`folio_ls` (V), plus `explore-live-moxy-allow` replay (the hook's live decision and the argument keys) |

## Manual steps that remain

1. Fix prerequisite FAILs and supervise (operator).
2. Approve the unconfined spawn if it prompts (operator).
3. Install a different spinclass build on PATH between baseline and restart, for a
   non-vacuous STALE check (operator).
4. Say whether a permission prompt appeared on the coordinator's real
   `folio_read` and `folio_ls` (operator).
5. Conversation-resumed check via the nonce (coordinator).
6. Troupe credential not revoked before the final close, revoked after
   (operator; spinclass cannot see it).
7. Tear-down and the revoke-on-close check.

## Not covered

As before: confined workers, forced restart, attached sessions and the posh
countdown, and the close-versus-restart race check (queue another restart and
`sc close` it from a terminal before it idles; expect clown's log to show
`--peek` reporting `closed` and no relaunch).
