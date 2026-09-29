# #335 / #319: a pluggable, trusted `[auth].url-resolver` for the token lane's ssh→https rewrite

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to
> implement this plan task-by-task. Tasks 1 and 2 are independent of each
> other. Task 3 depends on both. Task 4 depends on Task 3. Task 5 (docs, the
> protocol draft, the closing commit) goes last.

**Goal:** A session's FDR 0028 token lane stops string-mapping the origin's ssh
form to `https://<host>/<same path>`. That mapping is wrong on an owner-free
forge plane. On `code.linenisgreat.com`, HTTPS serves only
`https://code.linenisgreat.com/<repo>.git` and 404s on the owner path, while SSH
accepts both forms. So an owner-form origin (`git@host:linenisgreat/madder.git`
or `ssh://git@host/linenisgreat/madder.git`) works until the worktree-scoped
`insteadOf` applies, and then every fetch and merge fails with
`repository 'https://code.linenisgreat.com/linenisgreat/madder.git/' not found`
(#335, and #319 for both the scp and `ssh://` prefixes). The fix is an
operator-configured resolver. At session creation it maps the configured origin
to the forge's canonical HTTPS URL; `smith repo resolve {origin} --output json`
(smith#58, which reads papi#85's `path_includes_identity`) is the intended
plugin. The token lane is then wired from that answer, persisted, and replayed
onto the landing worktree.

**Build/test:** scoped runs only, `just debug-go-test <dir> [run-regex]`
(`git add -N` new files first; godyn only sees tracked paths). The codec is
generated: `just build-tommy-codegen` after any `Sweatfile` struct change (the
merge gate runs `verify-tommy-codegen`). Do NOT run full `just`: the merge gate
runs it.

**Rollback:** none (no knob). With no `url-resolver` configured anywhere,
behaviour is byte-for-byte today's (Decision 6). Revert the commits if needed.

---

## Decision

These were agreed with the operator on 2026-09-29. They are not open questions.

1. **Trust: only layers above the repo.** The resolver decides where the
   token-carrying push goes, so a branch must not be able to choose it. This is
   FDR 0031's trust rule applied to config instead of a predicate.
   `[auth].url-resolver` is honoured only from the global sweatfile
   (`$HOME/.config/spinclass/sweatfile`) and from parent-directory layers
   strictly above the repo root. A value in `<repo>/sweatfile` (the main
   checkout's working copy, which is committed content), in its realpath twin,
   or in a worktree layer is **ignored**. Session creation emits a visible SKIP
   point naming the file, and `sc validate` warns about it. To make the
   untrusted value unreachable by accident, `MergeWith` never merges the key,
   so `Hierarchy.Merged` never carries it. The only reader is
   `sweatfileio.TrustedURLResolver`, which walks `Hierarchy.Sources`.
   "Approved escape hatches" (a repo-level resolver the operator has blessed)
   are future work, recorded in the FDR amendment.
2. **Failure is a failed mint.** The resolver can fail in three ways:
   - it exits nonzero or times out;
   - its stdout is not one JSON object;
   - the JSON has no `canonical_https`, or the value is not an `https://` URL
     with a host.

   Any of these refuses session creation exactly like a failed mint: the
   worktree and branch are torn down, and the error carries the resolver's
   stderr. `[hooks].allow-no-credential` / `--allow-no-credential` degrades this
   to skipping the **whole** `[auth]` lane: no rewrite, no token, no
   `credential.helper`, so the session pushes over ssh as before FDR 0028.
   **Never** fall back to the string rewrite: that is the bug.
3. **When: create time only, resolve BEFORE mint**, in the same funnel after
   the orphan sweep and after the `forge-hosts` gate. The order is
   sweep → forge-hosts gate (on the *origin* host) → resolve → mint → write
   credential → inject → record. Resolving first is better for three reasons:
   - A refusal happens before any token exists, so there is nothing to revoke
     and nothing for the sweep to orphan.
   - The credential-store line and the `insteadOf` key must name the
     *canonical* HTTPS host, which only the resolution knows.
   - The mint-command then sees canonical `SPINCLASS_FORGE_HOST` /
     `SPINCLASS_FORGE_REPO`. On `code.linenisgreat.com` these are owner-free,
     the same URL the token will authenticate. papi#73's `--repo` already
     accepts the owner-less name and resolves the owner (FDR 0028 Examples;
     fleet-placement design §1.2).

   `SPINCLASS_ORIGIN_URL` gives all three `[auth]` commands the raw origin.
   The `forge-hosts` gate stays on the origin host, so a GitHub-origin repo
   never pays for a resolver call, and a resolver cannot widen the allow-list.

   The resolved remote is persisted as a new `session.Credential.Remote`
   record. `auth.MirrorInto` injects the stored record into the landing
   worktree instead of re-parsing origin, and `revoke` uses the stored forge
   env. **Verified during planning:**
   - `sc rebuild` and resume auto-rebuild do NOT re-run the lane.
     `worktree.Reapply` only re-applies setup (`core.hooksPath` in
     `config.worktree`). The create-time rewrite and helper survive it, which
     matches "fixed per worktree". Task 4 pins that with a test.
   - `sc resurrect` bypasses the lane **entirely**. `resurrect.Run` calls
     `worktree.Create` directly, not `shop.createWorktree`, so today a
     resurrected session silently has no credential and pushes over ssh. That
     contradicts FDR 0028's fatal-mint rule. Task 4 routes it through the
     extracted lane, so it re-sweeps, re-resolves and re-mints.
4. **Contract.** `url-resolver` is a shell string, like `mint-command`. It runs
   as `sh -c` with cwd = **repo root** and the **ambient PATH**: no `direnv
   exec` and no devshell, because the devshell is head-controlled (FDR 0031).
   The runner is a sibling of `hookrun.CommandCapture` without the direnv wrap,
   the same shape as `merge/policy.go`'s `runExemption`, but with stdout kept
   separate from stderr.
   - **Env:** `SPINCLASS_ORIGIN_URL` (the CONFIGURED `remote.origin.url`,
     before insteadOf, read the way `auth.originRemote` reads it), plus
     `SPINCLASS_FORGE_HOST` / `_FORGE_REPO` (parsed from that origin), plus the
     `SPINCLASS_SESSION_ID` / `_REPO` / `_BRANCH` / `_WORKTREE` identity.
   - **Substitution:** `{origin}` in the string is replaced with the
     **shell-quoted** origin URL, so write it bare, for example
     `url-resolver = "smith repo resolve {origin} --output json"`.
   - **Output:** stdout must be exactly one JSON object. `canonical_https`
     (string) is required. `canonical_ssh` (string) is optional. Other fields
     are ignored; smith also emits `api_host`, `owner` and `name`.
   - **Timeout:** 30s, a package var so tests can shorten it. The process gets
     a 2s `WaitDelay` so an orphaned grandchild holding stdout cannot hang
     creation past the cap.
   - **What spinclass writes:**
     `url.<canonical_https>.insteadOf = <configured origin URL>`, plus a second
     value `<canonical_ssh>` when it is present and different, so a later
     origin switch to the canonical form keeps the lane. The credential line is
     keyed on canonical_https's host. When the origin already equals
     canonical_https, no `insteadOf` is written and only the helper is set.
5. **Protocol:** plain JSON on stdout now. A separate stub records the
   direction: newline-delimited JSON-RPC 2.0 over stdio (juggler(7)'s framing)
   for one-shot plugins (start-commands, FDR 0031 exemptions, this resolver).
   The repo has **no `docs/rfcs/`**, so per the operator's fallback it is an
   FDR draft, `docs/features/0033-one-shot-plugin-protocol.md` (the next free
   number as of 2026-09-29; re-check at write time).
6. **No detector without a resolver.** With `url-resolver` unset, the built-in
   `https://<host>/` + ssh-prefix rewrite is unchanged, and an owner-form
   origin on an owner-free forge is still a trap. This is documented as a
   limitation, and #319's second comment (an `ls-remote` probe at creation)
   gets a follow-up issue in Task 5.

**Record:** amend FDR 0028 (Interface, a `url-resolver` subsection, and
Limitations) and add one line to FDR 0027 (resurrect now runs the lane). No new
FDR for the resolver itself. The final commit closes #335 and #319. Earlier
commits cite `(#335, task N)`.

## Non-goals

- Re-resolving before each merge. The rewrite is fixed per worktree. A forge
  that moves its canonical URL mid-session needs a fresh session.
- A repo-level escape hatch for `url-resolver`.
- The `ls-remote` probe or mismatch detector (see Decision 6).
- `sc fork` (`worktree.CreateFrom`). It never ran the lane and still does not.
- Implicit sessions and the `disable-merge-queue` landing path. Both are
  already outside FDR 0028.
- Adding `url-resolver` to the fleet's `~/eng/repos/sweatfile` (circus-
  generated) or to spinclass's own repo `sweatfile`. The repo layer would be
  ignored anyway. Rollout order is **release first, then circus**: an older
  installed `sc validate` flags the new key as an unknown field (error
  severity), exactly as `forge-hosts` did (see the comment in spinclass's
  `sweatfile`).

---

### Task 1: schema, trusted-layer lookup, `sc validate` warning

**Context:** spinclass (Go, module `code.linenisgreat.com/spinclass`). FDR 0028's
`[auth]` table (`internal/sweatfile/sweatfile.go` `type Auth`: `mint-command`,
`revoke-command`, `forge-hosts`) configures a per-session forge push token.
Issue #335 adds `[auth].url-resolver`, a command that maps the repo's origin to
the forge's canonical HTTPS URL. It decides where token-carrying pushes go, so
it may be honoured ONLY from sweatfile layers **above the repo**: the global
`$HOME/.config/spinclass/sweatfile` and parent directories strictly above the
repo root. It is never honoured from `<repo>/sweatfile`, its realpath twin, or
a worktree layer. `sweatfileio.LoadHierarchy(home, repoDir)`
(`internal/sweatfileio/hierarchy.go`) records each layer in `Hierarchy.Sources`
in order: global → lexical parents → realpath parents → repo (lexical, then
realpath). `LoadWorktreeHierarchy` / `LoadHierarchyWithLayer` append one more
top layer. This task adds only the schema, the trusted lookup and the
validation. Nothing consumes the lookup yet (Task 3 does).

**Files:**
- Modify: `internal/sweatfile/sweatfile.go` (`Auth.URLResolver`)
- Modify: `internal/sweatfile/hierarchy.go` (MergeWith `[auth]` block: a comment only; the key is deliberately not merged)
- Regenerate: `internal/sweatfile/sweatfile_tommy.go` via `just build-tommy-codegen`
- Test: `internal/sweatfile/auth_test.go`
- Create: `internal/sweatfileio/trust.go`, `internal/sweatfileio/trust_test.go`
- Modify: `internal/validate/validate.go` (`CheckAuth` signature + `Run`)
- Test: `internal/validate/run_test.go` (reuse `writeSweatfile`), `internal/validate/validate_test.go`

**Behaviour:**
1. `Auth` gains `URLResolver *string \`toml:"url-resolver"\``. Its doc comment
   says three things:
   - the contract in one line (a shell string, run ambient in the repo root,
     printing `{canonical_https, canonical_ssh?}` JSON);
   - trusted only from layers above the repo;
   - read ONLY through `sweatfileio.TrustedURLResolver`: `MergeWith` never
     merges it, so `Merged` cannot leak a repo-layer value.

   Add no `Sweatfile` accessor for it. In `MergeWith`'s `[auth]` block, add a
   one-line comment stating the omission is deliberate (#335 trust rule). Then
   run `just build-tommy-codegen`.
2. `internal/sweatfileio/trust.go`:
   - `func LayerAboveRepo(layerPath, home, repoRoot string) bool`. It returns
     true when `layerPath == filepath.Join(home, ".config", "spinclass",
     "sweatfile")`. Otherwise, with `dir := filepath.Dir(layerPath)`, it
     returns true iff for some `d ∈ {dir, canonicalDir(dir)}` and some
     `r ∈ {filepath.Clean(repoRoot), canonicalDir(repoRoot)}`,
     `rel, err := filepath.Rel(d, r)` gives `err == nil`, `rel != "."`,
     `rel != ".."`, and `!strings.HasPrefix(rel, ".."+string(filepath.Separator))`.
     In words: `dir` is a strict ancestor of the repo root. A label layer such
     as `abc1234:sweatfile` (Dir `"."`) is therefore never above.
   - `func TrustedURLResolver(h sweatfile.Hierarchy, home, repoRoot string) (command string, ignored []string)`.
     It walks `h.Sources` in order and skips `SkipReason != ""` and `!Found`.
     For each source whose `File.Auth != nil && File.Auth.URLResolver != nil`:
     if `LayerAboveRepo(src.Path, …)`, it sets
     `command = strings.TrimSpace(*v)` (scalar override: last trusted layer
     wins, and an explicit `""` clears it back to the built-in rewrite);
     otherwise it appends `src.Path` to `ignored`.
3. `validate.CheckAuth(sf sweatfile.Sweatfile, layerAboveRepo bool) []Issue`
   keeps today's three checks. It adds a `SeverityWarning` issue,
   `Field: "auth.url-resolver"`, when `sf.Auth.URLResolver != nil &&
   !layerAboveRepo`. The message: "[auth] sets `url-resolver` in a repo-level
   sweatfile: it is ignored — it decides where a session's token-carrying
   pushes go, so only sweatfiles above the repo (global, parent directories)
   may set it; move it up".

   In `Run`, compute the main root once:
   `mainRoot, err := git.DetectRepo(repoDir); if err != nil { mainRoot = repoDir }`.
   `DetectRepo` climbs from a session worktree or subdirectory to the main
   checkout. Import `internal/git`. Then call
   `CheckAuth(src.File, sweatfileio.LayerAboveRepo(src.Path, home, mainRoot))`.

**Failing tests first:**
- `TestMergeWithNeverCarriesURLResolver` (auth_test.go, package sweatfile).
  Compute `(Sweatfile{}).MergeWith(Sweatfile{Auth: &Auth{MintCommand: &m, URLResolver: &r}})`.
  Assert `MintCommand` is `m` and `Auth.URLResolver == nil`.
- `TestParseURLResolver` (trust_test.go). `Parse([]byte("[auth]\nurl-resolver = \"smith repo resolve {origin} --output json\"\n"))`
  yields `Data().Auth.URLResolver` with that value. Red until codegen.
- `TestTrustedURLResolverHonoursOnlyLayersAboveRepo`. Set `home := t.TempDir()`
  and `repo := home/work/repo` (mkdir). Write sweatfiles containing only
  `[auth]\nurl-resolver = "<name>"`:
  - global = `"global"`;
  - `home/work/sweatfile` = `"parent"`;
  - `repo/sweatfile` = `"repo"`.

  With `h, _ := LoadHierarchy(home, repo)`, assert
  `TrustedURLResolver(h, home, repo)` returns `("parent", [repo/sweatfile])`.
  Then delete the parent file, reload, and assert `"global"`. Then write the
  parent file as `url-resolver = ""`, reload, and assert `""` (cleared).
- `TestTrustedURLResolverIgnoresWorktreeLayer`. Use
  `LoadWorktreeHierarchy(home, repo, repo/.worktrees/x)` with
  `repo/.worktrees/x/sweatfile` = `"wt"` and the parent = `"parent"`. Assert
  the command is `"parent"` and `ignored` contains the worktree path.
- `TestTrustedURLResolverViaSymlinkedRepo`. Make the real tree
  `home/real/work/repo` with `home/real/work/sweatfile` = `"parent"`, and a
  symlink `home/link → home/real/work`. `LoadHierarchy(home, home/link/repo)`
  must resolve `"parent"`, with the realpath repo layer (if a resolver is
  written there) in `ignored`. This mirrors the symlink fixtures in
  `internal/sweatfile/sweatfile_test.go` (~lines 472, 528).
- `TestCheckAuthWarnsRepoLayerURLResolver` (validate_test.go).
  `CheckAuth(sf{Auth{URLResolver:&r}}, false)` has an issue with
  `Field=="auth.url-resolver"`. With `true` it has none.
- `TestRunWarnsOnRepoLayerURLResolver` (run_test.go). Set
  `t.Setenv("GIT_CEILING_DIRECTORIES", home)` so a TMPDIR inside some checkout
  cannot make `DetectRepo` climb out. Write `repoDir/sweatfile` with
  `[auth]\nurl-resolver = "x"`. Assert the output contains
  `auth valid # warning` and `url-resolver`, and does NOT contain
  `unknown field` (this proves the codegen). The companion
  `TestRunAcceptsParentLayerURLResolver` puts it in `home/eng/sweatfile`
  (repo = `home/eng/myrepo`, as in `TestRunValidHierarchy`) and asserts no
  `url-resolver` warning.

**Implement**, run `just build-tommy-codegen`, then prove with
`just debug-go-test internal/sweatfile`, `just debug-go-test internal/sweatfileio`,
`just debug-go-test internal/validate`.

---

### Task 2: `auth.Resolve`, the ambient runner, and the persisted remote

**Context:** spinclass (Go, `code.linenisgreat.com/spinclass`). FDR 0028's
token lane lives in `internal/auth/auth.go`. `Mint` reads the configured
`remote.origin.url` (`originRemote`, deliberately not `git remote get-url`) and
parses it with `ParseForgeRemote` into `Remote{Host, OwnerRepo, SSHPrefix}`. It
gates on `[auth].forge-hosts`, runs `mint-command` devshell-scoped
(`hookrun.CommandCapture`), writes `.spinclass/git-credentials`, then calls
`Inject`. `Inject` sets worktree-scoped `credential.helper` and
`url.https://<host>/.insteadOf = <ssh prefix>`, and Mint records
`session.Credential{MintedAt}`. `MirrorInto` re-parses origin to wire the
merge's landing worktree.

Issue #335/#319: that string rewrite keeps the owner path, which 404s on an
owner-free HTTPS plane. This task adds an optional resolver command. Its JSON
answer (`canonical_https`, optional `canonical_ssh`) replaces the string
rewrite. The rewrite is persisted on the session state, and `MirrorInto` and
`revoke` read the stored record. Task 3 decides which command string to pass;
here it is a plain parameter, and existing call sites pass `""`, which keeps
today's behaviour exactly.

**Files:**
- Modify: `internal/hookrun/hookrun.go` (add `CommandCaptureAmbient` beside `CommandCapture`; do NOT refactor `CommandCapture`)
- Create: `internal/hookrun/ambient_test.go`
- Modify: `internal/session/session.go` (`Credential.Remote`, new `CredentialRemote`)
- Modify: `internal/auth/auth.go`
- Test: `internal/auth/auth_test.go` (reuse `setupRepo`, `authSweatfile`, `runGit`)
- Create: `internal/auth/resolve_test.go`
- Update call sites (compile only): `internal/shop/shop.go:160` (`Mint(…, "")`), `internal/close/close_test.go:430`, `internal/clean/clean_test.go:129` (`Mint(…, "")`), `internal/merge/merge.go:474` (new `MirrorInto` args), `internal/merge/landing_test.go:196` (`Inject` now takes a `Rewrite`: `auth.Rewrite{CredentialHost: "example.com"}`)

**Behaviour:**
1. `hookrun.CommandCaptureAmbient(ctx, dir, cmd string, extraEnv []string) (string, error)`:
   - runs `sh -c sweatfile.NormalizeCommand(cmd)` (an empty script is an
     error) with `Dir = dir`, `Env = append(os.Environ(), extraEnv...)` (no
     `WORKTREE=` injection, no direnv), and `WaitDelay = 2*time.Second`;
   - captures stdout and stderr separately and returns stdout;
   - on error, folds trimmed stderr into it (`%w: <stderr>`), like
     `CommandCapture`.

   Its doc comment says why there is no devshell: the devshell is
   head-controlled (FDR 0031's reasoning).
2. `session.Credential` gains `Remote *CredentialRemote \`json:"remote,omitempty"\``
   with the fields below. `Write`'s carry-forward already copies the whole
   `*Credential`, so there is no change there.

       type CredentialRemote struct {
           OriginURL      string   `json:"origin_url"`      // configured remote.origin.url at mint time
           ForgeHost      string   `json:"forge_host"`      // SPINCLASS_FORGE_HOST the mint saw
           ForgeRepo      string   `json:"forge_repo"`      // SPINCLASS_FORGE_REPO the mint saw
           CredentialHost string   `json:"credential_host"` // host[:port] in .spinclass/git-credentials
           HTTPS          string   `json:"https"`           // url.<HTTPS>.insteadOf
           From           []string `json:"from,omitempty"`  // that key's values
           Resolved       bool     `json:"resolved,omitempty"` // produced by [auth].url-resolver
       }

3. In `auth`, add these types. `Remote` stays as-is.

       type Rewrite struct { CredentialHost, HTTPS string; From []string; Resolved bool }
       type Resolution struct { OriginURL string; Forge Remote; Rewrite Rewrite }

   - `builtinRewrite(r Remote) Rewrite` =
     `{CredentialHost: r.Host, HTTPS: "https://"+r.Host+"/", From: [r.SSHPrefix] if non-empty}`,
     which is exactly today's key/value.
   - `readOrigin(dir) (url string, Remote, error)` replaces `originRemote`'s
     body. Keep the doc comment about not using `get-url`.
   - `Identity.env(r Remote, originURL string)` adds
     `SPINCLASS_ORIGIN_URL=<originURL>`.
   - `var urlResolverTimeout = 30 * time.Second`.
   - `func Resolve(ctx context.Context, resolver string, id Identity, originURL string, origin Remote) (Resolution, error)`:
     - A blank `resolver` returns `{originURL, origin, builtinRewrite(origin)}`.
     - Otherwise it replaces `{origin}` with `shellQuote(originURL)` (single
       quotes, `'` → `'\''`). It runs
       `hookrun.CommandCaptureAmbient(ctxWithTimeout, id.RepoPath, script, id.env(origin, originURL))`.
     - Errors, each prefixed `[auth] url-resolver`:
       - `timed out after 30s` when the context deadline was exceeded;
       - `failed: %w` otherwise (the error already carries stderr);
       - empty stdout → `printed no JSON object on stdout`;
       - `json.Unmarshal(bytes.TrimSpace(out), &struct{CanonicalHTTPS, CanonicalSSH *string})`
         fails → `printed invalid JSON: %v (stdout: <first 200 bytes>)`;
       - missing or blank `canonical_https` → `output has no "canonical_https"`;
       - `url.Parse` fails, the scheme is not `https`, or the host is empty →
         `canonical_https %q is not an https URL`;
       - a non-empty `canonical_ssh` that `ParseForgeRemote` rejects or that
         yields an empty `SSHPrefix` → `canonical_ssh %q is not an ssh URL`.
     - On success, with `u` = the parsed canonical_https: `Forge =
       Remote{Host: u.Hostname(), OwnerRepo: trimRepoPath(u.Path)}`, and
       `Rewrite = {CredentialHost: u.Host, HTTPS: canonicalHTTPS, From:
       dedup(originURL, canonicalSSH) minus "" and minus canonicalHTTPS,
       Resolved: true}`.
4. `Inject(dir, credFile string, rw Rewrite) error` keeps the guard, the
   `extensions.worktreeConfig` setting and the helper. When `len(rw.From) > 0`,
   with `key := "url." + rw.HTTPS + ".insteadOf"`, the first value is set with
   `--worktree --replace-all key From[0]` and the rest with
   `--worktree --add key v`.
5. `Mint(ctx, sf, id, urlResolver string) (MintOutcome, error)`. `MintOutcome`
   gains `Resolved string` (canonical_https when a resolver ran). The order is:
   no mint-command → noop; `readOrigin` error → Skip (unchanged); forge-hosts
   gate on the ORIGIN host → Skip (unchanged, and the resolver never runs);
   `Resolve` → return its error as-is, before anything is written; run
   mint-command with `id.env(res.Forge, res.OriginURL)`;
   `writeCredential(wt, res.Rewrite.CredentialHost, token)`;
   `Inject(wt, credPath, res.Rewrite)`; record
   `session.Credential{MintedAt, Remote: <res as CredentialRemote>}`.
   The doc comment gives the resolve-before-mint rationale (Decision 3).
6. `MirrorInto(repoPath, branch, sessionWorktree, dir string) error`:
   - If `!Minted(sessionWorktree)`, it is a noop.
   - Otherwise it reads `session.Read(repoPath, branch)`:
     - on success with `Credential.Remote != nil`, it builds the `Rewrite`
       from the stored record;
     - on `errors.Is(err, os.ErrNotExist)`, or a record with a nil `Remote`
       (minted before #335, so necessarily the built-in form), it falls back to
       `builtinRewrite` of the parsed origin (the zero `Remote` on a parse
       error, which gives helper only, as today);
     - on any other read error, it returns
       `fmt.Errorf("read session state for the stored credential remote: %w", err)`.
   - It then calls `Inject(dir, credentialPath(sessionWorktree), rw)`.

   Update the merge.go call to `auth.MirrorInto(repoPath, branch, wtPath, landPath)`.
7. `revoke` reads the session state first. When `Credential.Remote != nil`, it
   builds the env from the stored `ForgeHost` / `ForgeRepo` / `OriginURL`;
   otherwise it uses today's origin parse. That way mint and revoke see the
   same values.

**Failing tests first:**
- `TestCommandCaptureAmbientSplitsStreams` (ambient_test.go):
  - `"echo out; echo err >&2"` returns `"out\n"`, nil;
  - `"echo out; echo boom >&2; exit 3"` returns an error containing `boom` and
    `exit status 3`;
  - extraEnv `FOO=bar` with `printf %s "$FOO"` returns `"bar"`;
  - with `ctx` timing out after 200ms, `sleep 5` (NOT `exec sleep`, which
    exercises `WaitDelay`) returns an error in under 4s.
- `TestResolveBuiltinWhenNoResolver` (resolve_test.go). For
  `Resolve(ctx, "", id, "git@forge.example.com:owner/repo.git", parsed)`,
  `Rewrite` must equal
  `{CredentialHost:"forge.example.com", HTTPS:"https://forge.example.com/", From:["git@forge.example.com:"]}`.
- `TestMintWithURLResolverRewritesOriginToCanonical`. Use `setupRepo` (origin
  `git@forge.example.com:owner/repo.git`). Set `envFile`, then
  `sf := authSweatfile("env | grep '^SPINCLASS_' | sort > "+envFile+"; echo tok", "true")`
  and a resolver of
  `printf '%s' '{"canonical_https":"https://vanity.example.com/repo.git","canonical_ssh":"ssh://git@vanity.example.com/repo.git","owner":"x"}'`.
  After `Mint(ctx, sf, id, resolver)`:
  - `outcome.Minted` is true and `outcome.Resolved == "https://vanity.example.com/repo.git"`;
  - `git -C wt config --worktree --get-all url.https://vanity.example.com/repo.git.insteadOf`
    = the origin URL, then `ssh://git@vanity.example.com/repo.git`;
  - `git -C wt config --worktree --get url.https://forge.example.com/.insteadOf`
    fails (the built-in key was not written);
  - `git -C wt remote get-url origin` = `https://vanity.example.com/repo.git`
    (the end-to-end proof);
  - the credential line is `https://spinclass:tok@vanity.example.com`;
  - the mint env has `SPINCLASS_FORGE_HOST=vanity.example.com`,
    `SPINCLASS_FORGE_REPO=repo`, and
    `SPINCLASS_ORIGIN_URL=git@forge.example.com:owner/repo.git`;
  - `session.Read` gives `Credential.Remote` with `Resolved`, the `HTTPS`, the
    two `From` values, and `CredentialHost=="vanity.example.com"`.
- `TestURLResolverSeesOriginEnvAndRepoRootCwd`. Set the resolver to
  `printf '%s' {origin} > A; env | grep '^SPINCLASS_' | sort > E; pwd -P > D; printf '{"canonical_https":"https://forge.example.com/repo.git"}'`
  (A/E/D are temp files). Assert:
  - A == the origin URL (quoting worked);
  - E has `SPINCLASS_ORIGIN_URL`, `SPINCLASS_FORGE_HOST=forge.example.com`,
    `SPINCLASS_FORGE_REPO=owner/repo`, and `SPINCLASS_SESSION_ID=repo/feature-x`;
  - D == `filepath.EvalSymlinks(repoPath)`.
- `TestURLResolverFailuresAreFatalAndWriteNothing`. This is table-driven, with
  a mint-command that writes a marker file.
  | resolver | error contains |
  |---|---|
  | `echo nope >&2; exit 7` | `url-resolver`, `nope` |
  | `true` | `no JSON` |
  | `echo not-json` | `invalid JSON` |
  | `echo '{}'` | `canonical_https` |
  | `echo '{"canonical_https":"http://x/y.git"}'` | `not an https URL` |
  | `echo '{"canonical_https":"https://x/y.git","canonical_ssh":"/srv/y.git"}'` | `canonical_ssh` |
  | `sleep 5`, with `urlResolverTimeout` set to 200ms and restored via `t.Cleanup` | `timed out` |

  For every row, also assert:
  - the mint marker file is absent;
  - `Minted(wt)` is false;
  - `git -C wt config --worktree --get-regexp '^url\.'` exits nonzero;
  - `session.Read` shows no `Credential`.
- `TestURLResolverSkippedOutsideForgeHosts`. With `ForgeHosts=["github.com"]`
  and a resolver that writes a marker: `outcome.Skipped` is set and the marker
  is absent.
- `TestURLResolverCanonicalEqualsHTTPSOriginWritesHelperOnly`. Run
  `runGit(repo, "remote", "set-url", "origin", "https://forge.example.com/repo.git")`,
  with a resolver echoing that same URL. The helper is set and no `url.*` key
  exists.
- `TestMirrorIntoReplaysStoredRemoteNotOrigin`. Mint with the vanity resolver,
  then run `remote set-url origin git@forge.example.com:other/thing.git`, add
  the `.land-x` worktree as in `TestMirrorIntoWiresAnotherWorktree`, and call
  `MirrorInto(repoPath, "feature-x", wt, land)`. The landing's
  `--get-all url.https://vanity.example.com/repo.git.insteadOf` must equal the
  stored `From`, and no `url.https://forge.example.com/.insteadOf` may exist.
- `TestMirrorIntoLegacyRecordUsesBuiltinRewrite`. Mint with `""`. Then
  `session.UpdateCredential(repo, "feature-x", &session.Credential{MintedAt: time.Now()})`
  (strip `Remote`) and call `MirrorInto`. The landing gets
  `url.https://forge.example.com/.insteadOf = git@forge.example.com:`.
- Extend `TestMintWritesCredentialInjectsConfigAndRecordsState`: the recorded
  `Remote` is the built-in form (`Resolved == false`, `From ==
  ["git@forge.example.com:"]`), and the env has `SPINCLASS_ORIGIN_URL`.
  Extend `TestSessionWriteCarriesCredentialForward`: `Credential.Remote`
  survives. Existing `Mint(…)` calls get a trailing `""`, and
  `TestMirrorInto*` calls get the new arguments.

**Implement**, then prove with `just debug-go-test internal/hookrun Ambient`,
`just debug-go-test internal/auth`, `just debug-go-test internal/session`,
`just debug-go-test internal/merge 'Credential|Landing'`,
`just debug-go-test internal/close`, `just debug-go-test internal/clean`, and
`just debug-go-test internal/shop` (which must compile and stay green).

---

### Task 3: wire the trusted resolver into the creation funnel (depends on Tasks 1 and 2)

**Context:** spinclass (Go, `code.linenisgreat.com/spinclass`). Every new
worktree session from `sc start`, `sc spawn` and `sc run` goes through
`shop.createWorktree` (`internal/shop/shop.go` ~line 91). After
`worktree.Create` it:
- sweeps orphaned credentials (`auth.SweepOrphans`);
- calls `auth.Mint`;
- on a mint error, tears down the worktree and (for a fresh branch) the branch,
  and returns an error hinting at `--allow-no-credential`, unless
  `opts.AllowNoCredential` is set, in which case it emits a warn `NotOk` and
  continues;
- emits a `SKIP` for a skipped mint and `ok - mint credential <branch>` on
  success.

Task 1 added `sweatfileio.TrustedURLResolver(h, home, repoRoot) (command,
ignored)`, which reads `[auth].url-resolver` only from sweatfile layers above
the repo. Task 2 added `auth.Mint(ctx, sf, id, urlResolver)`. With a resolver,
Mint resolves before minting, returns a pre-write error on any resolver
failure, and reports `MintOutcome.Resolved`. This task extracts that block into
an exported function (Task 4 reuses it for `sc resurrect`) and feeds it the
trusted resolver. Issue #335.

**Files:**
- Modify: `internal/shop/shop.go` (extract `ProvisionCredential`; `createWorktree` calls it)
- Create: `internal/shop/credential_test.go`

**Behaviour:**
1. `func ProvisionCredential(ctx context.Context, tw *tap.Writer, h sweatfile.Hierarchy, rp worktree.ResolvedPath, allowNoCredential bool) error`
   holds the whole existing block (sweep, `Identity`, the mint switch, and the
   teardown + hint error), moved verbatim with the comment. It adds:
   - `home, _ := os.UserHomeDir()`;
   - `resolver, ignored := sweatfileio.TrustedURLResolver(h, home, rp.RepoPath)`;
   - when `ignored` is non-empty, `tw.Skip("url-resolver "+rp.Branch, "ignored [auth].url-resolver in "+strings.Join(ignored, ", ")+": only sweatfiles above the repo are trusted (FDR 0028)")`
     (or `log.Warn` when `tw == nil`), emitted before the mint;
   - `auth.Mint(ctx, h.Merged, id, resolver)`;
   - in the `outcome.Minted` case, when `outcome.Resolved != ""`, emit
     `tw.Ok("resolve origin " + rp.Branch + " " + outcome.Resolved)` before the
     existing `tw.Ok(mintDesc)`.

   The fatal-teardown and allow-no-credential branches are unchanged. A
   resolver error flows through them because Mint returns it before writing
   anything, so the degraded path really skips the whole lane.
   `createWorktree` becomes `if err := ProvisionCredential(context.Background(),
   tw, result, worktreePath, opts.AllowNoCredential); err != nil { return false,
   err }`. Keep its funnel comment and point it at `ProvisionCredential`.

**Test fixture** (credential_test.go): `setupAuthRepo(t)`.
- `root := t.TempDir()`; `t.Setenv("HOME", root)`;
  `t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))`.
- `repo := root/work/repo`: `git init`, user config, and an empty initial
  commit (the shape of `TestCreateTapNewWorktree`).
- `git remote add origin ssh://git@127.0.0.1:1/owner/repo.git`. The fetch
  fails fast, and every `Create` passes `AllowStaleBase: true`, so the
  base-branch step is a SKIP.
- `rp := worktree.ResolvedPath{AbsPath: repo/.worktrees/feature-x, RepoPath: repo, Branch: "feature-x", SessionKey: "repo/feature-x"}`.
- `writeResolverScript(t, dir, json string) string` writes `dir/resolve.sh`
  containing `printf '%s\n' "$1" > dir/resolver-arg; printf '%s\n' '<json>'`
  and returns the command `"sh " + path + " {origin}"`.
- Parent layer: `root/work/sweatfile`. Repo layer: `repo/sweatfile`.

**Failing tests first:**
1. `TestCreateResolvesOriginViaTrustedURLResolver`. The parent layer is
   `[auth]` with `mint-command = "echo tok"`, `revoke-command = "true"` and
   `url-resolver = "<script cmd>"`, and the JSON has
   `canonical_https = https://vanity.test/repo.git`. Call
   `Create(&buf, rp, CreateOpts{Format: "tap", AllowStaleBase: true}, nil)`.
   Assert:
   - no error;
   - the output has `resolve origin feature-x https://vanity.test/repo.git`
     and `mint credential feature-x`;
   - `resolver-arg` == the origin URL;
   - `git -C wt remote get-url origin` == `https://vanity.test/repo.git`.
2. `TestCreateIgnoresRepoLayerURLResolver`. `mint-command` / `revoke-command`
   are in the parent layer, and `url-resolver` is ONLY in `repo/sweatfile`.
   Assert:
   - the output has a `# SKIP` line containing `url-resolver` and
     `repo/sweatfile`;
   - `resolver-arg` does not exist (it never ran);
   - the built-in rewrite applied:
     `git -C wt config --worktree --get url.https://127.0.0.1/.insteadOf` ==
     `ssh://git@127.0.0.1:1/`.
3. `TestCreateURLResolverFailureRefusesAndTearsDown`. The parent-layer
   resolver is `echo resolver-said-no >&2; exit 7`, and the mint-command writes
   a marker. Assert:
   - `Create` errors, and the error contains `url-resolver`,
     `resolver-said-no` and `--allow-no-credential`;
   - `rp.AbsPath` does not exist;
   - `git -C repo rev-parse --verify refs/heads/feature-x` fails;
   - the marker is absent.
4. `TestCreateURLResolverFailureWithAllowNoCredentialSkipsWholeLane`. Same
   setup, with `AllowNoCredential: true`. Assert:
   - no error, and the worktree exists;
   - the output has `not ok` … `mint credential feature-x` with
     `severity: warn` and a message containing `url-resolver`;
   - `git -C wt config --worktree --get-regexp '^url\.'` and
     `--get credential.helper` both exit nonzero;
   - `.spinclass/git-credentials` is absent.
5. `TestCreateWithoutURLResolverKeepsBuiltinRewrite`. This is a regression pin
   for Decision 6. With no resolver anywhere, `url.https://127.0.0.1/.insteadOf`
   == `ssh://git@127.0.0.1:1/`, and there is no `resolve origin` line.

**Prove:** `just debug-go-test internal/shop`, then
`just debug-go-test internal/spawn` and `just debug-go-test internal/run`, the
other `Create` callers.

---

### Task 4: `sc resurrect` runs the lane; pin that `sc rebuild` preserves it (depends on Task 3)

**Context:** spinclass (Go, `code.linenisgreat.com/spinclass`). FDR 0028 says
a session configured for a forge push credential must never silently push over
the inherited ssh-agent. Tasks 1–3 made the credential lane resolve the origin
through a trusted `[auth].url-resolver` (#335), inside
`shop.ProvisionCredential`, which `shop.createWorktree` calls.

Two other paths recreate or refresh worktrees:
- `resurrect.Run` (`internal/resurrect/resurrect.go`, FDR 0027) calls
  `worktree.Create` directly and never runs the lane. A resurrected session
  therefore has no credential and pushes over ssh. This task fixes that.
- `sc rebuild` and resume auto-rebuild call `worktree.Reapply`, which
  re-applies setup only. By design (the rewrite is fixed per worktree at
  creation) it must leave the lane's worktree config alone. This task pins
  that with a test and changes no code there.

**Files:**
- Modify: `internal/resurrect/resurrect.go`
- Test: `internal/resurrect/resurrect_test.go`
- Test: the `internal/worktree` test file that covers `Reapply` (`rg -l Reapply internal/worktree`; if there is none, use `worktree_test.go`)

**Behaviour:**
1. In `resurrect.Run`:
   - create `tw := tap.NewWriter(w)` up front when `format == "tap"`, else nil;
   - keep the `worktree.Create` result `h`;
   - build `rp := worktree.ResolvedPath{AbsPath: newPath, RepoPath: st.RepoPath, Branch: branch, SessionKey: filepath.Base(st.RepoPath) + "/" + branch}`
     (`ExistingBranch` stays empty, so a refusal also deletes the recreated
     branch);
   - call `shop.ProvisionCredential(context.Background(), tw, h, rp, h.Merged.AllowNoCredential())`
     BEFORE `session.Write(fresh)`. On error, return it. Teardown already
     happened and the tombstone is untouched, so the resurrect can be retried.
     There is deliberately no CLI or MCP flag; only the sweatfile knob applies,
     as for `sc spawn`.

   Replace `tw.PlanAhead(1)` with a trailing `tw.Plan()` after the final
   `tw.Ok("resurrect …")`. The `SessionKey` the lane records must equal
   `fresh.SessionKey`. Update the package doc comment with one sentence about
   the lane.
2. Make `setupClosedSession` hermetic with `t.Setenv("HOME", root)`. Today it
   reads the developer's real global sweatfile, which would now be able to
   trigger a mint.
3. No change to `worktree.Reapply`.

**Failing tests first:**
- `TestRunProvisionsCredentialThroughTrustedResolver`. After
  `setupClosedSession(t, "feature-x")`:
  - `git -C repo remote add origin git@forge.example.com:owner/repo.git`
    (resurrect does not fetch);
  - write the GLOBAL layer `root/.config/spinclass/sweatfile` with `[auth]`
    `mint-command = "echo tok"`, `revoke-command = "true"`, and a
    `url-resolver` printing
    `{"canonical_https":"https://vanity.example.com/repo.git"}`. Use the
    script-file approach from Task 3's fixture. `root` is the parent of
    `repoPath`.

  After `Run(&buf, "repo/feature-x", "", "tap")`, assert:
  - `.spinclass/git-credentials` exists in the recreated worktree;
  - `git -C wt remote get-url origin` == `https://vanity.example.com/repo.git`;
  - `session.Read` gives `Credential.Remote.Resolved`;
  - the output has `resolve origin feature-x` and ends with a valid plan.
- `TestRunRefusesWhenURLResolverFails`. Same setup with the resolver
  `echo no >&2; exit 1`. Assert:
  - `Run` errors, and the error contains `url-resolver`;
  - the worktree path does not exist, and
    `git rev-parse --verify refs/heads/feature-x` fails;
  - `session.FindByTarget("repo/feature-x")` is still a tombstone with the
    same `DeletedSHA`, so a retry is possible.
- `TestReapplyPreservesCredentialWiring` (internal/worktree). `Create` a
  worktree (repo with an empty commit, `HOME` set to a tempdir). Then set
  `git -C wt config extensions.worktreeConfig true` and, with `--worktree`,
  `credential.helper "store --file=/x"` and
  `url.https://vanity.test/repo.git.insteadOf git@host:owner/repo.git`. Call
  `Reapply(repo, wt)`. Both keys must be unchanged. This test is green on
  arrival: it pins Decision 3's "rebuild keeps the create-time rewrite". Say
  so in its comment.

**Prove:** `just debug-go-test internal/resurrect`, then
`just debug-go-test internal/worktree Reapply`.

---

### Task 5: docs, the protocol draft, the follow-up issue, and the closing commit (after Tasks 1–4)

**Context:** spinclass (Go, `code.linenisgreat.com/spinclass`). Tasks 1–4
shipped `[auth].url-resolver` (#335, closing #319 too):
- It is honoured only from sweatfile layers above the repo
  (`sweatfileio.TrustedURLResolver`); a repo-layer value gets a SKIP point and
  an `sc validate` warning.
- At session creation it runs `sh -c` in the repo root with the ambient PATH
  and a 30s cap, after the forge-hosts gate and before the mint. `{origin}` is
  replaced shell-quoted. The env adds `SPINCLASS_ORIGIN_URL` to the
  `SPINCLASS_*` set.
- It prints `{"canonical_https": …, "canonical_ssh"?: …}` on stdout.
- spinclass then writes `url.<canonical_https>.insteadOf = <origin>` (plus
  `canonical_ssh`), keys the credential on the canonical host, and gives the
  mint canonical `SPINCLASS_FORGE_HOST` / `_FORGE_REPO`. It persists all of
  this as `session.Credential.Remote`, which `auth.MirrorInto` and `revoke`
  replay.
- Any resolver failure is a failed mint (fatal, or with allow-no-credential
  the whole lane is skipped), never the string rewrite.
- `sc resurrect` now runs the lane. `sc rebuild` keeps the create-time wiring.

No behaviour changes in this task.

**Steps:**
0. File a follow-up with the `eng:file-issue` skill (dedupe first). Title:
   "session create: verify the token lane's rewritten origin resolves
   (ls-remote probe) and flag owner-path vs owner-free mismatch when no
   [auth].url-resolver is configured". The body cites #319's second comment
   (the store-valid-URL / `ls-remote` / worktree-config-comment ideas) and
   Decision 6 ("no detector" was an explicit non-goal of #335). Record the
   number for step 1.
1. `docs/features/0028-per-session-forge-push-credentials.md`. Do not change
   `status`. Add under the H1:
   `> **Amended 2026-09-29 (#335, #319):** pluggable, trusted url-resolver.`
   - **Interface:** add an `[auth].url-resolver` bullet after `forge-hosts`.
     Amend Lifecycle step 2's `insteadOf` bullet: "with a resolver:
     `url.<canonical_https>.insteadOf = <origin>` (+ `<canonical_ssh>`)".
     Amend step 1 to state the order (sweep → forge-hosts gate → resolve →
     mint) and that the mint env carries canonical values plus
     `SPINCLASS_ORIGIN_URL`.
   - **New `### url-resolver` subsection** (end of Interface):
     - the contract (Decision 4, verbatim in substance);
     - the trust rule and why (FDR 0031's analogue: it decides where the
       token-carrying push goes; the repo layer is committed content);
     - the failure semantics (Decision 2);
     - the resolve-before-mint rationale (Decision 3);
     - persistence and replay (`Credential.Remote`, `MirrorInto`, `revoke`;
       legacy records fall back to the built-in form);
     - resurrect and rebuild behaviour;
     - an example: `url-resolver = "smith repo resolve {origin} --output json"`
       (smith#58, papi#85);
     - placement (the fleet root `~/eng/repos/sweatfile`; foreign repos
       inherit it harmlessly, since GitHub origins are gated out by
       `forge-hosts`);
     - rollout order (release before circus sets the key; older `sc validate`
       errors on it as an unknown field).
   - **Limitations:**
     - no detector without a resolver: an owner-form origin on an owner-free
       forge still 404s (cite the step-0 issue);
     - create-time only (a canonical URL that moves mid-session needs a fresh
       session);
     - ambient PATH trust (as FDR 0031);
     - the repo-level "approved escape hatch" is future work;
     - the JSON-on-stdout contract is provisional (cite FDR 0032).

     Update "Mint once" to say that `sc resurrect` now runs the lane.
2. `docs/features/0027-resurrect-closed-session.md`: one sentence in its
   interface/behaviour section saying resurrect now runs the FDR 0028
   credential lane before re-registering. A failed mint or resolve refuses the
   resurrect and leaves the tombstone intact.
3. `docs/features/0033-one-shot-plugin-protocol.md` (verify 0033 is still
   free). Front matter: `status: proposed`, `date: 2026-09-29`, and
   `promotion-criteria` (proposed → experimental: one of the three surfaces
   speaks it behind a declared opt-in while the legacy contracts keep
   working). One page:
   - **Problem:** three ad-hoc external-command contracts:
     - `[[start-commands]]` `exec-completions` / `exec-start`: argv + JSON
       stdout (spinclass-start-commands(7));
     - FDR 0031 `[[pre-merge-exemptions]]`: exit code;
     - `[auth].url-resolver`: JSON stdout.
   - **Sketch:** a one-shot process; newline-delimited JSON-RPC 2.0 over stdio
     with juggler(7)'s WIRE FORMAT framing; exactly one request line on stdin,
     one response line on stdout; stderr is diagnostics. Method names such as
     `start/completions`, `start/exec`, `merge/exempt`, `auth/resolve-url`. A
     `protocol_version` param. JSON-RPC error objects replace exit-code
     semantics.
   - **Open questions:** opt-in declaration shape; the timeout owner.
   - **Non-goals:** long-lived plugins.

   Say explicitly that it is a draft FDR because the repo has no `docs/rfcs/`.
4. `doc/spinclass-sweatfile.5.scd`, in `## [auth]` (~line 681):
   - add `*SPINCLASS\_ORIGIN\_URL*` to the env sentence, and note that with a
     resolver `SPINCLASS\_FORGE\_HOST` / `\_REPO` are the canonical ones;
   - add a `*url-resolver*` entry after `*forge-hosts*` covering the contract,
     the `{origin}` quoting, the JSON fields, the 30s cap, and the trust rule
     ("*Merge:* not merged — read only from the global and parent-directory
     layers; a repo- or worktree-level value is ignored with a warning");
   - in `*mint-command*`, make the `insteadOf` sentence resolver-aware;
   - in the trailing paragraph, add that `sc validate` warns on a repo-level
     `url-resolver` and that a failed resolve is a failed mint;
   - also touch `*allow-no-credential*` (~line 670): it also covers a failed
     `url-resolver`.

   Keep scdoc syntax consistent with neighbours (escape `_` as `\_`).
5. `README.md` line ~109, the `[auth]` bullet: add one clause about
   `url-resolver` (trusted only above the repo; maps the origin to the forge's
   canonical HTTPS URL for the rewrite).
6. `AGENTS.md` (CLAUDE.md is a symlink). **Hard cap 40000 bytes. It is at
   39974 now.** In the **Per-session forge push credentials** bullet:
   - Delete ` (Forgejo ignores the username when the password is a token)`.
   - Replace `(the out-of-session \`sc merge\`/\`sc run\` worktree removal — no
     tombstone is written there, so the sweep could never find it)` with
     `(out-of-session removal)`.
   - Delete ` — the mechanism behind the fleet placement (one root entry in
     \`~/eng/repos/sweatfile\`, \`docs/plans/2026-09-03-auth-fleet-placement-design.md\`)`.
   - Replace `(\`CreateOpts.AllowNoCredential\`, the \`allow-stale-base\`
     two-halves shape, no MCP parameter)` with `(no MCP parameter)`.
   - Replace `(parsed from the CONFIGURED \`remote.origin.url\` —
     \`auth.ParseForgeRemote\`; never \`git remote get-url\`, which applies
     insteadOf)` with `(parsed from the CONFIGURED \`remote.origin.url\`, never
     \`get-url\`)`.
   - After `…are outside it.` append: "`[auth].url-resolver` (#335) is read only
     from layers ABOVE the repo (`sweatfileio.TrustedURLResolver`; repo-layer ⇒
     SKIP + validate warn): ambient `sh -c` in the repo root before the mint;
     its `canonical_https` JSON becomes `url.<canonical_https>.insteadOf =
     <origin>`, stored on `Credential.Remote` for `MirrorInto`. Failure =
     failed mint, never the string rewrite."
   - Reflow to about 80 columns.

   The five trims free about 440 bytes and the addition costs about 365, so
   the file should land near 39900. Confirm the size is ≤ 40000 with
   `folio_ls` flags `-la`. If it is still over, shorten the appended sentence
   (for example, drop the parenthetical) before touching any other bullet.
7. Commit the docs. The message carries the trailers `Closes #335` and
   `Closes #319` on their own lines.
8. In the final report, tell the operator that circus's generated
   `~/eng/repos/sweatfile` should gain
   `url-resolver = "smith repo resolve {origin} --output json"` under `[auth]`
   only after an `sc` carrying this change is installed fleet-wide (cross-repo;
   not done here).

**Verify:** no code changes. Check the AGENTS.md size and that the manpage
still renders (the merge gate's build runs scdoc). Do not run `just`.
