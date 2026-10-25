# #45: what a no-args, non-TTY `sc resume` from the main repo should do (decision)

Date: 2026-10-25. Author: spinclass/firm-mulberry/bozo. For the operator's
decision sheet.

Claims are labelled V (observed), R (read in code), or I (inferred).

## What happens

- The bats test `spinclass_resume_from_main_repo_no_args_lists_ids`
  (zz-tests_bats/session.bats) starts one session and runs `sc resume` from
  the main repo with no target and no TTY. It expects the refusal to list
  the session IDs.
- In gate merge-35cd8327 the command printed
  `resume requires an interactive terminal (or pass -y to skip confirmation)`
  and no IDs (V). The test is skipped again, with that reason.
- The message comes from `resumeConfirmPlan`
  (cmd/spinclass/resume_confirm.go:66-69). That function runs on an
  already-resolved session state (R). So with one session, the target was
  resolved before the confirmation gate ran, and the gate refused because
  there was no TTY and no `-y`. The picker's own non-TTY refusal,
  `no session selected; available: <ids>`
  (internal/bravo/sessionpick/sessionpick.go:194), was never reached (I:
  the single session was picked without a prompt; not traced).

## Options

- **(a) Code: list the sessions on a non-TTY no-target resume.** Before the
  confirmation gate, refuse with the picker's
  `no session selected; available: …` whenever there is no explicit target
  and no TTY, even with one session.
  - It never silently picks the only session.
  - It gives an agent or script the ID to pass back.
  - It changes behaviour: with `-y`, a single-session no-arg resume currently
    proceeds (R, resumeConfirmPlan returns early on `yes`), and that would
    need a decision of its own.
- **(b) Test-only: assert the confirmation refusal.** Keep the code. The test
  asserts `resume requires an interactive terminal (or pass -y …)`. The
  "lists IDs" claim moves to a test with two sessions, where the picker
  cannot auto-resolve. That the picker refuses with the list when there are
  two or more sessions is inferred and would be verified by the new test.
- **(c) Leave it skipped.** No change.

## Recommendation

**(b).** It needs no behaviour change. It pins what the command does today,
and it keeps the "lists IDs" coverage where it truly applies (2+ sessions).
(a) is a reasonable UX change, but it alters the `-y` single-session path, so
it is a separate decision.

If (b) is chosen, the work is:
- test-first: the two-session test goes red first if the picker does not list
  IDs there;
- one merge, targets [].
