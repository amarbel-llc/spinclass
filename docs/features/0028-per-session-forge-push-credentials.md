---
status: proposed
date: 2026-09-01
promotion-criteria: A working [auth] mint→inject→revoke lifecycle drives a papi-minted per-repo token through a real merge push on the forge; a crashed session's orphaned token is revoked by the sweeper; no credential is ever written to the root checkout's on-disk config.
---

# Per-session forge push credentials

## Problem Statement

Session git pushes — both interactive and `merge-this-session` — authenticate off the inherited `SSH_AUTH_SOCK`, a forwarded card-backed ssh-agent that drops mid-run. On 2026-09-01 two multi-hour fleet passes died at ~45m–1h40m with `Permission denied (publickey)` on every subsequent push. A session needs a credential that is established once at session start, survives the whole session with no live forwarded agent, is scoped to just the repo the session works on, and is revoked when the session ends.

## Interface

A new sweatfile `[auth]` section declares how a session mints and revokes its push credential:

- `[auth].mint-command` — run at session creation. It receives the session's target repo and a requested TTL (via the `SPINCLASS_*` identity env) and returns a forge access token on stdout. The canonical value is a papi call (papi#73) that mints a **fine-grained, per-repo** Forgejo token through the forge **API** (`ResourceAllRepos=false` plus an `access_token_resource` row for the session's repo) — never `forgejo admin user generate-access-token`, which hardcodes `ResourceAllRepos=true` (all repos). Scope is `write:repository` (plus `read:repository` for a private repo; public forge repos are anonymously readable, so read scope is only needed for private ones).
- `[auth].revoke-command` — run at session close; receives the session id / token handle and revokes the token.

Both keys merge across the sweatfile hierarchy with scalar-override semantics, consistent with `[hooks]`.

Lifecycle:

1. **Mint** at `shop.Create` — the single worktree-creation funnel below `sc start` / `sc spawn` / `sc run` (`internal/basebranch` freshening already lives on this funnel). The returned token is written to a mode-600 file `.spinclass/git-credentials`; `.spinclass/` is already git-excluded and per-session.
2. **Inject** via worktree-scoped git config (`extensions.worktreeConfig`, already enabled for `core.hooksPath` at `internal/sweatfile/precommit.go:102-106`, with the `commonConfigHasWorktreeOverride` footgun guard at `:225-233`):
   - `credential.helper = store --file .spinclass/git-credentials`
   - `url.https://<forge-host>/.pushInsteadOf = git@<forge-host>:` — forge-host-scoped, so GitHub remotes keep SSH.
   Applied to the **session worktree** at creation (interactive pushes) and, per FDR 0013 / #284 (Alt B), to the **disposable detached merge worktree** at landing time (the `merge-this-session` push). The config dies with the throwaway merge worktree; the root checkout's on-disk config is never touched.
3. **Revoke** at `close.RunResolved` (`internal/close/close.go`, where nix-gc already hooks) on `sc close` / `close-child-session`, and — closing a gap — at `clean.removeWorktree` (which has no teardown hook today, so a `sc clean`-removed session would otherwise orphan its token).
4. **Sweep** orphaned tokens (crashed / never-closed sessions): spinclass best-effort revokes tokens for tombstoned/abandoned sessions at the next `sc start`; papi (papi#73) enforces a server-side TTL sweep as the backstop.

The credential never rides in any process environment (only the file path is referenced by the helper). An absent or expired credential is a clean no-op rather than a divergence: under #284 (Alt B) the merge push is `git push origin <landingSha>:refs/heads/<default>`, whose ff-check mutates nothing on an auth failure (contrast today's ff-then-push, where a late auth failure leaves the local default branch ahead of origin).

## Examples

sweatfile:

    [auth]
    mint-command = "papi forge-token mint --repo $SPINCLASS_REPO --scope write:repository,read:repository --ttl 12h --session $SPINCLASS_SESSION_ID"
    revoke-command = "papi forge-token revoke --session $SPINCLASS_SESSION_ID"

At `sc start` in a forge-origin repo, spinclass runs `mint-command`, writes `.spinclass/git-credentials`, and sets the two worktree-scoped git-config keys. A `git push` in the session worktree — interactive or via `merge-this-session` — then authenticates over HTTPS with the per-repo token; no ssh-agent is involved, so a dropped forwarded agent no longer kills the push. At `sc close`, `revoke-command` kills the token. A session that crashes without closing has its token swept by papi's TTL and, belt-and-suspenders, revoked by the next `sc start`'s orphan sweep.

## Limitations

- **Worktree sessions only.** Implicit main-checkout sessions (FDR 0014) are the operator working in the root checkout; their push (`internal/merge/merge.go:680`) is genuinely in the root and keeps SSH + the card agent by design. `[auth]` does not apply to them.
- **Forge plane only.** The `pushInsteadOf` rewrite is scoped to the forge host. GitHub-hosted repos are a separate plane, solved independently by gh's host-level credential helper; `[auth]` never touches them.
- **Hard dependency on #284 (Alt B).** The `merge-this-session` push authenticates off the serve-process env unless it runs in a worktree carrying the scoped config. Without Alt B the push runs in the root checkout (`git.CommonDir(cwd)`), where worktree-scoped config cannot reach it, forcing an env-injection fallback (`git.RunEnv`, `internal/git/git.go:30-58`). Alt B is the injection surface this feature assumes.
- **Sweeper mandatory now.** Forgejo tokens have no native expiry today, so ephemerality is a mint + revoke + sweep lifecycle owned by the issuer (see the expiry-aware future path below).

## Tuning Levers

| Lever | Current | Rationale | Change signal |
|---|---|---|---|
| token TTL | 12h | covers a long interactive/worker session without mid-run expiry, while bounding an orphaned token's lifetime | passes routinely exceed it (mid-run 401s) → raise; or an expiry-aware forge lands → shorten toward the sweep interval |
| sweep trigger | next `sc start` + papi server-side | no daemon; the next session is a natural, cheap sweep point | orphans accumulate between sparse `sc start`s → add a timer |

## More Information

- **Forward-looking design decision — expiry-aware minting (the sweeper-killer).** The sweeper is this feature's only structural cost, and it exists solely because Forgejo tokens lack native expiry (open + unimplemented upstream as forgejo#8837, "feat: Expiration of API token"). When a patched Forgejo carrying token expiry lands (circus's build + an upstream contribution against #8837), `mint-command` passes an `expires_at`, the forge invalidates the token itself, and spinclass's sweeper degrades to a belt-and-suspenders prune — no spinclass code change beyond threading the expiry field. The feature is deliberately shaped so the sweeper becomes vestigial rather than requiring a redesign. (Examination tracked on GitHub amarbel-llc/circus#205; forge-build side owned by circus.)
- **Rejected alternative — SSH client certificates (card-CA).** A card-CA-signed short-lived SSH user cert (Forgejo `SSH_TRUSTED_USER_CA_KEYS`) has native expiry (no sweeper) and keeps SSH, but Forgejo SSH auth is all-or-nothing at the user level — it cannot express the per-repo write the operator wanted. Chosen against for that reason; retained as the no-sweeper fallback should the per-repo-vs-sweeper trade ever be revisited. (Full ranking, client-side POC, and forge-source verification on spinclass#285.)
- Companion records: #284 (Alt B — the disposable detached merge worktree that carries the credential to the merge push); #285 (this feature's tracking issue and the SSH-CA-vs-token ranking); papi#73 (the mint / revoke / sweep issuer, with the API-mint constraint).
- FDR 0013 (isolated build worktree — the detached-worktree pattern Alt B extends); FDR 0014 (implicit sessions — the SSH-keeping split).
