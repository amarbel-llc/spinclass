---
status: experimental
date: 2026-09-01
promotion-criteria: A working [auth] mint→inject→revoke lifecycle drives a papi-minted per-repo token through a real merge push on the forge; a crashed session's orphaned token is revoked by the sweeper; no credential is ever written to the root checkout's on-disk config.
---

# Per-session forge push credentials

## Problem Statement

Session git pushes — both interactive and `merge-this-session` — authenticate off the inherited `SSH_AUTH_SOCK`, a forwarded card-backed ssh-agent that drops mid-run. On 2026-09-01 two multi-hour fleet passes died at ~45m–1h40m with `Permission denied (publickey)` on every subsequent push. A session needs a credential that is established once at session start, survives the whole session with no live forwarded agent, is scoped to just the repo the session works on, and is revoked when the session ends.

## Interface

A new sweatfile `[auth]` section declares how a session mints and revokes its push credential:

- `[auth].mint-command` — run at session creation, `sh -c` in the new session worktree, devshell-scoped exactly like the lifecycle hooks. It receives the session identity as `SPINCLASS_SESSION_ID` / `SPINCLASS_REPO` / `SPINCLASS_BRANCH` / `SPINCLASS_WORKTREE` plus `SPINCLASS_FORGE_HOST` and `SPINCLASS_FORGE_REPO` (`owner/name`), both derived from the origin remote, and prints a forge access token on stdout. The canonical value is a papi call (papi#73) that mints a **fine-grained, per-repo** Forgejo token through the forge **API** (`ResourceAllRepos=false` plus an `access_token_resource` row for the session's repo) — never `forgejo admin user generate-access-token`, which hardcodes `ResourceAllRepos=true` (all repos). Scope is `write:repository` (plus `read:repository` for a private repo; public forge repos are anonymously readable, so read scope is only needed for private ones). The requested TTL is part of the command string.
- `[auth].revoke-command` — run at session close with the same environment; revokes the token by session id.

Both keys merge across the sweatfile hierarchy with scalar-override semantics, consistent with `[hooks]`. `sc validate` warns when only one of the two is set.

Lifecycle (`internal/auth`):

1. **Mint** at `shop.createWorktree` — the single worktree-creation funnel below `sc start` / `sc spawn` / `sc run` (`internal/basebranch` freshening already lives on this funnel), immediately after the worktree is created and its setup applied. The token is written to the mode-600 file `.spinclass/git-credentials` in git-credential-store form, `https://spinclass:<token>@<forge-host>` (`.spinclass/` is already git-excluded and per-session). The username is a fixed literal: Forgejo (Gitea lineage, `services/auth/basic.go`) looks a non-empty basic-auth password up as an access token and never checks the username against the token's owner, while git-credential-store needs one to consider the entry complete. **A failed mint is fatal to session creation** and the half-built worktree is torn down: a session that silently pushed off the inherited agent instead would be exactly the failure this feature removes, and a spawned worker has no operator to notice. The mint is recorded on the session state as `credential.minted_at` (`session.State.Credential`; `session.Write` carries it forward from the worktree-local state, since the attach/spawn paths write their own state after the funnel).
2. **Inject** via worktree-scoped git config (`extensions.worktreeConfig`, already enabled for `core.hooksPath` by the per-commit hook, with the same `core.worktree` footgun guard):
   - `credential.helper = store --file=<worktree>/.spinclass/git-credentials`
   - `url.https://<forge-host>/.insteadOf = <ssh prefix of origin>` (`git@<host>:` or `ssh://git@<host>[:port]/`) — forge-host-scoped, so GitHub remotes keep SSH. This is `insteadOf`, not the `pushInsteadOf` the original draft named: the merge's own fetch must be agent-free too (below), and `pushInsteadOf` would have left it on SSH.
   Applied to the **session worktree** at creation (interactive pushes and fetches) and, per FDR 0029 (#284, Alt B), mirrored onto the **disposable detached landing worktree** at landing time (`auth.MirrorInto`, pointing at the session worktree's credential file) so the `merge-this-session` push authenticates with it. The config dies with the throwaway landing worktree; the root checkout's on-disk config is never touched.
3. **Credentialed pull.** The merge's default-branch pull (`PrepareMerge`, and again under the landing lock) no longer runs `git pull` in the root: `merge.pullDefault` fetches from the **session worktree** — where the credential is wired — then fast-forwards the root's local ref from the remote-tracking ref (through whichever worktree has it checked out, or by moving the ref when none does). So neither the fetch nor the push of a merge touches the ssh-agent.
4. **Revoke** at `close.RunResolved` (`sc close` / `close-child-session`), before the worktree is removed, and — closing a gap — at `clean.removeWorktree` (`sc clean`'s merged-worktree reaping had no teardown hook). Best-effort: a failed revoke is a `severity=warn` point, the credential file is left, and the token stays recorded unrevoked for the sweeper. A successful revoke removes the file and stamps `credential.revoked_at` on the state (live file or tombstone).
5. **Sweep** orphaned tokens (crashed / never-closed sessions): at the next session creation on the repo, `auth.SweepOrphans` runs the revoke command for every tombstoned or abandoned session of that repo whose credential record has no `revoked_at`, with that session's identity env, and stamps the tombstone. Best-effort per session; failures are reported and left for the next sweep. papi (papi#73) enforces a server-side TTL sweep as the backstop.

The credential never rides in any process environment (only the file path is referenced by the helper). An absent or expired credential is a clean no-op rather than a divergence: under FDR 0029 the merge push is `git push origin <landingSha>:refs/heads/<default>`, whose ff-check mutates nothing on an auth failure.

## Examples

sweatfile:

    [auth]
    mint-command = "papi forge-token mint --repo $SPINCLASS_FORGE_REPO --scope write:repository,read:repository --ttl 12h --session $SPINCLASS_SESSION_ID"
    revoke-command = "papi forge-token revoke --session $SPINCLASS_SESSION_ID"

At `sc start` in a forge-origin repo, spinclass runs `mint-command`, writes `.spinclass/git-credentials`, and sets the worktree-scoped git-config keys (`✓ mint credential <branch>`). A `git push` in the session worktree — interactive or via `merge-this-session` — then authenticates over HTTPS with the per-repo token; no ssh-agent is involved, so a dropped forwarded agent no longer kills the push. At `sc close`, `revoke-command` kills the token (`✓ revoke credential <branch>`). A session that crashes without closing has its token swept by papi's TTL and, belt-and-suspenders, revoked by the next `sc start`'s orphan sweep (`✓ revoke N orphaned credential(s)`).

## Limitations

- **Worktree sessions only.** Implicit main-checkout sessions (FDR 0014) are the operator working in the root checkout; their push is genuinely in the root and keeps SSH + the card agent by design. `[auth]` does not apply to them.
- **Forge plane only.** The `insteadOf` rewrite is scoped to the forge host. GitHub-hosted repos are a separate plane, solved independently by gh's host-level credential helper; `[auth]` never touches them.
- **Hard dependency on FDR 0029 (#284, Alt B).** The `merge-this-session` push authenticates off the serve-process env unless it runs in a worktree carrying the scoped config; the landing worktree is that surface. The `[hooks].disable-merge-queue` rollback path lands the old way and does not carry the credential.
- **Creation-time fetches stay on SSH.** The base-branch freshening at session creation (FDR 0024) fetches before any token exists for the new session, so it still needs a live agent (or `--allow-stale-base`). The credential covers everything after creation.
- **Mint once.** `sc resume`, `sc rebuild`, and resume auto-rebuild do not re-mint; a session whose token expired mid-life needs a close and a fresh start.
- **Sweeper mandatory now.** Forgejo tokens have no native expiry today, so ephemerality is a mint + revoke + sweep lifecycle owned by the issuer (see the expiry-aware future path below).

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| token TTL | 12h (in the mint command) | covers a long interactive/worker session without mid-run expiry, while bounding an orphaned token's lifetime | passes routinely exceed it (mid-run 401s) → raise; or an expiry-aware forge lands → shorten toward the sweep interval |
| sweep trigger | next session creation + papi server-side | no daemon; the next session is a natural, cheap sweep point | orphans accumulate between sparse `sc start`s → add a timer |

## More Information

- **Forward-looking design decision — expiry-aware minting (the sweeper-killer).** The sweeper is this feature's only structural cost, and it exists solely because Forgejo tokens lack native expiry (open + unimplemented upstream as forgejo#8837, "feat: Expiration of API token"). When a patched Forgejo carrying token expiry lands (circus's build + an upstream contribution against #8837), `mint-command` passes an `expires_at`, the forge invalidates the token itself, and spinclass's sweeper degrades to a belt-and-suspenders prune — no spinclass code change beyond threading the expiry field. The feature is deliberately shaped so the sweeper becomes vestigial rather than requiring a redesign. (Examination tracked on GitHub amarbel-llc/circus#205; forge-build side owned by circus.)
- **Rejected alternative — SSH client certificates (card-CA).** A card-CA-signed short-lived SSH user cert (Forgejo `SSH_TRUSTED_USER_CA_KEYS`) has native expiry (no sweeper) and keeps SSH, but Forgejo SSH auth is all-or-nothing at the user level — it cannot express the per-repo write the operator wanted. Chosen against for that reason; retained as the no-sweeper fallback should the per-repo-vs-sweeper trade ever be revisited. (Full ranking, client-side POC, and forge-source verification on spinclass#285.)
- Companion records: #284 / FDR 0029 (Alt B — the disposable detached landing worktree that carries the credential to the merge push); #285 (this feature's tracking issue and the SSH-CA-vs-token ranking); papi#73 (the mint / revoke / sweep issuer, with the API-mint constraint).
- FDR 0013 (isolated build worktree — the detached-worktree pattern Alt B extends); FDR 0014 (implicit sessions — the SSH-keeping split); FDR 0019 (the per-commit hook that first enabled `extensions.worktreeConfig` and owns the footgun guard).
