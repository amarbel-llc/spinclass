# `sc run` under the circus cascade: card, token, or neither, step by step; and batching tokens at cascade start (2026-11-04)

Research and design, no code. Asked by the operator through
circus/eager-holly (coordinator) so `circus update` can run its cascade of
`sc run`s overnight with the card away. Cascade facts come from
circus/quiet-maple (its 2026-11-04 report) and from circus's cascade scripts,
read read-only; everything about `sc run` is read from this tree at the
commit this note lands in.

Labels: **R** read in code or docs (file:line), **V** observed in a run,
**I** inferred or proposed, **U** unverified.

## 0. The cascade as it is today (R, from circus; V by quiet-maple)

- `circus update --from clown` runs ONE `sc run` per repo, strictly serial,
  17 repos in dependency order (clown, roustabout, juggler, galapagos,
  hyphence, posh, papi, madder, spinclass, pigsty, moxy, maneater,
  cutting-garden, smith, nebulous, chrest, dodder); circus and mephisto are
  excluded. Every origin is `ssh://git@code.linenisgreat.com/<repo>.git`, the
  owner-less vanity plane; none is GitHub.
- Per repo, `circus-cascade-repo` (circus
  `infra/hosts/home/circus-cli/cascade/repo.bash`) first runs
  `forge_git fetch -q origin` and `forge_git pull -q --rebase` in the MAIN
  checkout over HTTPS with smith's read token, via per-invocation `-c`
  config only (`cascade/forge-git.bash:59-70`): the checkout's on-disk config
  is untouched, so anything else that runs git in that checkout still rides
  SSH and the agent (`forge-git.bash:15-18`). Then, with no `--allow-*`
  flags and no profile (repo.bash:120-127):

      sc --format plain run --description "<repo>: nixpkgs-master bump (circus cascade)" \
        [--post-merge '<nix flake archive ... --to $CIRCUS_CACHE_URL ...>'] < bump-repo.bash

- The in-session script (`cascade/bump-repo.bash`) runs doppelgang, `nix flake
  update`, the follows repair, the go.nix reconcile, then a plain
  `git commit` (bump-repo.bash:147).
- The trip pool circus provisions (`infra/hosts/trip-pool.json`, host
  forge.starbrandshoes.com) declares slots for clown, papi and spinclass
  only, 2 each; the other 14 repos have none (quiet-maple, V).

## 1. One `sc run`, in order, and what each step needs

The three answers are **card** (the operator's PIV card through its agent:
piggy-agent for ssh and signing, `papi ... --card-login` for the forge API),
**token** (the session's per-repo forge token, FDR 0028), or **neither**.

1. **Resolve the repo and the default branch.** `run.Run` detects the repo,
   resolves the worktree path and the default branch
   (`internal/foxtrot/run/run.go:98-148`). `merge.ResolveDefaultBranch` may
   prompt on a main/master ambiguity; with stdin not a TTY the run fails
   fast instead (run.go:139-143). **Neither.**

2. **Freshen the base (FDR 0024).** `shop.Attach` → `shop.Create` →
   `createWorktree` calls `basebranch.Freshen(ctx, repoPath,
   allowStale=false, required=true)` before anything touches the filesystem
   (`internal/echo/shop/shop.go:113-123`). Freshen fetches the default
   branch **in the main checkout** (`internal/bravo/basebranch/basebranch.go:176-181`,
   `target.Fetch(fctx, repoPath)`), i.e. `git fetch --no-tags origin
   +refs/heads/<default>:refs/remotes/origin/<default>` with
   `GIT_SSH_COMMAND=ssh -o BatchMode=yes` and every prompt disabled
   (`internal/0/git/git.go:127-135`). The main checkout's origin is the ssh
   vanity URL and carries no credential wiring (section 0), so this fetch
   authenticates through the **ambient ssh-agent: card.** With the card
   away it fails; `required && !allowStale` makes that `ErrStaleBase` and the
   run fails before a worktree exists (basebranch.go:155-168, 184-192). With
   `--allow-stale-base` (or `[hooks].allow-stale-base`) the failure degrades
   to a skip and the base becomes the last-fetched tracking ref
   (basebranch.go:156-160), which the orchestrator refreshed over HTTPS
   seconds earlier (section 0), so it is in fact fresh. The cascade passes
   neither flag today (R, repo.bash). **This is the first card touch of a
   run, and it comes before any token is claimed.** U: whether flic's ssh
   config offers an agent-less identity that would let this fetch succeed
   with the card away; how long `agentretry.Do` (basebranch.go:184-186,
   spinclass#411) spins before giving up on a dead agent.

3. **Create the worktree and apply its setup.** `worktreesetup.Create`
   (`internal/charlie/worktreesetup/worktreesetup.go:39-84`): `git worktree add
   -b` at the base sha, then `applyWorktreeConfig` (worktreesetup.go:168-279):
   madder/dodder init, git excludes, `apply.Setup` (Claude settings, dotenv,
   direnv allow, the per-commit hook), `.mcp.json`, the repo's create hook,
   re-allow direnv. The per-commit hook install now evaluates the landed
   base's devShell once (`nix develop git+file://<repo>?rev=<base>`,
   `internal/bravo/apply/frozenhook.go`, spinclass#459): nix, substituters
   over https, no auth. **Neither.** U: a repo's own create hook
   (`[hooks].create`) is repo-defined; none of the 17 was read for this.

4. **Provision the credential (FDR 0028).** `shop.ProvisionCredential`
   (shop.go:169-242), on the same funnel:
   - **Orphan sweep first** (shop.go:176, `auth.SweepOrphans`,
     `internal/charlie/auth/auth.go:753-793`): every abandoned session of this
     repo whose credential was never marked revoked has its pinned
     revoke-command run. That command is `papi forge token revoke ...
     --card-login ...` (the repo sweatfile, `./sweatfile:39`; a pool token
     pins the same verb with `--session <slot>`, `auth/pool.go:93-96`):
     **card.** A failure is reported as a warn point and left for the next
     sweep (shop.go:244-247); it never fails the run. So with the card away,
     each `sc run` starts by failing to revoke the previous run's token in
     that repo, loudly but harmlessly.
   - **Mint** (`auth.Mint`, auth.go:395-524), from the sweatfile hierarchy
     with the repo layer read at the landed ref (auth.go:690-705): no
     mint-command and no pool directory → nothing (auth.go:399-401); origin
     host outside `[auth].forge-hosts` → visible skip, no credential
     (auth.go:416-418); the optional url-resolver runs (auth.go:419-425; U
     whether the fleet configures one and what it needs). Then, **pool
     first** (auth.go:426-443): if `$XDG_STATE_HOME/spinclass/token-pool`
     exists, `claimFromPool` renames the first unclaimed, unexpired slot
     whose manifest `repo` matches this repo's owner/name (an owner-less
     vanity origin matches on the last segment, pool.go:247-262) into
     frontend state (pool.go:174-232): **token, no card.** If the pool has no
     such slot: with a mint-command configured it **runs the mint-command**
     (auth.go:449-466), which is `papi forge token mint ... --card-login ...
     --ttl 12h` (`./sweatfile:38`): **card**; a failure is `[auth]
     mint-command failed`, and without `--allow-no-credential` the half-built
     worktree and branch are removed and the run fails (shop.go:216-221).
     Without a mint-command it is a visible skip and the session runs
     **with no credential** (auth.go:447-448), which defers the card need to
     step 7.
   - On a mint: the token is written 0600 under
     `$XDG_STATE_HOME/spinclass/credentials/` (auth.go:526-551), the
     worktree-scoped git config gets `credential.helper=store` and the
     ssh→https `insteadOf` rewrite for the forge host only (auth.go:559-581),
     and the record (`session.Credential`, with the revoke-command pinned) is
     written (auth.go:485-518).

5. **Record the session** inactive, PID 0 (run.go:168-175, Attach with
   `noAttach`). **Neither.**

6. **The step.** `runStep` (run.go:271-305) runs the bump script with cwd =
   the worktree, under `direnv exec <worktree>` when direnv resolves
   (`internal/charlie/sessionexec/exec.go:111-132`), with the SPINCLASS_*
   identity env. Inside it (bump-repo.bash):
   - doppelgang, `nix flake update`, go.nix reconcile: nix fetches inputs
     over https from the forges' archive URLs (anonymous reads on the vanity
     plane; U for any input that needs auth). **Neither** (U as noted).
   - `git commit` (bump-repo.bash:147): the per-commit dispatcher runs the
     frozen formatter and the repo's native hooks (**neither**, U per repo
     for native hooks). **Signing**: nothing in spinclass adds or suppresses
     a signature (`spinclass-grit(7)` COMMIT SIGNATURES, lines 308-316; the
     bump script's plain `git commit` neither), so the commit is signed iff
     the operator's global git config asks for it. If `commit.gpgsign` is on
     with a key behind piggy-agent (the fleet's convention; this tree's
     CLAUDE.md tells an agent whose commit fails on signing to have the
     operator unlock piggy-agent), the commit **needs the card** and fails
     with it away. **U: flic's global `commit.gpgsign`, `gpg.format` and
     `user.signingkey`.** This is the one need no token can satisfy.

7. **Merge** (run.go:228-236, `merge.ResolvedContext` with gitSync=true,
   `internal/delta/merge/merge.go:155`):
   - `PrepareMerge` fetches the target from the **session worktree**
     (merge.go:777-794: credDir is the worktree, "the one place a
     per-session push credential is wired, so a dropped ssh-agent cannot
     fail it"): **token** when minted, else ssh via the ambient agent:
     **card.**
   - Rebase onto the target, the pre-flight checks, then the pre-merge gate
     (`just`) in the detached build worktree (FDR 0013): nix builds and
     tests. **Neither.** U: a gate that fetches an authenticated input; the
     pre-merge attestation gate (FDR 0007) and whether an exemption applies
     to the CLI `sc run` path (cascades have run before, so I assume yes; U).
   - Landing (merge.go:584-622): the merge-queue lock, a re-fetch under the
     lock from the worktree (**token / card** as above), the disposable
     landing worktree with the credential wiring mirrored into it
     (`auth.MirrorInto`, merge.go:619, auth.go:589-614), then
     `target.Land` → `git push origin <sha>:refs/heads/<default>` from it
     (merge.go:671, `internal/alfa/landing/landing.go:113-122`,
     git.go:552-554): **token** when minted, else **card.** Without a
     credential and with the card away this is where a run dies, after the
     whole gate has run.
   - Advance the local default branch: a local fast-forward. **Neither.**

8. **Post-merge leg.** Every `[[post-merge]]` target the repo's sweatfile
   declares at the landed sha runs (FDR 0026; `sc run` passes
   `--post-merge-targets`/`--no-post-merge` through but the cascade sets
   neither, so **all** targets run). spinclass's own repo sweatfile declares
   none (R, `./sweatfile`), the other 16 and the trusted layers above the
   repos were not read (U). Then the cascade's dynamic `--post-merge`
   (run.go:449-480, ambient env, in the repo path): `nix flake archive
   --to $CIRCUS_CACHE_URL` of the merged rev. Its authentication is the
   cache's, not spinclass's: **U** (quiet-maple marked it U too).

9. **Revoke and tear down.** The merge's own teardown (inSession=false)
   revokes the credential (merge.go:1024-1040, `auth.Revoke`,
   auth.go:632-655) by running the pinned revoke-command: **card.** With the
   card away it fails; the record keeps `RevokedAt` nil, the credential file
   is removed anyway (auth.go:652-654), and the token stays live on the forge
   until the next sweep in that repo (step 4) or `sc revoke-orphans`
   (`cmd/spinclass/revoke_orphans_cmd.go:24-43`) succeeds with the card, or
   the issuer's TTL expires it. The worktree and branch are removed by the
   merge; `sc run` drops the index entry (run.go:384-388). **Card** for the
   revoke, **neither** for the removal.

**Summary of card touches with the pool slot present**: step 2 (the base
fetch in the main checkout), step 6 if commits are signed by the card, step
4's sweep and step 9's revoke (both fail-soft). **Without a slot** add step
4's mint (fatal) or, without a mint-command, step 7's fetch and push
(fatal, late). Tokens cover steps 4, 7 and nothing else.

## 2. The token's life

- **Minted or claimed** once per session, at creation, after the worktree
  exists and before the state is written (step 4). A pool slot is consumed by
  renaming its file out of the pool (pool.go:213); once the session is wired
  the renamed file is deleted (auth.go:469-477, `claim.discard`); it is put
  back only if wiring fails (`claim.release`). **A slot is consumed per
  session and never returns to the pool.** The forge-side token lives until
  its deadline (the slot's `--ttl`, recorded as `Deadline`, pool.go:58-64)
  or a revoke.
- **Released** (revoked) by the pinned revoke-command at close or merge
  teardown (step 9), by the orphan sweep at the next session creation in the
  same repo (step 4), or by `sc revoke-orphans [repo]`. Every one of those is
  `papi ... --card-login`: **the card is needed to release, never to use.**
  The sweep and `sc revoke-orphans` retry until one succeeds; the TTL is the
  backstop (auth.go:742-752).
- **Where the slot binds**: to a repo (`slot.Repo`, matched to the session's
  resolved owner/name), not to a session; the first unexpired, unclaimed slot
  for the repo wins (pool.go:195-230). Two slots per repo = two sessions of
  that repo before the pool is dry for it.
- **A repo with no slot** falls through to the mint-command, i.e. the card
  (auth.go:444-466); with no mint-command either, the session has no
  credential and its fetch and push ride the agent (section 1, steps 7).
  Neither case is detected before the step and the gate have run.
- **A GitHub origin**: `[auth].forge-hosts` lists the Forgejo plane only
  (`./sweatfile:37`), so the mint is skipped as "origin host not in
  forge-hosts" and the session has no credential; its push needs the card.
  The pool cannot cover it either: the manifest's forge host must be in
  forge-hosts (pool.go:151-160) and papi mints Forgejo tokens (U for GitHub).
  Moot for this cascade (no GitHub origins), relevant if one is added.

## 3. What tokens do not fix (the findings)

1. **The base fetch runs in the main checkout over ssh, before any
   credential exists** (step 2). With the card away every `sc run` fails
   there unless `--allow-stale-base` is passed. Passing it is safe for the
   cascade because the orchestrator has just refreshed the same tracking ref
   over HTTPS; the "stale" base is seconds old.
2. **Commit signing** (step 6) is outside the token's reach. If flic signs
   with a card-backed key, the cascade needs either unsigned commits for
   this path (`git -c commit.gpgsign=false commit ...` in bump-repo.bash,
   circus's lane; FDR 0036 already accepts unsigned grit commits by the
   operator's 2026-10-25 ruling) or an agent-less signing key. Operator
   decision; verify the config first.
3. **Revocation needs the card**, so an overnight cascade leaves one live
   token per repo until the operator returns (bounded by the TTL). The
   failures are loud (a warn point per run, and the next run's sweep) but
   not fatal.
4. **No-slot repos fail late**: a repo with neither slot nor usable
   mint-command runs its whole gate and then fails the push. spinclass has
   no "require a credential" switch today.
5. **Post-merge targets run by default**; the cascade should decide per
   repo whether a deploy belongs in a bump (circus's lane, with
   `--no-post-merge` / `--post-merge-targets`).

## 4. Design: batching tokens at cascade start

### What the pool already is

`papi forge token mint --batch <rows> --out $XDG_STATE_HOME/spinclass/token-pool
--ttl <dur>` is already "one card touch mints N per-repo tokens" (FDR 0028
"Token pool", `docs/features/0028-per-session-forge-push-credentials.md:68-72`;
the plan `docs/plans/2026-10-26-unattended-spawn-without-agent.md`), and
`sc run` already claims from it ahead of the mint-command (step 4). So a
`sc run-multi` or a spinclass pre-mint verb would re-implement the batch
mint papi owns and the claim spinclass already does. **Nothing new is needed
to mint in a batch; what is missing is coverage and the three gaps above.**

Why the pool is not enough for a cascade over every first-party repo today:

- **Coverage**: the pool is provisioned for 3 of the 17 repos
  (circus `trip-pool.json`). Each `sc run` consumes one slot of its repo
  (section 2), so a cascade needs one slot per repo per run, plus a spare
  for a retry.
- **TTL**: a slot's deadline must outlast the whole serial cascade
  (17 gates), not one session. The mint-command uses `--ttl 12h`; the batch
  rows carry their own TTL (papi#80, U for the exact flag).
- **Pre-flight**: nothing refuses a run whose repo has no slot before the
  gate runs; nothing checks the pool against the cascade's list.
- **Release**: 17 revokes with the card at the end, one per repo
  (`sc revoke-orphans <repo>` × 17 today).

### Options

**A. Extend the pool, no new verb (recommended first step).** circus
declares a slot per cascade repo (17 rows, one or two slots each) in
`trip-pool.json`; `circus update` gains a pre-step the operator runs with
the card present: `papi forge token mint --batch` for the cascade's repo
list with a TTL of the expected cascade duration plus margin; then the
cascade runs. Changes: circus only (declaration, pre-step, `--allow-stale-base`,
the signing decision). spinclass: none required.

**B. spinclass pre-flight and credential-first creation (recommended
second step, spinclass).**
- `[auth].require-credential = true` (trusted layers) and/or
  `sc run --require-credential`: a run whose repo gets no credential (no
  slot, no usable mint, skipped host) is refused at step 4, before the step
  and the gate. Small; test-first.
- **Credential-first creation**: claim or mint BEFORE the base fetch, and
  fetch the base with the session's credential through per-invocation git
  config in the main checkout (`-c credential.helper=store --file=<cred>
  -c url.<https>.insteadOf=<ssh origin>`, the same shape forge_git uses),
  never writing the main checkout's config. The credential file is keyed by
  the worktree path (auth.go:122-130), which exists as a path before the
  worktree does, so the order can change; `Inject` into the worktree stays
  after `git worktree add`. This removes the first card touch for every
  session with a slot, not just cascade runs. Medium; it reorders
  `createWorktree` (shop.go:113-143) and needs a Freshen variant that takes
  the credential. Until then, `--allow-stale-base` from the cascade is the
  interim.
- `sc revoke-orphans --all` (or a cascade-side loop) so the operator
  releases every cascade token in one sitting; a papi `revoke --batch`
  (papi's lane) would make that one card login instead of 17 (U whether
  papi's `--card-login` is per invocation).

**C. One cascade-wide token.** A single token scoped to all 17 repos (or to
the user) for the cascade window. Rejected: papi mints per-repo tokens and
spinclass's manifest and credential are per repo by design (least privilege
per session, FDR 0028); one token would widen the blast radius of any leak
to every first-party repo for the TTL, with no gain over A.

### Security trade-offs of batching

- **Standing credentials**: a pool is N live bearer tokens for the TTL,
  stored 0600 in frontend state outside every worktree and unit bind
  (`confinement.ProtectedStateDirs`, pool.go:33-35), consumed by rename so
  two claimers cannot share one. The exposure window is the TTL; the
  mitigation is a TTL sized to the cascade (hours, not days) and `sc
  revoke-orphans` on return.
- **Scope**: per repo, as today. Option C would widen it; A and B do not.
- **Revoke**: unchanged, card-gated, fail-soft, TTL-backstopped. A cascade
  that aborts midway leaves its unclaimed slots in the pool (unused but
  live) and its claimed tokens unrevoked until the operator's return; both
  expire with the TTL.
- **What a worker could do**: nothing new. The pool directory and the
  credential files are never bound into a unit; a worker's `grit_remote`
  push uses the wired helper, never the pool.

### Recommendation

1. circus: declare all 17 cascade repos in the trip pool (one slot each,
   plus one spare per repo for a retry), size the TTL to the cascade, and
   run the batch mint as the operator's single card touch at cascade start.
   Pass `--allow-stale-base` to the cascade's `sc run` (the orchestrator's
   HTTPS fetch is the freshness) until B's credential-first creation lands.
2. Operator: settle commit signing for cascade commits (unsigned via
   `-c commit.gpgsign=false` in bump-repo.bash, or an agent-less key).
   Without this, no token design makes the cascade card-free.
3. spinclass (test-first, merge on a go): `require-credential` as a
   pre-flight; credential-first creation so the base fetch uses the session
   token; `sc revoke-orphans --all`. Each is a separate small change.
4. circus: decide `--no-post-merge` / `--post-merge-targets` per repo for
   bump runs, and confirm the cache archive's own authentication is
   card-free.

## 5. Unverified, in one place

- flic's global git signing configuration (step 6), and its ssh config's
  behaviour with the card away (step 2).
- `agentretry.Do`'s total wait on a dead agent (step 2).
- Whether the fleet configures `[auth].url-resolver`, and what it needs.
- Each repo's create hook, native commit hooks, gate inputs and
  `[[post-merge]]` targets (only spinclass's repo sweatfile was read).
- The pre-merge attestation gate's applicability to the CLI `sc run` merge.
- The `nix flake archive --to $CIRCUS_CACHE_URL` authentication.
- papi's batch TTL flag and whether its `--card-login` is one login per
  invocation; whether papi can mint GitHub tokens.
- The exact cascade duration (sizes the TTL).

## 6. Sources

spinclass: `internal/foxtrot/run/run.go`, `internal/echo/shop/shop.go`,
`internal/bravo/basebranch/basebranch.go`, `internal/0/git/git.go`,
`internal/charlie/worktreesetup/worktreesetup.go`,
`internal/bravo/apply/frozenhook.go`, `internal/charlie/auth/auth.go`,
`internal/charlie/auth/pool.go`, `internal/delta/merge/merge.go`,
`internal/alfa/landing/landing.go`, `internal/charlie/sessionexec/exec.go`,
`cmd/spinclass/revoke_orphans_cmd.go`, `./sweatfile`, FDR 0028,
`spinclass-grit(7)`, `spinclass-sweatfile(5)`. circus (read-only):
`infra/hosts/home/circus-cli/cascade/{repo,bump-repo,forge-git}.bash`.
quiet-maple's two reports of 2026-11-04 for the repo list, origins, slots
and invocation.
