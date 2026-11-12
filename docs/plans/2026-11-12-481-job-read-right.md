# A read-only job lookup right for handle holders (spinclass#481): design note

Status: DESIGN ONLY, 2026-11-12. Nothing built. It adds a right to FDR 0032
D13's vocabulary and reads another session's ringmaster jobs, so it waits on
the operator's rulings in §7. Evidence labels: **V** read in code, a
manpage or a record named here, **I** inference, **U** unknown.

## 1. The problem (from the issue)

A coordinator held a handle with `observe`, `merge` and `check` on a child.
It asked ringmaster `job_wait` for the child's merge job (`merge-bfc0afcf`)
and got `no such job`. ringmaster scopes `job_status`, `job_read`,
`job_wait` and `job_output` to the calling session's own jobs. So the
coordinator polled origin/master and asked the child by chat instead.

Facts (**V**, ringmaster(1) FILES and SESSION KEY):

- A job lives at `$XDG_STATE_HOME/ringmaster/jobs/<channel-id>/<job-id>.jsonl`
  (the journal: `started`, `progress`, the terminal record with its state,
  message and `resources`) and `<job-id>.out` (the output spool).
- `<channel-id>` is derived from the session key, so "scoped to the
  caller" is a directory choice, not an access control: the files belong
  to the same uid.
- flic's `[folio]` stanza denies `home:.local/state/ringmaster` to every
  session's moxy tools (circus `spinclass-workstation.nix`, credential-bearing
  state, circus#417). So the files cannot be read around ringmaster either.

## 2. What is in a job, by sensitivity

| part | spinclass merge/check job | other jobs (moxy async, just-us async, ringmaster producers) |
|---|---|---|
| id, source, label, state, started/ended, elapsed, liveness | low | low (the label can name a tool; **U** whether it carries args) |
| terminal message | low: the merge verdict and the landed sha or failing stage | **U**: a moxy async summary may quote the tool result |
| `resources` (madder blob refs) | the hook output blob | the tool's full result blob |
| output spool / tail | the pre-merge hook's build and test output: store paths, test names, sometimes environment the hook prints | the tool's output: file contents, command output, possibly secrets |

The issue's worry ("job output can contain sensitive tool args") is real for
the right-hand column. For the left it is mostly theoretical, but a hook that
echoes its environment, or a test that prints a token, would land in the
spool. **I**: redacting arbitrary build or tool output is not something
spinclass can do reliably. A redaction rule would give false comfort.

## 3. Proposal

### 3.1 The right

A new object right **`jobs`**: "read the status, terminal record and output
of *that* session's jobs". It is the ninth name in the D13 vocabulary
(`spinclass:jobs` in the namespaced form). It is:

- **not** folded into `observe` (the issue's ask, and D13's own pattern:
  `restart` was added beside `observe`, not inside it). `observe` is
  implied by holding at all, and `jobs` reveals output, which a lazy
  `observe,close` handoff must not;
- **not** in the default grant (`DefaultHandleRights` stays
  `observe,close`);
- minted to the spawner with the rest (the spawner already gets every
  right, D13);
- grantable only by a holder that has it (D11, D13: narrowing only);
- **read-only**: no cancel, resolve, or wait-and-consume. Cancelling stays
  with `merge`/`check` (issue item 3). Waiting is reading: a `wait` that
  blocks until terminal is allowed, since it changes nothing.

### 3.2 Two tiers, by what the holder sees

- **Status** (the left column's first two rows, for every job of the
  session): id, source, label, state, timestamps, liveness, and the
  terminal message for jobs whose source is `spinclass`. Granted by `jobs`.
- **Output** (the spool, tail and `resources`): granted by `jobs` **only for
  jobs whose source is `spinclass`** (merge, check, restart outcome), whose
  content is the session's own gate output. For every other source, output
  needs an operator ruling (§7 Q3); the default is no.

That is the redaction answer: **select by job source, do not filter text.**
Tool arguments and tool results stay out of reach unless the operator rules
otherwise.

### 3.3 Where the check runs

The issue asks for the lookup to go through spinclass or ringmaster "with
the handle checked, not by widening ringmaster's per-session scoping for
everyone".

- **Recommended: spinclass checks, ringmaster serves.** A new spinclass tool,
  `session-jobs` (list and status) with `job` and `tail`/`wait` parameters,
  resolves the child (`session.FindByTarget`), checks the caller holds
  `jobs` on it, then reads through ringmaster for the child's session key.
  ringmaster stays the one reader of its files (D7: "the job is the
  host-local enforcement point"); spinclass only decides who may ask.
- **What ringmaster has to offer (ringmaster's lane, not designed here):**
  a read entry point that takes a target session key from a trusted local
  caller, for example the linked `jobwake` library or a socket verb.
  ringmaster's MCP tools already take a `target` channel override (**V**,
  their schemas), but those are the session's own tools. Whether a
  spinclass serve may read another channel through them, or needs a
  library call, is ringmaster/troupe's to decide. I'd hand the question to
  ringmaster's session rather than design it here.
- spinclass must **not** read `$XDG_STATE_HOME/ringmaster/jobs/` directly:
  that would duplicate ringmaster's file format and route around its owner.

### 3.4 Listing and audit

- `whoami` and `list-handles` show `jobs` in the rights list (issue item 4),
  through the existing `HandleRightNames`.
- Each output read (not status reads) appends a line to the handle journal
  proposed in #480's note (`act: job-read`, `child`, `job`, `by`), so the
  operator can see who read whose gate output.

### 3.5 ajour (issue item 5)

`session-jobs` is a spinclass verb with declared atoms: the verb, the
`child` target, the `job` id, the `part` (`status` or `output`) and the job
`source`. An operator-act profile can then narrow it further with ajour's
`Evaluate`, for example "status only" or "output for spinclass jobs only".
The handle right is one stencil in that stack (cutting-garden FDR 0028:
spinclass is named as one of the three enforcers "for its verbs").

## 4. FDR 0032

- **D12.** Unaffected, except that with #480 a `jobs` grant is effective at
  once and the recipient is told.
- **D13.** The ninth right, as §3.1. It is an object right with no ambient
  counterpart: reading a job is not execution, so it needs no
  execution-class ambient right (unlike `merge`/`check`).
- **D19.** Not engaged: reading a job is neither scheduling nor a budget. It
  also does not touch ringmaster's ownership of scheduling.

## 5. What it does not do

- No access to jobs of sessions the caller holds no handle on.
- No cancel or resolve (issue item 3).
- No direct read of ringmaster's files.
- No text redaction (§3.2).

## 6. A smaller step first

If the operator wants the coordinator's case unblocked before ringmaster has
a cross-session read, spinclass can serve **its own jobs only**: the async
merge and check jobs it ran for the child.

- They are recorded worktree-locally. `internal/alfa/job` (**V**) keeps
  state under `<worktree>/.spinclass/`, with the pre-merge hook's live
  output in a sibling `job.log`, and reports a job whose owning serve has
  died as interrupted.
- The holder's serve, after the `jobs` check, reads the child's job state
  and tails its `job.log`. No ringmaster change is needed.
- That covers the observed failure (waiting on a child's merge) with both
  status and output, and nothing beyond spinclass jobs.
- **Waiting** has to be a poll inside the tool (bounded, like `smith`'s
  `pr-wait`). spinclass's own state has no wake channel for a third
  session.
- The holder's moxy tools still cannot read that directory. flic's
  `[folio]` denies `spinclass:worktrees/*/.spinclass`, and this tool does
  not change that: serve reads it on the holder's behalf, after the check.

## 7. Open questions for the operator

1. Name and place: a separate `jobs` right (recommended), or part of
   `observe`? The issue leans separate.
2. Tiers (§3.2): status and spinclass-job output under `jobs`. Is that
   split right, or should output be a second right (`jobs-output`)?
3. Non-spinclass job output (moxy async results, just-us async runs): never
   through this right (recommended for v1), or under `jobs` with a ruling on
   sensitivity?
4. Where the check runs (§3.3): spinclass checks, and ringmaster serves the
   cross-session read. May I hand ringmaster the "read another channel for a
   trusted local caller" question?
5. The smaller first step (§6): build that now, spinclass jobs from the
   child's worktree state, and the ringmaster-backed full right later?
   Recommended: it unblocks the observed case with no cross-repo work.
6. Audit (§3.4): journal output reads only, or status reads too?
