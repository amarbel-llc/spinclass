# Merge admission: the operator's rulings for C (#429, #432), 2026-11-04

Rulings note, no code. Piece C of the confined-merge arc
(`2026-11-03-432-confined-self-landing.md`, section 8 step 3) was blocked on
four questions about who may land or gate a session and how that is checked.
The operator confirmed the four rulings below on 2026-11-04 through the
coordinator, with one future-direction note that is recorded here and not
built. This note is the reference for C's implementation and for B's
test-harness fix, which follows it.

Labels: **R** read in code or docs, **I** inferred or proposed, **U**
unverified.

## 1. Where things stand (R)

- Of the eight FDR 0032 D13 handle rights, three are enforced (`close`,
  `grant`, `restart`); `observe`, `instruct`, `cap`, `merge` and `check` are
  recorded on grants and checked by no tool (spinclass#429, the rights table
  test in `cmd/spinclass/handle_rights_table_test.go`).
- The first build of B (branch `light-cedar--432-first-build` at 6b3b974, held)
  adds the trusted `[permissions] allow-self-merge` scope, the confined-merge
  constraints and the gate-definition rule; it relaxes `merge-this-session*`
  and `nothing-but-the-truth` for a confined caller's own session and keeps
  `check-this-session*` refused (section 12 item 3 of the design note).
- The design note's section 9 item 4 recorded "merge right: both", a sweatfile
  scope for self-landing and a handle right for landing a child, and proposed
  the handle as a virtual sweatfile at the top of the chain. That proposal is
  what ruling (a) and the future-direction note settle.

## 2. The rulings (operator, 2026-11-04)

**(a) Self-merge admission is sweatfile policy, not a handle right.** Whether
a session may land its own branch is decided by `[permissions]
allow-self-merge` in a trusted layer (above the repo; a repo or worktree value
is ignored), the shape `allow-self-close` set (#389). A handle is the wrong
object for a session acting on itself. Default: off everywhere, and it stays
off until B lands and a confined canary round has run.

**(b) Merge performs a single admission check.** One function answers "may
this caller land this session", consulted once per merge before any
attestation peek or claim, and its answer is the only admission the merge
tools use. Today's pieces (the confined-caller refusal, the implicit-session
refusal, the foreign-branch refusal of #463, and B's `allow-self-merge`)
become inputs to that one check rather than a sequence of independent gates
in the handler. A refusal from it never touches the attestation (#317's rule).

**(c) Check gets parity with merge.** `check-this-session*` is admitted under
the same check as `merge-this-session*`: a caller that may land its own
session may gate it, and the first build's "check stays refused" (section 12
item 3) is reversed. The gate-free CLI `sc check` is unchanged.

**(d) The attestation stays as the gate.** Admission says who may ask;
`nothing-but-the-truth` and the pre-merge gate still decide whether a merge
proceeds. A self-written attestation is accepted for now (ruling 2 of
2026-11-03); its external certification (#301, #337) is a later tightening,
not a precondition for C.

## 3. Future direction, recorded and not built: the sweatfile cake

The operator wants a handle-right option later, in this shape: a handle **is**
a virtual sweatfile, overlaid at the very end of the sweatfile layering
(global → parent directories → repo → *the handle*). A spawner's grant to a
child is then a last-layer overlay that can set or narrow the same
`[permissions]` keys a trusted layer can, rather than a separate right type
checked by a separate function. The existing trusted-layer rule (the layer
above the repo wins, last wins) extends with the handle as the top layer; a
nearer layer can only narrow (EPIC 0001 principle 3).

What (a) to (d) leave room for: ruling (a) makes `allow-self-merge` a sweatfile
key, which is exactly what a last-layer overlay would set, so the cake needs no
second admission path; ruling (b)'s single check reads the merged sweatfile, so
adding a layer changes its input and not its shape; ruling (c) means check
follows automatically; ruling (d) is orthogonal. The `merge` and `check` names
in the D13 vocabulary stay recorded and unenforced until the cake exists; when
it does, "a holder may land that child" is "the holder's handle overlay grants
`allow-self-merge` to the child", not a check on the holder's rights list.

Not decided here: how a handle overlay is stored and signed (FDR 0032 D8/D14
have the records), which keys it may set beyond `[permissions]`, and whether
the spawner's overlay is pinned on the child's session state like
`[session-entry]` is. Those are the cake's own design note.

## 4. What C builds (I, test-first, after this note lands)

1. **The single admission check** (ruling b): a function in the merge/check
   handler path taking the caller classification, the session resolved by its
   worktree (#463), and the merged trusted sweatfile, returning admit or a
   named refusal. Inputs today: implicit session (refuse, #317), foreign
   branch checked out (refuse, #463), confined caller without
   `allow-self-merge` (refuse), confined caller with a `target`, `branch`,
   `local_only` or non-empty `targets` (refuse, section 6 of the design note).
2. **Check parity** (ruling c): `check-this-session*` admitted through the same
   function; the first build's check refusal removed; the handle-rights table
   test updated if any name moves.
3. **No new handle enforcement for `merge`/`check`** (ruling a and section 3):
   the D13 names stay in `unenforcedHandleRights` with a comment pointing here.
   `observe`, `instruct` and `cap` are out of C's scope (#429 keeps them).
4. **Attestation untouched** (ruling d).

## 5. B's test-harness fix, which follows

`TestMergeRefusesAChangedGateDefinitionForAGuardedSession` on the #432 branch
fails for lack of a clown scope id in context: the guarded gate's inputs
evaluation requires one (`internal/charlie/check/lockpolicy.go`, R). The fix is
to the test harness (supply the scope the way the serve does), not to the
gate. The operator lifted the branch hold for this fix and a clean check only;
no canary and no merge of B yet.

## 6. Sequence

This note lands (docs, `targets=[]`). Then B's test-harness fix and a clean
check on the branch. Then C as in section 4, test-first, merged on the
operator's go since it is policy code. Then B's canary, then B ships. The
frozen hook (`2026-11-04-459-frozen-pre-commit-hook.md`) is ahead of all of
it in the arc's order.
