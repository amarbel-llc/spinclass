---
status: proposed
date: 2026-09-29
promotion-criteria: |
  proposed -> experimental: one of the three surfaces (start-commands, pre-merge
  exemptions, `[auth].url-resolver`) speaks the protocol behind a declared
  opt-in while the legacy contracts keep working unchanged.
---

# One-shot plugin protocol

This is a draft FDR because the repo has no `docs/rfcs/`; it should be
promoted to an RFC if one is created.

## Problem Statement

spinclass shells out to operator-configured commands through three unrelated,
ad-hoc contracts:

- `[[start-commands]]` `exec-completions` / `exec-start`: argv in, JSON on
  stdout (spinclass-start-commands(7));
- FDR 0031 `[[pre-merge-exemptions]]`: the exit code is the answer;
- `[auth].url-resolver` (FDR 0028, #335): JSON on stdout.

Each invents its own input channel (argv, env, `{origin}` substitution), its
own error signal (exit code, stderr text, malformed JSON) and its own
versioning story (none). Adding a fourth surface repeats the work.

## Sketch

A one-shot process speaking newline-delimited JSON-RPC 2.0 over stdio, using
juggler(7)'s WIRE FORMAT framing:

- exactly one request line on stdin, exactly one response line on stdout;
- stderr is diagnostics only;
- method names such as `start/completions`, `start/exec`, `merge/exempt`,
  `auth/resolve-url`;
- a `protocol_version` param on every request;
- JSON-RPC error objects replace exit-code semantics.

## Open Questions

- What is the opt-in declaration shape (a per-command `protocol = "jsonrpc"`
  key, a separate table, a sniffed banner)?
- Who owns the timeout: spinclass per surface, or the request itself?

## Non-goals

- Long-lived plugins. Every call is a fresh process.
- Changing the legacy contracts. They keep working.
