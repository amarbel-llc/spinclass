# Confinement test harness and conformance recipe (plan, 2026-10-22)

Status: plan only, nothing built. The operator approved planning on
2026-10-22, via circus/eager-holly/clarabell. Context: FDR 0035, "Live
evidence" (rounds J to P).

## 1. A local confinement test harness

Goal: run a real transient systemd unit, built from a launch spec that the code
under test writes, without deploying spinclass, without touching real
sessions, and without turning confinement on for flic.

### Two shapes

**A. NixOS VM test (recommended as the gate).** A `nixosTest` in spinclass's
flake:
- one VM user with a systemd user manager;
- clown and spinclass built from the flake under test;
- a fixture git repo with a local bare `origin`;
- a fake troupe and ringmaster runtime layout (directories and files only).

The test writes the VM user's own global sweatfile with `[confinement]
enabled`, and runs `sc spawn --confined` with a fake harness in place of
claude. The fake harness is a spawn-entry script that runs the canary probe
recipes inside the unit and writes their output to the worktree. The frontend
side then runs:
- `sc confinement-check`;
- a frontend grit push;
- a frontend `git config` and a sibling spawn;
- close.

All of this happens inside the VM. Nothing reaches the host's sessions.

**B. On-host debug recipe (interactive only, not in the gate):**
`debug-confinement-harness`.
- It runs with `HOME`, `XDG_STATE_HOME`, `XDG_CONFIG_HOME` and `TMPDIR` under a
  throwaway directory, so its sweatfile hierarchy, state dir, records and
  credentials are its own.
- It shares the real user bus and the real `XDG_RUNTIME_DIR`, which systemd
  user units need.
- Its units carry a `spinclass-harness-` prefix.
- An `EXIT` trap stops every unit with that prefix, and a separate reaper
  recipe lists and stops leftovers.

### How it leaves flic's confinement setting alone

The switch is read from the trusted layers above the repo
(`TrustedConfinement`), and the harness's HOME is its own. So the harness's
global sweatfile turns confinement on for the harness only. The real
`~/.config/spinclass/sweatfile` is never read or written. In shape A the VM
has no access to the host's sweatfiles at all.

### Safety

- The harness only ever stops units it started, matched by prefix and by its
  own state dir's unit records.
- Since b502958 `RequestClose` never signals a confined session, nor any
  process it cannot prove belongs to the session. The harness never calls
  close on a session outside its own state dir.
- No real forge: `origin` is a local bare repo, and the "minted" credential is
  a file written by a fake `[auth].mint-command`.
- Shape B touches the real runtime dir, so it must never bind or delete real
  troupe or ringmaster files. Its spec's runtime binds point at fixture copies
  under its own temp directory. That needs a spinclass-side override of the
  runtime paths for the harness only (a test seam, not a sweatfile key).

### Findings from rounds J to P it would have caught

| Round | Finding | A | B |
|---|---|---|---|
| J | refs and logs read-write: raw ref create and raw push | yes | yes |
| K | credential readable after a frontend push (mount lost on rename) | yes: frontend `git credential approve`, then the probe | yes |
| K | `just` runtime dir read-only (shebang recipes fail) | yes | yes |
| L | nested read-only file binds hold; EBUSY on rename and unlink | yes | yes |
| L | F11 false positive (PID from the unit's pid namespace) | yes | yes |
| M | `<common>/config` stale versus detached after a frontend rewrite | yes | yes |
| N | zombies in the unit's `/proc` | only if the fake harness reproduces `dbus-launch` | same |
| N, O | other sessions' troupe account files readable | yes, with clown's real agent-scope binds over the fixture troupe dir | yes, with the fixture troupe dir |
| N | the `--unconfined` CLI gate | no unit needed; bats covers it | — |
| P | tracked `.envrc` rewritten; revoke under a blocked `.envrc` | no unit needed; unit tests cover it | — |

### What the harness cannot catch

- **Real XMPP chat and job-wake delivery.** Shape A could run a local
  prosody, but the cost is high; this stays a live-round item.
- **A real forge push with a minted token**, and revocation on the forge.
- **The real claude harness**: tool lists, profile exclusions pushed to moxy,
  readback, ToolSearch.
- **The operator's real runtime layout and long-lived serve staleness**
  (#399).
- **nikulin-specific state**: other hosts' sessions and their files.

### Cost (estimates, not measured)

- **Shape A:** a VM image built once per flake change, then a few minutes per
  run. It needs KVM on the x86_64-linux builder, which I have not checked for
  flic. If KVM is missing, shape A runs only where it exists.
- **Shape B:** seconds per run, but it needs the real user bus, so it is not
  a sandboxed nix check.

Proposal: build A first as an opt-in check (`checks.confinement-vm`), not part
of `just`'s default gate until its run time is known. Use B only for
debugging.

**Update (2026-10-22, measured on flic): shape A is not feasible here.**
`/dev/kvm` does not exist and no kvm module is loaded. `/proc/cpuinfo` has the
`hypervisor` flag but no svm/vmx, so flic is itself a VM without nested
virtualization. nix.conf has `builders =` empty. A NixOS VM test would run
under qemu TCG; its run time was not measured. By the coordinator's ruling the
harness falls back to **shape B**, with the test-only runtime-path seam, and
stays opt-in. **Shape A is dropped** (coordinator, 2026-10-22: no `/dev/kvm` on
flic, measured).

Shape B must also carry a fast local lane for the serve integration tests
(`TestServeMergeThisSessionStdioIntegrity`, `TestServeCheckThisSession`,
`TestServeMergeThisSessionHiddenWhenDisabled`,
`TestServeCheckThisSessionHiddenByDefault` in
`cmd/spinclass/serve_integration_test.go`). They need a `go` toolchain on PATH,
so godyn's inner loop skips them and only the pre-merge lane runs them. That
is how merge-7be3acab found the gate regression d56307e fixed.

That lane is built as option (y): the flake package
`.#spinclass-serve-integration`, the buildGoApplication build with a checkPhase
that runs only those four tests, run by the recipe `debug-serve-integration`.
It is a package, not a check, so it stays outside the gate. Option (x),
running the tests natively through `godyn-go`, is not used: one attempt
rewrote the tracked `go.nix` (golang.org/x/text v0.36.0 to v0.37.0) and ran no
test. The question of a frozen, no-resolve invocation went to igloo's owner.

Dependency: both shapes use clown's real agent-scope launcher as a binary, so
the harness must pin the clown that ships the launcher under test.

## 2. A standing conformance recipe

Goal: one pass that runs every probe from rounds K to P and prints a pass or
fail table. It reuses the canary probe recipes on canary master.

### Shape

There are two halves, because some probes only make sense inside the unit:

- **Frontend half**, in spinclass:
  `just debug-confinement-conformance <session> <principal>`. It runs:
  - `sc confinement-check` (F1 to F15, with the strict notes);
  - a read of the unit spec (the binds the spec must carry);
  - frontend reads through `/proc/<main_pid>/root`, the same view F9 uses,
    for the credential, `<common>/config` and its inode, the troupe accounts'
    readability, and the ringmaster and troupe state directories;
  - `sc list` for the session;
  - a tombstone check after close.
- **Worker half**: the canary recipes the worker runs with `run_recipe`.
  They are `probe-env`, `probe-gitptr`, `probe-cfgwt`, `probe-cred`,
  `probe-refs`, `probe-commoncfg-read`, `probe-commoncfg-write`,
  `probe-runtime`, `probe-procs`, `probe-troupe-perms`, `probe-nixdirenv`
  and `probe-ls`. Each prints `label: rc=N ...` lines.

A small aggregator turns both halves into one table:
- a `--format` like `sc confinement-check`'s;
- input from the worker's recorded output file, written to its worktree
  `.tmp`;
- columns: row, source, result, evidence, needs.

### Rows

| Row | Probe | Needs |
|---|---|---|
| mounts: `.git` pointer and `config.worktree` read-only, EBUSY on rename and unlink, still mount points (F9) | probe-gitptr, probe-cfgwt, F9 | live unit; worker and frontend |
| refs and logs read-only; status, log and diff work | probe-refs, F9 | live unit; worker |
| credential absent or unreadable in the unit, before and after a frontend push | probe-cred, F9, frontend grit push | live unit, frontend, a forge or local origin |
| `<common>/config` read-only; stale-inode reading | probe-commoncfg-*, frontend stat | live unit; worker and frontend |
| F9 to F15 with strict verdicts | `sc confinement-check` | live unit; frontend |
| runtime-dir listing | probe-runtime | live unit; worker |
| procs by state (zombies) | probe-procs, F10 | live unit; worker |
| troupe accounts: only the worker's own readable | probe-troupe-perms, F14 | live unit; worker and frontend |
| ringmaster and troupe state visibility | probe-ls | live unit; worker |
| exclusion behaviour: moxy grit_* absent; a spinclass stash refusal, a force deny | worker tool calls | live unit and harness (claude) |
| close: clean tree, no-force close, no signal to a foreign pid | close, unit tests | offline-capable (unit tests); live for the real close |
| `--unconfined` CLI gate refused with stdin /dev/null | the spawn command | offline-capable |
| spec shape (binds, deny paths, no credential entry) | spec read, unit tests | offline-capable |

### In the gate

Only the offline-capable rows can run in spinclass's own gate without a unit:
- spec shape;
- the CLI gate (the bats test exists);
- close never signalling a foreign or confined PID;
- a tracked `.envrc` left clean.

Those already exist as unit and bats tests; a bats lane would only collect
them under one name. The live rows need shape A of the harness, or a live
round. Once A exists, the conformance aggregator becomes A's assertion step,
and conformance and harness are one check.
