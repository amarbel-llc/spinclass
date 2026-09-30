# Epics

An **epic** is the widest design record in this repository. It states a vision
that spans several repositories, in plain language, for a reader who is outside
this ecosystem.

## What an epic is for

- Say what should become true, and why it is worth building. Not how.
- Name the value it delivers and the impact if it lands.
- Link the child records (FDRs, RFCs, notes, tracking issues) that implement
  it, in any repository.

An epic is a parent record. Each child cites its epic as parent, the same way an
FDR's companion records cite that FDR.

## Voice

An epic is read by people who have never seen this ecosystem, so:

- **No product or repository names in the body.** Write "the session manager",
  "the messaging layer", "the job platform", "the hardware key". The body then
  stays readable when a component is replaced or renamed.
- **Explain every concept the first time it appears.** Analogies are welcome;
  explain the analogy too.
- Prefer short sentences and bulleted lists over dense paragraphs.

Every epic carries a **"Where this lives today"** appendix. That appendix is the
only place product names appear: it maps each term in the vision onto the
component that realizes it and the child record that tracks it.

## Format

Epics are numbered `NNNN-slug.md` in this directory, like FDRs in
`docs/features/`.

Frontmatter mirrors an FDR's, plus `children`:

    ---
    status: vision
    date: 2026-09-30
    promotion-criteria: |
      vision -> active: ...
      active -> realized: ...
    children:
      - <repo> FDR 0032 (docs/features/0032-....md)
      - <repo>#123
    ---

`status` values:

- **`vision`**: written, no child has landed yet.
- **`active`**: at least one child has landed.
- **`realized`**: the promotion criteria hold.
- **`superseded`**: replaced by a later epic, which it names.

`children` is a list of record references: a design record with its repository
and path, or a tracking issue. It is the epic's index, kept current as children
are filed and land.

Epics appear in the dynamic system-prompt design-record index tagged `EPIC`,
listed above FDRs.
