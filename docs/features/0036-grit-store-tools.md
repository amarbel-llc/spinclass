---
status: proposed
date: 2026-10-20
promotion-criteria: |
  proposed -> experimental: step 1 of the implementation list lands (the
  `store.Backend` interface, the git backend's read verbs, and the
  `Classify` function) behind a declared opt-in, with moxy's grit untouched.
---

# Grit store tools in spinclass

This is STEP 0 of spinclass #379, "Move the grit tools from Moxie to Spin
Class": design only, no code. It settles the questions the sequencing plan
(Google Doc "Moving grit from moxy to spinclass: sequencing plan") listed under
"Step 0", using the operator's decisions recorded as comments on that doc.
Related: moxy #482 (grit moxin hand-off), moxy #483 (worktree-aware
stash/rebase permissions), spinclass #380 (worktree-scoped stashes and
rebases), moxy #330, #390, #48.

## Problem Statement

moxy's `grit` moxin exposes nine git tools (`branch`, `clone`, `commit`,
`merge`, `remote`, `stage`, `stash`, `tag`, `worktree`). Its permission model
is per tool, or per subcommand through moxy's `perms-request` annotations. It
knows nothing about spinclass sessions, so:

- every `stash`, `rebase`, `reset` and `branch delete` prompts even when it
  touches only the session's own branch (moxy #483, spinclass #380);
- the git stash stack is shared by the main checkout and every worktree, so a
  session can pop another session's stash;
- the only safety rule is "never on main/master", which is a deny-list and
  says nothing about *whose* branch an operation targets.

spinclass already knows who the caller is (the principal, FDR 0032), which
worktree and branch it owns, and has a hook-side permission seam
(`internal/perms`). The tools belong next to that knowledge.

## Decisions (from the operator, 2026-10-20)

Taken as decided; where this FDR had to interpret one, the interpretation is
called out under "Interpretations".

1. The surface is a **plugin for a backing-store system**. Here it is the git
   plugin; a future dodder plugin could have different merge semantics.
2. **All nine** tools move.
3. Permissions become a **prefixed allow-list only**: operations on
   session-owned branches auto-allow, everything else is a permission request.
4. **Stash is emulated** (ephemeral branches, or a patch directory the tool
   maintains) so stashes are confined to their own session.
5. Tool names stay **stable**.
6. The tools need **not** work outside a spinclass session, but must be
   exportable for a moxin path as a backup for ad hoc sessions under moxy.
7. **No stopgap** for moxy #483.

## Design

### 1. The store-plugin boundary

Two layers, in two packages:

- `internal/store`: the backend-neutral layer. It owns the verb vocabulary,
  the ownership/permission classification, the stash shelf contract, and MCP
  tool registration. It knows nothing about git.
- `internal/store/git`: the git backend, the only one implemented here.
  Replaces moxy's bash wrappers with Go over the existing thin `internal/git`
  (`git -C <dir>`) wrapper.

The boundary is a Go interface, grouped by verb family rather than by git
command so another store can implement a subset:

| Family     | Verbs (git backend names)                                | Notes |
|------------|----------------------------------------------------------|-------|
| Snapshot   | stage add/unstage/rm/mv/status/diff, commit              | |
| Lines      | branch create/delete/checkout/log/rev-parse/diff         | "line of history" |
| Rewrite    | rebase, restack, cherry-pick, revert, reset-soft/hard    | git-shaped; optional |
| Integrate  | merge                                                    | result type carries conflicts abstractly |
| Remote     | fetch, pull, push, push-stack, clone                     | |
| Marks      | tag create/delete/list/push/verify                       | |
| Workspaces | worktree list/remove                                     | |
| Shelf      | stash save/apply/drop                                    | emulated, see below |

```go
type Backend interface {
    Name() string                          // "git"
    Capabilities() Capabilities            // which verbs/params exist
    Do(ctx context.Context, s Session, op Op) (Result, error)
    // Owned reports whether ref belongs to the session's namespace.
    Owned(s Session, ref Ref) bool
    // Classify is the permission-relevant reading of an invocation; it
    // performs no side effect and is shared by the tool and the hook.
    Classify(s Session, op Op) Verdict
}
```

Rules that keep git semantics out of the neutral layer:

- A backend declares `Capabilities`; an unsupported verb or parameter returns
  a typed `ErrUnsupported`, never a silent no-op. A dodder backend would
  declare no `rebase`, and a different `merge` conflict model.
- Tool **schemas** stay git-flavored on purpose (decision 5: names and
  argument shapes are stable), but they are generated from the backend's
  capability set rather than hand-written in the neutral layer. git-only
  parameters (`autosquash`, `lease_ref_sha`, `update_refs`) are declared by
  the git backend only.
- The merge **result** is a neutral `IntegrateResult{Clean bool,
  Conflicts []Conflict}`; `git merge` output is parsed in the backend.
- The backend is selected per repo by a sweatfile key (`[store] backend =
  "git"`, default `git`; schema to be added to `spinclass-sweatfile(5)`).
- Out-of-process backends are explicitly **not** built now. When one is
  wanted it should speak FDR 0033's one-shot JSON-RPC protocol
  (`store/do`, `store/classify`, `store/owned`), with `Backend` becoming a
  client of it. The interface above is already shaped as request/response so
  that move is mechanical. FDR 0033 is itself only proposed, so this is a
  dependency to track, not to assume.

Out of scope for the boundary: `sc merge` and `merge-this-session` (FDR
0013/0022/0029). They keep using `internal/git` directly. Moving them behind
`Backend` is a possible later cleanup, not part of #379.

### 2. Permission tiers

moxy annotates each tool `always-allow`, `dynamic` (a helper command computes
allow/ask per call) or `each-use` (always ask); the default is
`delegate-to-client` (moxin(7), PERMISSIONS). spinclass's `internal/perms` has
a different shape: curated JSON allow-rule tiers (`global.json`,
`repos/<repo>.json`) applied by a PermissionRequest hook, plus the `AlwaysAsk`
floor, a PreToolUse-side predicate over the *invocation* that overrides every
allow-list. Note `BuildPermissionString` discards MCP tool input, so a tier
rule cannot discriminate on arguments; that is exactly why `AlwaysAsk` exists.

Mapping:

| moxy tier      | spinclass                                                        |
|----------------|------------------------------------------------------------------|
| `always-allow` | verb class `read`: `Classify` returns `Allow` for any ref        |
| `dynamic`      | verb class `write`: `Classify` returns `Allow` iff the target is session-owned, else `Ask` |
| `each-use`     | verb class `floor`: `Classify` returns `Ask` unconditionally (an `AlwaysAsk` entry) |

Concretely, a new `perms.StoreVerdict(toolName, toolInput)` is consulted by
the same PreToolUse hook that consults `AlwaysAsk`:

1. `AlwaysAsk(...)` true -> ask (unchanged).
2. Tool is a store tool -> resolve the caller's session (principal ->
   `session.SessionKeyOfPrincipal`), call `Backend.Classify`.
   - `Allow` -> `permissionDecision: allow`.
   - `Ask` -> no decision / explicit `ask`: the human is prompted.
   - Session unresolved (no session, ambiguous principal) -> `Ask`, fail
     closed, matching `SessionKeyOfPrincipal`'s ambiguity behaviour.
3. Everything else -> existing tier-file logic.

The enforcement seam is therefore the hook, with one shared `Classify`. The
tool body does **not** re-check ownership and refuse: a tool cannot prompt
(elicitation is #254, not available), and after a human approves a foreign
operation the tool must run it. Consequence, stated plainly: for foreign
operations the human prompt is the only gate. The tool body does keep one
hard refusal, the existing safety floor on the default branch (force-push,
hard-reset, rebase, branch-delete, merge-into on `main`/`master` or the repo's
`DefaultBranch`), because nothing should be approvable into that.

Verb-to-class assignment (from moxy's current tiers; **I did not have the
moxin definitions and could not verify moxy's exact annotations**, only the
tool descriptions in this session):

- `read`: stage status/diff, branch log/rev-parse/diff, tag list/verify,
  worktree list, remote fetch.
- `write` (owned-only auto-allow): stage add/unstage/mv/rm, commit, branch
  create/checkout/delete/reset-*/rebase/restack/cherry-pick/revert, merge,
  stash, remote pull/push/push-stack, tag create/push/delete, clone dest.
- `floor`: `push --force` or `delete` of a remote ref on a non-owned ref,
  `worktree remove` of a worktree that is not this session's own, anything
  with `force: true`. Handled as `AlwaysAsk` entries. Failing closed on
  argument shape as `forceRequested` does.

### 3. The prefixed allow-list

**Conflict with the decision as worded.** Spinclass branch names are *not*
prefixed with the session id: a session `spinclass/glad-buckeye` has branch
`glad-buckeye` (an `adjective-noun` from `internal/worktree/namegen.go`, no
dots, reserved for the room-JID separator, #275). So "session-id-prefixed
branches" has no referent today. Git also refuses `refs/heads/X` and
`refs/heads/X/y` to coexist, so a `glad-buckeye/foo` namespace cannot sit next
to the session branch.

Proposal (needs operator confirmation, see Open Questions):

- A branch ref `b` is **owned by session `s`** (with session branch `B`) iff
  `b == B` or `b` starts with `B + "--"`. `--` cannot occur in a generated
  name (single-hyphen words), so no session's namespace is a prefix of
  another's. A session creates scratch/topic branches as `<B>--<topic>`.
- Non-branch refs the tools create live under `refs/spinclass/<B>/...`, not
  `refs/heads`, so they never collide with branches and never show in `git
  branch`.
- "Owned" for other object kinds: a worktree is owned iff it is the session's
  own worktree path; a tag is owned iff its name starts with `B + "--"`
  (tag push to remote still asks, see Remote below); a remote ref is owned iff
  its short name is owned.
- A target is resolved to its **canonical full ref before the check**
  (`git rev-parse --symbolic-full-name`, fail closed on failure) so
  `HEAD`, `@`, `refs/heads/x`, abbreviations and reflog syntax cannot launder a
  foreign branch past a string prefix test. Operations with no named target
  (`commit`, `stage add`) bind to the checked-out branch of the worktree the
  call executes in, resolved the same way; the worktree itself must be the
  session's (`repo_path` is rewritten into the session worktree by the
  existing path rewrite, `internal/hooks`, #176).
- The default branch is never owned.

Where it is checked: only in `Backend.Owned`, called from `Classify`; the hook
and (for the CLI/backup path) the dynamic-perms helper both go through it.

### 4. Stash emulation: patch directory (chosen)

Real `git stash` is rejected: `refs/stash` is repo-wide (shared by main and all
worktrees), which is the bug. The two options offered were ephemeral branches
or a tool-maintained patch directory. **Choice: patch directory**, stored at
`<worktree>/.spinclass/stash/<n>/`.

Per entry:

- `meta.json`: id, message, base sha (HEAD at save), branch, time, mode flags.
- `tracked.patch`: `git diff --binary --full-index HEAD` (staged + unstaged
  tracked changes collapsed, as `git stash` does).
- `index.patch` (only if `--keep-index`/index preservation is requested).
- `untracked/`: copies of untracked files (tool-honoured `include_untracked`),
  with a manifest of relative paths and modes.

`save` writes the entry, then restores the tree (`git checkout -- .` /
remove the saved untracked files) exactly like `git stash push`. `apply`
re-applies with `git apply --3way` (conflicts leave markers and a clear
error, entry kept); `drop` deletes the directory. `list` shows only this
session's entries. `pop` stays as apply+drop if present in moxy's schema
(moxy's tool exposes save/apply/drop only; **not verified beyond the
description**).

Why a patch directory over ephemeral branches:

- **Confinement by construction.** A directory inside the session's own
  worktree cannot be addressed by another session's tool call. Branches live
  in the shared ref namespace; confinement would rest on the prefix
  convention, i.e. the same mechanism we are using to gate everything else.
- **Lifecycle is free.** `sc close` removes the worktree, and the stashes go
  with it. Branches would need explicit teardown on close, clean and
  resurrect (and leak on crash). Counter: this also means stashes do not
  survive `sc close`; FDR 0027 resurrect restores the commit, not the
  stash. That is acceptable, a stash is working state, but it is a behaviour
  difference from git.
- **No ref pollution.** `git branch`, `git for-each-ref`, `push --all` and
  `sc clean` never see them.

Costs, accepted:

- Reimplements parts of `git stash`: untracked and mode handling, and no
  support for submodules or ignored-file stashing (`--all`) in v1 (refuse
  with a clear error).
- Binary and rename fidelity relies on `git apply --binary/--3way`; git's
  own merge machinery is used on apply, but a stash made on a different base
  than HEAD is less robust than git's tree-merge.
- Entries are not in the object database, so no gc protection concerns, but
  also no reflog.
- Considered and rejected a hybrid: `git stash create` (makes the commit
  without touching `refs/stash`) pinned at `refs/spinclass/<B>/stash/<n>`. It
  has exact git fidelity and is gc-safe, but the refs are repo-visible and
  must be torn down on close/clean/crash like branches. Worth revisiting if
  the patch directory proves lossy; it is a localized change inside the git
  backend's `Shelf` implementation.

`.spinclass/` is assumed to be git-excluded in session worktrees, since
`job.log`/`job.json` already live there; **not verified, check
`internal/apply` excludes in step 3**.

`Shelf` is a neutral interface method so a dodder backend can implement it
natively (dodder has its own object store).

Stash and rebase classes: stash operations act only on the session's own
shelf and so are `Allow` with no ownership argument needed. Rebase/restack are
`write` class: auto-allowed iff the target branch and `--onto` are owned (a
rebase onto the default branch remote-tracking ref of an owned branch is the
normal case and is allowed; rewriting a non-owned branch asks). This is the
spinclass #380 / moxy #483 payoff.

### 5. Remote operations and credentials

Push/fetch/pull/push-stack/tag push interact with FDR 0028's per-session
credentials. Auto-allow applies only to owned refs and only through the
session's worktree-scoped credential helper, never an ambient ssh-agent.
Where no credential was minted (`allow-no-credential` or non-`forge-hosts`
origins) pushes of owned refs still classify `Allow` but use whatever the
worktree config provides; **interaction with `url.insteadOf` rewriting and
moxy #390 (force-with-lease "stale info") needs a look at implementation and is
not settled here**. `push` of a non-owned ref, `--delete` on any remote ref
and `clone` stay `Ask`. Landing the session branch to the default branch stays
the merge tool's job (FDR 0029); `remote push` of the default branch is `Ask`.

### 6. Names and migration

"Stable" is read as: tool leaf names (`branch`, `clone`, `commit`, `merge`,
`remote`, `stage`, `stash`, `tag`, `worktree`), subcommands and argument
schemas stay identical, so agent habits and prompts keep working. What
necessarily changes is the qualified name, because the server prefix changes
(`mcp__plugin_moxy_moxy__grit_<tool>` today; the qualified name under
spinclass follows its plugin prefix, `mcp__plugin_spinclass_spinclass__...`).
**I did not verify the exact final tool-name registration**: spinclass's
existing tools are un-prefixed (`spawn-session`), so I recommend registering
the new ones as `grit_<tool>` (`grit_branch`, ...) to keep moxy's leaf names
byte-identical and avoid colliding with `merge-this-session`.

Migration: the old qualified names appear in operators' tier files and
`claude-allow` entries, so `sc perms` gets a one-shot rewriter (rules naming
`...moxy__grit_*` are *reported*, not auto-promoted, because the new model
auto-allows less than a blanket allow-rule did). moxy keeps the old grit moxin
until its own removal question (moxy #482) is settled; both can coexist since
the names differ.

### 7. Moxin-path backup export

Requirement: ad hoc sessions under moxy, outside spinclass, can add the tools
manually. moxy's moxin format is declarative TOML (a `_moxin.toml` manifest
plus per-tool `<tool>.toml`, with `perms-request` and a `dynamic-perms`
helper table: command/args/arg-order/stdin-param/timeout-ms) found via
MOXIN_PATH entries (moxin(7)).

Design:

- spinclass gets CLI-only subcommands `sc grit <tool> [json]` (run one tool:
  same code as the MCP path, args as JSON on stdin or argv, JSON result) and
  `sc grit-perms <tool>` (reads the invocation JSON on stdin, prints
  `allow`/`ask`; the `dynamic-perms` helper).
- `sc moxin-export <dir>` writes a ready moxin directory (manifest plus nine
  tool files) whose tools shell out to `sc grit ...` and whose `perms-request`
  is `always-allow` / `dynamic` (-> `sc grit-perms`) / `each-use` per the tier
  table in section 2. The operator adds `<dir>` to MOXIN_PATH by hand. Nix
  packaging of the export (a derivation emitting the dir) is a follow-up.
- No session is required: with no resolvable session the shared `Classify`
  falls back to a **static** classification (`read` -> allow, `write`/`floor`
  -> ask), i.e. no auto-allow of mutations. That is the stated "work outside
  a spinclass session" boundary: it works, just without the ownership
  payoff. The tool bodies do not require a session either; stash then uses
  the cwd's worktree directory (`.spinclass/stash/` is created on demand), so
  the plain-checkout use is not session-confined, only worktree-local.

Decision 6 said they "do NOT need to work outside a session"; this export
makes them work (conservatively) there, which I read as the intended backup.

### 8. Implementation steps

Each step is a merge; moxy's grit stays live until step 7.

1. `internal/store` skeleton: `Backend`, `Op`, `Verdict`, `Capabilities`,
   `ErrUnsupported`; `internal/store/git` read verbs (stage status/diff,
   branch log/rev-parse/diff, tag list/verify, worktree list). Register the
   MCP tools (`grit_branch`, `grit_stage`, ...) with only these verbs. Tests
   use real temp git repos.
2. `Classify`/`Owned`: ref canonicalization, `<B>--` namespace, default-branch
   floor; `perms.StoreVerdict` wired into the PreToolUse hook; unresolved
   session fails closed. Table tests for laundering cases (`HEAD`, `@`,
   abbreviations, `refs/heads/` forms, `..` forms).
3. Owned-branch writes: stage add/unstage/rm/mv, commit, branch
   create/checkout/delete, reset, cherry-pick, revert, rebase/restack. Verify
   the `.spinclass` exclude and that the pre-commit hook (FDR 0019) still runs
   on `commit`.
4. Stash `Shelf` (patch directory) with `list/save/apply/drop`; document the
   close-time lifetime; bats test for two sessions' stashes not seeing each
   other. Closes the stash half of spinclass #380.
5. Remote family and `merge`/`tag push`/`clone`, against FDR 0028
   credentials; decide moxy #390 disposition.
6. CLI `sc grit`, `sc grit-perms`, `sc moxin-export`; `spinclass-*(5)/(7)`
   manpages (`spinclass-grit(7)`, sweatfile `[store]`); `perms` rewriter report.
7. moxy side (not this repo): deprecate or keep grit per moxy #482 using the
   export as the fallback.

## Interpretations

- **"Prefixed allow-list"**: no session-id prefix exists on branches; I defined
  ownership as session branch plus `<B>--*`, and tool-created refs under
  `refs/spinclass/<B>/`. If the operator meant a different prefix scheme
  (e.g. renaming session branches), this section changes.
- **Reads auto-allow regardless of ownership.** "Everything else is a
  permission request" read literally would prompt for `git status` on another
  branch; I kept moxy's `always-allow` read tier. Easy to tighten.
- **Hook as the sole gate** for foreign operations (tools cannot prompt).
- **"Stable"** means leaf names and schemas, not the qualified MCP name.
- **Stash lifetime ends with the worktree.** Chosen over a persistent stash.
- **Backup export works, conservatively, outside a session** (decision 6).

## Open Questions

- Confirm the ownership prefix (`<B>--` and `refs/spinclass/<B>/`).
- Final registered tool names (`grit_<tool>` vs un-prefixed) and the moxy-side
  deprecation plan (moxy #482).
- How the dynamic-perms helper gets a principal: moxy calls the helper with
  tool args, and spinclass resolves principal -> session via
  `SessionKeyOfPrincipal`; whether the helper process can see the
  principal (env) is **unverified**; falls back to static classification if
  not.
- Whether moxy's current annotations match section 2's table (I could not
  read the grit moxin definitions).
- moxy #390 (force-with-lease "stale info") and the interaction of remote verbs
  with the FDR 0028 credential/`insteadOf` lane.
- Whether `Shelf` should keep stashes across `sc close` (resurrect parity).

## Limitations

- Design only: nothing here is implemented or verified by running code.
- No out-of-process store backends; the dodder plugin is a design target only.
- The stash emulation is not byte-for-byte `git stash` (see Costs).

## More Information

FDR 0032 (principals), FDR 0028 (per-session credentials), FDR 0027
(resurrect), FDR 0019 (per-commit hook), FDR 0033 (one-shot plugin protocol),
FDR 0001/0014 (worktree-local state), `internal/perms`, moxin(7).
spinclass #379, #380; moxy #482, #483, #330, #390, #48.
