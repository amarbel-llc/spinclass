# Loose ends: everything merged by spinclass/firm-mulberry since 2a79497

2026-10-25. Evidence labels: V verified (code read or a test run), R read
from someone else's report, I inferred. Nothing here is deployed on flic,
which runs 22e43a0 (R, coordinator), so no entry is verified live.

| Commit | Untested or unverified | Docs / drift | Follow-up |
|---|---|---|---|
| c029a54 PreToolUse verdict log | No size bound or rotation on `.spinclass/hook-verdicts.ndjson`, untested (I: it grows with every spinclass tool call) | Not described in a manpage (I: not searched exhaustively) | Decide a bound, or document it as session-lifetime |
| afce66e, 9172942 issue triage | Rows are leads by design | Many (a) rows are now done but not marked | Mark the done rows in a pass |
| 6773fc7 #423 maintenance off | Three packages got the guard; the rest of the git-running packages were not audited for detached maintenance (I) | none | Audit on the next #425-type flake |
| 7566032, a88b22e #45 | The resume test is re-scoped, not fixed | Decision note waits on the operator | #45 decision (option b recommended) |
| fa9b307 #230 tty | `tty.Interactive` has no unit test: it needs a real terminal (V) | none | none |
| 93cf947 #371 presence | The `sc list` presence column has not been checked against a live clown since the fix | none | Check after the next deploy |
| 2df1443 #365 stack metrics | The datagrams are tested; no stats-me series observed live | spinclass-sweatfile(5) updated | Look for the series after deploy |
| e3ed177 #329 model description | Rendered once when `serve` starts (V), so a sweatfile edit needs a serve restart; not documented | not documented | One line in the tool text, if wanted |
| 4d43a80 #305 output tail | Whether crap's compact renderer shows the diag message was not checked (I) | none | Legacy `[hooks].post-merge` path: triage row (unfiled) |
| 85e7daa #280 activity tee | none | none | #278 decides interleaving; the test pins either way |
| e2c2c5a #377 BindNew | A race loser's record stays in the store, unbound and without authority (V); no GC | FDR 0034 item 19 updated | none |
| d845036 #413 | `profile_narrowing` in authorityFields was my call on the open question (reversible) | none | Operator or coordinator to confirm |
| d974419, ae042dd #419 restart | No live restart has run. `closing_at` is not in authorityFields although close and the peek act on it (V) | FDR 0037, runbook, spinclass-sweatfile(5) updated | Flip the force refusal when clown ships `now`; first live run per the runbook |
| 7870223 #426 signing | Why the observed grit commits were unsigned is not established (I: signing not enabled in serve's git config) | spinclass-grit(7) COMMIT SIGNATURES | none |
| 4387264 #270 bodyless close | Warn point only: the whole tree is not stopped. Live repro blocked: spawn-session's origin fetch fails with publickey on flic (V, 2026-10-25) | none | posh/clown design for stopping the tree; proving an attached session's `sc` launcher pid |

No loose ends found: c628262, 639bea1, e8f2922, 9a44c51, 72949ee, abdc882,
caec5ea, 4220d18, dd05e33, fee2dea.
