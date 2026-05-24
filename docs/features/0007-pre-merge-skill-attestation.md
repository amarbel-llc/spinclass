---
status: proposed
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

### Lifecycle — fresh per merge call

Every invocation of `merge-this-session` or
`check-this-session` consumes one attestation. After consumption,
the attestation record is cleared from session state — a
subsequent merge attempt requires a new call to
`nothing-but-the-truth` against the current diff.

This is deliberate. The threat model is "agent forgets to use
the review skills before merging." A sticky once-per-session
attestation would degrade to a startup ritual the agent learns
to dispatch up-front and ignore. Fresh-per-call forces the
agent to re-examine each merge moment against the listed skills.

There is no expiration on the attestation itself between the
`nothing-but-the-truth` call and the immediately following merge
call — the next gated tool consumes it, and only one attestation
is buffered. If the agent attests then takes 30 minutes editing
before calling merge, the attestation still applies, but in
practice the merge usually follows within the same turn.

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

### State persistence — session state JSON

The buffered attestation lives in the existing per-session state
file at `~/.local/state/spinclass/sessions/<hash>-state.json`,
under a new top-level field:

```json
{
  "id": "spinclass/slim-sequoia",
  ...
  "pre_merge_attestation": {
    "recorded_at": "2026-05-24T17:42:18Z",
    "skills": [
      { "name": "eng:code-reviewer", "used": true,  "reasoning": "..." },
      { "name": "simplify",          "used": false, "reasoning": "..." },
      { "name": "security-review",   "used": false, "reasoning": "..." }
    ]
  }
}
```

The field is cleared by the gated tool after a successful merge
or check (i.e. after the pre-merge hook returns). Survives MCP
server restarts.

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
- **Attestation is single-use and not re-emittable.** Once
  consumed by a merge call, the attestation record is gone. If
  the merge's pre-merge hook fails and the agent fixes the
  underlying issue, a fresh attestation is required for the
  retry. This is intentional (the diff may have changed) but may
  feel friction-y in tight iterate-and-retry loops.
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
