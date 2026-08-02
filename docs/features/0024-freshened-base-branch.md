---
status: experimental
date: 2026-08-02
promotion-criteria: |
  experimental -> testing: two weeks of ordinary use across several repos with
  (1) at least one observed refusal that was correct — a genuinely unreachable
  remote or a dirty checkout blocking the fast-forward — and cleared by fixing
  the repo rather than by reaching for the override, (2) no session refused
  for being merely ahead of upstream (the false-positive this design most
  needs to avoid, since it is the state of every repo after a --local-only
  merge), and (3) one confirmed spawn into an untouched sibling repo that
  freshened its base rather than inheriting a stale tree.
  testing -> accepted: no repo has had to set [hooks].allow-stale-base
  persistently, and no report of `sc start` latency being noticeable. If a repo
  DOES need the knob permanently, that is a signal the refusal set is drawn
  wrong, not that the repo is unusual.
---

# Session creation cuts from a freshened default branch

## Problem Statement

`sc start` / `spawn-session` created a worktree with:

```go
git worktree add -b <branch> <worktreePath>
```

No start-point, and nothing fetched first. Two independent defects hid in that
one line.

**The base was HEAD, not the default branch.** git-worktree(1): *"If `<branch>`
doesn't exist, a new branch based on **HEAD** is automatically created as if
`-b <branch>` was given."* So the base was whatever branch the operator's
checkout happened to be sitting on. Park the main checkout on a feature branch
and every session created from it silently inherited that branch. No amount of
fetching fixes this — the wrong ref was being read.

**Only some paths freshened anything.** `shop.Attach` called `pullMainWorktree`
before creating, so `sc start` / `resume` / `run` got *something*. But
`spawn.Launch` calls `shop.Create` directly and bypassed it entirely — and
dispatching a worker into a *sibling* repo is precisely the case most likely to
be stale, because nobody has been working in it. The pull was also aimed wrong
in two ways: it ran `git pull` on the checkout's **current** branch (so it could
advance a feature branch), and it **skipped silently** whenever the tree was
dirty, which is exactly when a human would want to know.

### Why this is worse than "you'll rebase later"

A stale base is not just stale source — it is a stale **toolchain**.
`flake.lock` is part of the tree, so a session branched from an old default
branch materializes its devShell from that old lock: the pinned formatter, the
codegen tool, the linters, the `[hooks].pre-commit` wrapper.

The motivating failure: a worktree whose devShell predated a `flake.lock` bump
kept invoking the *old* codegen tool from its cached PATH. Every commit silently
regenerated a generated Go file with the pre-rename module path, producing a tree
that did not compile. Hand-fixing did not stick — the next commit re-broke it.
Diagnosis required comparing store paths, because `nix develop --command` was
correct throughout (it re-evaluates the current flake) and only the PATH-resolved
hook was stale.

## Design

`internal/basebranch` owns the policy; `internal/git` gains only verbs.

`Freshen(ctx, repoPath, allowStale, required)` resolves the default branch,
fetches it, fast-forwards the **local** default branch when — and only when —
that is a pure fast-forward, and returns the resulting **sha** as the base.

It is called from `shop.createWorktree`, inside the `IsNotExist` branch and
before anything touches the filesystem. That location is the design: it is the
single funnel below `sc start`, `sc run`, `spawn-session` and the
`[[start-commands]]` plugins, so the spawn gap closes *by construction* rather
than by remembering to add a call. Running before `worktree.Create` means a
refusal cannot leave a half-built worktree behind.

### The outcome table

| Condition | Refuses? |
|---|---|
| No remote configured | no — nothing to be stale against |
| `main` + `master`, `origin/HEAD` doesn't decide | no — falls back to HEAD |
| Fetch failed | **yes** |
| Already current / fast-forwarded | no |
| Local default **ahead** of upstream | no |
| Dirty worktree blocks the fast-forward | **yes** |
| Local default **diverged** | **yes** |

Every refusal is overridable by `--allow-stale-base` or
`[hooks].allow-stale-base`.

**Ahead is not staleness.** Local contains everything upstream has, and it is
the state of every repo immediately after a `--local-only` merge. Treating it as
stale would produce a routine false positive, which would train operators to set
the override permanently and defeat the whole feature. This is why the design
classifies with a local `IsAncestor` check in both directions rather than
letting `git fetch <r> <d>:<d>` reject ahead, diverged and network failure
identically.

**Dirty refuses rather than warns.** The operator's call: creation demands a
verified base. The cost is real — a stray edit in the main checkout blocks every
new session, including non-interactive `spawn-session`, whose only way out is
the sweatfile knob. That is the intended pressure.

### No MCP parameter, deliberately

`--allow-stale-base` exists on `sc start`, `sc run` and the plugin
start-commands. It is CLI-only *by construction*: those register `RunCLI` with
no `Run`, and `RegisterMCPToolsV1` skips such commands.

`spawn-session` and `fork-session` get no equivalent. A driver agent must not be
able to wave away its worker's stale toolchain — that is the failure this record
exists to prevent, and an agent under time pressure is exactly who would reach
for the flag. The sweatfile knob still applies to spawned workers, so a repo's
owner can opt out deliberately; the agent cannot opt itself out.

### Two mechanics that are easy to get wrong

**The fetch refspec is explicit:** `+refs/heads/<b>:refs/remotes/<r>/<b>`.
Whether a bare `git fetch <remote> <branch>` *also* updates the remote-tracking
ref depends on `remote.<name>.fetch` covering it, and the ancestry classification
reads that ref back — so it must be updated unconditionally. Naming the
destination also keeps the write off any local branch, so git's refusal to
update a checked-out branch can never apply.

**The fetch is context-bounded with every interactive path disabled**
(`GIT_TERMINAL_PROMPT=0`, `GIT_ASKPASS=/bin/false`, `SSH_ASKPASS=/bin/false`,
`GIT_SSH_COMMAND="ssh -o BatchMode=yes"`). An ssh passphrase prompt and a git
credential helper both read `/dev/tty`, not stdin, so a nil `Stdin` is not
protection. Under `spawn-session` — stdio pointed at a log file, a 60s hello
deadline — a hang there surfaces as a spawn timeout with nothing to explain it.
`internal/git` had no context-bounded runner at all before this.

**Never prompt.** `merge.ResolveDefaultBranch` huh-prompts on an ambiguous
`main`/`master`. Calling it here would hang `spawn-session`. `basebranch` has its
own non-interactive resolver that consults `origin/HEAD` and otherwise returns
"", degrading to the pre-#250 HEAD-based behaviour.

**The base is a sha, not a branch name.** Deterministic against another process
moving the branch between the fast-forward and the `worktree add`, and a sha
start-point cannot trip `branch.autoSetupMerge = always` into silently giving the
session branch an upstream.

## What this does not fix

A long-lived worktree that rebases past a `flake.lock` bump **mid-session** —
which is the exact sequence that produced the motivating failure. spinclass
worktrees are explicitly long-lived workers accumulating many merge cycles, so
that path stays reachable; the workaround remains restarting the session with a
`direnv reload` in between. Tracked separately as #255. Creation-time enforcement
is the higher-value half and removes the largest source of divergence, but it is
not the whole of it.

Shallow and partial clones can make the `IsAncestor` classification answer
wrongly across a graft boundary. That degrades to a skip rather than a wrong
fast-forward.

## Rollback

Set `[hooks].allow-stale-base = true` globally to reduce the gate to advisory
everywhere. The explicit base passed to `git worktree add` is *not* covered by
that knob and does not need to be — it is a straight correctness fix with no
condition attached.
