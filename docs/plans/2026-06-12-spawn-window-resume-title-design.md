# spawn-window + resume-title design (#149, #154)

Approved 2026-06-12. Issues: #149 (spawn opens a terminal window already
attached to the worker; kitty/sway on Linux, kitty+Hammerspoon on macOS)
and #154 (`sc resume` into a spawned session leaves a stale terminal
title). Designed together: a #149 window runs `sc resume {id}` and would
hit #154 immediately.

## Diagnosis recap

- Terminal titles are owned by the interactive fish *inside* a session's
  pty (the user's `fish_title` emits `sc/$SPINCLASS_SESSION_ID` at every
  prompt). Spawned (FDR 0006) sessions run the harness directly — no
  fish inside, nothing ever writes a title, so an attaching terminal
  keeps its stale outer title (confirmed symptom: the launching shell's
  `prompt_pwd`). The inner harness does NOT rewrite titles, so a
  one-shot emission sticks.
- The user's kitty config already has `allow_remote_control = yes` and
  `shellIntegration.mode = "no-title"`; macOS already uses
  `open -na kitty --args …` as the launch idiom and PaperWM tiles new
  windows passively. Hammerspoon needs no active role in v1.

## Design

### Sweatfile surface

Two new `[session-entry]` fields, per-field-override merge like their
siblings:

```toml
[session-entry]
# argv exec'd fire-and-forget after the spawn template returns. {id} =
# the worker's session key, {dir} = the worker worktree. No
# {entry}/{prompt}. Unset (default) = no window.
spawn-window = ["sc-spawn-window", "{id}", "{dir}"]

# title sc resume emits before exec'ing the attach entrypoint. {id}
# substituted. Default "{id}"; empty string disables emission.
resume-title = "sc/{id}"
```

`sc validate`: `spawn-window` referencing `{entry}` or `{prompt}` is an
error (wrong template family); neither `{id}` nor `{dir}` present warns
(window can't identify its session). `resume-title` is free-form.

### Spawn flow (#149)

In `internal/spawn.launchRendered`, immediately after the spawn
template's `cmd.Run()` succeeds and BEFORE `chat.WaitForHello` (the
user chose window-at-launch: watch the 1–3 minute harness boot live,
see boot failures directly):

- Render `spawn-window` with the `SubstituteSpawn` family minus the
  entry splice ({id} = session key, {dir} = worktree).
- Exec fire-and-forget: `cmd.Start()` with `cmd.Dir` = worktree and the
  same `workerEnv` (window command sees the worker's `SPINCLASS_*`
  identity); a goroutine `Wait`s and logs a warning on nonzero exit.
- Render errors are warnings too — by then the worker is already
  launching; a broken window template must not kill a healthy spawn.
- One shared seam: applies to `sc spawn`, `sc fork --brief`, and the
  spawn-session/fork-session MCP tools identically.

### Resume title (#154)

In the resume path, just before exec'ing the attach entrypoint and only
when stdout is a TTY: render `resume-title` ({id} = session key) and
emit `\033]2;<title>\007` to stdout. One shot — sticks for spawned
sessions (nothing inside rewrites it), harmlessly overwritten moments
later by the inner fish in normal sessions. Covers manual resumes AND
the spawn-window windows (which run `sc resume {id}`).

### The ~/eng side (user config, checked in)

Home-manager installs `sc-spawn-window` via `writeShellScriptBin`,
platform variant chosen at Nix EVAL time (no runtime uname dispatch):

- Linux: `exec kitty --detach --title "sc/$1" --directory "$2" sc resume "$1"`
  (sway tiles it).
- macOS: `exec open -na kitty --args --title "sc/$1" --directory "$2" sc resume "$1"`
  (PaperWM tiles it; Hammerspoon passive — the existing alt+return
  idiom parameterized). `--title` covers the window's boot phase before
  `sc resume`'s own emission.

The rcm-managed global sweatfile gains the two knob lines.

## Testing

- Go: template-render units (substitution, {entry}/{prompt} rejection,
  validate warning); `launchRendered` with a stub spawn-window writing
  a marker (ran in worktree, worker env, fired pre-hello; failing stub
  does not fail the launch). Resume-title: render test + TTY gate
  (non-TTY emits no escape bytes).
- bats: spawn.bats stub spawn-window records argv; spawn succeeds when
  the stub exits 1. Resume through a pipe asserts no escape leakage.
- Production: next real spawn on this host (doubles as FDR 0006
  promotion soak).

## Rollback

Both knobs unset = exactly today's behavior. Rollback = delete the
sweatfile lines. Additive + config-gated; no dual-architecture needed.

## Tuning levers

- `resume-title` default `"{id}"` — change signal: bare keys read
  poorly for other users, or the `sc/` prefix proves universal.
- Fire-and-forget, warning-only window failures — change signal:
  silent window-open failures going unnoticed in practice would justify
  surfacing them in the spawn result text.
- Window-at-launch timing — change signal: window litter from
  hello-failed spawns becoming annoying would justify an after-hello
  option.

---

:clown: drafted by [Clown](https://github.com/amarbel-llc/clown) from the
2026-06-12 brainstorm; approved by the user section-by-section.
