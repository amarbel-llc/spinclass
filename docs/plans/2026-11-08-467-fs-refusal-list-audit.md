# fs: refusal list audit (spinclass#467, 2026-11-08)

Asked by circus/eager-holly on the operator's behalf. Explore and docs only; the
`fs:` anchor stays unbuilt until the operator rules on §7. Parent design:
`docs/plans/2026-11-08-spawn-time-folio-extension.md` (§6a records the starting
list). Labels: **O** observed in code, docs or a live call this session; **I**
inferred, not tested; **U** unknown, needs the operator or another repo.

## 1. Threat model: what a spawn-time read grant must never reach

A grant lets one worker's moxy read tools (folio read, rg, ls, glob and the
like) see a path outside its worktree. Assume the worker is prompt-injectable
and that a grant, once given, is used to the full. The grant must never reach:

1. **Credentials and the means to mint them.** Forge tokens, ssh and pivy/piggy
   agent state, card-derived keys, the per-session push credentials
   (`$XDG_STATE_HOME/spinclass/credentials`, `auth` pool), signer lists
   (`profile-grant-signers`), cloud and API keys, anything under a name that says
   so. A read of any of these is a standing authority, not a data read. O: the
   #438 record already names one live precedent, a process environment that
   exposed a forge credential on 2026-10-27, which is why `proc:` is a fixed
   list.
2. **The control plane of the sandbox itself.** Frontend records
   (`$XDG_STATE_HOME/spinclass/{records,units,principals,serves,grants,rebases}`),
   the policy broker and serve sockets, clown's own state, and the sweatfile
   layers (`~/.config/spinclass`). Reading them shows exactly where the unit's
   walls are and which paths the next grant could name. O: the paths exist as
   listed (`internal/bravo/confinement/*.go`, `grantstore`, `rebaserecord`).
3. **Live sockets and runtime state** (`/run`, `$XDG_RUNTIME_DIR`): agent
   sockets, dbus, the broker socket named in `CLOWN_POLICY_BROKER`, service
   secrets that systemd and secret managers drop under `/run`. I: a read of a
   socket path is a stat, but a tool that opens it is a connection; the
   broker answers per path, not per file type.
4. **Other tenants and other sessions.** Other users' homes, other sessions'
   `.spinclass/` (already denied for worktrees in the shipped stanza, O), mail,
   browser profiles, shell histories, transcripts (`~/.claude` is readable by
   the shipped stanza, O, and is outside this question).
5. **Kernel interfaces** with their own rules (`/proc`, `/sys`, `/dev`), covered
   by `proc:` for the six safe /proc shapes and otherwise refused.
6. **System configuration that is not data**: `/etc` (shadow, ssh host keys,
   nixos secrets paths), boot and kernel (`/boot`), `/root`.
7. **The grant mechanism and the deny list.** A worker must not be able to read
   the file that holds the refusal list or the grant (a compiled-in list is not a
   file; a sweatfile key is, and sits under `~/.config/spinclass`, item 2).

What a grant may reasonably reach: a named service-state directory the operator
chose (`/var/lib/redeploy-flic`, a job spool), read only, no more than its own
subtree.

## 2. The wider refusal list (recommended)

Two kinds of rule, applied to the entry at spawn and to every request path at
match time. All are directory-boundary prefixes after full resolution (§4).

**A. Refused subtrees (path prefixes).** The operator's starting list plus the
audit additions:

| prefix | why | status |
|---|---|---|
| `/proc`, `/sys`, `/dev` | kernel interfaces; `proc:` is the only door | ruled |
| `/etc` | system config, host keys | ruled |
| `/root` | root's home | ruled |
| `$HOME/.ssh`, `$HOME/.gnupg` | keys | ruled |
| spinclass state: `$XDG_STATE_HOME/spinclass`, `$XDG_CONFIG_HOME/spinclass` (`~/.config/spinclass`), `$XDG_DATA_HOME/spinclass`, `~/.local/log/spinclass` | control plane, credentials, tool-use logs | ruled in outline ("its own state directories"); this names them |
| `/run`, `/var/run` | sockets, runtime secrets (`/var/run` is a symlink to `/run` on this host, I) | **new** |
| `/boot` | kernel and initrd | new |
| `$HOME/.local/state` entirely, and `$HOME/.config` entirely | circus already denies `.local/state/{ssh,piggy,troupe,ringmaster}` by name, O; a grant is stricter than the shipped allow list, so refuse the parents rather than enumerate | new |
| `$XDG_STATE_HOME/circus` | #431 (confined units must not bind or expose it), O as an open issue | new |
| `/home/*` other than `$HOME` | other users | new (I: this host is single user, so low cost) |
| `$HOME/.password-store`, `$HOME/.mozilla`, `$HOME/.config/google-chrome` and similar | covered by the `$HOME/.config` and dotfile rule below when dotfile-parents are refused | folded in |

**B. Refused path names (a segment rule, any depth under a granted prefix).** A
segment equal (case-insensitive, exact) to any of: `secret`, `secrets`, `key`,
`keys`, `credential`, `credentials`, `creds`, `token`, `tokens`, `password`,
`passwords`, `private`, `.ssh`, `.gnupg`, `.aws`, `.kube`, `.docker`,
`.netrc`, `.git-credentials`; and a segment with a suffix `.pem`, `.key`,
`.p12`, `.pfx`, `.kdbx`, `.age`. I recommend exact segment matching, not a
substring match: substring on `key` would refuse `keyboard` and `monkey`,
while an attacker who can create the directory can always choose a name the
list does not know (item 3 of §6). So this rule limits accidents and the
common layouts; it is not a boundary on its own. U: the right word list is a
judgment call for the operator.

**C. Dotfile-parents under the user's home.** Refuse a grant whose prefix is
`$HOME` itself, or any direct child of `$HOME` that starts with `.` unless
that child is on a short allow list the operator names (today: `.claude`, which
the shipped stanza reads anyway). A grant of `$HOME/projects/x` is allowed.
Rationale: most credential stores live in a dotdirectory directly under HOME.

**D. The floor.** Keep the two-segment floor from the design: no `fs:` entry
may be fewer than two segments (so `/var`, `/home`, `/opt`, `/tmp`, `/` and
`/mnt` are refused, as are `fs:` entries equal to a refused prefix).

## 3. Parent versus child grants

- **A grant above a refused path is refused.** If a granted prefix contains a
  refused subtree (`fs:var` containing nothing here, but `fs:home/sasha`
  containing `.ssh`), rule D already refuses most; for the rest, refuse the
  grant when the refused path is a descendant of the granted prefix, rather than
  carve it out. I: carving out relies on deny-over-allow specificity (O, in the
  evaluator: a deny of equal or greater specificity wins), which works, but a
  refusal at spawn is simpler to explain and cannot be undone by a later change to
  the evaluator. Recommended: refuse at spawn, name the offending child.
- **A grant below a refused path is refused.** `fs:run/user/1000/x` is under
  `/run`. Rule A applies to the prefix of the entry, not only to the entry equal
  to the refused path.
- **A child inside an allowed grant that is a refused name is denied at match
  time** (rule B, rule A applied to the request path). Implementation shape: the
  refusal list compiles into deny entries added after the grants in the policy
  the serve builds, so "deny wins ties" (O: eval.go ties go to deny) keeps them
  in force even when a grant is more specific. I: a more specific allow would
  still beat a less specific deny under the CSS-like tuple, so the deny entries
  must be compiled at maximal specificity, or the evaluator needs a separate
  hard-deny pass that runs first. Recommended: a hard-deny pass before the
  specificity evaluation, not entries in the same list.

## 4. Symlinks

- O: `folioeval.Resolve` resolves the **parent** of a path and keeps the **leaf
  as named** (roots.go, comment: so a `result` link into `/nix/store` inside a
  worktree still counts as a worktree path). Roots are resolved the same way.
- Consequence for `fs:` (I, not tested): a regular file or directory named
  `notes` inside a granted tree that is a symlink to `/etc/shadow` is a leaf, kept
  as named, matches the grant, and is allowed by the broker; the tool then
  follows it. The same holds for a symlink created by the worker in a granted
  tree it cannot write (so only a pre-existing or externally created link), and
  for one inside a path the worker can write via its own worktree grant if the
  grant points into it.
- Recommendations:
  1. **At spawn, fully resolve the entry** (`EvalSymlinks` on the whole path,
     leaf included) and apply rules A to D to the **resolved** path, and refuse a
     grant whose resolved path differs from the requested one unless the
     coordinator names the resolved path (so the approval prompt shows what is
     really granted). A missing path is refused (already in the design).
  2. **At match time for `fs:` only**, when the request path exists, resolve the
     leaf too and require the resolved path to still be inside the grant and not
     under a refused subtree; otherwise `deny`. This is a new, `fs:`-only rule so
     the worktree `result` link behaviour is unchanged. The cost is one
     `EvalSymlinks` per fs: decision (I: acceptable; the broker's per-check
     latency was measured by `debug-bench-policy-broker`, not for this case).
  3. **A hard link or bind mount** cannot be detected by name (U); both need
     write access the worker should not have to the granted tree, and the
     read-only bind in the unit spec (ruled) removes the bind-mount case.
  4. **Race.** The check and the use are separate (I): a link swapped in between
     is a TOCTOU gap for any path-based broker, including today's. For a
     confined worker the read-only bind makes the tree immutable to it; for an
     unconfined worker the grant is a soft grant by ruling.

## 5. Where the list lives

- **Compiled in (recommended for A, C, D).** The prefixes that do not depend on
  the host (`/proc`, `/sys`, `/dev`, `/etc`, `/root`, `/run`, `/boot`, `/var/run`)
  and the structural rules (floor, dotfile-parents) belong in code with the
  anchor, covered by tests, and not settable by any layer. A sweatfile cannot
  remove an entry; this is the "never widen a default" reading.
- **Derived at run time (A for the user-relative entries).** `$HOME`,
  `$XDG_STATE_HOME`, `$XDG_CONFIG_HOME` are read from the serve's environment
  the way the anchors do (O: `RootsFor`), so the list follows the host.
- **Trusted-layer extension (B's word list and any extra prefix), narrow only.**
  A new key, for example `[folio] fs-refuse = [...]`, readable only from trusted
  layers (global or above the repo, never pinned, like `restart` and
  `allow-posture-widening`, O as a pattern), which **adds** to the compiled list
  and can never remove from it. `sc validate` reports a repo-level value and an
  entry that is not a clean prefix or a clean segment. This answers "extended on
  request" without a rebuild.
- **Not a file the worker can read.** The trusted layer sits under
  `~/.config/spinclass`, which rule A refuses as a grant, so the list's own
  file cannot be granted (item 7 of §1). The shipped stanza's reads do not include
  `~/.config` (O: `home:.config` was denied in a call this session).

## 6. Residual risks that no list removes

1. A readable file whose **content** is a secret (a deploy log with a token in a
   granted directory). Needs the operator to choose the directory, which is what
   the always-ask is for.
2. A **name the list does not know** (rule B's limit). I would not claim the name
   rules are a boundary; the boundary is the explicit grant, the ask, the ceiling
   (narrow-only against the spawner's policy) and the read-only bind.
3. **Soft grants for unconfined workers** (ruled): no boundary, only the broker.
4. **A grant outlives a path's meaning.** `/var/lib/redeploy-flic` today holds
   state; tomorrow it may hold a key. The grant ends with the session (ruled), and
   the always-ask text should print the resolved path and its top-level entries
   so the approver sees what is there at grant time (new, small).

## 7. Questions for the operator (ranked)

1. **Hard-deny pass versus deny entries** (§3): recommend a hard-deny pass
   before specificity evaluation. If no: deny entries at maximal specificity.
2. **Match-time leaf resolution for `fs:`** (§4 item 2): recommend yes. If no:
   resolution only at spawn.
3. **Refuse parents rather than carve out** (§3): recommend refuse at spawn.
4. **Refuse all of `$HOME/.config` and `$HOME/.local/state`** and dotfile
   children of HOME (rules A and C): recommend yes. If no: enumerate, which
   will drift.
5. **Segment word list** (rule B): recommend the list above, exact segments.
   Operator to extend or cut.
6. **A trusted-layer `fs-refuse` key** (§5): recommend yes, add-only. If no:
   compiled in only.
7. **Print the resolved path and top-level entries in the always-ask**
   (§6 item 4): recommend yes.

## 8. Sources

- `internal/bravo/folioeval/roots.go` (Resolve keeps the leaf), `eval.go`
  (deny wins ties, matchUnder, the anchors), `internal/0/folio/pattern.go`.
- `docs/plans/2026-11-08-spawn-time-folio-extension.md` §5, §6a, §7.
- `internal/bravo/confinement/{record,mapping,serves,caller}.go`,
  `internal/alfa/grantstore/grantstore.go`, `internal/0/rebaserecord/`,
  `internal/charlie/auth/pool.go` for the state paths.
- circus `infra/hosts/home/spinclass-workstation.nix` folio stanza (read via
  the policy broker this session) for the existing credential denies.
- spinclass#431, #438, #442, #465, #467.
