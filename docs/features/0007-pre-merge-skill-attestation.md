---
status: experimental
date: 2026-05-24
promotion-criteria: |
  Promote to `experimental` once the gate ships and at least one
  repo's sweatfile declares a non-empty `[[pre-merge-skills]]` list
  exercised across multiple merge cycles. Promote to `accepted`
  once operating experience shows the articulation step actually
  changes which skills agents invoke before merge — measured by
  comparing pre-attestation vs post-attestation transcript samples
  for invocations of the listed skills.
---

# Pre-merge skill attestation gate

## Problem Statement

`merge-this-session` and `check-this-session` currently run their
`[hooks].pre-merge` command and ship the result back. There is no
mechanism for the sweatfile to require an agent to *acknowledge*
a specific review checklist before that hook runs — skills like
`eng:code-reviewer`, `simplify`, or `security-review` get skipped
silently and the merge proceeds.

Bare instructions ("always run code-reviewer before merging") live
in CLAUDE.md and adjacent prose, but those are advisory: the merge
tool doesn't check, the agent isn't forced to articulate, and the
omission leaves no trace in the session record. The result is that
agents reliably forget or de-prioritise the review skills the user
asked them to use.

This feature gives the sweatfile a way to declare a list of skills
the agent **must address** before each merge, and gates the merge
tool behind a new attestation tool that forces the agent to write
down — per skill — whether they used it and why.

## Locked-in design

These pieces are settled from the scoping conversation.

### Sweatfile schema — `[[pre-merge-skills]]`

The sweatfile gains a new array-of-tables. Each entry names a
skill and gives the rationale the user wants the agent to address.

```toml
[[pre-merge-skills]]
name      = "eng:code-reviewer"
rationale = "Mandatory second-pass review on every diff merging into master."

[[pre-merge-skills]]
name      = "simplify"
rationale = "We've shipped too much premature abstraction recently — actively prune before merge."

[[pre-merge-skills]]
name      = "security-review"
rationale = "Required for any diff touching auth, crypto, or secret handling."
```

Merge semantics mirror the existing `[[mcps]]` and
`[[start-commands]]` arrays-of-tables in the sweatfile cascade
(global → parent dirs → repo): dedup-by-name, later definitions
override earlier ones. A name-only entry (rationale omitted or
empty) removes an inherited skill, so a child sweatfile can opt
out of a parent's listing.

A non-empty resolved list activates the gate. An empty / absent
list leaves the existing merge behaviour untouched.

### New MCP tool — `nothing-but-the-truth`

A new tool is registered alongside the active merge / check tool
(see "Mutual exclusivity" below) whenever the resolved sweatfile
has a non-empty `[[pre-merge-skills]]` list. Input shape:

```json
{
  "skills": [
    {
      "name": "eng:code-reviewer",
      "used": true,
      "reasoning": "Ran eng:code-reviewer over the diff; addressed two findings about error handling in internal/git/run.go."
    },
    {
      "name": "simplify",
      "used": false,
      "reasoning": "Diff is a one-line bugfix in a leaf function — no abstraction surface to prune."
    },
    {
      "name": "security-review",
      "used": false,
      "reasoning": "Diff is documentation-only (docs/features/0007-*.md), no code paths touched."
    }
  ]
}
```

**Validation: strict on presence, lenient on content.** Every
skill in the resolved `[[pre-merge-skills]]` list MUST have an
entry whose `name` matches exactly; missing or extra entries are
rejected. `reasoning` MUST be non-empty. The tool does not police
reasoning length, quality, or truthfulness — the act of writing
it down is the deliverable, not the prose itself.

On success, the tool records the attestation in session state
(see "State persistence") and returns `ok`. On a presence /
name-match failure, the tool returns a structured TAP error
listing which expected names are missing and which provided
names are unrecognised, so the agent can correct the input and
retry without re-fetching the full list.

### Lifecycle — fresh per landing

Each *landing* (a merge that lands, or a check that passes) consumes
one attestation. A subsequent merge requires a new call to
`nothing-but-the-truth`. The attempt that commits to a merge only
*claims* the attestation; a failed attempt (red hook, refused fetch,
REPAIR refusal, rebase conflict, cancel) landed nothing, so the
"merge moment" the gate guards has not happened. Demanding a
byte-identical re-attest there trains agents to re-paste JSON instead
of re-reviewing (#219, #303).

The threat model is still "agent forgets to use the review skills
before merging." A sticky once-per-session attestation would degrade
to a startup ritual, so every landing needs its own; only failures
are free to retry.

Staleness is made visible, never blocking. `nothing-but-the-truth`
records `head_sha`, and the *attested* verdict appends
`; N commits since the attestation at <sha12>: ...` when the pinned
branch carries commits the attestation never saw. The count is
patch-id aware (`git rev-list --right-only --cherry-pick`), so a pure
rebase reads as plain *attested*. Rejected alternatives: a strict HEAD
pin (fails #219's own repro, a one-line test fix committed after a
red hook, and re-imposes the five-skill re-run); a cumulative-diff
patch-id fingerprint (flips on any conflict-resolved rebase and says
nothing about *what* changed); a TTL (no evidence it is needed).
There is no expiry: the lifetime ends at a landing, a re-record, or
the end of the session.

### Gate failure shape

When the resolved sweatfile has a non-empty
`[[pre-merge-skills]]` and no fresh attestation is buffered,
`merge-this-session` / `check-this-session` fail before running
any pre-merge hook with a structured response:

```
TAP version 14
1..1
# directive: this repo requires pre-merge skill attestation; call `nothing-but-the-truth` first
not ok 1 - pre-merge skill attestation missing
  ---
  required_skills:
    - name: eng:code-reviewer
      rationale: "Mandatory second-pass review on every diff merging into master."
    - name: simplify
      rationale: "We've shipped too much premature abstraction recently — actively prune before merge."
    - name: security-review
      rationale: "Required for any diff touching auth, crypto, or secret handling."
  required_tool: nothing-but-the-truth
  message: |
    Before this merge can proceed, call the `nothing-but-the-truth`
    tool with one entry per skill listed above, stating whether you
    used the skill and why (or why not).
  ---
```

The full list with rationales is included in the failure response
so the agent sees the user's stated reasons inline — no separate
fetch step. The directive line matches the style of the existing
merge tool's response header (FDR 0005).

### CLI gating — MCP-only

`sc merge` and `sc check` from the terminal **do not** enforce the
attestation gate. The threat model is agent-driven merges; humans
running the CLI from a terminal are out of scope. This keeps the
human workflow unchanged and avoids inventing an interactive huh
form for the attestation step.

The agent-facing tools (`merge-this-session`,
`check-this-session`) are the sole enforcement surface.

Since FDR 0031 the terminal exemption is explicit policy (operator
decision 2026-09-28, #326): no plugin can change it, and a terminal merge
with a live gate records a `pre-merge policy` SKIP point
(`attestation bypassed (terminal)`) instead of bypassing silently. FDR
0031 also adds `[[pre-merge-exemptions]]`, which can admit an un-attested
MCP merge.

### State persistence — session state JSON

The buffered attestation lives in the existing per-session state
file at `<worktree>/.spinclass/state.json` (implicit sessions:
`state-<rand>.json`), under a new top-level field:

```json
{
  "id": "spinclass/slim-sequoia",
  ...
  "pre_merge_attestation": {
    "recorded_at": "2026-05-24T17:42:18Z",
    "head_sha": "9f3c1a2b4d5e...",
    "skills": [
      { "name": "eng:code-reviewer", "used": true,  "reasoning": "..." },
      { "name": "simplify",          "used": false, "reasoning": "..." },
      { "name": "security-review",   "used": false, "reasoning": "..." }
    ],
    "claim": { "id": "...", "pid": 41207, "claimed_at": "2026-05-24T17:43:02Z" }
  }
}
```

`head_sha` is the session worktree's HEAD at attest time (best effort,
absent on error). `claim` is present only while an attempt holds the
attestation. The gated tool **claims** it when it commits to a merge or
check (sync `merge-this-session` before `PrepareMerge`, `check-this-session`
before the hook, the async twins at dispatch or enqueue) and **settles** the
claim when the attempt ends (#219):

| Outcome | Attestation |
|---|---|
| Merge landed (sync, async, or a queued entry's `FinishMerge` nil); a failed post-merge target or a skipped local advance still counts as landed | consumed |
| Check passed | consumed |
| `PrepareMerge` or `FinishMerge` failure (fetch, REPAIR, rebase conflict, red hook, timeout, refused push) | kept |
| Cancel (`session-job-cancel` or ringmaster cancel), drained queue entry, dispatch/`job.Start` failure, red or cancelled check | kept |
| Refusal before the claim (bad params, busy, implicit-session merge, missing attestation) | untouched |
| Exemption-admitted or terminal merge (FDR 0031) | nothing held, nothing consumed |

While a live claim exists the attestation is unavailable to every other
attempt, so each FDR 0025 batch still needs its own. Re-recording replaces
the whole record (the newest wins); a settle whose hold no longer matches
the buffered claim id is a no-op. A claim whose PID is
dead is void, so a serve crash mid-gate never burns the attestation and
needs no cooperation from the dead process. The field survives MCP server
restarts. Implicit sessions store the same record in their per-rand state
file.

A `null`/absent field is equivalent to "no attestation buffered"
and causes the gate to fail as described above.

### Mutual exclusivity preserved

The existing `[hooks].disable-merge` flag picks between
`merge-this-session` and `check-this-session` in the MCP tool
catalog. `nothing-but-the-truth` is registered alongside whichever
of those is active, never independently and never both. Concretely:

- `disable-merge` unset/false, `[[pre-merge-skills]]` non-empty
  → `merge-this-session` + `nothing-but-the-truth`.
- `disable-merge = true`, `[[pre-merge-skills]]` non-empty →
  `check-this-session` + `nothing-but-the-truth`.
- `[[pre-merge-skills]]` empty/absent →
  `nothing-but-the-truth` not registered, gate not enforced.

## Examples

### Repo opts in

```toml
# spinclass/sweatfile (repo-level)
[[pre-merge-skills]]
name      = "eng:code-reviewer"
rationale = "Required on every diff; we don't merge without a second pass."
```

Agent attempts to merge:

```
> mcp__plugin_spinclass_spinclass__merge-this-session
not ok 1 - pre-merge skill attestation missing
  required_skills:
    - name: eng:code-reviewer
      rationale: "Required on every diff; we don't merge without a second pass."
  required_tool: nothing-but-the-truth
```

Agent runs the review, then attests:

```
> mcp__plugin_spinclass_spinclass__nothing-but-the-truth
  skills:
    - name: eng:code-reviewer
      used: true
      reasoning: "Ran eng:code-reviewer; addressed one finding about a missing nil check in internal/session/state.go."
ok 1 - attestation recorded
```

Agent retries merge — proceeds normally, runs pre-merge hook, etc.

### Child sweatfile opts out of an inherited skill

```toml
# global sweatfile (~/.config/spinclass/sweatfile)
[[pre-merge-skills]]
name      = "security-review"
rationale = "Default policy across all my repos."

# repo-level sweatfile (docs-only repo)
[[pre-merge-skills]]
name = "security-review"   # name-only entry removes the inherited skill
```

Resolved list for this repo: empty → gate not enforced.

### Multiple skills, mixed used/unused attestation

```toml
[[pre-merge-skills]]
name      = "eng:code-reviewer"
rationale = "Mandatory."

[[pre-merge-skills]]
name      = "simplify"
rationale = "Watch for premature abstraction."
```

```json
{
  "skills": [
    { "name": "eng:code-reviewer", "used": true,  "reasoning": "Reviewed the 80-line diff; no findings." },
    { "name": "simplify",          "used": false, "reasoning": "Pure bugfix, no new abstractions introduced." }
  ]
}
```

Both entries present, both have non-empty reasoning → attestation
accepted regardless of the `used` boolean.

## Out of scope

- **Reasoning quality enforcement.** The tool does not police
  style, length, sentence count, or content. Articulation is the
  deliverable; whether the reasoning is *good* is a human-review
  problem, not a spinclass problem.
- **Transcript audit / freud-style cross-check.** Verifying that
  a `used: true` claim corresponds to an actual `Skill` tool
  invocation in the transcript is explicitly deferred. Ship the
  attestation surface first, watch how it is used, add audit as
  a follow-up if articulation alone proves theatrical. The freud
  transcript-inspection primitives already exist; wiring them
  into spinclass is straightforward when the time comes.
- **CLI attestation UX.** `sc merge` / `sc check` from a terminal
  skip the gate. Building a human-facing huh form for the
  checklist is not part of this feature.
- **Per-skill input shape extensions.** No fields beyond
  `{name, used, reasoning}` in v1. No severity levels, no linked
  findings, no nested sub-attestations. Future iterations may
  add them, but the v1 surface stays minimal.
- **Sweatfile-driven skill discovery.** The `name` field is a
  free-form string; spinclass does not validate it against any
  catalog of installed skills. If a sweatfile lists a skill that
  doesn't exist in the harness, the agent will (correctly) attest
  `used: false` with reasoning to that effect, and the user will
  see the misconfiguration in the response.

## Limitations

- **The gate is theatre if the agent lies.** A trivially-bypassed
  failure mode is the agent attesting `used: true` with
  plausible-sounding reasoning without actually running the skill.
  v1 accepts this — the design bet is that forcing articulation
  against the user's stated rationale will change behaviour for
  honestly-operating agents. Dishonesty is a separate problem
  addressed by the deferred transcript audit.
- **Consumed per landing, not per attempt.** Commits made after
  attesting ride on the old attestation and are only *reported*
  (the "N commits since the attestation" suffix on the verdict),
  never blocked. An agent that fixes a red hook and retries is not
  re-asked; re-recording when the fix changed what the skills
  reviewed is the agent's call.
- **Other state writers are unlocked (#348).** Record, Claim and
  Settle are a locked read-modify-write (`session.Update`: sidecar
  flock + atomic rename, host-local). The other state writers
  (update-this-session-description, attach/resume, handle grants,
  `UpdateCredential`, `Tombstone`) still read-modify-write the whole
  State unlocked. A stale one can drop a claim (letting that
  attestation admit a second attempt) or resurrect a consumed one
  (which then shows as a live claim held by this serve and blocks
  until a re-record).
- **PID-based staleness can misjudge.** A claim is void when its
  owner PID is dead; a reused PID keeps a dead owner's claim alive
  until that PID exits, and re-recording clears it.
- **A landed-locally merge whose push fails keeps the attestation.**
  With `[hooks].disable-merge-queue` on a gitSync repo,
  `finishMergeUnqueued` fast-forwards the LOCAL default branch and
  can then fail the push in `teardownAndPush`. That error settles as
  not-landed, so the attestation is kept although the merge landed
  locally. This is the chosen behaviour: the merge is not on origin,
  so the retry is still the merge moment the gate guards.
- **No way to attest for the worktree without merging.** There is
  no standalone "record attestation" CLI surface; the only way to
  invoke `nothing-but-the-truth` is via MCP from an agent
  session. Humans running `sc merge` from a terminal bypass the
  system entirely (by design).
- **Sweatfile cascade ordering is name-based.** The dedup-by-name
  rule means a child sweatfile can override a parent's rationale
  by re-declaring the same skill name. There is no way to
  *augment* a parent's rationale (e.g. add a repo-specific reason
  on top of a global one) — child rationale fully replaces parent
  rationale.

## More information

- FDR 0005 (`docs/features/0005-merge-this-session-output-shape.md`)
  — the response-shape conventions this feature's gate-failure
  response and attestation-tool response follow.
- `internal/sweatfile/` — TOML schema and merge logic that gains
  the `[[pre-merge-skills]]` array. The existing `[[mcps]]` and
  `[[start-commands]]` arrays-of-tables are the template.
- `internal/session/state.go` — session state JSON serialisation
  that gains the `pre_merge_attestation` field.
- `cmd/spinclass/commands_mcp_only.go` — current
  `merge-this-session` / `check-this-session` registration gated
  on `[hooks].disable-merge`. The new `nothing-but-the-truth`
  tool registers in the same place under the same conditional.
- `cmd/spinclass/doc/spinclass-sweatfile.5` — manpage update
  documenting the new schema entry.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown).
