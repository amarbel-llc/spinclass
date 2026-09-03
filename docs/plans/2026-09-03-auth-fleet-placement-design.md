# `[auth]` fleet placement — design for review (not a rollout)

**Status:** proposal for the operator's review, 2026-09-03. Nothing here is
implemented or rolled out; FDR 0028 is live on spinclass's own sweatfile only.

**Question:** how does the FDR 0028 `[auth]` section (per-session forge push
credentials) reach the ~29 forge-origin repos under `~/eng/repos` — one entry
per repo, or one root-level entry — with GitHub-origin repos never trying to
mint a forge token, and without any repo's session creation ever failing
because of the placement itself.

## 1. Facts the design rests on

### 1.1 The sweatfile cascade (`sweatfileio.LoadHierarchy`)

For a repo at `~/eng/repos/<name>` the merge order is:

1. `~/.config/spinclass/sweatfile` — global (today: claude-allow, session-entry;
   no `[hooks]`, no `[auth]`).
2. `~/eng/sweatfile` — an **ancestor** of every repo under `~/eng/repos` and
   also eng's **own** repo sweatfile (eng is GitHub-origin). Carries the fleet
   `[hooks]` (pre-merge/pre-commit/repair), the fleet `[direnv.dotenv]`
   (`GOLANGCI_LINT_CACHE`, `TROUPE_*`), and `[[pre-merge-skills]]`.
3. `~/eng/repos/sweatfile` — ancestor of every repo under `repos/` only; today
   just `pre-merge = "just"`.
4. `<repo>/sweatfile`, then (at merge/close/clean time) `<worktree>/sweatfile`.

`[auth]` merges with scalar override: a later level's non-nil `mint-command`
/ `revoke-command` replaces the earlier one. An explicit **empty string**
(`mint-command = ""`) is non-nil, so it overrides — and `auth.Mint` /
`auth.Revoke` treat a blank command as "unset" and do nothing. That is an
opt-out that works **today with no code change**.

So "a root-level entry" has two candidate homes: `~/eng/sweatfile` (reaches eng
itself + all of `repos/`) or `~/eng/repos/sweatfile` (reaches `repos/` only).
The first is wrong for this feature — eng is GitHub-origin — so **root = `~/eng/repos/sweatfile`** in the rest of this doc.

### 1.2 How a session's forge coordinates are populated

`auth.Mint` reads the repo's **configured** `remote.origin.url` (not
`git remote get-url`, which would return the insteadOf-rewritten form) and
parses it with `auth.ParseForgeRemote`:

| origin | `SPINCLASS_FORGE_HOST` | `SPINCLASS_FORGE_REPO` |
|---|---|---|
| `git@code.linenisgreat.com:<name>.git` (25 repos) | `code.linenisgreat.com` | `<name>` (owner-less; papi resolves the owner against the forge) |
| `git@forge.starbrandshoes.com:owner/<name>.git` (`resume-builder`) | `forge.starbrandshoes.com` | `owner/<name>` |
| `git@github.com:owner/<name>.git` (`circus`, `stats-me`, `tacky`; eng itself) | `github.com` | `owner/<name>` |

Nothing in spinclass decides whether a host *is a forge*; the env is passed to
whatever `mint-command` says. On a GitHub-origin repo with the root entry and
no opt-out, `papi forge token mint --host forge.starbrandshoes.com --repo
owner/<name>` runs against the forge API for a repo that does not exist there
→ nonzero exit → **session creation fails and the half-built worktree is torn
down** (FDR 0028's deliberate fatal-mint rule). That is the failure mode any
root placement must design out.

Inventory (`just explore-repo-origins`, 2026-09-03): 29 repos under
`~/eng/repos`; origin hosts as above; conformist flake-input revs: spinclass
`7baec98`, everything else older (`3ada15e`, `9fd5a0a`, `a27fdd5`, `6ca81e1`,
`c2d3679`, `d2a1f71`) or no conformist input at all (`chaos`, `conformist`,
`igloo`, `just-us`).

## 2. Placement options

### A. Per-repo entries (29 sweatfile commits)

Each forge-origin repo gets the two `[auth]` lines in its own `sweatfile`.

- **Opt-out:** implicit — GitHub-origin repos simply never get an entry.
- **Failure mode of a miss:** a forgotten repo has no credential and pushes
  over ssh exactly as today. Safe, silent.
- **Cost:** 29 commits through 29 merge gates, each already needing the
  conformist bump first (§3), so realistically one `sc run` per repo — which
  is what the A2 update-nix convergence already does per repo, so the entries
  could ride along with it. Drift risk afterwards: the mint/revoke strings are
  duplicated 29× (a `--ttl` or `--host` change is another 29 commits).

### B. Root entry + explicit per-repo opt-outs (works today)

`~/eng/repos/sweatfile` carries `[auth]`; `circus`, `stats-me`, `tacky` each
add `[auth] mint-command = "" / revoke-command = ""` to their sweatfile.

- **Opt-out:** explicit, per GitHub-origin repo, must be present **before** the
  root entry lands.
- **Failure mode of a miss:** the dangerous direction — a GitHub-origin repo
  without its opt-out **cannot create sessions** (fatal mint). A new
  GitHub-origin repo added under `repos/` later inherits the breakage by
  default.
- **Cost:** 1 root commit + 3 opt-out commits; one place to tune later.

### C. Root entry + host allow-list in spinclass (recommended)

Add one knob, `[auth].forge-hosts = ["code.linenisgreat.com",
"forge.starbrandshoes.com"]` (array; nil inherits, override-not-append like
`doc-index-dirs`). `auth.Mint` mints only when `SPINCLASS_FORGE_HOST` is in
the list; otherwise it emits a `skip` test point (`mint credential <branch> #
SKIP origin host github.com not in [auth].forge-hosts`) and the session is
created exactly as today (ssh). `auth.Revoke`/the sweep are unaffected (they
only act on sessions that recorded a mint).

- **Opt-out:** by construction — positive gating on the forge host. GitHub-
  origin repos, and any future repo on an unlisted host, degrade to today's
  behavior automatically and *visibly* (the skip point).
- **Failure mode of a mis-typed host:** the same as A's miss — no credential,
  ssh as today — never a failed creation. `sc validate` warns when
  `mint-command` is set and `forge-hosts` is empty/unset (root entries are
  exactly where that would be a mistake).
- **Cost:** a small spinclass change (knob + `Mint` guard + validate warning +
  FDR/manpage lines; ~a dozen lines plus tests), then the 1 root commit. B's
  empty-string opt-out remains available as a per-repo override for anything
  the host list can't express.
- **Why not "absence of forge host = skip":** a host is always derivable from
  any origin URL, so absence is not a signal; and encoding "which hosts are
  forges" in spinclass code (rather than config) would be the wrong layer.

**Recommendation: C.** It is the only option whose *miss* is safe in both
directions, and it keeps the mint/revoke strings in one place.

## 3. Rollout sequencing (the conformist ordering)

conformist `git-remotes(#8)` before `7baec98` reads the insteadOf-rewritten
URL, so a session with `[auth]` on fails its **merge gate** (not its creation)
until the repo's `flake.lock` pins conformist ≥ `7baec98`. With a root entry,
`[auth]` turns on for every forge repo the moment the root commit lands, so
the ordering must be enforced by sequencing, not by the entry itself:

1. **A2 first, with the fleet still on ssh.** The update-nix convergence runs
   `sc run` per repo; today only spinclass has `[auth]`, so those sessions mint
   nothing and push over ssh exactly as before. A2 is what bumps conformist to
   ≥ `7baec98` everywhere (and spinclass to ≥ `0a860b7`, §4). No chicken and
   egg: the pins move while `[auth]` is still off.
2. **Verify before flipping:** `just explore-repo-origins` (in spinclass) lists
   each repo's pinned conformist rev; the root entry lands only when every
   forge-origin repo with a conformist input shows ≥ `7baec98`. Repos with no
   conformist input (`chaos`, `igloo`, `just-us`, conformist itself) have no
   git-remotes gate to trip.
3. **Flip:** land the root entry in `~/eng/repos/sweatfile` (with C's
   `forge-hosts`). It applies at the next `sc start`/`spawn`/`run` per repo;
   running sessions are untouched (mint-once).
4. **Straggler self-heal:** if a repo is missed, its first `[auth]` session
   fails at the gate with the git-remotes message; the fix inside that session
   is a conformist bump as its first commit — the gate runs on the committed
   `flake.lock`, so the same merge then passes. Nothing is bricked.

## 4. Profile prerequisite: `0a860b7`

Until the live profile carries spinclass ≥ `0a860b7` (merge/`sc run` teardown
revokes the credential), every out-of-session `sc merge <target>` and default
`sc run` on an `[auth]` repo orphans its token to papi's 12h TTL sweep. That is
a degradation (bounded leak, no breakage), but A2 is built on `sc run`, so the
root flip should come **after** the eng bump that puts `0a860b7` (or later)
into the profile — which A2 itself performs. Order: A2 → profile switch → §3
step 2 → flip.

## 5. Blast radius: papi / forge API unreachable at session creation

Today (FDR 0028 rule): mint failure is **fatal** — `sc start` errors, `sc
spawn`'s hello never arrives (the worker is reaped), `sc run` exits nonzero;
the half-built worktree is removed. Also fatal: no live ssh-agent at creation
(`--card-login` signs through it).

With a root entry that becomes fleet-wide: a forge **API** outage (the
canonical host) blocks *creating* sessions in all 26 forge repos even when the
git ssh plane is up. Options:

- **Keep hard-fail (recommended default).** Silently falling back to ssh is the
  exact failure this feature replaces, and a merge from such a session would
  hang on the flaky agent later. Failing at creation is early and loud.
- **Add an explicit escape hatch:** `sc start --allow-no-credential` /
  `[hooks].allow-no-credential = true` (the `--allow-stale-base` shape):
  degrade to ssh with a `severity=warn` point instead of failing. Deliberately
  **no MCP parameter** (a driver can't wave away a worker's missing credential),
  same reasoning as `allow-stale-base`. Small change; can ship with C.

Rollback of the whole placement is removing the root entry: sessions already
minted keep their credential until close; new sessions go back to ssh.

## 6. Decisions requested

1. Placement: **C** (root entry at `~/eng/repos/sweatfile` + `forge-hosts`),
   vs A or B.
2. Whether to ship the `--allow-no-credential` escape hatch with C, or keep
   hard-fail only.
3. Confirm the sequencing: A2 (pins + profile) → verify revs → flip.

Pointers: FDR 0028 (lifecycle), FDR 0029 (landing worktree), papi FDR-0016
(split planes: API host vs git host), spinclass#285, papi#73, conformist
`7baec98` (git-remotes reads the configured url), `just explore-repo-origins`.
