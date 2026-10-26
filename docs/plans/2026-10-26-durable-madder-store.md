# A madder store that outlives the session worktree

Status: implemented (retention knob `[madder].retain` deferred as a follow-up:
it needs tommy codegen; for now entries and blobs are kept forever).
Trigger: #373 follow-up. The operator ruled that the attestation blob must
outlive the session worktree, and that circus needs madder stores outside
spinclass sessions generally.

Claims are labelled R (read in code or docs), V (verified by running) and
I (inferred / proposed).

## Today

- R: `attestationBlob` (`cmd/spinclass/attestation_blob.go`) writes through
  `madder.Write`, which runs `madder write -format json .default -` with
  `MADDER_CEILING_DIRECTORIES=<worktree>`. So the blob lands in the
  per-worktree store `<worktree>/.madder/local/share/blob_stores/default/`
  (FDR 0003) and dies with the worktree at `sc close`.
- R: the URI handed to post-merge is `madder://blobs/<digest>`. It names no
  store, so a consumer must already know where to look.
- R (madder(1) FILES): madder has three store roots:
  - unprefixed ids: per user, `$XDG_DATA_HOME/madder/blob_stores/<id>/`;
  - `.`-prefixed ids: CWD-relative, the FDR 0003 store;
  - `%`-prefixed ids: the purgeable cache, managed by madder-cache(1).

## Proposal

### 1. Where it lives: one per-user store, not one per repo (I)

The store would be an unprefixed madder store with the fixed id `spinclass`,
at `$XDG_DATA_HOME/madder/blob_stores/spinclass/`. spinclass inits it on
first use, guarded by an existence check (FDR 0003's `blob_store-config`
marker, since `madder init` is not idempotent). It uses `-encryption none`,
as FDR 0003 does.

Why per user rather than per repo:
- Blobs are content-addressed, so cross-repo deduplication is free.
- Circus consumers work across repos and should not have to enumerate
  stores.
- The repo and session are recorded in the index (section 3), not in the
  store's location.

The writer must stop madder's walk-up from finding a CWD `.madder/` override.
`MADDER_XDG_USER_LOCATION_ONLY=1` exists for this (R, madder(1)
ENVIRONMENT). It should be set on that one invocation only, the way FDR 0003
scopes `MADDER_CEILING_DIRECTORIES`.

It must not be the `%` cache store: that store is purgeable by design, and
the ruling asks for durability.

### 2. Lifetime and GC: spinclass owns it, through a reference index (I)

spinclass inits the store and owns its retention. madder has no retention
automation, and FDR 0003 explicitly does none.

Proposed retention:
- An index entry pins a blob.
- `sc clean` drops index entries older than a sweatfile-configurable
  `[madder].retain` (default: keep forever, so nothing is deleted until
  someone opts in).
- Unpinned blobs are then removed.

Open question for madder's lane: whether madder offers a supported blob
delete or GC primitive. I did not find one in madder(1)'s FILES or
ENVIRONMENT sections and did not read further. Until it does, retention is
"index entries expire; blobs stay", which is safe and costs only disk.

### 3. Finding a blob by claim id: an index under XDG state (I)

One JSON file per claim:

```
$XDG_STATE_HOME/spinclass/attestations/<claim_id>.json
{"claim_id", "store": "spinclass", "digest", "repo", "session_key",
 "head_sha", "landed_sha", "recorded_at"}
```

- It is written after the merge lands, when `landed_sha` is known. The blob
  itself is written as today, just before post-merge.
- Post-merge keeps getting `SPINCLASS_ATTESTATION_BLOB=madder://blobs/<digest>`.
  It also gets a new `SPINCLASS_ATTESTATION_STORE=spinclass`, so the URI
  is resolvable.
- Circus and other consumers can look a claim up by id with a new read-only
  `sc attestation <claim_id>`, or read the index file directly. Either way
  they then fetch the blob with madder from the named store.
- A lookup by landed sha (which attestation authorized this commit?) is a
  scan of the index. That is cheap at expected volumes; add a second index
  if it is not.

The general "madder stores outside sessions" need is covered by the same
store id and the same index shape. Other record kinds would get sibling
directories under `$XDG_STATE_HOME/spinclass/` and share the store.

### 4. Relation to FDR 0003 (I)

The per-worktree `.default` store keeps its role: worktree-scoped scratch,
plus the pre-merge hook output `resource_link`, which only matters while the
session lives.

Durable records go only to the per-user `spinclass` store. They are written
once, not mirrored, so there is one copy whose lifetime is the index's.

FDR 0003 is unchanged except for a cross-reference. This would become a new
FDR, or an amendment to FDR 0002, its exploratory parent, once confirmed.
The activation model stays FDR 0003's Candidate F: dormant unless spinclass
was built with a madder pin.

## Operator answers (2026-10-26)

1. Default retention: keep forever, for now.
2. Store id: `spinclass`, owned by spinclass (init and GC).
3. Consumer lookup: the index file plus a read-only
   `sc attestation <claim_id>`; no madder-native lookup.
