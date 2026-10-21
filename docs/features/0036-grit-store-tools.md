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
   - The hook's principal is `CLOWN_SESSION_ID` when set, else the hook JSON
     `session_id` (`hooks.principalForHook`, the same source
     `recordOwnPrincipal` uses); both set and different is a known but
     unusable principal. Principal known but session unresolved (no recorded
     session, ambiguous, conflicting sources) -> `Ask`, fail closed, force
     included, matching `SessionKeyOfPrincipal`'s ambiguity behaviour. No
     principal at all (a confined caller the hook cannot see) -> a non-force
     call is deferred to the serve (`Allow`); force still asks (see "Hook defers an
     unresolved session" below).
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

`.spinclass/` is git-excluded in session worktrees (verified step 4:
`sweatfile.GetDefault` seeds it into the baseline excludes), so the shelf never
shows as dirty or untracked work. The shelf also never shelves itself: save
filters `.spinclass/` out of the untracked set even where no exclude exists.

Implemented in step 4 (`internal/store/shelf.go`, `internal/store/git/shelf.go`),
with these concrete choices:

- Entries are numbered `1, 2, ...` as `max(existing)+1` (so the number is
  reused only after the newest entry is dropped). `stash_ref` is `stash@{N}`
  (Nth newest, like git) or a bare entry number; empty means the newest.
- `index.patch` is written whenever the index differed from HEAD at save
  (moxy's `stash` has no keep-index parameter, so there is no flag to key on);
  apply restores exactly that staged state.
- `list` is a fourth verb, classified like the others (session-worktree
  binding, so a foreign `repo_path` asks).
- Apply is `git apply --3way` (which stages), then a path reset and
  `git apply --cached index.patch`. Conflicts leave markers, name the paths and
  keep the entry (`store.ShelfConflictError`). git refuses the 3-way when the
  same file has an unstaged edit ("does not match index"); that is reported
  as a plain error, nothing changes and the entry is kept. Untracked files that
  already exist block the apply before anything is touched.
- Submodule changes are refused; ignored files are never shelved.
- Teardown guards: `close.RunResolved` (so `sc close`, `close-child-session`,
  multi-target close and the shop auto-close) refuses a non-empty or
  unreadable shelf without force, naming the entry count, and with force warns;
  `sc clean` skips a shelved merged worktree; an out-of-session `sc merge`
  keeps the worktree and branch with a "keep worktree" point. `sc resurrect`
  reports that the shelf is empty. `sc run`'s teardown is not guarded.
- Real `git stash` is still used transiently by `rebase --autostash`
  (`branch rebase` with `autostash`), which pushes and pops on the shared
  `refs/stash` within one call; that is git's own rebase machinery, not the
  stash tool.

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
session's worktree-scoped credential helper, never an ambient ssh-agent. The
remote verbs run git with the session worktree as its working directory (so the
worktree-scoped `credential.helper` and `url.insteadOf` apply), with
`SSH_AUTH_SOCK` and `SSH_AGENT_PID` removed from the environment and
`GIT_TERMINAL_PROMPT=0`. Where no credential was minted (`allow-no-credential`
or non-`forge-hosts` origins) there is no fallback: the verb fails with git's
own auth error. moxy #390 (force-with-lease "stale info") is settled by
carrying `force_with_lease` and `lease_ref_sha` as given: both set is the
explicit `--force-with-lease=<branch>:<sha>` form, `force_with_lease` alone the
boolean form. `push` of a non-owned ref, `--delete` on any remote ref and a
`push --force` of a non-owned ref stay `Ask`. Landing the session branch to the
default branch stays the merge tool's job (FDR 0029); `remote push` of the
default branch is `Ask` for a resolved caller and refused outright by the body
for a confined or unresolved one.

#### Implemented in step 5

Built (spinclass #379): `remote fetch|pull|push|push-stack`, `tag
create|delete|push`, `merge`, `clone` and `worktree remove`, registered as
`grit_remote`, `grit_merge`, `grit_clone` and extended `grit_tag` and
`grit_worktree` (`internal/store/git/remote.go`).

- **No agent.** All remote verbs and `clone` run through `remoteRunner`
  (`git` with `cmd.Dir` = the session worktree, ambient ssh-agent variables
  dropped, `GIT_TERMINAL_PROMPT=0` last). `GIT_SSH`/`GIT_SSH_COMMAND` are
  dropped and `GIT_SSH_COMMAND` is then pinned to `ssh -o IdentityAgent=none
  -o IdentitiesOnly=yes -o BatchMode=yes`, so neither the environment nor an
  `IdentityAgent` in the operator's ssh config reaches an agent.
- **Remote names only.** `remote` must be a configured remote name or empty
  (git's default, `origin` for pull/push). A URL is refused by the body for
  every caller and classifies `Ask`, so the worktree's credentials cannot be
  sent to a host nobody configured. This also means an approved fetch of an
  arbitrary URL is not possible through the tool.
- **Verdicts.** `fetch` is read class (`Allow`; a non-configured remote name
  asks). `pull`, `push`, `push-stack`, `tag push` are write class: `Allow` iff
  the remote is configured and the targets are owned (`pull`: the current
  branch). `delete`, `force` and anything non-owned ask. `merge` is owned iff
  the current branch is. `tag create|delete` iff the name is `<B>--*`.
- **Push.** The refspec is always explicit, `refs/heads/<src>:refs/heads/<dst>`
  with `src` canonicalized (default the current branch). `force`,
  `force_with_lease`, `lease_ref_sha`, `force_if_includes`, `set_upstream` and
  `delete` are supported; `lease_ref_sha` or `force_if_includes` without
  `force_with_lease`, and `force` with `force_with_lease`, are errors.
  `push-stack` takes `--force-with-lease` (boolean form, per branch) after a
  dry run of every branch and returns `{"phase","results":[{branch,status,
  output}]}`; a failure returns the same JSON as the error, and nothing is
  pushed unless the whole dry run passed. `lease_ref_sha` does not apply to it.
- **Default branch.** A confined or unresolved caller pushing or deleting
  `main`/`master`/origin's HEAD (as source or destination) is refused by the
  body regardless of `force`; a resolved caller is asked by the hook.
  `push-stack` and `merge` into the default branch are refused for every
  caller (`push-stack` always force-pushes). Remote verbs' hooks are disabled
  for confined callers like the other write verbs, and `fetch` is added to that
  set (it fires `reference-transaction`).
- **Unrecognised caller (ruling b).** `resolveGritCaller` no longer borrows the
  cwd's branch. A caller owns something only when its worktree resolves to a
  live, non-implicit session (`session.FindByWorktreePath` on the git
  toplevel); otherwise `store.Session.NoSession` is set, `Branch` stays empty,
  and the body refuses every write (any confinement) with an error pointing at
  `sc start`; `Classify` asks. Reads are unaffected. This also applies to an
  unconfined plain checkout: the section 7 "works outside a session" backup
  path now reads only, until the export in step 6 decides otherwise. The
  session branch is now the recorded one, not the checked-out branch, so a
  worktree sitting on `<B>--topic` still owns `<B>`.
- **`worktree remove`** moved from floor to write class: `Allow` iff the target
  is another worktree whose checked-out branch is owned and `force` is unset;
  the body always refuses the worktree the call runs in.
- **`clone`**: `Allow` iff the destination (symlinks resolved on the part that
  exists) lies inside the session worktree, `.tmp` included; a confined or
  unresolved caller is refused outright. The source is not gated. Clone is also
  run without the ambient agent, which the ruling did not name.
- **`tag create`** honours `sign` (default true) and `lightweight`; signing
  failures surface as git's own error. `pull` always passes `--no-rebase` or
  `--rebase` explicitly, since git refuses a diverged pull otherwise. The merge,
  pull and tag-create verbs stamp the `[store]` identity.
- **moxy #390 disposition.** Carried as given (above). The "stale info" failure
  of the boolean form was not reproduced: with no remote-tracking ref git let
  the boolean lease through, so the boolean form gives no protection there,
  while the explicit form does (tested).

Not exercised: any https push with a minted credential (no forge), the
`allow-no-credential` ssh path, a real confined unit or clown, and signing
with a real key (the tests only see the no-key failure). `lease_ref_sha`
validation accepts 4 to 64 hex characters.

#### Accepted forms for branch create/checkout/delete (#393)

A canary against moxy's grit showed `branch checkout ref=origin/master
new_branch=true` creating a local branch literally named `origin/master`, and
`branch delete name=refs/heads/x` reporting not found where the short form
worked. The accepted forms are now one each:

- **create**: `name` is the new branch (short name), `start_point` the optional
  start (default HEAD). Returns `created branch <name> at <sha>`.
- **checkout** of an existing branch: `ref`. **checkout with `new_branch`**:
  `name` is the new branch, `start_point` the start. The legacy form (`ref`
  alone names the new branch) still works only when `ref` is a plain
  local-style name; otherwise it is refused with a pointer to
  `name`/`start_point`. `name` and `ref` together with `new_branch` is refused,
  as is `name`/`start_point` without `new_branch`.
- **new-branch names** (create and checkout) are refused when they begin with
  `refs/`, resolve as `refs/remotes/<name>`, or begin with a configured remote's
  name plus `/`. Nothing is created on refusal.
- **delete**: the short name or `refs/heads/<name>`, normalised to the short
  name before `git branch -d/-D`; any other `refs/` prefix is refused. `heads/x`
  is not an accepted alias (git would look for `refs/heads/heads/x`).
- **Ownership** (Classify, and so the confined body check) is judged on the same
  ref: the new branch's name for create and checkout `new_branch` (`name`, else
  legacy `ref`), the normalised `refs/heads/<short>` for delete, which must
  exist. `start_point` never affects ownership.
- Every branch verb returns non-empty text: git's own output, else the
  confirmation above (`deleted branch <name>` for delete). The handler keeps its
  generic fallback for the remaining silent verbs. The JSON parse error seen on
  moxy's `create` was not reproduced here: this port never emitted anything
  JSON-shaped for these verbs (only `push-stack` returns JSON).

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

**Amended in step 5 (operator ruling, 2026-10-21).** The MCP tools are
read-only for a caller outside a recognised session worktree (section 5), so
the paragraph above no longer describes the MCP path. The backup path is a
human-run CLI escape hatch in `sc grit` (step 6): it runs grit operations for
an operator outside any session, or when the rules misfire, so recovery is
never lost. It is not reachable from the MCP tools or the hook path, it
refuses without a TTY, and every use is logged. **Hard dependency:** moxy's
grit moxin must not be deprecated or removed (moxy #482, step 7) until this CLI
exists. moxy's grit stays untouched until then, so merging the MCP tools
loses no recovery today.

#### Implemented in step 6 (escape hatch)

Built (spinclass #379): `sc grit <tool> [json]` (`cmd/spinclass/grit_escape_cmd.go`),
the human-run CLI described in the amendment above. The `sc grit-perms` helper,
`sc moxin-export`, the `spinclass-grit(7)` page and the `perms` rewriter report
are not part of this slice (see "Implemented in step 6 (rest)" below).

#### Implemented in step 6 (rest)

- `spinclass-grit(7)` and the `[store]` table in `spinclass-sweatfile(5)`.
- `sc grit-perms <tool>` (`cmd/spinclass/grit_perms_cmd.go`): CLI-only; reads the
  invocation JSON on stdin, prints `allow` or `ask`; exit 0 = allow, non-zero =
  ask (moxy's dynamic-perms convention). It fails closed on an unknown tool,
  malformed JSON or a missing subcommand. The session is resolved from
  `CLOWN_SESSION_ID` the way the hook does; unresolved falls back to
  `NoSession` (reads allow, writes ask). Whether moxy can feed a
  `[dynamic-perms]` helper the full JSON on stdin, and whether the helper sees
  the principal variable, is unverified.
- Perms migration report (`internal/perms/grit_migration.go`): `perms-list`
  appends the tier rules naming `moxy__grit_*` with the equivalent spinclass
  tool name; it never rewrites. `claude-allow` entries in sweatfile layers are
  not scanned, and `perms-review` does not use it.
- **`sc moxin-export` is NOT built.** Section 7 says the exported tools shell out
  to `sc grit ...`, but `sc grit` refuses without a TTY and is not meant to be
  reachable from MCP or hook paths, so a moxin tool could never run it. The FDR
  names no non-TTY entry point; this needs a ruling before it can be built.

- **CLI-only.** `RunCLI` and no `Run`, so it is not an MCP tool and the hook path
  never reaches it; a test pins both. It is not `Hidden`, so the generated
  `spinclass-grit(1)` page exists.
- **Same code.** `runGrit` was split: `runGritAs` takes an already-resolved
  `store.Session` and calls the same `gritBackend.Do`, so remote verbs keep their
  agentless `remoteRunner`.
- **Terminal required.** Refuses unless stdin and stdout are both terminals
  (`golang.org/x/term`, behind the `gritEscapeIsTerminal` seam). Arguments come
  only from argv (one JSON object, default `{}`); stdin is never read for
  arguments, only for the typed confirmation.
- **Caller.** Unconfined. The session is the recognised session worktree when the
  cwd is in one, otherwise `Dir` is the cwd, `Branch` is empty and `NoSession` is
  false, so the read-only refusal does not apply. The `[store]` identity is
  stamped as for the MCP path.
- **Confirmation.** The verdict is `Classify` on that session. A non-`Allow`
  verdict prints the verb and arguments to stderr and needs a typed `y`; `--yes`
  skips the typing (a terminal is still required). A declined confirmation runs
  nothing and is logged. Outside a session every write classifies `Ask` (no owned
  branch), so every write there needs `y` or `--yes`.
- **Log.** One JSON line per invocation that gets past the argument checks
  (`time` UTC, `cwd`, `tool`, `subcommand`, `args`, `verdict`, `confirm_required`,
  `confirmed`, `outcome`, `error`) appended to
  `$XDG_STATE_HOME/spinclass/grit-escape.log` (default
  `~/.local/state/spinclass/`), directory 0700, file 0600. The file is opened
  before anything runs, so an unwritable log refuses the operation; a failure to
  write the final line after the operation ran is reported as an error (the
  operation cannot be undone). A run refused for lack of a terminal, or for
  malformed arguments or an unknown tool, is not logged: nothing was attempted.
- **Protections that still apply.** The body's default-branch refusals
  (`refuseDefaultBranchOp`: reset-hard, rebase, restack, merge, delete on
  main/master/origin HEAD) run for this caller too, `--yes` or not. The force-push
  and delete refusals of `refuseDefaultBranchPush` are scoped to a confined or
  unresolved caller, so the hatch adds its own rule in `grit_escape_cmd.go`
  (operator ruling): "the escape hatch never force-pushes, deletes, or rewrites
  the default branch". Before the verb runs, and regardless of `--yes` or a typed
  `y`, it refuses (and logs `outcome` error with the refusal text) a `remote push`
  of main, master or origin's HEAD branch (source or `remote_branch`
  destination) with `force` or `force_with_lease`, with `delete`, or that is not a
  fast-forward (the remote-tracking ref is not an ancestor of the pushed commit,
  by `git merge-base --is-ancestor`; a missing remote-tracking ref counts as
  unknown and is refused), and any `remote push-stack` naming it. A plain
  fast-forward push of the default branch is unaffected: it is an `Ask` answered
  with a typed `y`. `tag push` is unaffected. The default-branch resolution is a
  small local copy of the store's (`internal/git.Run`), not an export.
  The fast-forward check reads the local remote-tracking ref without fetching, so
  a stale ref can pass it; git itself still rejects a real non-fast-forward.

Not tested: a real terminal run (the test environment has none; the terminal check
is exercised through its seam), the generated manpage, and a push over a real
remote. `--yes` is only checked through the flag struct; positional parsing is
checked through `App.RunCLI`.

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
   other. Wire the shelf into `close.RunResolved` (non-empty shelf = refuse
   without force) and `sc resurrect` (reports an empty shelf). Closes the
   stash half of spinclass #380.
5. Remote family and `merge`/`tag push`/`clone`, against FDR 0028
   credentials; decide moxy #390 disposition.
6. CLI `sc grit`, `sc grit-perms`, `sc moxin-export`; `spinclass-*(5)/(7)`
   manpages (`spinclass-grit(7)`, sweatfile `[store]`); `perms` rewriter report.
   `sc grit` includes the human-run escape hatch (section 7): TTY required,
   every use logged, never reachable from the MCP or hook path.
7. moxy side (not this repo): deprecate or keep grit per moxy #482 using the
   export as the fallback. **Blocked until step 6's escape-hatch CLI exists.**
8. Confined callers (section 9): the single stamping setting, the hook policy
   for proxied commits, and the confined-caller ownership check in the tool
   body. The read-write `.git` bind removal itself is clown/FDR 0035 work
   (clown #274).

### 9. Confined callers: grit as the proxy (clown #274)

Added 2026-10-20 at the operator's request, relayed by the circus session
(clarabell). The operator agreed to proxy git through these tools. A confined
worker (FDR 0035) calls the grit tools; the calls reach `spinclass serve` in
the **frontend**, which can write the main `.git`. The worker's own `.git`
stays read-only. This is the planned answer to the read-write half of
clown #274; clown #274 narrows to what remains for clown, mainly dropping grit
from the in-unit moxyfile.

1. **Where the tools run.** For a confined caller the tool body runs in the
   frontend `serve`, never inside the unit. The unit needs no read-write
   `.git` binds. Nothing in this FDR's design requires the tools to run in
   the caller's own process; the `Backend` is told the session worktree and
   works on it from outside.
2. **Authorship.** The frontend stamps author and committer, so nothing is
   staged in the unit and the worker needs no git identity. *Whose* identity
   goes there is the open question in circus #351. This FDR makes it a single
   spinclass-side setting, one value read at commit time (proposed home:
   sweatfile `[store]`, key name to be chosen when step 8 lands), so the proxy
   can ship before #351 is decided. An unset value falls back to git's own
   resolution and fails the commit visibly, never silently under a guessed
   name.
3. **Hooks on proxied commits.** The per-commit hook (FDR 0019) re-evaluates
   the worktree's devshell when the flake lock moved (#267). If the frontend
   commits for a confined worker, that would execute **worker-authored code
   outside the unit**. Two options, **undecided** (flagged for the operator):
   - *Skip hooks on proxied commits* and rely on the merge gate (which runs in
     the pre-merge build worktree, FDR 0013). Safe, loses authoring-time
     repair for confined workers, so mechanical drift surfaces at merge.
   - *Run hooks inside the unit* (the proxy hands the staged tree to a unit
     process). Keeps repair, needs a unit-side hook runner and a way to carry
     the result back.
   The first is the conservative default if nothing is decided: it never
   crosses the trust boundary.
4. **Ownership in the tool body, for confined callers only.** Section 2 says
   the hook is the only gate, because a tool cannot prompt and, once a human
   approves a foreign operation, the tool must run it. For a confined worker
   under `dontAsk` an `ask` is a deny, so the hook is already safe, and a body
   check is cheap insurance against a hook misconfiguration. This does not
   contradict section 2 if the body check is scoped: it applies only when the
   caller is *confined* (and therefore cannot have been shown a prompt),
   refusing any target `Owned` rejects, plus the existing default-branch
   refusal. For unconfined callers the hook remains the sole gate. How the body
   learns "confined" (the FDR 0035 confinement record for the principal) is to
   be settled when step 8 lands; unresolved confinement is treated as
   confined (fail closed).
5. **Longer view (a later FDR, not this one).** `folio` moves into spinclass
   with path visibility and worktree enforcement done natively there, and
   later alternative backends (dodder-backed, cloud-backed). They should reuse
   this FDR's `Backend` and `Classify` shape and FDR 0033's one-shot protocol.
   No design is made here; recorded so the boundary in section 1 is not
   narrowed in the meantime.

#### Implemented in step 8 (confined-commit slice)

Built (spinclass #379); the read-write `.git` bind removal and clown's side
stay with FDR 0035 / clown #274.

- **Identity setting.** Sweatfile `[store]` with `author-name`, `author-email`,
  `committer-name`, `committer-email`; scalar override down the cascade;
  committer keys default to the author keys per field. Read by `runGrit` from
  the merged hierarchy of the session worktree and carried in
  `store.Session.Identity`. `Do` stamps it as `GIT_AUTHOR_*`/`GIT_COMMITTER_*`
  on `commit`, `branch cherry-pick`, `revert`, `rebase` and `restack` for every
  caller, confined or not. Unset fields add nothing, so git's own resolution
  stays as before. Git keeps the original author of replayed commits
  (cherry-pick, rebase, restack, amend), so for those only the committer
  changes. Documented in `spinclass-sweatfile(5)`.
- **Hooks.** For a confined caller every mutating verb runs as
  `git -c core.hooksPath=/dev/null <verb>`, and `commit` also gets
  `--no-verify`. This is broader than the commit-creating verbs on purpose: the
  hooks directory is worker-writable, so `post-checkout` on `branch checkout`
  would run worker code in the frontend just like `pre-commit`. Unconfined
  callers are unchanged (the hook runs). The merge gate is the remaining check.
- **Body ownership check.** For a confined caller `Do` refuses a write verb when
  `writeAllowed` (the same checker `Classify` uses: canonicalized targets,
  session-worktree binding of `repo_path`, owned current branch) rejects it,
  ignoring the `force` flag, which only decides whether a human is asked. The
  default-branch refusal still follows. Unconfined callers: no body check.
  Read verbs are not checked, matching the always-allow read tier.
- **Zero value fails closed.** `store.Session.Confinement` has an unresolved
  zero value that counts as confined; a caller is unconfined only when
  explicitly set so.
- **Detection.** `confinement.ClassifyCaller(sys, stateDir, principal)`.
  Confined unless every source positively shows otherwise: an empty or
  unusable principal, a frontend principal mapping for it, a unit record
  naming it (one, or several, which is reported as ambiguous), an unreadable or
  unparsable record, an unreadable records directory, or a process inside a
  clown agent unit all say confined. Only an absent mapping plus a records
  directory that is absent or holds no record for the principal says
  unconfined. The serve passes its own `CLOWN_SESSION_ID`.

**Can the serve identify a confined caller?** Yes, from frontend state, not
from the caller. The tool body runs in the frontend `serve`, which clown's
pluginhost starts as the launching clown's child, so its environment carries
that clown's `CLOWN_SESSION_ID`; the unit record's `principal` (written by the
same clown) and the frontend mapping `principals/<principal>.json` (written by
`RecordPrincipalFromUnit` from that record) are keyed by the same value. Both
live under `$XDG_STATE_HOME/spinclass/` in the frontend's home, which the unit
cannot see. This assumes one serve per launching clown (per session), as FDR
0035 describes; if a single serve ever fronts several sessions, its principal
cannot tell callers apart and everything would classify as confined.

**Left open.**

- The PreToolUse hook runs in the confined worker's own process, where
  `CLOWN_SESSION_ID` is absent (clown#136) and the state directory is hidden.
  `resolveStoreSession` therefore finds no session and the hook asks, which
  under `dontAsk` is a deny. A confined worker can only commit once the hook
  can allow it, so the hook needs a session source that does not depend on the
  principal environment variable (for example the worktree path the hook runs
  in); the body check exists so such an unauthenticated hook decision is not the
  only gate. `ClassifyCaller` already treats a process inside an agent unit as
  confined for that future use.
- An empty `CLOWN_SESSION_ID` (a bare serve outside clown) counts as confined,
  per the fail-closed ruling, so non-clown use of the grit tools gets the body
  check and no hooks. Flip the call site in `resolveGritCaller` if that proves
  too strict.
- (Resolved, see "Implemented: option 3 and hardening".) `[store]` was merged
  from every layer including the worker-editable worktree sweatfile.
- A corrupt record for an unrelated session makes every caller classify as
  confined (fail closed); repair or remove the record.
- (Resolved, see below.) Reads by a confined caller were not scoped.
- Not exercised against a real confined unit or a live clown.

#### Implemented: option 3 and hardening

Built (spinclass #379). The operator chose "option 3" for the hook problem
above: for a caller the hook cannot resolve, the in-unit PreToolUse hook stops
asking and the serve decides.

- **Hook defers an unresolved session only when no principal is visible**
  (`perms.StoreVerdict`; refined by #392). If the session cannot be resolved and
  the hook sees no principal at all (a confined caller, clown#136), a non-force
  call returns `Allow` with the reason "deferred to the serve: body ownership
  check applies". The hook's principal is `CLOWN_SESSION_ID` when set, else the
  hook JSON `session_id` (clown does not export `CLOWN_SESSION_ID` to the
  hook; `hooks.principalForHook`, the same source `recordOwnPrincipal` records
  from); if both are set and differ, the principal is known but unusable.
  `hooks.storeDecision` passes `perms.StoreVerdict` that principal plus whether
  one was seen. A principal that is known but maps to no recorded session, maps
  ambiguously, or conflicts returns `Ask` ("could not be resolved from its
  principal, so ownership cannot be judged; confirm"): the deferral is not
  justified because the principal is known. A resolved session is unchanged
  (`Classify` decides; owned targets allow, the rest ask). `force:true` asks in
  both unresolved cases, and the `AlwaysAsk` floors stay ahead of
  `StoreVerdict` in `runPreToolUse`, unchanged. The deferral is safe only
  because the body fails closed: a zero-value or unresolved `Session` counts as
  confined, and a write verb whose ownership cannot be computed (no session
  branch or no session worktree) is refused by `refuseUnownedForConfined`
  (`TestWritesWithoutComputableOwnershipAreRefused`).
- **One serve per principal** (`internal/confinement/serves.go`). At start
  `spinclass serve` writes `{principal, pid, ppid, started}` to
  `$XDG_STATE_HOME/spinclass/serves/<principal>-<pid>.json` (directory 0700,
  file 0600, removed on exit; records of dead pids are pruned at the next start
  and ignored when counting). `ClassifyServeCaller` (used by
  `resolveGritCaller`) adds to `ClassifyCaller`: more than one live record for
  the principal, or this serve's own record missing or mismatched, is confined.
  An empty principal writes no record and stays confined. A failed record write
  leaves the serve without a record, so confined.
- **`[store]` identity from trusted layers only.** `[store]` is no longer merged;
  `sweatfileio.TrustedStoreIdentity` reads each key from the last layer above
  the repo (global, parent directories) that sets it, as
  `TrustedURLResolver` does, and a repo or worktree value is ignored. `sc
  validate` warns (`validate.CheckStore`). Documented in
  `spinclass-sweatfile(5)`.
- **Confined reads are scoped.** For a confined or unresolved caller a read verb
  whose `repo_path` has a git toplevel other than the session worktree is
  refused (`refuseReadOutsideSession`); an absent `repo_path` uses the session
  worktree. Unconfined callers are not checked.

**Still open.**

- The one-serve record detects a duplicated `serve` process claiming a
  principal. It does not detect several peers sharing one serve through the
  clown stdio-bridge loopback port: they appear as one serve with one principal.
  That needs a peer check in clown.
- The hook may not see `CLOWN_SESSION_ID` even for unconfined sessions. Those
  sessions now get no human prompt on foreign grit operations; they get a hard
  body refusal on writes they do not own instead (operator-accepted). The
  body derives the session from the serve's cwd and the frontend mapping when
  the principal is unmapped.
- Not exercised against a real confined unit or a live clown.

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
- Section 9: the author/committer identity for proxied commits (circus #351);
  hooks on proxied commits (skip vs. run in the unit); and how the tool body
  detects a confined caller.
- (Settled in step 5) moxy #390 and the interaction of remote verbs with the
  FDR 0028 credential/`insteadOf` lane: see section 5.
- (Settled 2026-10-20) Stashes die with the worktree, but `sc close` must not
  lose them silently given FDR 0027: a non-empty shelf counts as uncommitted
  work, so `close.RunResolved` refuses without `--force` and names the entry
  count; with force it warns. `sc resurrect` recreates the commit only and
  reports that the shelf is empty. Added to implementation step 4.

Operator answers (2026-10-20) confirming the interpretations above: ownership
is `B` and `B--<topic>`; reads auto-allow on any branch.

## Limitations

- Design only: nothing here is implemented or verified by running code.
- No out-of-process store backends; the dodder plugin is a design target only.
- The stash emulation is not byte-for-byte `git stash` (see Costs).
- **Key-file ssh (step 5).** Remote verbs strip the ambient ssh-agent and pin
  ssh to `IdentityAgent=none`, but ssh key files it finds itself (default
  `~/.ssh/id_*`, `IdentityFile` in config) are not an agent and are not
  blocked. The operator ruled to leave ssh remotes allowed (2026-10-21).
- **Owned-branch force pushes are unprompted (step 5).** A `push` with
  `force_with_lease` of an owned branch classifies Allow; plain `force` asks
  (`Classify` asks on any `force` flag). Non-owned refs and remote deletes
  ask. Flagged for the operator.
- **No-session callers are read-only (step 5).** Outside a recognised session
  worktree every write is refused, with no in-band escape; recovery is the
  step 6 CLI (section 7), which does not exist yet. Until it does, moxy's grit
  is the fallback.
- **`reset-hard` discards uncommitted work (step 5).** On an owned branch, in
  the session's own worktree, `branch reset-hard` classifies Allow (operator
  ruling, 2026-10-21): it never prompts, and it throws away uncommitted
  changes and any commits not reachable from the new ref. The default branch
  and unowned branches still ask or are refused.
- A manager provisioning git credentials for the workers it spawns is not
  covered here; see spinclass #390 (an `[auth].spawn-grant` design) and papi #88.

## More Information

FDR 0032 (principals), FDR 0028 (per-session credentials), FDR 0027
(resurrect), FDR 0019 (per-commit hook), FDR 0033 (one-shot plugin protocol),
FDR 0001/0014 (worktree-local state), FDR 0013 (build worktree), FDR 0035
(session confinement), `internal/perms`, moxin(7).
spinclass #379, #380; moxy #482, #483, #330, #390, #48; clown #274;
circus #351.
