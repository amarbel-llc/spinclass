# #219 / #303: consume the pre-merge attestation only when the merge lands

> **For Claude:** implement task-by-task with a fresh subagent per task
> (eng:test-driven-development: failing test first). Tasks 1 and 2 are
> independent of each other. Task 3 depends on both. Task 4 (docs, tool
> descriptions, the closing commit) goes last.

**Goal:** Stop burning the FDR 0007 attestation on an attempt that lands
nothing. The gate currently clears `session.State.PreMergeAttestation` when an
MCP merge/check *commits*: before the rebase for sync `merge-this-session`,
before the hook for `check-this-session`, and at dispatch/enqueue for the
async twins. Every later failure therefore forces a byte-identical
re-attestation:

- a red pre-merge hook (#219);
- a fetch refused by a locked ssh agent (#219 comment);
- a REPAIR refusal on a dirty tree (#303);
- a rebase conflict (#303 comment).

That trains agents to re-paste JSON instead of re-reviewing. After this change,
an attempt **claims** the attestation. The attestation is **consumed** only when
the merge lands, or when the check passes. Every other outcome **releases** the
claim, and the attestation stays buffered for the retry. #219 subsumes #303.

**Build/test:** scoped runs only: `just debug-go-test <dir> [run-regex]`.
Run `git add -N` on new files first, because godyn only sees tracked paths. Do
NOT run full `just`: the merge gate runs it.

**Rollback:** none (no knob). Revert the commits.

---

## Decision

1. **What consumes, what keeps.** A *hold* is the claim one attempt has on the
   attestation that admitted it.
   - **Consumed** (the hold settles as `landed`):
     - sync `merge-this-session` whose `merge.Resolved` returned nil;
     - an async or queued merge job whose `FinishMerge` returned nil;
     - `check-this-session` / `-async` whose hook passed (`hookErr == nil`).

     A landed merge whose post-merge target failed still consumes, because
     post-merge is non-fatal (FDR 0023). So does a landed merge whose
     local-default advance was skipped (#295 SKIP).
   - **Kept** (the hold settles as released; the attestation stays buffered):
     - any `PrepareMerge` failure: fetch, merge-driver preflight, rebase,
       conflict check, nothing-to-merge, REPAIR, target validation;
     - any `FinishMerge` failure: hook red, inactivity timeout, integration
       conflict, refused push;
     - cancel (`session-job-cancel`, or an external ringmaster cancel, which
       ends the job `aborted`);
     - a queued entry that is drained after its predecessor failed (FDR 0025),
       or that fails to dispatch at dequeue;
     - a `job.Start` refusal;
     - a red or cancelled check.

     Refusals before the claim (bad params, busy with stacking disabled,
     implicit-session merge, missing attestation) never touch the attestation,
     as today.
   - An exemption-admitted merge (`GateNeedsExemption`) and a terminal merge
     (`GateTerminal`) hold nothing and consume nothing. That is unchanged.
2. **Claim, not clear, at commit time.** The hold is written into the
   attestation itself as `session.PreMergeAttestation.Claim{ID, PID, ClaimedAt}`.
   - A claim whose PID is alive (`session.IsAlive`) makes the attestation
     **unavailable** to every other attempt, so `Peek`/`Claim` refuse. This
     keeps FDR 0025's rule that each batch needs its own attestation: a second
     `merge-this-session-async` while the first still holds it is refused
     (or admitted by exemption), exactly as when the first one cleared it.
   - A claim whose PID is **dead** is void, and the attestation is available
     again. So a serve crash mid-gate never burns it (item 4).
   - `Settle(hold, landed)` compares the buffered attestation's `RecordedAt`
     and `Claim.ID` with the hold.
     - If both match: `landed` clears the attestation, and anything else
       clears only the claim.
     - If they do not match (the agent re-recorded while the attempt ran):
       Settle is a no-op. The newest attestation always wins the single slot.
3. **Staleness: bind a sha, never block on it.** `nothing-but-the-truth` records
   `HeadSha`, the session worktree's `HEAD` at attest time (best effort, `""`
   on error).
   - The attestation stays valid when the tip moves. #219's own repro is a
     one-line test fix committed after a red hook, and a strict HEAD pin would
     re-impose the five-skill re-run the issue exists to remove.
   - FDR 0007's intent (skills were run against *this* diff) is kept by making
     drift **visible**. The GateAttested verdict gains a suffix when the pinned
     branch carries commits the attestation never saw:
     `✓ pre-merge policy: attested; 2 commits since the attestation at <sha12>: <sha12> <sha12>`.
   - The count is patch-id aware (`git rev-list --right-only --cherry-pick`),
     so a pure rebase onto a moved default reads as plain `attested`.
   - Re-recording is the agent's call when the fix changed what the skills
     reviewed.
   - No time expiry. The lifetime ends at a landing, at a re-record, or at the
     end of the session.

   Rejected alternatives:
   - A strict HEAD pin: it fails #219's repro.
   - A cumulative-diff patch-id fingerprint: it flips on any conflict-resolved
     rebase and says nothing about *what* changed.
   - A TTL: no evidence it is needed.
4. **Crash and cancel.** Cancel is an ordinary non-landing settle: the job
   returns and the closure settles `false`. A serve crash (the #26 flock lets
   the reaper write `interrupted`) needs no cooperation. The claim's PID is
   dead, so the next `Peek`/`Claim` treats the attestation as free. The
   in-process merge queue dies with serve, and so do its holds (all on the one
   slot, all void). Every settle is `defer`red, so a panic in the sync handler
   (recovered by the MCP layer, serve still alive) cannot strand a live claim.
5. **Check tools consume on green, symmetric with merge.**
   `check-this-session`(-async) is registered only under
   `[hooks].disable-merge`, where a green check IS the gated success moment.
   - A red check keeping the attestation is exactly #219's scenario.
   - Never consuming would turn the gate into the once-per-session ritual that
     FDR 0007 rejects.
   - A check whose session identity failed to resolve (`gitErr`) still runs
     ungated, as today.
6. **Where the state lives and how writes are ordered.** The state stays in
   session state JSON: the worktree state file, or the per-randID implicit
   file. `attestation` gains a `Slot` type (worktree or implicit) so one set of
   functions serves both.
   - An in-process `sync.Mutex` in `internal/attestation` serializes each
     Record/Claim/Settle read-modify-write.
   - `mergeQueueMu` still wraps the async handler's busy-decision + claim +
     enqueue, so two async merges cannot both enqueue on one attestation.
   - Cross-process and cross-writer races are unchanged from today, and
     recorded as a limitation. Other `session.Write` callers
     read-modify-write the whole `State`, so a stale concurrent write can drop
     a claim or resurrect a consumed attestation.

**API impact:**
- `session.PreMergeAttestation` gains `HeadSha string` (`head_sha`) and
  `Claim *AttestationClaim` (`claim`). New type `AttestationClaim{ID, PID, ClaimedAt}`.
- `attestation` gains `Slot`, `WorktreeSlot`, `ImplicitSlot`, `Ticket`,
  `Claim`, and `Settle`.
  - `Record` becomes `Record(slot, skills, headSha)`.
  - `Peek` becomes `Peek(merged, slot)`.
  - `RecordImplicit`, `PeekImplicit`, `Check`, `CheckImplicit`, `Consume` and
    `ConsumeImplicit` are deleted.
- `merge.PostMergeOptions` gains `AttestedSha`.
- In `cmd/spinclass`:
  - `startSessionJob` gains an `onNotStarted func()` parameter.
  - `queuedMerge` gains `release func()`.
  - `buildQueuedMergeRun` gains a hold parameter.
  - `resolveGatedSession`, `enforceAttestation`, `enforceAttestationImplicit`
    and `consumeGate` are deleted, and `holdGate` is added.

**Record:** amend FDR 0007 (Lifecycle, State persistence, Limitations), FDR 0025
(Attestation semantics, Concurrency, Interface), and FDR 0031 (decision table,
verdicts). Update the roadmap's FDR watch: 0025 is unblocked. Do NOT change any
FDR status here. Commits cite `(#219, task N)`, and the final commit closes
#219 and #303.

## Non-goals

- A file lock around session state writes (the cross-writer race predates this).
- Changing FDR 0025/0007 status.
- Any CLI (`sc merge`/`sc check`/`sc run`) change: the terminal path holds nothing.
- Persisting the merge queue.
- A per-queued-entry cancel surface.

---

### Task 1: attestation holds (`Slot`, `Ticket`, `Claim`, `Settle`) and `HeadSha`

**Context:** spinclass (Go, module `code.linenisgreat.com/spinclass`). FDR 0007
(`docs/features/0007-pre-merge-skill-attestation.md`) gates MCP merges behind a
buffered `session.State.PreMergeAttestation`. `nothing-but-the-truth` records
it. Today `internal/attestation/attestation.go` offers:
- `Peek`, which checks the attestation without consuming it;
- `Consume`, which clears it;
- `Check`, which is Peek + Consume;
- `*Implicit` twins of each, for main-checkout sessions.

The MCP handlers in `cmd/spinclass/commands_mcp_only.go` consume when a merge
*commits*, so every later failure burns it (issues #219, #303). This task adds
the primitives for "claim at commit, consume only on landing". The old
`Check`/`Consume`/`CheckImplicit`/`ConsumeImplicit`/`PeekImplicit` stay
(re-implemented over the new `Peek`) so the build stays green; Task 3 rewires
the handlers and deletes them.

**Files:**
- Modify: `internal/session/session.go` (the `PreMergeAttestation` struct at
  ~L175 plus the new `AttestationClaim`; update the field comment at ~L141)
- Test: `internal/session/pre_merge_attestation_test.go`
- Modify: `internal/attestation/attestation.go`
- Test: `internal/attestation/attestation_test.go` (reuse `setupGateSession`
  and `setupImplicitSession`)
- Modify: `cmd/spinclass/commands_mcp_only.go`, two call sites only:
  `handleNothingButTheTruth` (~L1165) and `peekGate` (~L821)

**Behaviour:**
1. Session schema, in `internal/session/session.go`:
   ```go
   type PreMergeAttestation struct {
       RecordedAt time.Time        `json:"recorded_at"`
       Skills     []AttestedSkill  `json:"skills"`
       HeadSha    string           `json:"head_sha,omitempty"` // session HEAD at record time; "" if unknown (#219)
       Claim      *AttestationClaim `json:"claim,omitempty"`   // non-nil while an attempt holds it (#219)
   }
   // AttestationClaim marks the attestation as held by one in-flight merge or
   // check. It is void once PID is dead, so a crashed serve never burns it.
   type AttestationClaim struct {
       ID        string    `json:"id"`
       PID       int       `json:"pid"`
       ClaimedAt time.Time `json:"claimed_at"`
   }
   ```
   Rewrite both doc comments ("single-use: the next gated MCP tool consumes…")
   to: claimed by the attempt it admits; consumed only when that merge lands
   or that check passes; released otherwise.
2. `internal/attestation`:
   - `type Slot struct { load func() (*session.State, error); store func(session.State) error; missing string }`,
     with constructors:
     - `WorktreeSlot(repoPath, branch string) Slot`. It loads with
       `session.Read`, stores with `session.Write`, and its `missing` is
       today's "could not read session state to verify attestation; this MCP
       tool requires a tracked spinclass session".
     - `ImplicitSlot(checkout string) Slot`. It loads with
       `session.FindImplicitAtCwd`; a nil state is an error ("no live implicit
       session at <checkout>"). Its store re-finds the randID and calls
       `session.WriteImplicit`. Its `missing` is today's "could not find a
       live implicit session at checkout to verify attestation; the session
       may have ended".
   - `type Ticket struct { RecordedAt time.Time; HeadSha string; ClaimID string }`.
     The zero Ticket means "nothing held". Settling it is a no-op.
   - `var slotMu sync.Mutex`. Record, Claim and Settle each hold it across
     their load → mutate → store.
   - `Record(slot Slot, skills []session.AttestedSkill, headSha string) error`
     overwrites the attestation with `{RecordedAt: now UTC, Skills, HeadSha}`,
     with no claim. Delete `RecordImplicit`.
   - `Peek(merged sweatfile.Sweatfile, slot Slot) (ok bool, output string, err error)`
     has four outcomes:
     - The gate is dormant (no active skills): `(true, "", nil)`.
     - The load fails: `renderFailure(required, slot.missing)` +
       `ErrAttestationRequired`.
     - There is no attestation: today's "no fresh attestation buffered; call
       `nothing-but-the-truth` first, then retry" + `ErrAttestationRequired`.
     - The attestation is held by a live claim (`Claim != nil &&
       session.IsAlive(Claim.PID)`): `renderFailure(required, fmt.Sprintf(
       "the buffered attestation is held by an in-flight merge or check
       (claimed %s by pid %d); it is consumed if that attempt lands and
       released for a retry if it fails. To merge a further batch now,
       record a fresh attestation with `nothing-but-the-truth`",
       ClaimedAt.Format(time.RFC3339), PID))` + `ErrAttestationRequired`.

     Otherwise (none, or a dead-PID claim) it returns `(true, "", nil)`.
   - `Claim(merged sweatfile.Sweatfile, slot Slot) (Ticket, string, error)`.
     A dormant gate returns `(Ticket{}, "", nil)`. It refuses with exactly
     Peek's outputs. Otherwise it sets `Claim{ID: fmt.Sprintf("%d-%d",
     os.Getpid(), time.Now().UnixNano()), PID: os.Getpid(), ClaimedAt: now}`,
     stores it (a store error is returned wrapped: "claim pre-merge
     attestation: …"), and returns `Ticket{RecordedAt, HeadSha, ClaimID}`.
   - `Settle(slot Slot, t Ticket, landed bool) error`:
     - A zero `t.ClaimID` returns nil.
     - It loads; a load error returns nil (the session is gone, so there is
       nothing to settle).
     - If `a := st.PreMergeAttestation; a != nil && a.RecordedAt.Equal(t.RecordedAt) && a.Claim != nil && a.Claim.ID == t.ClaimID`:
       - `landed` sets `st.PreMergeAttestation = nil`;
       - otherwise it sets `a.Claim = nil`;
       - then it stores.
     - Otherwise it is a no-op (a re-record superseded the held attestation).
     - It returns an error only on a store failure.
   - The interim old API keeps its names:
     - `Check` and `Consume` take a `WorktreeSlot` internally (`Check` calls
       `Peek(merged, WorktreeSlot(repo, branch))`).
     - `PeekImplicit` becomes `Peek(merged, ImplicitSlot(checkout))`.
     - `CheckImplicit` and `ConsumeImplicit` use `ImplicitSlot`.

     Mark each `// Deprecated: removed in #219 task 3.`
   - Rewrite the package doc comment: the gate claims on commit, consumes on
     a landed merge or passing check, and releases on everything else; a
     dead-PID claim is void.
3. Call sites in `cmd/spinclass/commands_mcp_only.go`:
   - `handleNothingButTheTruth` builds the slot: `attestation.ImplicitSlot(cwd)`
     when implicit, else `attestation.WorktreeSlot(repoPath, branch)`. It
     computes `headSha, _ := git.RevParse(cwd, "HEAD")` (best effort; `""` on
     error) and calls `attestation.Record(slot, params.Skills, headSha)`.
     Leave its result text alone (Task 4).
   - `peekGate` calls `attestation.Peek(merged, attestation.WorktreeSlot(gs.repoPath, gs.branch))`.

**Failing tests first** (`internal/attestation/attestation_test.go`; the
required skill list is `[{eng:code-reviewer, Required.}]`, as in the existing
tests):
- `TestPeekAndClaimFailWithoutAttestation`. It moves today's
  `TestCheckFailsWithoutAttestation` assertions onto `Peek` and `Claim` with a
  `WorktreeSlot`: `ErrAttestationRequired`, `not ok 1 - pre-merge skill
  attestation missing`, `required_tool: nothing-but-the-truth`, the skill
  name, and `rationale: "Required."`.
- `TestClaimDormantReturnsZeroTicket`. With an empty `Sweatfile{}`, `Claim`
  returns a zero Ticket and nil, and `Settle` on it returns nil.
- `TestRecordStoresHeadSha`. `Record(slot, skills, "abc123")` produces
  `st.PreMergeAttestation.HeadSha == "abc123"` and `Claim == nil`.
- `TestClaimThenSettleLandedConsumes`. Record, then Claim: the state has
  `Claim.PID == os.Getpid()`, and the ticket carries `RecordedAt`, `HeadSha`
  and a non-empty `ClaimID`. `Settle(…, true)` leaves the attestation nil, and
  a following `Peek` fails with `ErrAttestationRequired`.
- `TestClaimThenSettleReleasedKeeps`. Record, Claim, then `Settle(…, false)`.
  The attestation is still present, with `Claim == nil` and the same
  `RecordedAt`. A second `Claim` succeeds.
- `TestLiveClaimBlocksPeekAndClaim`. After one Claim, `Peek` and a second
  `Claim` both return `ErrAttestationRequired`, with output containing
  `in-flight` and `nothing-but-the-truth`.
- `TestDeadClaimIsVoid`. Write state whose attestation carries
  `Claim{ID: "x", PID: deadPID(t)}`. `Peek` is ok and `Claim` succeeds.
  Helper: `deadPID(t)` runs `exec.Command("true")` and returns
  `cmd.Process.Pid` after `Run`.
- `TestSettleAfterReRecordIsNoOp`. Record, Claim (t1), then Record again
  (with a different `HeadSha`). Both `Settle(t1, true)` and `Settle(t1, false)`
  leave the NEW attestation present, unclaimed, and with the new `HeadSha`.
- `TestImplicitSlotClaimSettleRoundTrip`. Use `setupImplicitSession`. Record
  through `ImplicitSlot`, Claim, and `Settle(false)`: the attestation is kept.
  Claim, then `Settle(true)`: it is gone.
- `internal/session/pre_merge_attestation_test.go`: extend
  `TestPreMergeAttestationRoundTrip` so `HeadSha` and a `Claim` round-trip
  through `Write`/`Read`.

Keep the existing `Check*` tests passing unchanged (the interim API).

**Implementation**, then prove:
- `just debug-go-test internal/attestation`
- `just debug-go-test internal/session 'TestPreMergeAttestation'`
- `just debug-go-test cmd/spinclass 'TestMergeAsync|TestDecideMergeGate'`
  (a compile check of the two call sites)

Commit: `feat(attestation): claim/settle holds and head sha (#219, task 1)`.

---

### Task 2: the `attested` verdict names commits made since the attestation

**Context:** spinclass (Go). FDR 0031's pre-merge policy stage
(`internal/merge/policy.go` `runAttestationPolicy`) emits exactly one reporter
point before the hook. For an attestation-admitted MCP merge (`GateAttested`)
it is `ok - pre-merge policy: attested`. Issue #219 decided that an
attestation stays valid after the agent commits a fix (e.g. after a red hook),
but the verdict must show that drift. crap's `Ok` carries no diagnostic, so
the facts ride in the label, like the `exempt (…) base=… landing=…` verdict.
`PostMergeOptions` (`internal/merge/postmerge_options.go`) is the per-merge
struct that travels to `FinishMerge`. Its `Gate` field already says how the
gate was met. This task adds the sha the attestation was recorded at (the
cmd layer fills it in Task 3). It is independent of Task 1.

**Files:**
- Modify: `internal/merge/postmerge_options.go` (`PostMergeOptions.AttestedSha`)
- Modify: `internal/merge/policy.go` (the `GateAttested` case, a new helper,
  and the `GateAttested` const comment)
- Test: `internal/merge/policy_test.go` (reuse `setupPolicyRepo`, `runGit`,
  `runFinishOpts` and `policyPoint`)

**Behaviour:**
1. `PostMergeOptions.AttestedSha string`, with this doc: the session HEAD the
   admitting attestation was recorded at (FDR 0007, #219); `""` when unknown,
   or when the merge was not admitted by an attestation. It is only read for
   `GateAttested`.
2. In `runAttestationPolicy`, `case GateAttested:` becomes
   `ts.Ok(attestedLabel(repoPath, targetRef, pmAttestedSha, pinnedSha))`.
   Thread the value in: add an `attestedSha string` parameter after
   `landingSha`, and pass `pm.AttestedSha` at both call sites in `merge.go`
   (~L505 and ~L585).
3. `func attestedLabel(repoPath, targetRef, attestedSha, pinnedSha string) string`:
   - `attestedSha == ""` gives `policyLabel + ": attested"` (byte-identical
     to today).
   - Otherwise it runs `git.Run(repoPath, "rev-list", "--reverse",
     "--right-only", "--cherry-pick", "--no-merges",
     attestedSha+"..."+pinnedSha, "^"+targetRef)`. That is the branch commits
     in the pin that are neither on the landing target nor patch-equivalent
     to a commit the attestation saw, so a pure rebase counts 0.
     - On an error it returns `fmt.Sprintf("%s: attested; could not compare
       with the attested commit %s", policyLabel, shortSha(attestedSha))`.
     - With 0 lines it returns `policyLabel + ": attested"`.
     - With n > 0 it returns `fmt.Sprintf("%s: attested; %d %s since the
       attestation at %s: %s", policyLabel, n, noun, shortSha(attestedSha),
       list)`, where `noun` is `commit` or `commits`, and `list` is the first
       5 lines through `shortSha`, space-joined, plus ` …` if n > 5.
   - It never fails the merge.
4. Reword the `GateAttested` const comment to "an MCP merge admitted by a
   claimed attestation (consumed only if the merge lands, #219)".

**Failing tests first** (`policy_test.go`; each uses `setupPolicyRepo(t, "")`,
where the global skills make the gate live; the target is local `main`):
- `TestPolicyAttestedUnchangedTipIsPlain`: `attested := runGit(t, wtPath,
  "rev-parse", "HEAD")`. Running with `PostMergeOptions{Gate: GateAttested,
  AttestedSha: attested}` gives a Description exactly `policyLabel+": attested"`.
- `TestPolicyAttestedNotesCommitsSince`: capture `attested`, then commit
  `b.txt` in `wtPath` and capture `fix := rev-parse HEAD`. The Description has
  the prefix `policyLabel+": attested; 1 commit since the attestation at
  "+shortSha(attested)` and contains `shortSha(fix)`.
- `TestPolicyAttestedRebaseAloneIsPlain`: capture `attested`. Commit
  `other.txt` on `main` in `repoDir`, so PrepareMerge's rebase rewrites the
  feature sha. The Description is exactly `policyLabel+": attested"`.
- `TestPolicyAttestedRebasePlusNewCommitCountsOne`: the same as the previous
  test, plus one new feature commit, gives `1 commit since`.
- `TestPolicyAttestedUnknownShaSaysSo`: `AttestedSha:
  "0123456789abcdef0123456789abcdef01234567"` gives a Description containing
  `could not compare with the attested commit 0123456789ab`, the point is ok,
  and the merge lands.
- The existing `TestPolicyAttestedRecordsAttested` (with `AttestedSha` unset)
  must stay green.

If `--cherry-pick` with the extra `^targetRef` negative misbehaves in the
rebase cases, STOP and report rather than inventing a different comparison.

**Implementation**, then prove: `just debug-go-test internal/merge 'TestPolicy'`.

Commit: `feat(merge): attested verdict names commits since the attestation (#219, task 2)`.

---

### Task 3: handlers claim on commit and settle on outcome; delete the consume-at-commit path

**Context:** spinclass (Go). Task 1 added these to `internal/attestation`:
- `Slot` (`WorktreeSlot(repoPath, branch)` / `ImplicitSlot(checkout)`);
- `Ticket` (`RecordedAt`, `HeadSha`, `ClaimID`; the zero value holds nothing);
- `Claim(merged, slot)`, which refuses with a TAP doc +
  `ErrAttestationRequired` when there is no attestation or it is held by a
  live claim;
- `Settle(slot, ticket, landed)`, which consumes on `landed`, releases
  otherwise, and is a no-op on a mismatched or zero ticket.

Task 2 added `merge.PostMergeOptions.AttestedSha`, which the policy verdict
uses to name post-attestation commits. This task switches every MCP merge and
check handler in `cmd/spinclass/commands_mcp_only.go` and
`cmd/spinclass/merge_queue.go` from clear-at-commit to claim-at-commit plus
settle-on-outcome.
- A merge consumes only when it lands (`mergeErr == nil`).
- A check consumes only when its hook passes.
- Everything else releases: prepare failure (#303), hook red (#219), cancel,
  drain, `job.Start` refusal.
- A second async merge while the first holds the attestation is refused (or
  exemption-admitted) because `Peek`/`Claim` treat a live claim as absent.
  That preserves FDR 0025's per-batch attestation.

Worktree merges only (implicit merges are refused earlier, #317). The check
tools serve implicit sessions too.

**Files:**
- Modify: `cmd/spinclass/commands_mcp_only.go`
- Modify: `cmd/spinclass/merge_queue.go`
- Modify: `internal/attestation/attestation.go`: delete `Check`,
  `CheckImplicit`, `Consume`, `ConsumeImplicit`, `PeekImplicit`, and delete
  the tests that call them: `TestCheckDormantWhenNoSkills`,
  `TestCheckFailsWithoutAttestation`, `TestCheckConsumesBufferedAttestation`,
  `TestRecordImplicitAndCheckImplicitRoundTrip`,
  `TestCheckImplicitDormantWhenNoSkills`. Task 1's tests supersede them. Keep
  any `Validate`/`Record` tests.
- Test: `cmd/spinclass/merge_queue_test.go` (reuse `gatedWorktreeFixture`,
  `gateSweat`, `gateSweatNoStacking`, `startBlockingJob`)
- Test: `cmd/spinclass/commands_mcp_only_test.go`, `cmd/spinclass/merge_gate_test.go`

**Behaviour:**
1. A new helper type in `commands_mcp_only.go`:
   ```go
   // attestationHold is one merge/check attempt's claim on the attestation that
   // admitted it (#219): consumed if the attempt lands, released otherwise.
   type attestationHold struct {
       slot   attestation.Slot
       ticket attestation.Ticket
   }
   func (h attestationHold) settle(landed bool) // attestation.Settle; error → servelog.Errorf, never surfaced
   func holdGate(gs gatedSession, cwd string) (attestationHold, string, bool)
   ```
   `holdGate` replaces `consumeGate`.
   - Slot: `attestation.ImplicitSlot(cwd)` if `gs.implicit`, else
     `attestation.WorktreeSlot(gs.repoPath, gs.branch)`.
   - If the sweatfile is unloadable or the gate is dormant, it returns a zero
     hold, `ok=true`.
   - `attestation.Claim` → `ErrAttestationRequired` returns `(zero, output,
     false)`; any other error returns `"attestation gate error: …"`.
2. `handleMergeThisSession` (sync): when `gate == merge.GateAttested`, take a
   hold via `holdGate` (a refusal returns its text). Then:
   ```go
   landed := false
   defer func() { hold.settle(landed) }()
   pm.Gate = gate; pm.AttestedSha = hold.ticket.HeadSha
   ```
   After `merge.Resolved`: `landed = mergeErr == nil`.
3. `handleMergeThisSessionAsync`: under `mergeQueueMu`, `consumeGate` becomes
   `holdGate`. Set `pm.AttestedSha = hold.ticket.HeadSha` before either
   commit path.
   - **Enqueue:** append `queuedMerge{gitSync, run:
     buildQueuedMergeRun(…, pm, hold), release: func() { hold.settle(false) }}`.
   - **Immediate:** on `prepErr`, call `hold.settle(false)` before returning.
     The job closure does `landed := false; defer func(){ hold.settle(landed)
     }()` and sets `landed = mergeErr == nil` after `FinishMerge`. It passes
     `func() { hold.settle(false) }` as `startSessionJob`'s new
     `onNotStarted`.
4. `startSessionJob(wt, kind string, gitSync bool, fn job.Func, onNotStarted func()) *command.Result`
   calls `onNotStarted` (if non-nil) whenever `job.Start` returns an error,
   both for ErrAlreadyRunning and for any other error.
5. `merge_queue.go`:
   - `queuedMerge` gains `release func()`, plus a nil-safe method
     `releaseHold()`.
   - `buildQueuedMergeRun(repoPath, wtPath, branch, defaultBranch string,
     gitSync bool, pm merge.PostMergeOptions, hold attestationHold)` does
     `landed := false; defer func(){ hold.settle(landed) }()`. The prepErr
     path leaves `landed` false; after FinishMerge, `landed = mergeErr == nil`.
   - `processMergeQueue`:
     - **Drain path:** after unlocking, call `releaseHold()` on every drained
       entry (the `q` captured before clearing).
     - **Dequeue-Start-failure path:** call `releaseHold()` on `next` and on
       every entry it drains.
     - **Successful dequeue:** releases nothing; the run closure settles.
   - The drain wake message becomes: `"%d queued %s did not run: prior merge
     %s %s — its base assumption broke. Resolve the failure and re-merge the
     remaining commits; a failed or drained merge never consumes the
     attestation, so the buffered one still stands (re-record it only if your
     fix changed what the listed skills reviewed)."`
   - `enqueuedMergeResult`:
     - The body's "and you are woken to resolve, re-attest, and re-merge"
       becomes "and you are woken to resolve and re-merge".
     - The GateAttested note becomes: "The pre-merge attestation (if the gate
       is live) is now claimed by this queued merge: it is consumed only if
       the merge lands, and released for your retry if it fails or is
       drained. A further batch needs a fresh nothing-but-the-truth."
     - The GateNeedsExemption note is unchanged.
   - Rewrite the `queuedMerge` and `emitDrainWake` doc comments ("consumed and
     BOUND at enqueue" / "bound attestations are discarded") to claim/release
     language.
6. `handleCheckThisSession`: replace `resolveGatedSession` with
   `resolveSession`. `!ok` returns `failMsg`. When `gitErr == nil`, take
   `holdGate(gs, cwd)` (a refusal returns its text). When `gitErr != nil`, run
   ungated with a zero hold, as today. Use the
   `passed := false; defer func(){ hold.settle(passed) }()` pattern, with
   `passed = hookErr == nil`.
7. `handleCheckThisSessionAsync`: keep the busy refusal first, then the same
   resolve + hold as above. The job closure settles `hookErr == nil` via the
   defer pattern; `onNotStarted` releases.
8. Delete `resolveGatedSession`, `enforceAttestation`,
   `enforceAttestationImplicit` and `consumeGate`. Update the `gatedSession`,
   `resolveSession`, `peekGate`, `decideMergeGate`,
   `parsePostMergeTimeoutParam` and `jobAlreadyRunningResult` comments from
   consume to claim. Rename the test `TestResolveGatedSession` to
   `TestResolveSession` and call `resolveSession`; its dormant-gate HOME
   pinning comment still applies.

**Failing tests first:**
- `TestMergeSyncHookFailureKeepsAttestation` (commands_mcp_only_test.go, the
  #219 repro):
  - Setup: `gatedWorktreeFixture(t, "[hooks]\npre-merge = \"false\"\n\n"+gateSweat)`,
    then write and commit `x.txt` in `cwd`.
  - Step 1: `handleMergeThisSession(ctx, {"local_only":true})` returns
    `IsErr`. The attestation is still present, `Claim == nil`, and
    `RecordedAt` is unchanged.
  - Step 2: rewrite the repo sweatfile with `pre-merge = "true"` and call
    again. The result is not `IsErr`, and the attestation is nil (consumed on
    landing).
  - If the landing half proves environment-fragile, keep the failure half and
    say so in the report. Task 1 unit-covers `Settle(landed=true)`.
- `TestMergeAsyncPrepareFailureKeepsAttestation` (the #303 repro): with
  `gatedWorktreeFixture(t, gateSweat)` and no commits,
  `handleMergeThisSessionAsync({"local_only":true})` returns `IsErr` (nothing
  to merge, synchronous prefix). The attestation is present and
  `Claim == nil`.
- `TestMergeAsyncHookFailureKeepsAttestation`: the pre-merge `"false"` fixture
  plus one commit. `handleMergeThisSessionAsync({"local_only":true})` returns
  not-`IsErr` (started). Then `<-job.WaitDone(cwd)`. The attestation is
  present and `Claim == nil`.
- `TestCheckThisSessionConsumesOnlyOnGreen`: the fixture with
  `"[hooks]\npre-merge = \"false\"\n\n"+gateSweat`.
  - `handleCheckThisSession` returns `IsErr`; the attestation is present and
    unclaimed.
  - Rewrite with `pre-merge = "true"`: the call returns not-`IsErr`, and the
    attestation is nil.
- `TestMergeAsyncEnqueuesWhenBusy` (update):
  - After the enqueue, the attestation is PRESENT, with
    `Claim.PID == os.Getpid()`.
  - A second `handleMergeThisSessionAsync` returns `IsErr`, with text
    containing `in-flight` (no exemptions in `gateSweat`).
  - Queue length stays 1.
  - The result text contains `claimed` and `consumed only if the merge lands`.
- `TestMergeAsyncRefusalPreservesAttestation`: additionally assert
  `Claim == nil`.
- `TestProcessMergeQueueDrainReleasesHolds`: two entries whose `release`
  increments a counter. `processMergeQueue(wt, job.KindMerge,
  job.StatusFailed, "p")` leaves the counter at 2.
- Extend `TestProcessMergeQueueDequeuesOnSuccess`: the dequeued entry's
  `release` is NOT called.
- `TestStartSessionJobCallsOnNotStartedWhenBusy`: with `startBlockingJob(t,
  wt)` running, `startSessionJob(wt, job.KindMerge, false, fn, onNotStarted)`
  returns the already-running error, and `onNotStarted` ran once. Neither
  `fn` nor anything else started.
- `TestDecideMergeGateHonoursClaims` (merge_gate_test.go): use
  `gatedWorktreeFixture(t, gateSweat)`, then set
  `PreMergeAttestation.Claim` via `session.Read`/`Write`.
  - With a live PID (`os.Getpid()`), `decideMergeGate` returns `ok=false`,
    with a message containing `in-flight`.
  - With a dead PID (the `deadPID` helper, duplicated locally), it returns
    `merge.GateAttested`.

**Implementation**, then prove:
- `just debug-go-test cmd/spinclass 'TestMergeSync|TestMergeAsync|TestCheckThisSession|TestProcessMergeQueue|TestStartSessionJob|TestDecideMergeGate|TestResolveSession'`
- `just debug-go-test internal/attestation`

Commit: `fix(mcp): hold the attestation per attempt, consume only on a landed merge or green check (#219, task 3)`.

---

### Task 4: docs, tool descriptions, closing commit

**Context:** spinclass. Tasks 1 to 3 changed the FDR 0007 attestation
lifecycle, and every surface that describes it now says the wrong thing.

The new lifecycle:
- An MCP merge or check **claims** the buffered attestation when it commits.
  The claim lives in session state as `pre_merge_attestation.claim`: id, pid,
  and claimed_at.
- A **landed** merge or a **passing** check consumes it.
- Every failure, cancel, refusal, drain and crash **keeps** it. The release is
  explicit, and a dead-PID claim is void.
- While one attempt holds it, it is unavailable to others, so each FDR 0025
  queued batch still needs its own attestation.
- Re-recording replaces it, and the newest wins.
- `nothing-but-the-truth` records `head_sha`. The GateAttested verdict appends
  `; N commit(s) since the attestation at <sha>: …` when the pin carries
  unseen commits. It never forces a re-attest, and there is no TTL.

**Files and exact edits:**
1. `cmd/spinclass/commands_mcp_only.go`, tool text:
   - `buildNothingButTheTruthDescription`: replace the final sentence ("Each
     attestation is consumed by the next …") with: "The next
     merge-this-session / check-this-session (or async twin) claims it, and it
     is consumed only when that merge lands or that check passes; a failed,
     cancelled or refused attempt leaves it buffered for the retry. Recording
     again replaces it — do so when a fix changed what the listed skills
     reviewed."
   - The `handleNothingButTheTruth` result text: "ok - attestation recorded
     for %d skill(s)%s; the next merge/check claims it and consumes it only
     once the merge lands or the check passes". Here `%s` is ` at <HEAD
     short-12>` when the head sha is known, else empty.
   - `buildMergeThisSessionDescription`: append to `base` " When
     [[pre-merge-skills]] is live, the buffered attestation is consumed only
     if this merge lands; any failure leaves it buffered for the retry."
   - `buildMergeAsyncDescription`:
     - Replace "Consumes the pre-merge attestation only when it commits to the
       merge (dispatch OR enqueue); a refusal never consumes it." with "Claims
       the pre-merge attestation when it commits to the merge (dispatch OR
       enqueue) and consumes it only if that merge lands; a refusal, failure,
       cancel or drained queue leaves it buffered. While one merge holds it, a
       further batch needs a fresh attestation."
     - Replace "you are woken to resolve, re-attest, and re-merge" with "you
       are woken to resolve and re-merge".
   - `buildCheckThisSessionDescription` and `buildCheckAsyncDescription`:
     append " When [[pre-merge-skills]] is live, a passing check consumes the
     buffered attestation; a failing or cancelled check leaves it."
   - Grep the package for any test asserting the old strings, and update it.
2. `AGENTS.md` (symlinked as CLAUDE.md). HARD CAP 40000 bytes; it is ~38800
   now, so keep the net growth ≤ +700 bytes.
   - In **Async merge/check**: "consume the attestation" becomes "claim the
     attestation".
   - In **Stacked / queued intra-session merges**: replace the sentence "The
     **attestation** is consumed only once a merge is committed … split from
     `Check`)." with "An enqueue **claims** the attestation (a live claim makes
     it unavailable to the next batch); a drained entry releases it."
   - After the **Exemption predicates** bullet, add:
     "- **Attestation lifecycle** (FDR 0007, #219, `internal/attestation`): an
     MCP merge/check `Claim`s the attestation at commit
     (`pre_merge_attestation.claim`, PID-owned; a dead owner's claim is void,
     so a crash never burns it); `Settle` consumes on a landed merge / green
     check, releases on every failure, cancel, refusal or drain. A re-record
     supersedes (a stale settle no-ops). `head_sha` rides along; the policy
     point appends commits made since (patch-id aware) — informational, never
     a forced re-attest."
   - Check the size with `folio_ls -la`. If it is over 39500 bytes, trim the
     **Dynamic system-prompt fragment** bullet's manpage/repo-index
     sentences, which FDR 0030 already records: keep one line pointing at
     FDR 0030.
3. `doc/spinclass-sweatfile.5.scd`, *PRE-MERGE SKILL ATTESTATION* → *Gate
   behaviour* (~L1160–1165). Replace the "A merge or check that proceeds …
   once-per-session mode." paragraph with the lifecycle above in manpage
   prose:
   - claim at commit;
   - consumed only when the merge lands or the check passes;
   - kept on failure, cancel, refusal, drain, or a crash of the MCP server;
   - one hold at a time, so a second batch needs a fresh attestation;
   - re-recording replaces it;
   - no sticky once-per-session mode, and no expiry.

   In the verdict paragraph (~L1172), add that *attested* may carry "; N
   commits since the attestation at <sha>: …". L514 ("does not consume") stays
   true; leave it.
4. `docs/features/0007-pre-merge-skill-attestation.md` (keep status
   `experimental`):
   - Retitle "Lifecycle — fresh per merge call" to "Lifecycle — fresh per
     landing" and rewrite it. Each *landing* (or passing check) consumes one
     attestation. A failed attempt landed nothing, so the "merge moment" the
     gate guards has not happened, and forcing a re-attest there trains
     re-pasting (#219). Add the head-sha/drift-visibility rationale, and the
     rejected strict pin, patch-id fingerprint and TTL.
   - In **State persistence**: add `head_sha` and `claim` to the JSON example.
     Replace the "The field is cleared … (#219 tracks relaxing that) …
     corrected in #328 …" paragraph with the claim/settle table (consumed
     vs. kept, per Decision 1), including crash semantics.
   - In **Limitations**: replace "Attestation is single-use and not
     re-emittable" with "Consumed per landing, not per attempt": commits made
     after attesting ride on the old attestation and are only *reported*.
     Add a limitation: claims and consumption ride `session.Write`'s whole-state
     read-modify-write, with no file lock (a pre-existing race). PID-based
     staleness can misjudge a reused PID. A post-landing error from
     `FinishMerge` would keep rather than consume (none exists today).
5. `docs/features/0025-stacked-queued-merges.md` (do NOT change `status`):
   - In the promotion criteria: "consumes the attestation" becomes "claims
     the attestation".
   - In the Interface bullet "If the running merge FAILS": drop "re-attest".
     The drained entries' claims are released, and the buffered attestation
     still stands.
   - Rewrite **Attestation semantics**: claimed at enqueue (not consumed);
     consumed when that entry lands; released on drain, dispatch failure or
     failure. Add a sentence: the #219/#303 caveat is resolved, so the FDR is
     unblocked for its experimental → testing observation.
   - In **Concurrency**: "the attestation consume" becomes "the attestation
     claim"; a live claim is what stops a second peek from reusing it.
6. `docs/features/0031-pre-merge-exemption-predicates.md`:
   - Decision table row 1: "Consume as today (#265 ordering unchanged)"
     becomes "Claim; consumed only if the merge lands (#219)".
   - The paragraph under the table: `check-this-session` now consumes only on
     a passing check.
   - Verdicts: `✓ pre-merge policy: attested` may carry `; N commit(s) since
     the attestation at <sha>: …`.
7. `docs/plans/2026-09-28-roadmap.md` FDR watch: "**0025:** blocked on
   #219/#303." becomes "**0025:** unblocked — #219/#303 landed; record the
   experimental → testing observation."

**Prove:** `just debug-go-test cmd/spinclass 'Description|NothingButTheTruth'`
(plus any test that asserts tool text). Also confirm `AGENTS.md` ≤ 40000
bytes. The manpage renders in the merge gate.

**Commit** (this closes both issues):
```
docs(attestation): claim-per-attempt, consume-per-landing lifecycle (#219, task 4)

Closes #219
Closes #303
```
