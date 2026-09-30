# #349: a madder write failure must never break the pre-merge hook

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to
> implement this plan task-by-task. Task 1 is the fix. Task 2 (the lock
> refresh) runs after Task 1 so one `bats-madder` build proves both. Task 3
> (docs, the upstream madder issue, the closing commit) goes last.

**Goal:** In a dodder session worktree, `sc merge` failed. The issue reads it as
"the hook-output resource_link write failed". The code says otherwise, and the
real failure is worse:

- `runHookPhase` (`internal/check/check.go`) already treats a madder error as
  non-fatal. `madderErr` only becomes the `resource_link_error` output line and
  diagnostic key. The phase returns `hookErr`, never `madderErr`.
- The merge failed because the **hook** failed: "`just` exited 101 after 218 ms
  with empty captured output". The issue files that as separate and
  undiagnosed. It is the same bug.
- The mechanism: on the default `raw` format, the hook's stdout+stderr go to
  `io.MultiWriter(madderStdin, ring, lw)`. The pinned madder (0.3.40 on the
  failing host) exits at once, because it cannot decode dodder's sibling
  `default-default` store config (`!toml-blob_store_config-multi-v1`). The next
  write to its stdin pipe returns `EPIPE`. `io.MultiWriter` stops at the first
  failing writer, so the ring and the live-output writer never get the bytes
  (hence "empty captured output").
- os/exec's copy goroutine then returns and closes the read end of the hook's
  stdout pipe. Stdout and stderr share that pipe, because the same writer backs
  both. `just` is a Rust binary, and Rust ignores SIGPIPE. Its next `print!`
  gets `EPIPE` and panics, and a Rust panic exits with **101**. That also
  explains why a direct `just` run did not reproduce it.

So a broken *attachment* sink killed the *gate*. The fix makes the madder sink
unable to back-pressure into the hook: its writer latches the first error and
swallows the rest, and `finish()` reports the failure. The write also names
spinclass's own store explicitly (`.default`).

**Build/test:** scoped runs only: `just debug-go-test <dir> [run-regex]`, and
`just debug-bats-madder` for the madder-pinned bats lane (`git add -N` new files
first; godyn and `nix build` see only tracked paths). Do NOT run full `just`:
the merge gate runs it.

**Rollback:** revert the commits. There is no knob. The only behaviour change is
that a dead madder no longer kills the hook.

---

## Decision

1. **(a) Scope of the store read: CLI targeting cannot fix this, in any madder
   version.**
   - `madder write` already accepts a store id (`madder-write(1)`: "a
     blob-store-id that switches the active store for subsequent writes").
   - But madder decodes **every** store config before it resolves any id.
     `blob_stores.MakeBlobStores` → `makeBlobStoreConfigs`
     (`go/internal/foxtrot/blob_stores/main.go:39-78` on madder master, the
     frame in the issue's trace) globs every `*/blob_store-config` under the
     cwd `.madder/` tree (the ancestor walk is bounded by
     `MADDER_CEILING_DIRECTORIES`), plus the user XDG root and the system `//`
     root. It calls `ctx.Cancel` on the FIRST decode failure.
   - Master does the same. It just happens to know `multi-v1` today, so a
     future `multi-v2` written by a newer dodder would break a pinned master
     identically.
   - spinclass itself never enumerates stores, so there is no spinclass-side
     "skip with warning" to add. A spinclass pre-scan cannot know which types
     the pinned binary decodes.

   So spinclass will:
   - (i) make the sink fail-soft (the actual gate fix);
   - (ii) address `.default` explicitly in `madder write`. This is hygiene. It
     pins writes to the store `resources.MadderProvider` reads (`cat .default`),
     so a sibling store can never capture the blob. It is not the #349 fix
     (see above);
   - (iii) file the real "tolerate undecodable sibling configs" fix upstream
     against madder (Task 3);
   - (iv) keep the pin current (Decision 3).
2. **(b) Failure semantics.** A madder failure must never fail a gate or a
   merge. Today that holds for the *return value* but not for the *pipe*. After
   the fix, `madder.Write`'s writer never returns an error:
   - The first write error is latched, and later bytes are dropped without a
     syscall.
   - `finish()` returns the process error (with madder's stderr) when madder
     exited nonzero.
   - If madder exited 0 but a write was latched, `finish()` returns a "stdin
     closed early" error. A blob of truncated output must never be linked.

   The existing surfaces (the `resource_link_error` output line and diagnostic
   key, and `storeResultBlob`'s job.log line) are unchanged. The fix goes in
   `internal/madder`, the single funnel, so all three `madder.Write` callers
   (the raw tee, the two structured post-hook writes, and `job.storeResultBlob`)
   inherit it.
3. **(c) The madder bump. Input shape: confirmed correct, and no flake.nix edit
   is needed.**
   - spinclass's `madder` input is `https://code.linenisgreat.com/madder/archive/master.tar.gz`,
     a **branch tarball**. flake.lock records the rev (`1a55d290`, locked
     2026-09-29T20:50Z). The `madder.inputs.*.follows` lines only dedupe
     madder's own inputs onto ours, and nothing makes `madder` follow a
     sha-pinned node.
   - So `nix flake update madder`, and therefore a `circus cascade`, moves it.
     That satisfies the operator's "not pinned to a particular madder sha".
   - **Important:** spinclass's own flake.lock madder feeds only the
     `spinclass-madder` / `bats-madder` lane. The production binary's madder
     comes from the CONSUMER: eng's `flake.nix` passes `inputs.madder` to
     `lib.mkSpinclass` (`spinclassPinned`, and `lib/circus.nix`
     `spinclassPlugin`).
   - eng's `madder` is also a master tarball (locked `9929f577`, 2026-09-28),
     and eng sets `spinclass.inputs.madder.follows = "madder"`. So the fleet
     graph has one madder node, and the cascade moves it.
   - On this host `sc version` reports `madder-0.4.6`, which decodes
     `multi-v1`. The failing host's `madder 0.3.40` (released 2026-06-15) is a
     stale eng lock or an un-switched profile there. Rebuilding eng on that
     host clears it. That is an operator action, not a spinclass change.
   - The spinclass lock refresh is therefore a separate, lane-only commit
     (Task 2), and a no-op if the lock is already current.
4. **(d) The `just` exit 101 is IN scope.** It is not a separate bug: it is the
   failure mechanism described in the Goal, and Task 1's `internal/check` test
   reproduces it deterministically. Do not file it separately. File it only if
   an exit-101-with-no-output recurs *after* this lands with no
   `resource_link_error` beside it. In that case the operator files it with the
   ringmaster job id (`just debug-ringmaster-job <id>`).
5. **(e) Tests.**
   - A unit test with a fixture `.madder` tree holding an undecodable config
     cannot prove anything about spinclass: spinclass never reads those
     configs, and a fake madder only proves what the fake does.
   - The unit tests instead fake the madder *behaviour* the fixture causes:
     madder closes stdin and exits 1 with the real error text before reading.
   - The **bats-madder** lane plants the real fixture: a sibling store dir with
     a config type NO madder registers
     (`! toml-blob_store_config-zz-foreign-v0`), so the case is independent of
     the pinned madder's version. It then runs `sc check` against the real
     pinned binary. No newer madder is needed.

**Record:** amend FDR 0003 (Limitations: the shared `.madder/` tree) and
`spinclass-build-pins(7)` (the write invocation, its failure semantics, and pin
currency). Add one sentence to AGENTS.md. Earlier commits cite
`(#349, task N)`. The final commit carries `Closes #349`.

## Non-goals

- Moving spinclass's store out of the shared `.madder/` tree, or giving the
  pinned madder a hermetic XDG root. dodder's pin deliberately adopts
  `.default` (`spinclass-build-pins(7)` "dodder"), so the shared tree is
  intended.
- A spinclass-side pre-scan or validation of sibling store configs (see
  Decision 1).
- `resources.MadderProvider.ReadResource` (`madder cat .default …`). On an
  outdated pin with a foreign sibling config it still fails. That is a
  read-time MCP error, not a gate failure, and it is fixed by pin currency and
  the upstream madder issue.
- dodder writing stores into spinclass's tree (dodder#401, the dodder half).
- Editing eng's flake or lock (cross-repo; the cascade does it).
- Cutting a spinclass release (the fix reaches the fleet after release, then
  the cascade bumps eng's `spinclass` master-tarball input).

---

### Task 1: fail-soft madder sink, explicit `.default`, and the regression tests

**Context:** spinclass (Go, module `code.linenisgreat.com/spinclass`). When
spinclass is built via `lib.mkSpinclass { madder = …; }`, `embeds.MadderBin()`
is an absolute store path.

`runHookPhase` (`internal/check/check.go` ~371) runs the pre-merge hook via
`hookrun.PreMergeInDir`, which sets the hook's `Stdout` and `Stderr` to one
writer. On the default `raw` format, that writer is
`io.MultiWriter(madderStdin, ring, lw)` (plus `activity`). `madderStdin` is the
writer returned by `madder.Write` (`internal/madder/madder.go` ~80), the stdin
pipe of `madder write -format json -`.

If madder exits before reading, for example because it cannot decode a
SIBLING store config another tool wrote under `<worktree>/.madder/` (madder
decodes every config eagerly and cancels on the first failure), the tee breaks:
1. The next write returns `EPIPE`.
2. `MultiWriter` stops, so `ring` and `lw` get nothing.
3. os/exec closes the hook's stdout pipe.
4. The hook dies of SIGPIPE/EPIPE. `just` (Rust) panics with exit 101.

That is issue #349: a failed *attachment* failed the *gate*. The other
`madder.Write` callers (the structured post-hook writes at check.go ~452/~476,
and `job.storeResultBlob` in `internal/job/runner.go` ~360) write after the
hook exits, so they only ever degraded. They benefit from clearer errors.

**Files:**
- Modify: `internal/madder/madder.go` (`Write` + a small latching writer type)
- Test: `internal/madder/madder_test.go`
- Modify: `internal/check/check.go` (comments only: the `!structured && madderPinned` block ~395-404 and the `runHookPhase` doc)
- Test: `internal/check/check_test.go` (new helper `withEarlyExitMadder`, new test)
- Test: `zz-tests_bats/hooks.bats` (new case, madder-pinned lane)

**Behaviour:**
1. `madder.Write` spawns `madder write -format json .default -`. The store id
   goes between the flags and `-`, because madder applies a store id to the
   args after it. `.default` is FDR 0003's store and the id
   `resources.MadderProvider` reads with `cat .default`. cwd and
   `MADDER_CEILING_DIRECTORIES` are unchanged.
2. The returned `io.WriteCloser` is an unexported `latchingWriter`
   (mutex-guarded):
   - `Write(p)` forwards to the stdin pipe only while no error is latched. It
     latches the first error and ALWAYS returns `len(p), nil`.
   - `Close()` closes the pipe.
3. `finish()` does the following, in order:
   - close the sink;
   - `cmd.Wait()`; a nonzero exit returns
     `fmt.Errorf("madder write: %w\n%s", err, stderr)` exactly as today;
   - if a write error was latched, return
     `fmt.Errorf("madder write: stdin closed before all bytes were written: %w", latched)`
     with id `""`;
   - otherwise parse the JSON id as today. The `store` field newer madders add
     for a non-default store is ignored by the existing struct.
4. The `Write` doc comment says why, in two or three lines: madder decodes
   every store config under the tree before honouring a store id, so a
   sibling config the pinned madder can't decode fails the invocation. A sink
   error must never propagate into the hook's pipe (#349).
5. `check.go` needs no logic change. Update the two comments to say the madder
   sink is fail-soft and cannot affect the hook.

**Failing tests first:**
- `TestWrite_AddressesDefaultStore` (madder_test.go). The fake appends `"$@"`
  to a log, runs `cat >/dev/null`, and prints
  `{"id":"x","size":0,"source":"-"}`. Assert the log is exactly
  `write -format json .default -\n`.
- `TestWrite_WriterSurvivesEarlyExit`. The fake runs `exec 0<&-`, then
  `echo 'madder: no coders available for type: "!toml-blob_store_config-multi-v1"' >&2`,
  then `exit 1`. Write 256 KiB in 4 KiB chunks. The total exceeds any pipe
  buffer, and the fake never reads, so an `EPIPE` is certain. Assert every
  `Write` returns `(4096, nil)` and `Close` returns nil. `finish()` returns
  id `""` and an error containing `no coders available`.
- `TestWrite_TruncatedInputWithZeroExitIsAnError`. The fake runs `exec 0<&-`,
  prints `{"id":"sha256-partial","size":0,"source":"-"}`, and runs `exit 0`.
  Write 256 KiB. Assert `finish()` returns id `""` and an error containing
  `stdin closed before all bytes were written`.
- The existing `TestWrite_*` and `TestInit_*` tests stay green unchanged.
  `TestWrite_PropagatesNonZeroExit` still sees stderr in the error.
- `TestRunHookPhase_MadderEarlyExitDoesNotBreakHook` (check_test.go).
  - Add a helper `withEarlyExitMadder(t)` shaped like `withFakeMadder`: its
    `init)` arm creates the `default/blob_store-config` marker; its `write)`
    arm does the early-exit dance from `TestWrite_WriterSurvivesEarlyExit`.
    Install it via `embeds.Set` and restore it in `t.Cleanup`.
  - Use `setupRepoWithWorktree(t, "feature-madder-dead")` and
    `writeSweatfile(t, wtPath, "[hooks]\npre-merge = \"seq 1 20000; echo tail-marker\"\n")`.
    That is about 109 KiB, so the dead pipe is hit deterministically.
  - Call `links, recs, err := runCheck(t, wtPath)`.
  - Assert:
    - `err == nil`;
    - `len(links) == 0`;
    - `assertNodeEndExit(t, recs, 0)`;
    - `outputText(recs)` contains `tail-marker`, `20000`,
      `resource_link_error:` and `no coders available`.
  - **Red today:** the hook is killed by SIGPIPE, so `err != nil` and the lines
    are missing.
- `pre_merge_raw_hook_survives_undecodable_sibling_store` (hooks.bats, after
  the tap-ndjson cases).
  - Start with `require_madder_pinned`. Call
    `pre_merge_setup_worktree` with
    `[hooks]\npre-merge = "seq 1 20000; echo tail-marker"\n`. The format stays
    raw by default.
  - Plant `.madder/local/share/blob_stores/zz-foreign/blob_store-config`
    containing `---\n! toml-blob_store_config-zz-foreign-v0\n---\n` (use
    `printf`). The real pinned madder cancels on it.
  - Run `run_sc_crap check`. Assert:
    - `assert_success`;
    - `assert_crap '[.[] | select(.type == "node_end")] | all(.exit_code == 0)'`;
    - `assert_output --partial tail-marker`;
    - `assert_output --regexp 'resource_link(_error)?: '`. Either outcome is
      accepted, so the case stays green if madder later gains tolerance (Task 3
      step 0). The invariant is that the hook survives with its output intact.
  - This also proves the real binary accepts the new `.default` argv (and so do
    the existing tap-ndjson `resource_link: madder://blobs/` cases in the same
    lane).

**Implement**, then prove with:
- `just debug-go-test internal/madder`;
- `just debug-go-test internal/check TestRunHookPhase`;
- `just debug-go-test internal/job` (`storeResultBlob` callers are unaffected);
- `just debug-bats-madder`.

Commit: `fix(madder): a dead madder sink can no longer kill the pre-merge hook (#349, task 1)`.

---

### Task 2: confirm the madder input tracks master; refresh the lane's lock (after Task 1)

**Context:** spinclass's `flake.nix` input `madder` (~line 49) is the branch
tarball `https://code.linenisgreat.com/madder/archive/master.tar.gz`, and
`flake.lock` records the rev. It feeds ONLY `spinclass-madder =
mkSpinclass { madder = madder.packages.${system}.default; }` and the
`bats-madder` lane.

The production binary's madder is chosen by the consumer: eng's
`spinclassPinned` / `spinclassPlugin` pass eng's own `inputs.madder`, also a
master tarball, with `spinclass.inputs.madder.follows = "madder"`. The
operator asked that madder not be sha-pinned, so that `circus cascade` bumps
it. The input shape already satisfies that. This task confirms it and
refreshes the lane lock. It is not the #349 fix (Task 1 is).

**Files:**
- Modify (maybe): `flake.lock` only. Do not edit `flake.nix`.

**Steps:**
1. Confirm without editing:
   - `madder.url` is the `…/archive/master.tar.gz` form;
   - no line makes `madder` itself `follows` another node (only
     `madder.inputs.*.follows`, which dedupe madder's inputs);
   - `flake.lock`'s `nodes.madder.original.url` is the master tarball
     (`jq_jq` over `flake.lock`: `.nodes.madder.original`).

   If any of these fail, stop and report. It would be a regression that needs
   an operator decision.
2. Update the lock. spinclass has no input-bump recipe (`list_recipes`), so use
   `chix_flake-update` with `args: ["madder"]` and `flake_dir` set to this
   worktree. Do not add a recipe for a one-off.
3. If `flake.lock` is unchanged, the lane is already current. Do not commit,
   and say so in the report with the locked rev and date.
4. If it changed:
   - Check the pinned version at eval time (no build) with
     `chix_derivation-show`, `path: ".#spinclass-madder"`,
     `jq_filter: '[.. | strings | select(test("/nix/store/[a-z0-9]{32}-madder-[0-9]"))] | unique'`.
   - Expect a single `madder-X.Y.Z` path at or above `0.4.6`, the version this
     host runs, which registers `!toml-blob_store_config-multi-v1`. `0.3.x` is a
     failure.
   - Then `just debug-bats-madder` must pass. It exercises the new pin with
     Task 1's `.default` argv and the new sibling-store case.
   - No downstream test is expected to change. If a madder CLI change breaks the
     lane, fix it in this commit when it is mechanical. Otherwise file it with
     `eng:file-issue` and stop.
   - Commit only `flake.lock`: `bump madder: <old7> → <new7>` (the repo's
     `bump <input>: a → b` convention).

**Verify:** `git diff --stat` touches only `flake.lock` (or nothing).

---

### Task 3: docs, the upstream madder issue, and the closing commit (after Tasks 1–2)

**Context:** spinclass (Go, `code.linenisgreat.com/spinclass`). Tasks 1–2 did
the following (#349):
- `madder.Write` now spawns `madder write -format json .default -`.
- Its writer latches the first error and never fails, so a madder that dies
  early (e.g. it cannot decode a sibling store config another tool, such as
  dodder (dodder#401), wrote under `<worktree>/.madder/`) can no longer break
  the pre-merge hook's pipe. Before this, `just` died with exit 101 and empty
  output.
- `finish()` reports the failure, and a zero-exit madder that closed stdin
  early is an error too (a truncated blob is never linked).
- The lane's madder input is a master tarball, so the cascade moves it. The
  production madder is the consumer's (eng's) `inputs.madder`.

No behaviour changes in this task.

**Steps:**
0. File the upstream fix with the `eng:file-issue` skill against
   `linenisgreat/madder` (dedupe first). Title: "blob_stores: skip (warn)
   sibling blob_store-configs this build cannot decode instead of cancelling
   the env".
   - Body: `makeBlobStoreConfigs` (`go/internal/foxtrot/blob_stores/main.go`)
     calls `ctx.Cancel` on the first undecodable config. So every invocation
     fails whenever ANY store under the cwd `.madder/` tree, the user XDG
     root, or the system root was written by a newer madder, even when the
     addressed store (`write .default …`, `cat .default …`) decodes fine.
   - Cite spinclass#349 and dodder#401.
   - Record the number N and add a task-list entry for it.
1. `docs/features/0003-per-worktree-madder-blob-store.md`. Do not change
   `status`.
   - Under the H1 add
     `> **Amended 2026-09-30 (#349):** shared-tree tolerance.`
   - In **Limitations**, add the bullet **Shared `.madder/` tree.** It covers
     these points:
     - other tools (dodder) may add stores beside `default/`;
     - madder decodes every sibling config on each invocation, so a pinned
       madder older than the writer fails every write and read here;
     - the resource_link write targets `.default` explicitly and is
       fail-soft: no link, a `resource_link_error` line, and never a failed
       hook or merge;
     - the cures are a current pin (the consumer's `madder` input tracks
       master) and madder-side tolerance (madder#N).
2. `doc/spinclass-build-pins.7.scd`, in `## madder`. After the "compact
   response shape" paragraph (~line 67), add a paragraph covering:
   - the blob is written with *madder write -format json .default -*;
   - a failed write, for example when the pinned madder cannot decode a store
     config another tool added under _.madder/_, is reported as
     *resource\_link\_error* and never fails the hook, check or merge;
   - keep the pin current: the consumer flake's *madder* input should be a
     branch tarball (…/archive/master.tar.gz) so *nix flake update madder* and
     the fleet cascade move it. spinclass's own lock only feeds its
     madder-pinned test lane.

   Escape `_` as `\_`, as the neighbouring lines do.
3. `AGENTS.md` (CLAUDE.md is a symlink). **Hard cap 40000 bytes. It is at 39393
   now.** In the **External tool deps** bullet, after `(madder dormant, direnv
   from PATH).`, insert one sentence of at most ~260 bytes:
   "A madder write failure never fails a gate: `madder.Write` targets `.default`
   and its writer latches errors (madder decodes every sibling `.madder/` store
   config, so a stale pin died early and broke the hook's pipe — #349)."
   Reflow to about 80 columns. Confirm the size is ≤ 40000 with `folio_ls`
   flags `-la`. If it is over, drop the parenthetical first.
4. Commit the docs. The message carries `Closes #349` on its own line.
5. In the final report, tell the operator:
   - (a) The failing host's `sc` carried madder 0.3.40 (a June release), while
     this host's reports madder 0.4.6, which decodes `multi-v1`. Rebuild or
     switch eng on that host after the cascade bumps eng's `madder` input.
     spinclass follows it, so no spinclass pin exists to move.
   - (b) The spinclass fix reaches the fleet only after a spinclass release and
     the cascade bumping eng's `spinclass` input.
   - (c) dodder#401 (dodder writing stores into spinclass's tree) remains the
     dodder-side half.
   - (d) madder#N is the durable tolerance fix.

**Verify:** no code changes. Check the AGENTS.md size and that the manpage
still renders (the merge gate's build runs scdoc). Do not run `just`.
