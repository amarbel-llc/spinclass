# Direnv-less confined spawns: pilot brief (environment partition)

Status: step 1 measured, 2026-11-04, spinclass/light-cedar. Follows the
#459 deliberation with the operator (the per-commit hook was one symptom of
the problem this pilot is about). Steps 3 and 4 are the operator's; nothing in
this brief is spawned, deployed or merged by light-cedar on its own.

## The partition, and where it starts today

Confinement (FDR 0035) is a checkpoint clown places at the unit launch: the
filesystem (ProtectHome, binds), the sockets, the egress and the pid
namespace are checked there. The environment is not. `sc spawn` starts the
`[session-entry].spawn-entry` landside, and that entry is
`direnv exec <worktree> clown --clown-attach=spawn ...`: direnv loads the
`.envrc` chain and the repo's devShell into the launcher's environment
before any unit exists, and the unit inherits it. Since the nix store is
readable inside, every PATH entry the devShell contributed resolves inside
too. The devShell is the agent's environment despite confinement.

The airport shape the operator wants: the operator's corridor goes through
direnv (devShell, `.envrc` chain, the full toolbox); the agent's corridor is
parallel and isolated, with a minimal environment the launcher states
explicitly; privileged porters (a principal with the right, FDR 0032) may
cross between the two, for example the operator running `direnv exec <wt>`
against a worker's worktree to debug it, or later a supervising session
holding an attach right. This pilot builds the agent corridor's first
version with no spinclass code change: a different spawn-entry in a trusted
layer. The second lever, the unit starting from a clean environment plus the
spec's `env` field (a clown launch-side change), comes after, with the list
this pilot produces.

## Step 1 (done): what direnv carries across today

`just explore-direnv-env-delta` on light-cedar's worktree, 2026-11-04. The
launcher's environment with direnv has 191 variables against 115 without.
Direnv adds 76 and changes 3 (`PATH`, `SHLVL`, `XDG_DATA_DIRS`):

- The stdenv builder environment, about 55 names: `CC`, `CXX`, `LD`, `AR`,
  `NIX_CFLAGS_COMPILE`, `NIX_LDFLAGS`, `NIX_HARDENING_ENABLE`, `phases`,
  `buildPhase`, `shellHook`, `stdenv`, `out`, `system`, the `deps*` lists,
  `SOURCE_DATE_EPOCH`, `NIX_BUILD_CORES`, `NIX_STORE`, `NIX_CONFIG` and the
  rest of `mkShell`'s derivation attributes. None of it is for an agent.
- nix-direnv and devShell markers: `DIRENV_DIR`, `DIRENV_FILE`,
  `DIRENV_DIFF`, `DIRENV_WATCHES`, `DIRENV_IN_ENVRC`, `IN_NIX_SHELL`,
  `HOST_PATH`, `MANPATH`, `GOTOOLCHAIN`, `GOTOOLDIR`,
  `GOLANGCI_LINT_CACHE`, `BATS_LIB_PATH`. The guarded gate already refuses a
  frontend whose `DIRENV_DIR`/`DIRENV_FILE` point into a worktree.
- Troupe: `TROUPE_IPC`, `TROUPE_XMPP_DERIVE_ROOMS`,
  `TROUPE_XMPP_OPERATOR_JID`, `TROUPE_XMPP_ROOMS`. These come from a layer
  above the worktree's own `.envrc` (its four lines are `source_up`,
  `use flake`, `dotenv .spinclass/env`, `PATH_add <repo>/.git/spinclass/bin`)
  or from the session dotenv; which one is not settled from inside a
  session (the dotenv is spinclass-managed). Step 2 settles it.
- PATH: 36 store `bin` directories (the devShell: a C toolchain, binutils,
  glibc-bin, make, the Go tools, the linters, bats, tommy, just,
  ringmaster, conformist and its two hook commands) plus
  `<repo>/.git/spinclass/bin` (the claude wrapper spinclass installs) and
  `<worktree>/.direnv/bin`.

Expectation for what a confined worker needs, to be tested rather than
assumed: the identity and launch variables spinclass sets itself
(`SPINCLASS_*`, `TMPDIR`/`CLAUDE_CODE_TMPDIR`, `CLOWN_AGENT_SCOPE`,
`CLOWN_PROFILE_DIGEST`, `CLOWN_UNATTENDED`), the troupe variables, a PATH
that resolves `clown`, `ringmaster`, `sc` and `git` (the system profile
has all four), and possibly `<repo>/.git/spinclass/bin`. Nothing from the
first two groups and nothing from the devShell.

## Step 2 (done): where the troupe variables come from

`just explore-direnv-env-delta "$HOME"` and `... "$HOME/eng"`, 2026-11-04:
each level adds only direnv's own markers, `NIX_CONFIG` and `OLDPWD`, no
troupe variable and no PATH entry. The source is the sweatfile hierarchy's
`[direnv.dotenv]` map: `apply.SpinclassEnv` renders it to
`<worktree>/.spinclass/env` at `sc start`, and the generated `.envrc`'s
`dotenv .spinclass/env` line is the only thing that loads it. The spawn
environment spinclass builds for the worker (`workerEnvScoped`: the identity
variables, `[session-entry].env`, the tmp dirs, `CLOWN_AGENT_SCOPE`, the
profile digest) does not carry the dotenv map.

Consequence for the pilot: a direnv-less entry that is just `clown ...`
drops `TROUPE_*` and anything else a trusted layer puts in
`[direnv.dotenv]`. The dotenv is the one legitimate environment input that
is routed through direnv today, and it is a per-session file spinclass
already writes, so the pilot entry loads that file and nothing else. The
long-term shape is for spinclass to put the dotenv map into the spawn
environment itself (and into the spec's `env` field, the second lever), at
which point no entry needs a loader.

## Steps 3 and 4 as recipes

The filesystem half of steps 3 and 4 is outside any worktree, so a session
cannot do it (the policy broker denies the writes); the recipes below do
it from the operator's terminal, run from the spinclass checkout that has
them (`just --justfile <checkout>/justfile <recipe>` works from anywhere):

```sh
just debug-pilot-env-scaffold            # step 3: ~/eng/pilot, the loader layer, the repo, sc validate
just debug-pilot-env-spawn <OPERATOR-SESSION-KEY>   # step 4: direnv exec / sc spawn --confined ...
just explore-live-unit-env               # while the worker is live
just debug-pilot-env-layer bare          # optional control: rewrite the layer, then spawn again
just debug-pilot-env-teardown            # after sc close
```

The prose below is what the recipes do, kept for reading.

## Step 3 (operator): the pilot layer

A throwaway repo under its own parent directory, so that directory's
sweatfile is a trusted layer for it alone and the change touches no other
repo:

```sh
mkdir -p "$HOME/eng/pilot" && cd "$HOME/eng/pilot"
git init --bare sc-pilot-env.git
git init -b master sc-pilot-env && cd sc-pilot-env
printf 'pilot: direnv-less confined spawn\n' > README.md
cat > flake.nix <<'EOF'
{
  outputs = { self }: {
    checks.x86_64-linux.smoke = derivation {
      name = "smoke"; system = "x86_64-linux";
      builder = "/bin/sh"; args = [ "-c" "echo ok > $out" ];
    };
  };
}
EOF
printf '{"nodes":{"root":{}},"root":"root","version":7}\n' > flake.lock
git add -A && git commit -m "init"
git remote add origin "$HOME/eng/pilot/sc-pilot-env.git" && git push -u origin master
```

The flake is the canary's (no inputs, no devShell, a root-only lock), so
the guarded gate on this repo is a known quantity: the #449 canary's step 3
record. No `.envrc` in the repo: nothing in the repo shapes the worker's
environment, and with no devShell there is nothing for direnv to load even
if an entry still ran it, which is why the unit-environment check in step 4
is the real test, not the spawn succeeding. The parent-directory sweatfile
(`$HOME/eng/pilot/.sweatfile.toml`, or whatever name the hierarchy uses on
this host; `sc validate` from inside the repo shows the layers it read)
carries the direnv-less entry. Take the current trusted `spawn-entry` (shown
by `sc validate` or the global sweatfile) and replace its leading
`direnv exec {dir}` with a loader of the session dotenv only (step 2), keeping
everything after `clown` as is. In the shape the tests record the entry,
that is:

```toml
[session-entry]
spawn-entry = [
  "sh", "-c",
  "set -a; [ -f \"$1/.spinclass/env\" ] && . \"$1/.spinclass/env\"; set +a; shift; exec clown \"$@\"",
  "sh", "{dir}", "--clown-attach=spawn", "--", "{prompt}",
]
```

The loader sources `<worktree>/.spinclass/env` (the `[direnv.dotenv]` map,
KEY=value lines spinclass wrote at worktree creation) into the environment
and execs clown; no `source_up`, no `use flake`, no `PATH_add`. The control
entry, worth one spawn first if cheap, is the bare
`["clown", "--clown-attach=spawn", "--", "{prompt}"]`: expected to boot and
hello but to lose chat (no `TROUPE_IPC`), which is the proof that the dotenv
is the needed part and the devShell is not.

`sc validate` must pass and must list the pilot layer. `[confinement]
enabled` must already be on in a trusted layer (it is wherever confined
spawns run today). If the entry on this host carries more (a `--profile`,
provider flags), keep them after `--clown-attach=spawn`; only
`direnv exec {dir}` goes. spinclass inserts `--profile`, `--permission-mode`
and the model flag by scanning the argv for `clown`'s `--` separator, which
the loader form keeps, but this is the first entry whose argv[0] is not
`direnv` or `clown`: if the spawn refuses the entry, that refusal is a
finding (the splice assumes a shape), not a reason to fall back.

## Step 4 (operator): spawn, then measure airside

From a shell with NO worktree direnv loaded, inside the pilot repo:

```sh
cd "$HOME/eng/pilot/sc-pilot-env"
sc spawn --confined --profile canary-spinclass \
  --description "direnv-less confined pilot" \
  --brief 'You are a confined pilot worker for a spinclass environment experiment. Do exactly this: (1) send one chat message to the session key below listing the tools you have by name; (2) read README.md and append the line "pilot: airside" to it with the Edit tool; (3) if a grit_stage and grit_commit tool exist, stage README.md and commit with the message "pilot: airside", otherwise say so; (4) send a second chat message with whether you committed and the HEAD commit if you know it, and the output of running the recipe list tool if you have one (names only). Do not merge, do not run recipes, do not create other files. Operator session key: <OPERATOR-SESSION-KEY>'
```

Run the spawn as `direnv exec / sc spawn ...` so the terminal's own
`.envrc` chain is unloaded too: step 2 showed that chain adds nothing a
worker needs, and the loader entry supplies the session dotenv itself, so
the worker's environment is then exactly the terminal baseline plus what
spinclass and the loader add. That is the environment the second lever
would state in the spec's `env` field.

While the worker is live:

```sh
cd "$HOME/eng/repos/spinclass" && just explore-live-unit-env
```

Expected: one block for the pilot unit; its variable names hold the
spinclass identity set, the clown set and the troupe set, NONE of the stdenv
names (`CC`, `NIX_CFLAGS_COMPILE`, `phases`, `shellHook`, `stdenv`) and no
`IN_NIX_SHELL`/`DIRENV_*`; its PATH has no store `bin` beyond what the
system profile and clown put there. Anything from the devShell appearing
here is a finding (either the entry still loads direnv, or the unit inherits
it from somewhere other than the launcher).

Worker checks: hello within the timeout (the spawn's own result), both chat
messages arrive, the commit exists (`git -C <worktree> log --oneline -2`),
and from the operator's side `direnv exec / sc check --format plain` on the
worktree records the guarded gate point exactly as the #449 canary's step 3
did (`nix flake check --no-update-lock-file` from the built-in default, no
repair, exit 0).

Then `sc close sc-pilot-env/<branch>`.

## Step 5: what the result decides

- The list of variables the worker actually needed (step 4 worked) minus
  what it had (`explore-live-unit-env`) is the spec's future `env` field,
  the second lever: clown starts the unit from a clean environment plus that
  field. That proposal goes to clown with the list attached.
- Whether spinclass needs a `confined-spawn-entry` key or should strip
  `direnv exec` from the entry itself for a confined spawn. The pilot's
  answer tells which: if the only difference between the two entries is the
  prefix, stripping it in the driver is less configuration than a second
  key.
- The porter right: a named FDR 0032 right (working name `attach`) that
  lets a holder run the operator corridor's tools against a worker's
  worktree. The operator has it by being at a terminal; a supervising
  session would hold it on a handle. Not built here; named so the design
  does not forget it.

## Done when

Step 2's attribution is known, step 4's worker helloed, chatted, committed,
and `explore-live-unit-env` showed no devShell and no stdenv names in its
environment. Report to the operator: the step 2 attribution, the step 4
unit block, the worker's tool list, the commit, the `sc check` output, and
any variable the worker turned out to need that the entry did not pass.
Then `rm -rf "$HOME/eng/pilot"` and the pilot layer.
