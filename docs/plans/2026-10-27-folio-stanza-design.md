# spinclass#438: the sweatfile `folio` stanza (design draft, no code)

Date: 2026-10-27. Status: design draft for the operator and for moxy#487/#490, nothing
built. Labels: R read in code or docs, I inferred/proposed. Direction and ordering are
the operator's (#438 body and its two comments, circus#399 workaround checklist); this
note only works out the shape.

## 1. What decides a path today

- **moxy's folio moxin** (R, `moxins/folio/bin/folio-perms`, a bash script): reads
  allow inside the CWD, /nix/store, `~/.claude`, `/tmp/claude-$UID`,
  `~/.local/log/moxy`, `$CLAUDE_CODE_TMPDIR`, and any path under the PARENT of this
  repo's main worktree except inside the main worktree itself ("sibling repo
  checkouts"); writes allow inside the CWD, `~/.claude/plans`, `~/.claude/projects`,
  `/tmp/claude-$UID`, the session tmp; writes into /nix/store are denied (exit 2);
  everything else is exit 1 = ask. `dynamic-perms exit 1` becomes a hard denial in a
  dontAsk worker. Paths resolve with the PARENT resolved and the leaf NOT followed.
- **spinclass's hook** (R, internal/delta/hooks): `moxy_read.go` (#372) auto-allows
  read-only moxy calls (folio_read/ls/glob/tree/file-type, grit log/rev-parse/diff,
  stage status/diff) whose every path is inside the caller's own worktree or the
  worktree of a session it holds an ACCEPTED handle on, but never inside another
  session's `.spinclass/`; the main-worktree guard and path rewrite (#176) cover
  `file_path`-like args of built-in tools and every moxy tool (`pathArgKeys`);
  sweatfile writes ask or deny (`deny-sweatfile-edits`); the dontAsk conversion turns
  any ask into a deny; the new floor denies background-shell tools. Anything not
  decided falls through to Claude Code's normal flow, i.e. moxy's own gate above.
- The observed pain (#438): 34 `folio_read` and 6 `folio_ls` asks for OTHER sessions'
  worktrees, e.g. `<repo>/.worktrees/<child>/.spinclass/spawn.log`. #372 does not
  cover them because `.spinclass/` is excluded by design and because not every such
  session is held.
- `rg_search` is not behind the folio gate at all (moxy#490).

## 2. The stanza (proposal)

```toml
[folio]
# Each entry is  <anchor>:<path-pattern>  (see 3). Lists are append-merge across TRUSTED
# layers; an explicit [] in a nearer trusted layer clears the allows above it.
read  = ["pwd:", "nix-store:", "home:.claude", "env:CLAUDE_CODE_TMPDIR:",
         "spinclass:siblings", "spinclass:children"]
write = ["pwd:", "home:.claude/plans", "home:.claude/projects"]
deny  = ["spinclass:worktrees/*/.spinclass/state.json", "env:XDG_STATE_HOME:circus"]
unmatched = "ask"          # "ask" (attended modes) | "deny"; dontAsk always means deny
```

- Separate `read` and `write` allow lists; a `deny` list that applies to both and **wins
  over any allow in the same evaluation** (specificity question, section 6).
- `unmatched`: what a path no pattern matches does. Default `ask`; under permission mode
  dontAsk it is always `deny` (nobody can answer), which is today's behaviour.
- Layering and trust follow the existing sweatfile rules, like `[permissions]`: the
  stanza is never merged into `Hierarchy.Merged`, read through a Trusted accessor
  (`sweatfileio.TrustedFolio`): **allows** (`read`, `write`) are honoured only from
  layers above the repo (global, parent directories); a repo- or worktree-layer
  value is ignored with a `sc validate` warning, except that **denies from any
  layer are honoured** (narrowing is safe, an agent may restrict itself).
- Anchors expand to absolute roots at evaluation time (I):
  `pwd:` the session worktree (the hook's CWD); `home:` the HOME in the process that
  evaluates (the agent-home in a confined unit); `env:NAME:` the named variable's value
  (empty or unset = the entry matches nothing); `nix-store:` /nix/store;
  `spinclass:worktree` own worktree; `spinclass:repo` the main checkout;
  `spinclass:worktrees` the repo's `.worktrees/` (own + sibling sessions);
  `spinclass:children` the worktrees of sessions the caller holds an accepted
  handle on (what #372 hard-codes today, as a root so it becomes data);
  `spinclass:siblings` the parent of the main checkout minus the main checkout
  (moxy's current "sibling repos" rule).
- Pattern grammar: path-prefix on a directory boundary, with `*` matching exactly one
  path segment in the middle (`worktrees/*/.spinclass/spawn.log`) and an optional
  trailing `/**` (default for a directory entry). No regexes, no `..` or `~` inside the
  pattern. Paths resolve before matching: `..` collapsed, symlinks followed on the parent
  and NOT on the leaf (moxy's rule), so a `result -> /nix/store/...` link in the
  worktree counts as a worktree path.

## 3. Where it is parsed and evaluated

- Parse: a `Folio` table in the sweatfile schema (`internal/0/sweatfile`, tommy codec
  regenerated), validated in `sc validate` like `CheckPermissions`.
- Evaluate: ONE pure engine, `Evaluate(policy, op, absPath, ctx) -> allow|deny|ask|nomatch`,
  with the anchors supplied by the caller in `ctx`. Per the operator's refinement (#438
  second comment) the engine is shared with moxy (#487, an embeddable Go library) and
  later juggler and clown; the sweatfile stanza is the policy SOURCE for spinclass
  sessions, a clown profile (step 3 of the direction) a second source that can only
  narrow. Recommendation (I): the pattern grammar and `Evaluate` live in moxy's library
  (moxy already owns the folio semantics and the tools), spinclass imports it the way it
  links clown's profile library (flake input + go-pkgs bridge; note the digest-coupling
  trap in `2026-10-27-profile-digest-coupling.md`: a linked library version must be
  bumped with its consumers), and each consumer owns only its config surface, decoding
  into the engine's `Rules` type. The alternative, a tiny separate module, avoids
  spinclass depending on moxy's whole library; decision for the operator.

## 4. What the hook enforces versus what moves

- Slice 0 (independent, smallest): **moxyfile edits always ask** (denied in dontAsk): a
  write/edit/move/copy-to of any moxyfile in the hierarchy (`.moxy/moxyfile`, `moxyfile`,
  `~/.config/moxy/moxyfile`, parents) by Write/Edit/folio_write/cp/mv/ln, in every mode
  including auto. Precedent: `deny-sweatfile-edits`. It is hook-only.
- Slice 1: the hook consults the stanza for the path arguments of moxy path tools
  (folio_*, rg_search which closes #490 for spinclass sessions, grit `repo_path`/`paths`,
  using the existing `pathArgKeys`), BEFORE the fall-through: a matching allow returns allow,
  a matching deny returns deny, **no match changes nothing** (moxy's own gate still
  decides). Non-regressing by construction.
- Slice 2: the default stanza reproduces moxy's current rule (the lists in section 1) so
  the two agree, then moxy's gate becomes advisory (or calls the shared engine) and the
  hook is the one decider; the explicit `unmatched` per mode replaces exit codes 1/2.
- Stays in the hook regardless: the dontAsk conversion, the sc-verb and store tools, the
  background-shell floor, the main-worktree guard.

## 5. First slice that removes the prompts hit (reads of sibling worktrees)

Slice 1 plus a global-sweatfile stanza, no moxy change:

```toml
[folio]
read = ["spinclass:worktrees"]
deny = ["spinclass:worktrees/*/.spinclass/state.json", "spinclass:worktrees/*/.spinclass/credentials"]
```

This lets a coordinator read any session worktree of the repo, including its
`.spinclass/spawn.log`, without a prompt, which retires circus's `debug-worktree-read`
workaround (circus#399 first entry). It needs the specificity rule of section 6 so the
spawn log stays readable while the rest of `.spinclass/` is denied. It does NOT widen
workers: the allow comes from a trusted layer, and a worker session that is not given the
stanza keeps today's rule.

## 6. Open questions (for the operator)

1. **Allow-over-deny specificity.** "Deny always wins" cannot express "read
   `.spinclass/spawn.log` but nothing else in `.spinclass/`". Options: longest-literal-prefix
   wins with deny winning ties (recommended), or two deny tiers. Decide before slice 1.
2. **Where the engine lives** (moxy library vs a small shared module), section 3.
3. **Confined-unit views.** The hook runs inside the unit and sees the unit's paths; a
   frontend-scoped moxin sees the frontend's. Binds are same-path for the worktree and
   most state, but not for the staged agent-home (`transcripts:inUnit` pairs, the
   credentials bind). The engine must say which view a pattern is in; proposal: patterns
   are in the CALLER's view and the hook translates nothing, so a frontend moxin
   evaluates only paths that exist identically in both views until a mapping exists.
4. **Do the tools move** (a `folio` namespace in spinclass's MCP server) **or stay in
   moxy with enforcement in the hook?** Hook-first (slices 0-2) is the smaller step; moving
   buys one place for non-filesystem backends later.
5. **`spinclass:children` and handles**: keep the accepted-handle rule inside the
   anchor (a handle is authority; a worktree path alone is not), so the stanza cannot
   name a session the caller has no handle on by listing its path.
6. **Profile-level patterns and spawner narrowing** (steps 3 and 4): the child's set
   must be a subset of the parent's. That needs the stanza to be comparable, which is a
   reason to keep the grammar this small (prefix + one-segment `*`).
7. **Evaluation cost and failure mode**: the hook runs on every tool call; the engine is
   pure and cheap, but resolving symlinks touches the filesystem. A resolution error is a
   `nomatch`, never an allow.
