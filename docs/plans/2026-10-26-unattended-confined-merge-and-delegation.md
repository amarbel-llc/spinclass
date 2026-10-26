# Confined merging, permission delegation, and the token-lifetime fact-check

Date: 2026-10-26. Status: answers for the operator, docs only, nothing built.
Labels: **R** read in code at origin/master 913484b, **V** observed live,
**I** inferred. Estimates are **I**: they are rough guesses in worker-batches,
where one batch is one worker's test-first change plus one gate.

## 1. How far confined grit and merge are

### What refuses a confined caller today

- **grit_* store tools: nothing refuses them.**
  - R: they classify their caller themselves (FDR 0036 §9) and are exempt from the serve gate's completeness test (`serve_caller_gate_test.go:185`).
  - V per FDR 0035 rounds K, L and M: a confined worker ran grit add, commit and push through the frontend with the minted credential.
  - Remaining refusals are by verdict, the same for everyone: force, the default branch, foreign refs (spinclass-grit(7)).
- **merge-this-session(-async), check-this-session(-async) and nothing-but-the-truth are refused by policy, not by a missing mechanism.**
  - R: each handler calls `refuseConfinedServeCaller` first (`commands_mcp_only.go:486` for merge). The refusal names `sc merge <target>` as the operator's path.
  - R: the reason is EPIC 0001's "engineer-builder cannot merge".
  - R: the merge machinery already runs in the frontend serve (FDR 0013 build worktree, FDR 0029 landing worktree), so no unit plumbing is missing.
- **The confined profile hides moxy's grit tools.**
  - R: `requireConfinedProfile` requires a profile with "no grit tool reachable". That rule is about moxy's grit inside the unit, not spinclass's grit_* tools.

### The risk that a plain unlock adds

- **The pre-merge hook runs the branch's own code outside the unit.**
  - R: `[hooks].pre-merge` is `just`, run in the build worktree checked out at the branch tip, in the frontend. The justfile it runs is the worker's.
  - I: letting a confined worker start its own merge or check therefore lets it execute code it wrote outside its confinement. That is an escape, not a convenience.
  - I: the same is true today when the operator runs `sc merge` on a confined worker's branch. The operator's reading of the diff is the only guard.
- **Post-merge targets also run in the frontend.**
  - R: a changed `[[post-merge]]` section already needs the operator's typed digest (1cb83c5), so the deploy definitions cannot be changed through a merge. The deploy commands themselves still run whatever code they invoke at the landed sha (I).
- **The attestation is self-written.**
  - R: any non-empty reasoning passes, which is #301.
  - I: a confined worker's merge would be admitted on its own word.

### A safe variant, in pieces

| Piece | What | Repos | Estimate (I) |
|---|---|---|---|
| A | Run the pre-merge hook (and check) **inside a confined unit**: a transient unit with the build worktree bound read-write and nothing else, or reuse clown's agent-unit spec. Until A exists, B must stay off. | spinclass (hook runner, `internal/bravo/hookrun`), clown (if the clown unit launcher is reused) | 2-3 batches, plus a live canary round |
| B | Allow merge-this-session for a confined caller **only for its own session** (the serve's cwd session, never a target), and only once A is in place. | spinclass | 1 batch |
| C | Enforce the `merge` and `check` rights (#429). The worker's own merge needs the right granted by its spawner on itself, recorded on its state; a coordinator-landed child needs `merge` on that child. | spinclass | 1 batch |
| D | Freeze `[[post-merge]]` for confined merges: refuse a confined merge that runs any post-merge target, or run targets only with `targets: []`. | spinclass | under 1 batch |
| E | Attestation from distinct subagent principals (#337) or a coordinator review attestation, so the admission is not self-written (#301). | spinclass, clown (subagent principals) | 2+ batches; design first |

- **Smallest safe step for the run:** A, B, C and D, about 4-6 batches plus one live round.
- **Without A:** I do not consider B safe at any scope (I). Coordinator-landing a confined child through a new MCP tool has the same hook-escape problem, because the hook still runs the child's code.
- **Interim with no new code:** unconfined workers land their own work, as the operator ruled. Confined workers stay operator-landed.

## 2. Permission delegation to the coordinator

### What exists (R)

- **Hook paths:**
  - The PreToolUse hook (`internal/delta/hooks`) decides allow, deny or ask for spinclass tools.
  - The PermissionRequest hook path (`perms.RunCheck` and the tier JSON) handles everything else.
  - In a dontAsk session every `ask` becomes `deny` (`writeDontAskVerdict`).
- **The always-ask floor** (`perms.AlwaysAsk`) covers:
  - unconfined spawn, and non-confined spawn;
  - force close and force restart;
  - every grant and every release;
  - list-handles accept.
- **Handles and rights:**
  - Handles name who holds a session, with rights (FDR 0032).
  - Only close, grant and restart are enforced (#429). There is no "approve" right.
- **Messaging:**
  - troupe chat delivers DMs between sessions, and a wake reaches a live agent.
  - Hooks can run commands. Nothing today sends a request and waits for an answer.

### What is missing

| Piece | What | Repos | Estimate (I) |
|---|---|---|---|
| P1 | A request/response protocol: the worker's hook posts a structured request (tool name, an argument digest, the session key) to every holder with the approving right, then blocks for a bounded time (for example 2 minutes) for a signed or authenticated reply. Default deny on timeout. | troupe/clown (the transport and who may answer); spinclass (the hook side) | 2-3 batches, transport design first (troupe's lane) |
| P2 | An approving right, for example `approve` or reusing `instruct`, enforced on the answer, not just recorded. | spinclass (#429 family) | 1 batch |
| P3 | Coordinator-side tooling: a tool that lists pending requests and answers them by request id. Never free text, so a worker's request text cannot inject an approval. | spinclass or clown | 1-2 batches |
| P4 | An audit trail: every delegated decision, with requester, answerer and digest, in frontend state and the stats counter (#369). | spinclass | under 1 batch |

- **Must never be delegable**, even to a holder with every right (I, following the existing always-ask rationale in `check.go`):
  - force close and force restart (they discard work or kill jobs);
  - an unconfined spawn (the opt-out of confinement);
  - grants that widen rights;
  - accepting a `[[post-merge]]` digest;
  - every operator-card act (profile-grant-bless, repin);
  - any grit force or default-branch write;
  - anything that would let a confined worker run code outside its unit (section 1).
- **Delegable candidates:**
  - read-only tool prompts outside the worktree (the 59 freud prompts; #372 may remove most without delegation);
  - non-force spawn of a confined worker (already auto-allowed, #382);
  - grit writes to the worker's own refs (already allowed).
- **Risk (I):** the coordinator is itself an LLM agent. Delegation moves the decision from the operator to an agent that reads text the worker controls. Keeping the request structured (tool plus digest) and the non-delegable list fixed limits that, but does not remove it.
- **Total:** about 5-7 batches, transport first.

## 3. Fact-check: is the forge token minted once with a 12 h lifetime?

- **Minted once, at session creation** (R). `auth.Mint` has one caller, `shop.Create` (`internal/echo/shop/shop.go:212`). `sc resume` and `sc rebuild` do not re-mint (FDR 0028 "Mint once").
- **The merge does not mint or refresh** (R). `merge.go:615` calls `auth.MirrorInto`, which wires the landing worktree to the session's existing credential file.
- **12 h is papi's sweep TTL, not a forge expiry** (R).
  - spinclass's own sweatfile passes `--ttl 12h` to `papi forge token mint`.
  - FDR 0028:95 says "Forgejo tokens have no native expiry today": the TTL is papi's backstop sweep (papi#73).
- **Live evidence** (V). This session (spinclass/firm-mulberry) recorded `minted_at` 2026-10-22T02:09Z for code.linenisgreat.com. Its merges pushed to origin on 2026-10-26 through that credential: bdb34e6, 9760f7b, 76b0a0a, 5c8882f, 913484b. That is about 4.4 days later. The operator is right that long-lived workers push fine.
- **Caveat (I):** this holds while papi's TTL sweep does not revoke live sessions' tokens. Whether that sweep runs, and against what, is papi's lane and not read here.
- **Correction:** `2026-10-26-unattended-confinement.md` section 2 item 2, section 3 item 3 and decision 3 are corrected in the same commit as this page. My earlier chat answer to the coordinator repeated the wrong 12 h claim.
