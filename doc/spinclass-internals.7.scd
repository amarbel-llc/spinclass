spinclass-internals(7) "spinclass" "Spinclass Reference"

# NAME

spinclass-internals - non-obvious mechanisms that have no design record of their own

# DESCRIPTION

Contributor notes for mechanisms whose rationale is not captured by an FDR.
Design records live in *docs/features/*; config knobs in
*spinclass-sweatfile*(5). Read the relevant record or page before changing the
subsystem named here.

# ASYNC JOB LIVENESS (spinclass#26)

An async merge/check is a ringmaster job (FDR 0010). While it runs, *serve*
holds ringmaster's per-job advisory lock (*jobwake.AcquireJobLock*) so a
crashed *serve* is detectable: the OS drops the lock and the reaper writes
*interrupted* instead of leaving a stuck *running*. The lock must be an
in-process file descriptor, which is why *jobwake* is _linked_ at build time
(the *go.nix* bridge) in addition to the runtime *ringmaster* CLI on PATH.
The other jobs *serve* starts hold it the same way (#368): the async
*spawn-session* dispatcher while it waits for the worker's hello, and the
merge-queue notice job (*clown.HoldJobLock*, which also skips the lock for an
empty job id).

*clown.CheckProtocol* (memoized) compares the compiled-in
*jobwake.ProtocolVersion* with *ringmaster version --protocol*. An exact match
enables the flock. A mismatch degrades loudly and skips only the flock: async
wakes and cancel observation are pure CLI shell-outs and are unaffected by
skew, so only crash auto-reap is lost. *TestCheckProtocolMatchesRealRingmaster*
guards the drift against the checkPhase-pinned binary.

# CANCELLATION OBSERVER (spinclass#22)

*ringmaster cancel* is cooperative: it records a non-terminal
*cancel-requested* and wakes; it signals no process. Each async job therefore
runs an observer goroutine (*clown.WaitForCancel*, gated on *clown.Enabled()*
alone, so it survives a ProtocolVersion skew) that fires the job's context
cancel when a cancel-requested appears. The producer is the sole
terminal-writer: the terminal state is *aborted*, and the reaper writes
*interrupted* only if *serve* dies mid-teardown.

Non-obvious: *ringmaster wait --on-cancel* reports the _derived_ state
*running* for a cancel-requested job, never *cancel-requested*. Its only stop
conditions are a terminal or a cancel-requested, so a non-terminal return means
cancel-requested was observed. *clearRunning* joins the observer after
*cancel()* (which kills its *ringmaster wait* subprocess) before closing
*done*, so a woken caller is guaranteed the subprocess is already reaped.
Locked by *TestWaitForCancelObservesRealCancelRequested*.

# PRE-MERGE HOOK SYSTEMD SCOPE (spinclass#25)

The pre-merge hook runs under *systemd-run --user --scope*
(*clown.ScopeArgv*); on cancel, *clown.ScopeStop* kills the whole cgroup
(*TestPreMergeScopeReapsSubtreeOnCancel*). The scope id is
*clown.ScopeIDFromContext*: the async job id, or a *sync-...* local id
(*WithLocalScope*, its own context key, never sent to ringmaster). Post-merge
passes an empty id. With no scope tier (no user bus: sandbox, macOS) the hook
runs bare. A scope that fails to _set up_ falls back once to the bare hook with
a warning; a start marker written inside the scope proves a started hook is
never re-run. *[hooks].require-hook-scope* makes that fallback fatal (opt-in
now, default later, fallback eventually removed). The real scope path is
dogfooded, not CI. Signal and SIGTERM-versus-SIGKILL details:
*docs/plans/2026-10-04-188-sync-hook-cancellation.md* (spinclass#188).

# sc exec

*sc exec [--session <t>] [-- <util> ...]* (*internal/bravo/sessionexec*) runs a util
in a session worktree, devshell-scoped via *direnv exec*, with the
*SPINCLASS* identity env vars. With no *--session* it auto-detects from the cwd; the util
defaults to *$SHELL*. An explicit *--session* miss is an error; a cwd miss
degrades (runs in the cwd and strips the identity env). CLI-only, raw
passthrough. Motivating consumer: posh "escape to shell".

# CLOSING SEVERAL SESSIONS (purse-first#190)

*sc close A B C* closes each target independently and exits nonzero if any
failed. *target* is variadic, so params after it are flag-only (*force* and
*nix-gc* keep their positions). Zero or one target delegates to *close.Run*
(flat output unchanged); more than one emits one TAP document with a subtest
per target (*close.RunMany*, with *runResolvedInto* writing into the
caller-owned document). Nix gc reaps per target, because each plan is captured
before its worktree is removed.

# CONFINEMENT AGENT CENSUS (FDR 0035 F11)

*sc confinement-check* fact F11 counts agents by unit (*internal/bravo/confinement*).
Every live session needs a unit record whose cgroup, under
*clown-agent.slice*, holds at least one process now; a record whose cgroup is
gone or empty is named as a *stale record*. A process is confined iff its
cgroup lies within a running unit. Only the search for a runtime outside every
unit still goes by */proc/<pid>/comm*: the fallback list *DefaultAgentComms*
(*claude*, *.claude-wrapped*, *juggler*) plus every *harness* a live unit
record reports clown exec'd, cut to 15 bytes. That search is spoofable and is
a deprecated fallback, kept until clown records every agent it launches. F2's
ancestry walk also refuses an ancestor inside an agent unit. The passing
detail reads "N live sessions in running units; no <names> outside them".
Design: *docs/plans/2026-10-20-agent-census-by-unit.md*.

# PRETOOLUSE VERDICT LOG

*Run* in *internal/delta/hooks* captures what the PreToolUse handler writes and
appends one NDJSON line per spinclass MCP tool decision to
*.spinclass/hook-verdicts.ndjson* before copying the bytes to Claude Code
unchanged (other tools are never logged; their inputs may carry secrets). It is
best-effort, capped at 256 KiB, and never creates *.spinclass*. It is an
observability aid, not an authority: the agent can write its own worktree. See
*spinclass-grit*(7) DEFAULT PERMISSIONS; *just debug-hook-verdicts* prints it.

# READ-ONLY MOXY AUTO-ALLOW (spinclass#372)

Where no other rule decides, the PreToolUse hook allows (never denies or asks)
these moxy read tools when every path argument resolves inside the caller's own
worktree or the worktree of a live session it holds an *accepted* handle on
(FDR 0032 D12; a pending grant confers nothing): *folio\_read*, *folio\_ls*,
*folio\_glob*, *folio\_tree*, *folio\_file-type*, *grit\_branch* with
subcommand log, rev-parse or diff, and *grit\_stage* with subcommand status or
diff. A missing path means the working directory. Paths are symlink-resolved and
compared separator-aware, so *..*, symlink and prefix-collision escapes get no
decision; *folio\_glob* patterns with a *..* segment or a leading */* and
*madder://* file paths get none either, and neither does another session's
*.spinclass/*. Writes, other paths and whole-tool allows are out of scope. The
always-ask floor, subagent rules, the *.spinclass* guard and *dontAsk* all run
first.

# SEE ALSO

*spinclass-sweatfile*(5), *spinclass-build-pins*(7),
*spinclass-local-default-ref*(7)

spinclass FDR 0010 (clown job-wakeup producer), FDR 0013, FDR 0022.
