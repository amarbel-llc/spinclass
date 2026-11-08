# Spawn-time folio extensions (design note, 2026-11-08)

The operator wants the coordinator to spawn a session with extra folio paths for
that one session (for example `/var/lib/redeploy-flic` or a job-spool directory)
instead of widening the profile for everyone, and ruled that those paths stay
denied by default. Asked by circus/eager-holly on 2026-11-08, after #342. This
note is design only: nothing is built, and the questions in §7 wait for the
operator.

Labels: **R** read in code or docs, **I** inferred or proposed, **U** unverified.

## 1. What exists (R)

- **The policy is the trusted `[folio]` stanza**, read by the session's own
  `spinclass serve` at start from the sweatfile hierarchy above the repo
  (`sweatfileio.TrustedFolio`, `internal/alfa/sweatfileio/trust_folio.go`),
  compiled once (`newFolioEvaluator`, `cmd/spinclass/policybroker_serve.go:182`)
  and served to moxy over the broker socket clown names. A stanza that fails to
  compile is refused whole.
- **With no trusted stanza there is no broker.** `startPolicyBroker` returns
  without listening (`policybroker_serve.go:96-99`) and moxy keeps its static
  floor. So an extension on top of a missing stanza would be silently ignored.
- **The grammar has no absolute-path anchor.** The anchors are `pwd`, `home`,
  `env`, `nix-store`, `spinclass`, `madder` and `proc`
  (`internal/0/folio/pattern.go:14-37`) and a path under an anchor "must not
  start with a slash" (`pattern.go:113-115`). `/var/lib/redeploy-flic` cannot be
  written today, except through an `env:NAME:` anchor whose variable the serve
  happens to export.
- **Evaluation is most-specific-wins**, deny on a full tie (`folioeval`). An
  extension entry is just one more allow in the list; a deny anywhere still
  competes by specificity.
- **Spawn knows the worker's session key before anything launches**
  (`worktree.ResolvePath`, `spawn.LaunchDetachedWith`), and writes the worker's
  frontend record before the harness starts (`startDetachedEntry`). The worker's
  serve resolves its own session key at start (`currentSessionKey`) and already
  re-reads the session index every second for the `spinclass:children` anchor
  (`rootsTTL`, `policybroker_serve.go:160-163`).
- **A profile carries no path policy.** `ResolvedProfile` holds provider, model,
  kept servers, per-tool exclusions, env and the moxy table
  (`internal/foxtrot/spawn/narrows.go`); `catalogWidenings` compares those
  narrow-only. Profile-level folio patterns are the later step of #438.
- **The approval floor for spawns is `perms.AlwaysAsk`.** An unconfined spawn
  always asks; a confined spawn is the one carve-out and passes without a prompt
  (`internal/charlie/perms/check.go:41-71`), and a spawn passing `unconfined`
  asks whatever else is true (`unconfinedRequested`, fail-closed).
- **A confined unit sees the filesystem through its spec.** `binds_rw`,
  `binds_ro`, `deny_paths` (`unitSpec`, `internal/foxtrot/spawn/confine.go:182`).
  The broker answers moxy's question about a path; whether the file exists in
  the unit's mount namespace is the spec's business.
- **The frontend record is authoritative for a confined session** (the unit
  cannot write it); for an unconfined session the same user can write it
  (FDR 0037 Limitations says so for the restart request).

## 2. The ask, restated

1. `spawn-session` can carry a folio extension: extra read (and maybe write)
   paths for that one worker.
2. Narrow-only against the coordinator's own folio ceiling and the profile's: the
   coordinator cannot hand a child what it could not read itself.
3. The operator sees and approves it. An unconfined spawn already asks.
4. It shows in `sc validate` and in the unit spec.
5. It ends with the session.
6. Say whether it can be granted later or only set at spawn.

## 3. Findings that shape the design (R/I)

- **F1. A new anchor is needed.** Without one the example paths cannot be
  written. Proposal: an anchor `fs:` whose root is `/`, so `fs:var/lib/redeploy-flic`
  keeps the rule that a path under an anchor is relative and never starts with a
  slash. It needs a floor: at least two literal segments, and the `/proc`, `/sys`,
  `/dev`, `/run` and `/etc` subtrees refused outright (procfs is `proc:`'s fixed
  list, #465). It resolves symlinks before matching, like every other file anchor
  (`folioeval.Resolve`).
- **F2. The extension must arrive through state the worker cannot choose.** The
  worker's serve builds its policy from the hierarchy plus, now, the frontend
  record. For a confined worker that record is out of the unit's reach, so the
  extension is enforced; for an unconfined worker it is only as strong as the
  other unconfined guards (a same-user agent can write its own record, as for a
  forged `when: now`). The extension widens nothing a human did not see, but
  against an unconfined worker it is a statement of intent plus the broker, not a
  boundary. Confinement (FDR 0035) is the boundary.
- **F3. The "profile ceiling" has nothing to compare yet.** A profile has no
  path policy, so in v1 the only ceiling is the coordinator's own. If profiles
  grow folio patterns (#438, later), the check gains a second input; the code
  path can take a list of ceilings from day one.
- **F4. The confined spawn's no-prompt carve-out must not carry an extension.**
  The carve-out exists because a confined spawn only narrows. An extension
  widens the worker's reach relative to the stanza, so it is the one parameter
  that makes even a confined spawn ask, the way `unconfined` does.
- **F5. No stanza, no extension.** A spawn carrying an extension is refused
  before anything is created when the target has no trusted `[folio]` stanza
  (the broker would be off and the extension inert) or when the stanza is
  refused as a whole.
- **F6. A broker allow is not a bind.** For a confined worker the path may not be
  visible in the unit at all. Whether the extension also adds a read-only bind to
  the spec is a decision (§7).

## 4. Options

**A. Ad hoc patterns in the call (recommended, as asked).** `spawn-session`
gains `folio_extra`: `{ "read": ["fs:var/lib/redeploy-flic"] }` (a list of
entries in the existing grammar). The call itself is the request; the approval is
the always-ask prompt, which lists the entries verbatim.

**B. Named extension sets in a trusted sweatfile.** The operator declares
`[[folio-extension]] name = "redeploy" read = [...]` once, in a layer above the
repo; the spawn passes only the name. `sc validate` checks the sets; the prompt
can be skipped for a name the operator pre-approved. Safer, less ad hoc.

**C. Both.** Names for the standing cases, ad hoc entries for one-offs, the ad
hoc form always asks.

Recommendation: **A for v1**, with the shape of C open: the record field and the
check do not care where the entries came from, so a named set is a later
front door onto the same machinery.

## 5. Recommended design (I)

1. **Parameter.** `spawn-session` (and `sc spawn --folio-read <entry>`, repeatable)
   gains `folio_extra {read: [...]}`. Entries parse with `folio.ParsePattern`; an
   unparseable entry, a `proc:` entry in anything but read, a `madder:` entry, or
   an `fs:` entry under the floor refuses the spawn before creation. Write
   extensions are not in v1 (§7).
2. **Ceiling, narrow-only.** Each requested entry must be covered by what the
   spawner may read now: evaluate a sample of the entry's concrete path against
   the spawner's own compiled policy, and require `allow` with no deny winning
   (the same evaluator, run in the spawn, on the spawner's roots). An entry the
   spawner cannot read is refused with the verdict and reason. Because the check
   is "the spawner may read it", the operator's example works only if the
   coordinator's own `[folio]` allows it; that is the intended reading of
   "narrow-only", and it means the operator widens the coordinator's stanza
   (visible, in the trusted layer) before the coordinator can pass it on.
   Profile ceilings join the check when profiles carry paths (F3).
3. **Approval.** `perms.AlwaysAsk` asks for a `spawn-session` call that carries a
   non-empty `folio_extra`, confined or not, failing closed on any shape that is
   not a clean object of string lists. The reason names the entries. A confined
   spawn without the parameter keeps its carve-out. At a terminal the operator is
   the caller and nothing prompts, as for `--unconfined`.
4. **Delivery.** The grant is a new field on the worker's frontend record,
   `session.State.FolioExtra {Read []string, GrantedBy, GrantedAt}`, written by
   the spawn before launch and carried by resurrect only if the same checks pass
   again (not copied blindly). The worker's serve adds the entries, as allows, to
   the compiled policy when it builds it, and re-reads the record each
   `rootsTTL` as it does the session index, so a later narrowing (§7) needs no
   restart. Deny entries from every layer still apply.
5. **Unit spec.** For a confined worker, each extension entry that resolves to an
   existing path adds a read-only bind to `binds_ro` (F6), recorded in the spec,
   so the broker's allow and the unit's view agree; a path that does not exist at
   spawn is refused. This is the one place the extension reaches clown's spec
   contract, and `binds_ro` is already a field.
6. **Visibility.** The prompt (item 3), the spawn result text (the entries and
   who approved), the frontend record, `sc whoami` and `sc list` (a `FOLIO+`
   marker), and the serve log line at broker start.
7. **`sc validate`.** The extension is not a sweatfile key, so validate has
   nothing to read from a file. It gains: the `fs:` anchor in the stanza's
   grammar check (with the floor), and, for a named-set front door later, the
   sets. A live session's grant is visible in `sc list`/`whoami`, not validate.
8. **Expiry.** With the session: the record is the only copy, so a close or
   resurrect (which re-checks) ends it. No clock expiry in v1; an optional `ttl`
   field is cheap to add to the record and the evaluator (§7).
9. **Granted later or only at spawn.** Only at spawn in v1. A later grant is a
   new gate: a handle right (the `restart`/`close` shape in FDR 0032 D13, with
   the same "only narrow what you hold" rule on grant) plus the always-ask, and
   the serve re-read in item 4 already supports it. Keep it out until the
   operator wants it.

## 6. Tests and build sequence (I, test-first, not started)

- Grammar: `fs:` parses with the floor; refuses one segment, `fs:proc/...`,
  `fs:etc/...`, a leading slash, `..`; evaluator matches under `/` only for the
  entry's own prefix, symlinks resolved before matching.
- Ceiling: an entry the spawner can read passes; one it cannot is refused naming
  the verdict; one a deny wins is refused.
- Approval: `AlwaysAsk` table for `folio_extra` absent, empty, malformed and
  clean, confined and unconfined; the confined no-prompt carve-out intact
  without it.
- Delivery: the record field is written before launch; the worker's evaluator
  allows the extension and not its siblings; a record edit by an unconfined
  agent is the documented soft case; no stanza refuses the spawn.
- Unit spec: `binds_ro` carries the resolved path; a missing path refuses.
- Visibility: the result text, `whoami`, `sc list`.

Sequence: (1) `fs:` anchor, evaluator and validate; (2) the record field and the
serve merge; (3) the ceiling check and the parameter; (4) always-ask and the
CLI flag; (5) the unit bind; (6) visibility; (7) docs (sweatfile(5) `[folio]`,
FDR 0035/0036 passages, a new FDR section or amendment to the #438 record).

## 6a. Operator rulings (2026-11-08, via circus/eager-holly)

Recommendation A is accepted. Ruled:

- **Anchor, ceiling, approval, lifetime.** The `fs:` anchor with the floor;
  narrow-only against the spawner's own compiled policy; always-ask; the grant
  ends with the session (§7 questions 1, 2, 5 and the base of 8).
- **Unconfined workers (Q9): accepted, as a documented SOFT grant.** The
  extension is not confined-only. For an unconfined worker it is intent plus the
  broker's answer, not a boundary (F2); the docs and the result text say so.
- **Read-only bind (Q4): yes.** Each extension path also becomes a read-only
  bind in a confined unit's spec (§5 item 5).
- **Read only in v1 (Q3).** No write extension.
- **Refused subtrees under `fs:` (item A):** `/proc`, `/sys`, `/dev`, `/etc`,
  `/root`, `$HOME/.ssh`, `$HOME/.gnupg` and spinclass's own state directories,
  extended on request. A WIDER list (`/run`, any directory named like secrets,
  keys or credentials, and what else a spawn-time read grant must never reach)
  is to be audited first: spinclass#467, findings and recommendations in
  `docs/plans/2026-11-08-467-fs-refusal-list-audit.md`. It blocks building the
  `fs:` anchor until the operator rules on that note's §7.

Still with the operator, to come in later batches: granting later (Q7), expiry
(Q8, beyond "with the session"), resurrect (Q10), named sets (Q6), and the
"narrow-only means the coordinator's stanza is widened first" consequence.
Nothing is built.

## 7. Open questions for the operator

Status: questions 1, 2, 3, 4, 5 and 9 are ruled (§6a); 6, 7, 8 and 10 remain.

1. **The `fs:` anchor.** Wanted as designed (root `/`, two-segment floor, a
   refused subtree list)? Or a narrower form, for example named roots the
   operator declares (`redeploy` meaning `/var/lib/redeploy-flic`), which also
   gives option B for free?
2. **The ceiling.** "The spawner may read it now" (recommended), or a separate
   trusted-layer list of what a spawner may pass on, independent of what it may
   read itself?
3. **Read only in v1, or write too?** Recommended read only; a write extension
   asks for its own ruling.
4. **Do extension paths add a read-only bind to a confined unit's spec?**
   Recommended yes (F6). The alternative is the broker allow alone, which for a
   confined worker may name a path the unit cannot see.
5. **Ask for every extension, even a confined spawn?** Recommended yes (F4).
6. **Named sets (option B/C) now or later?**
7. **Granting later**, or only at spawn? Recommended only at spawn.
8. **A `ttl` on the grant**, or "with the session" alone?
9. **Unconfined workers.** The extension is only a statement of intent there
   (F2). Accept that, or refuse an extension for an unconfined spawn and make it
   a confined-only feature?
10. **Resurrect.** Carry the grant through `sc resurrect` after re-checking, or
    drop it and make the coordinator ask again?

## 8. Sources

`cmd/spinclass/policybroker_serve.go`, `internal/0/folio/pattern.go`,
`internal/bravo/folioeval/eval.go`, `internal/alfa/sweatfileio/trust_folio.go`,
`internal/charlie/perms/check.go`, `internal/foxtrot/spawn/{narrows,confine}.go`,
`docs/plans/2026-11-01-438-policy-broker-design.md`,
`docs/features/0035-session-confinement.md`,
`docs/features/0037-session-restart.md` (the unconfined-record caveat); the
coordinator's message of 2026-11-08.
