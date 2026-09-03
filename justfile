default: lint build test verify

# --- lint ---

lint: lint-fmt lint-worktree

# Read-only format + lint gate via the sandboxed checks.formatting
# derivation: formatter drift (Go/Nix/shell/TOML, per ./conformist.nix) plus
# the linters — shellcheck, statix/deadnix, tommy-codegen, and the
# eng-convention linters (eng-versioning, flake-outputs/lock, justfile-*) from
# conformist.lib.presets.eng. `just codemod-fmt` is the corresponding write
# mode. Folded into `just lint` → `just default`, so the pre-merge `just` hook
# enforces it on every merge.
#
# check formatting and the eng-convention lints, read-only
lint-fmt:
    #!/usr/bin/env bash
    set -euo pipefail
    system=$(nix eval --raw --impure --expr 'builtins.currentSystem')
    nix build ".#checks.${system}.formatting" --no-link --print-build-logs

# The impure eng-convention checks (git remotes, sweatfile, agents-md;
# gomod2nix is force-disabled here — see conformist-impure.nix) plus
# golangci-lint (the v2 `standard` set — config in .golangci.yml —
# relocated here from the pure lane: it needs ambient `go` + a writable
# build cache, unavailable in the sandboxed checks.formatting) against the
# working tree, where .git/go are available. Runs conformist from the
# devShell PATH (direnv `use flake`).
#
# golangci-lint's ambient `go` does NOT apply spinclass's goFlakeInputs
# bridge on its own (igloo#62 — mkGoEnv's devshell parity with `nix build`
# doesn't hold in practice, despite gomod2nix(7)'s prior claim; -modfile
# doesn't help either, since golangci-lint's own go/packages probe runs
# with GO111MODULE=off, which cmd/go rejects -modfile under). So this
# recipe temporarily materializes the SAME merged go.mod `nix build` uses
# (buildGoApplication's passthru.mergedGoMod — a plain `replace`, which
# go/packages reads natively) over the tracked go.mod for just the
# conformist run, then restores it — verified working end-to-end against a
# real bridged module (purse-first/libs/dewey/pkgs/mesa, #185).
#
# run the impure eng checks and golangci-lint against the working tree
lint-worktree:
    #!/usr/bin/env bash
    set -euo pipefail
    system=$(nix eval --raw --impure --expr 'builtins.currentSystem')
    cfg=$(nix build --no-link --print-out-paths '.#conformist-impure-config')
    merged=$(nix build --no-link --print-out-paths ".#packages.${system}.default.passthru.mergedGoMod")
    cp go.mod .go.mod.lintbak
    trap 'mv .go.mod.lintbak go.mod' EXIT
    cp "$merged" go.mod && chmod u+w go.mod
    conformist check --config-file "$cfg" --tree-root .

# --- build ---

build: build-nix build-tommy-codegen

# Build the spinclass binary via nix (burns version.env + commit into the
# binary via -ldflags — see mkSpinclass in flake.nix).
#
# build the spinclass binary via nix
build-nix:
    nix build --show-trace

# Regenerate the tommy-generated sweatfile codec (sweatfile_tommy.go) after a
# tommy bump or a Sweatfile struct change. The devshell ships the tommy binary
# from the flake's `tommy` input (the same rev that backs the bridged tommy
# library), so `go generate` (//go:generate tommy generate) finds it on PATH —
# do NOT `go build` tommy from the main module here: module graph pruning
# drops the codegen tool's transitive deps from go.sum (#140). Run after
# `just update-gomod2nix`. (No trailing gofumpt: tommy v0.4.6 gofumpt's its
# generated output internally, version-matched to go.mod via #134, so an
# extra pass is a no-op.)
#
# regenerate the tommy-generated sweatfile codec
build-tommy-codegen:
    nix develop --command go generate ./internal/sweatfile/

# --- test ---

test: test-nix

# Go unit tests plus every bats integration lane via `nix flake check` (the
# [checks] output: spinclass's `go test ./...`, bats-default, bats-race,
# bats-madder, plus the formatting gate — the same set `lint-fmt` targets
# individually).
#
# run the Go unit tests and every bats lane via `nix flake check`
test-nix:
    nix flake check --print-build-logs

# --- verify ---

verify: verify-version-burnin verify-tommy-codegen

# Verify that the nix-built binary has version+commit burnt in via the
# fork's buildGoApplication ldflags (auto-reading version.env —
# eng-versioning(7)), and that the version prefix matches version.env.
# `spinclass version` emits the blessed self-as-row component table
# (eng-versioning(7) "version subcommand output"), so the self version
# is the VERSION column of the first `spinclass-*` row, not raw stdout.
#
# verify version and commit are burnt into the nix-built binary
verify-version-burnin: build
    #!/usr/bin/env bash
    set -euo pipefail
    table="$(./result/bin/spinclass version)"
    echo "spinclass version:"
    echo "$table"
    got="$(awk '$1 ~ /^spinclass-/ { print $2; exit }' <<<"$table")"
    [[ -n "$got" ]] || { echo "no spinclass self-row in version table" >&2; exit 1; }
    [[ "$got" =~ ^[^+]+\+[^+]+$ ]] || { echo "bad shape: $got" >&2; exit 1; }
    [[ "$got" != "dev+unknown" ]]   || { echo "ldflags did not fire" >&2; exit 1; }
    . ./version.env
    prefix="${got%%+*}"
    [[ "$prefix" == "$SPINCLASS_VERSION" ]] || \
        { echo "version prefix '$prefix' != version.env '$SPINCLASS_VERSION'" >&2; exit 1; }
    echo "OK: shape, non-default, prefix match"

# Drift guard (#159): regenerate the codec and fail if it differs from the
# committed file — catches a `tommy` pin bump landed without a matching
# `just build-tommy-codegen`. Lives here (a go-available lane) rather than
# conformist: the [linter.tommy-codegen] check is a deliberate no-op because
# the conformist check lane lacks `go`. The conformist stanza automates
# regen in the repair lane; this enforces it.
#
# fail if the committed tommy codec differs from a fresh regen
verify-tommy-codegen: build-tommy-codegen
    git diff --exit-code -- internal/sweatfile/sweatfile_tommy.go

# --- codemod ---

codemod-fmt: codemod-fmt-tree

# Format the tree in place (repair mode) via `nix fmt`: Go (goimports →
# gofumpt), Nix (nixfmt), shell/bats (shfmt), TOML (tommy fmt). Config is
# Nix-generated from ./conformist.nix + presets.{eng,eng-go}. The read-only
# counterpart is `lint-fmt`.
#
# format the tree in place via `nix fmt`
codemod-fmt-tree:
    nix fmt

# --- clean ---

clean: clean-build

# remove the nix build result symlink
clean-build:
    rm -rf result

# --- maintenance ---

# Regenerate gomod2nix.toml after go.mod/go.sum changes. Deliberately NOT
# wired into `build`: spinclass regenerates this manually after a dependency
# change rather than on every build (unlike e.g. crap/papi's always-fresh
# convention).
#
# regenerate gomod2nix.toml after go.mod/go.sum changes
update-gomod2nix:
    nix develop --command gomod2nix

# [debug] Fast single-package Go test loop. `just test` runs the whole
# `nix flake check`, which is far too slow for red/green TDD iteration and
# only sees git-tracked files; this runs `go test` inside the devshell against
# the working tree. Serves the agent inner dev-loop — `just` is still the
# gate that counts (and is what the pre-merge hook runs).
#
# Pass a -run regex via the `run` parameter, NOT via args: just interpolates
# variadic args as raw text, so an alternation like 'A|B' reaches the shell
# with a live pipe and the second name is executed as a command. `run` is
# shell-quoted here, which is the only place that can be done correctly.
#
#     just debug-go-test ./internal/perms/ 'TestA|TestB' -v
#
# `pkg` is a single package pattern. Passing several relies on go's argument
# ordering and silently tests only some of them — use ./... or one at a time.
#
# run go test for one package in the devshell (fast inner loop)
[group('debug')]
debug-go-test pkg='./...' run='' *args='':
    nix develop --command go test {{ if run == '' { '' } else { '-run ' + quote(run) } }} {{ args }} {{ pkg }}

# [explore] Inspect nix-store --gc --print-roots output for entries pointing
# into the spinclass repo. Used to investigate issue #67 — what does
# print-roots show before vs after worktree removal?
#
# list nix gc roots pointing into the spinclass repo
[group('explore')]
explore-gcroots-spinclass:
    #!/usr/bin/env bash
    set -uo pipefail
    nix-store --gc --print-roots 2>/dev/null | grep -F 'spinclass' || echo "(no spinclass-rooted entries found)"

# [explore] Estimate the system-prompt cost of injecting sibling repos' just
# recipe names + doc lines (spinclass#287 / #286). For every
# code.linenisgreat.com repo under `repos` with a justfile, dump the recipe
# model, render the exact text a fragment would carry ("name  doc"), and report
# per-repo recipe count / chars / estimated tokens (chars÷4 — a rough
# heuristic, not a tokenizer) for all public recipes and for the debug/explore
# group subset, plus fleet totals. Repos whose justfile fails to dump are
# listed as skipped.
#
# estimate the prompt-token cost of injecting sibling repos' just recipe names + docs
[group('explore')]
explore-recipe-prompt-cost repos=(home_directory() / 'eng/repos'):
    #!/usr/bin/env bash
    set -uo pipefail
    all_q='.recipes | to_entries[] | .value | select(.private|not) | "\(.name)  \(.doc // "")"'
    dbg_q='.recipes | to_entries[] | .value | select(.private|not) | select((.attributes // []) | map(if type=="object" then (.group // "") else "" end) | any(. == "debug" or . == "explore")) | "\(.name)  \(.doc // "")"'
    printf '%-16s %8s %8s %8s | %8s %8s %8s\n' repo recipes chars '~tokens' 'dbg/expl' chars '~tokens'
    tr=0; tc=0; td=0; tdc=0; skipped=""
    for d in "{{ repos }}"/*/; do
      d=${d%/}; name=$(basename "$d")
      [ -f "$d/justfile" ] || continue
      url=$(git -C "$d" config --get remote.origin.url 2>/dev/null || true)
      case "$url" in *code.linenisgreat.com*) ;; *) continue ;; esac
      if ! dump=$(just --justfile "$d/justfile" --working-directory "$d" --dump --dump-format json 2>/dev/null); then
        skipped="$skipped $name"; continue
      fi
      all=$(printf '%s' "$dump" | jq -r "$all_q"); dbg=$(printf '%s' "$dump" | jq -r "$dbg_q")
      n=$(printf '%s\n' "$all" | grep -c . || true); c=$(printf '%s\n' "$all" | wc -c)
      dn=$(printf '%s\n' "$dbg" | grep -c . || true); dc=$(printf '%s\n' "$dbg" | wc -c)
      [ "$dn" -eq 0 ] && dc=0
      printf '%-16s %8d %8d %8d | %8d %8d %8d\n' "$name" "$n" "$c" $((c/4)) "$dn" "$dc" $((dc/4))
      tr=$((tr+n)); tc=$((tc+c)); td=$((td+dn)); tdc=$((tdc+dc))
    done
    printf '%-16s %8d %8d %8d | %8d %8d %8d\n' TOTAL "$tr" "$tc" $((tc/4)) "$td" "$tdc" $((tdc/4))
    [ -n "$skipped" ] && echo "skipped (dump failed):$skipped"
    exit 0

# [explore] Inventory the sibling repos a root-level sweatfile entry would reach:
# for each dir under `repos`, its configured origin host (what [auth] would
# derive SPINCLASS_FORGE_HOST from) and its conformist flake-input rev (the
# git-remotes(#8) fix, 7baec98, must be pinned before [auth] turns on there).
# Input to the FDR 0028 fleet-placement design.
#
# list each sibling repo's origin host and pinned conformist rev
[group('explore')]
explore-repo-origins repos=(home_directory() / 'eng/repos'):
    #!/usr/bin/env bash
    set -uo pipefail
    printf '%-16s %-28s %s\n' repo origin-host conformist-rev
    for d in "{{ repos }}"/*/; do
      d=${d%/}; name=$(basename "$d")
      [ -d "$d/.git" ] || [ -f "$d/.git" ] || continue
      url=$(git -C "$d" config --get remote.origin.url 2>/dev/null || echo '(no origin)')
      host=${url#*@}; host=${host#*://}; host=${host%%[:/]*}
      rev='-'
      if [ -f "$d/flake.lock" ]; then
        rev=$(jq -r '.nodes.conformist.locked.rev // "-" | .[0:7]' "$d/flake.lock" 2>/dev/null || echo '?')
      fi
      printf '%-16s %-28s %s\n' "$name" "$host" "$rev"
    done

# [debug] Run the LIVE profile's `sc` (not the devshell build) against this
# repo from the current worktree — the escape hatch for driving fresh sessions
# on a newly switched binary from an agent session that has no shell (e.g. the
# FDR 0028 promotion round-trip: start/merge/close sessions whose creation
# funnel mints a credential). Prints which binary ran first. Pass
# `SSH_AUTH_SOCK=` in env to prove a step is agent-free.
#
# run the live profile's sc with the given arguments
[group('debug')]
debug-sc *args:
    #!/usr/bin/env bash
    set -uo pipefail
    bin="$HOME/.nix-profile/bin/sc"
    [ -x "$bin" ] || bin=$(command -v sc)
    echo "sc = $bin ($("$bin" --version 2>/dev/null | head -n1))" >&2
    exec "$bin" {{ args }}

# [debug] Prove a session worktree's per-session credential (FDR 0028) carries
# a push WITHOUT the ssh-agent: with SSH_AUTH_SOCK unset, push the worktree's
# HEAD to a scratch remote branch (the worktree-scoped insteadOf + credential
# helper must supply HTTPS + token, or the push fails), then delete that
# remote branch again. Read-only for the default branch; the scratch branch is
# gone by the end.
#
# push a session worktree's HEAD to a scratch remote branch with no ssh-agent, then delete it
[group('debug')]
debug-agentless-push worktree scratch='fdr-0028-probe':
    #!/usr/bin/env bash
    set -uo pipefail
    echo "configured origin: $(git -C "{{ worktree }}" config --get remote.origin.url)" >&2
    echo "effective origin:  $(git -C "{{ worktree }}" remote get-url origin)" >&2
    env -u SSH_AUTH_SOCK git -C "{{ worktree }}" push origin "HEAD:refs/heads/{{ scratch }}" || { echo "PUSH FAILED without agent" >&2; exit 1; }
    env -u SSH_AUTH_SOCK git -C "{{ worktree }}" push origin --delete "{{ scratch }}"

# [debug] Run the live profile's `papi` — the [auth] issuer (papi#73) — to
# inspect or clean up forge tokens around a round-trip (e.g. `forge token list`).
#
# run the live profile's papi with the given arguments
[group('debug')]
debug-papi *args:
    #!/usr/bin/env bash
    set -uo pipefail
    bin="$HOME/.nix-profile/bin/papi"
    [ -x "$bin" ] || bin=$(command -v papi)
    echo "papi = $bin ($("$bin" --version 2>/dev/null | head -n1))" >&2
    exec "$bin" {{ args }}

# [explore] List ALL dangling auto-root links on the system (regardless of
# what they used to point at). These are zero-byte symlinks with broken
# targets — leftovers from removed checkouts/worktrees.
#
# list all dangling auto gc-root links on the system
[group('explore')]
explore-gcroots-auto-dangling:
    #!/usr/bin/env bash
    set -uo pipefail
    count=0
    for link in /nix/var/nix/gcroots/auto/*; do
      if [[ ! -e "$link" ]]; then
        target=$(readlink "$link" 2>/dev/null) || continue
        printf '%s -> %s\n' "$link" "$target"
        count=$((count + 1))
      fi
    done
    echo
    echo "Total dangling auto-root links: $count"

# [explore] What does `nix-store --gc --print-roots` say about a dangling
# auto-root? Set up a temp file, register it as an indirect root with
# nix-store --add-root, remove the file, and check what print-roots shows.
# Critically, also checks whether print-roots itself silently GCs dangling
# indirect roots — a known nix side-effect that bears on issue #67.
#
# probe how print-roots treats a dangling indirect auto-root
[group('explore')]
explore-print-roots-dangling:
    #!/usr/bin/env bash
    set -uo pipefail
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    store_path=$(nix-store -q --requisites $(which nix-store) | head -1)
    echo "Using store_path=$store_path"
    fake_root="$tmp/fake-root"
    nix-store --add-root "$fake_root" --indirect -r "$store_path" >/dev/null
    auto_link=""
    for link in /nix/var/nix/gcroots/auto/*; do
      if [[ "$(readlink "$link" 2>/dev/null)" == "$fake_root" ]]; then
        auto_link="$link"
        break
      fi
    done
    echo "auto_link=$auto_link"
    echo
    echo "[1] BEFORE removal — print-roots:"
    nix-store --gc --print-roots 2>/dev/null | grep -F "$tmp" || echo "  (not found)"
    echo "[2] BEFORE removal — auto/ link state:"
    if [[ -n "$auto_link" ]] && [[ -L "$auto_link" ]]; then
      if [[ -e "$auto_link" ]]; then echo "  LIVE: $auto_link -> $(readlink "$auto_link")"; fi
    fi
    rm -f "$fake_root"
    echo
    echo "[3] AFTER removal, BEFORE re-running print-roots — auto/ link state:"
    if [[ -L "$auto_link" ]]; then
      if [[ -e "$auto_link" ]]; then echo "  LIVE: $auto_link -> $(readlink "$auto_link")"
      else echo "  DANGLING: $auto_link -> $(readlink "$auto_link")"
      fi
    else
      echo "  GONE (auto/ link no longer exists)"
    fi
    echo "[4] AFTER removal — print-roots:"
    nix-store --gc --print-roots 2>/dev/null | grep -F "$tmp" || echo "  (not found)"
    echo "[5] AFTER removal, AFTER print-roots — auto/ link state:"
    if [[ -L "$auto_link" ]]; then
      if [[ -e "$auto_link" ]]; then echo "  LIVE: $auto_link -> $(readlink "$auto_link")"
      else echo "  DANGLING: $auto_link -> $(readlink "$auto_link")"
      fi
    else
      echo "  GONE (auto/ link no longer exists)"
    fi

# [explore] Inspect /nix/var/nix/gcroots/auto/ for symlinks whose target
# resolves into the spinclass repo. Lists each link, its readlink target,
# and whether the target still exists. Crucial input for #67's
# "are dangling auto-roots visible to print-roots?" question.
#
# list auto gc-root symlinks resolving into the spinclass repo
[group('explore')]
explore-gcroots-auto-spinclass:
    #!/usr/bin/env bash
    set -uo pipefail
    count=0
    dangling=0
    for link in /nix/var/nix/gcroots/auto/*; do
      target=$(readlink "$link" 2>/dev/null) || continue
      case "$target" in
        */spinclass*)
          count=$((count + 1))
          if [[ -e "$link" ]]; then
            status="LIVE"
          else
            status="DANGLING"
            dangling=$((dangling + 1))
          fi
          printf '%-9s %s -> %s\n' "$status" "$link" "$target"
          ;;
      esac
    done
    echo
    echo "Total auto-roots into spinclass: $count (dangling: $dangling)"

# [explore] Dodder-over-madder store reuse, CWD-local `.default` variant.
# Mirrors what spinclass does today (pinned `madder init -encryption none
# .default`) then asks dodder to adopt that store via -blob_store-id. This is
# the path the user CHOSE ("dodder over the madder store") and the one prior
# research saw fail with `blob store not found: ".default"`. Serves the
# dodder-integration FDR's store-model verification (issue TBD). Binaries
# resolve from PATH; override with DODDER_BIN / MADDER_BIN.
#
# probe dodder reuse of a CWD-local `.default` madder store
[group('explore')]
explore-dodder-reuse-cwd:
    #!/usr/bin/env bash
    set -uo pipefail
    dodder_bin="${DODDER_BIN:-dodder}"
    madder_bin="${MADDER_BIN:-madder}"
    scratch=$(mktemp -d)
    trap 'rm -rf "$scratch"' EXIT
    export MADDER_CEILING_DIRECTORIES="$scratch"
    export DODDER_CEILING_DIRECTORIES="$scratch"
    cd "$scratch"
    echo "## scratch:  $scratch"
    echo "## madder:   $(command -v "$madder_bin") :: $("$madder_bin" version 2>&1 | head -1)"
    echo "## dodder:   $(command -v "$dodder_bin") :: $("$dodder_bin" version 2>&1 | head -1)"
    echo
    echo "\$ madder init -encryption none .default"
    "$madder_bin" init -encryption none .default; echo "  -> exit $?"
    echo "\$ madder list"
    "$madder_bin" list 2>&1
    echo
    echo "\$ dodder init -encryption none -repo_id . -blob_store-id .default reuse-test"
    "$dodder_bin" init -encryption none -repo_id . -blob_store-id .default reuse-test 2>&1; rc=$?
    echo "  -> exit $rc"
    echo
    echo "## on-disk layout:"
    find .madder .dodder -maxdepth 5 2>/dev/null | sort | sed 's/^/    /'
    echo
    if [[ $rc -eq 0 ]]; then
      echo "VERDICT: CWD-local .default reuse WORKS with this binary pair"
    else
      echo "VERDICT: CWD-local .default reuse FAILS with this binary pair"
    fi

# [explore] Dodder-over-madder store reuse, XDG-named-store variant — the
# pivot path. Scopes XDG_*_HOME into the scratch dir, creates a plain-named
# madder store there, then points dodder at it by name. Prior research found
# THIS path works while the .default CWD path does not. Contrast with
# explore-dodder-reuse-cwd: if this succeeds and that fails on the SAME binary
# pair, the .default failure is dodder genesis discovery, not version skew.
#
# probe dodder reuse of an XDG-named madder store
[group('explore')]
explore-dodder-reuse-xdg store="shared":
    #!/usr/bin/env bash
    set -uo pipefail
    dodder_bin="${DODDER_BIN:-dodder}"
    madder_bin="${MADDER_BIN:-madder}"
    store="{{store}}"
    scratch=$(mktemp -d)
    trap 'rm -rf "$scratch"' EXIT
    export MADDER_CEILING_DIRECTORIES="$scratch"
    export DODDER_CEILING_DIRECTORIES="$scratch"
    export XDG_DATA_HOME="$scratch/xdg/data"
    export XDG_CONFIG_HOME="$scratch/xdg/config"
    export XDG_STATE_HOME="$scratch/xdg/state"
    export XDG_CACHE_HOME="$scratch/xdg/cache"
    mkdir -p "$XDG_DATA_HOME" "$XDG_CONFIG_HOME" "$XDG_STATE_HOME" "$XDG_CACHE_HOME"
    cd "$scratch"
    echo "## scratch:  $scratch"
    echo "## XDG_DATA_HOME: $XDG_DATA_HOME"
    echo "## madder:   $(command -v "$madder_bin") :: $("$madder_bin" version 2>&1 | head -1)"
    echo "## dodder:   $(command -v "$dodder_bin") :: $("$dodder_bin" version 2>&1 | head -1)"
    echo
    echo "\$ madder init -encryption none $store"
    "$madder_bin" init -encryption none "$store" 2>&1; echo "  -> exit $?"
    echo "\$ madder list"
    "$madder_bin" list 2>&1
    echo
    echo "\$ dodder init -encryption none -repo_id . -blob_store-id $store reuse-test"
    "$dodder_bin" init -encryption none -repo_id . -blob_store-id "$store" reuse-test 2>&1; rc=$?
    echo "  -> exit $rc"
    echo
    echo "## on-disk layout (xdg data + cwd repo):"
    find "$XDG_DATA_HOME" .dodder -maxdepth 6 2>/dev/null | sort | sed 's/^/    /'
    echo
    if [[ $rc -eq 0 ]]; then
      echo "VERDICT: XDG-named-store reuse WORKS with this binary pair"
    else
      echo "VERDICT: XDG-named-store reuse FAILS with this binary pair"
    fi

# [explore] Baseline: what does a plain `dodder init` (no -blob_store-id)
# create on its own? Shows the .dodder repo tree AND the .madder default store
# dodder's EMBEDDED madder writes, plus the store-id that embedded madder
# assigns. Reference point for the two reuse recipes above — confirms dodder's
# own madder uses a `.default`-style CWD store, which is what makes the
# reuse-vs-collision question sharp.
#
# show what a plain `dodder init` creates on its own
[group('explore')]
explore-dodder-init-plain:
    #!/usr/bin/env bash
    set -uo pipefail
    dodder_bin="${DODDER_BIN:-dodder}"
    madder_bin="${MADDER_BIN:-madder}"
    scratch=$(mktemp -d)
    trap 'rm -rf "$scratch"' EXIT
    export MADDER_CEILING_DIRECTORIES="$scratch"
    export DODDER_CEILING_DIRECTORIES="$scratch"
    cd "$scratch"
    echo "## scratch:  $scratch"
    echo "## dodder:   $(command -v "$dodder_bin") :: $("$dodder_bin" version 2>&1 | head -1)"
    echo
    echo "\$ dodder init -encryption none -repo_id . plain-test"
    "$dodder_bin" init -encryption none -repo_id . plain-test 2>&1; echo "  -> exit $?"
    echo
    echo "\$ madder list  (what the pinned madder sees in this dir)"
    "$madder_bin" list 2>&1
    echo
    echo "## on-disk layout:"
    find .madder .dodder -maxdepth 5 2>/dev/null | sort | sed 's/^/    /'

# [explore] Build the dodder-pinned consumer example and run its e2e
# harness: a real `sc start` that inits a per-worktree dodder repo over the
# madder store, signs with the pivy key, and wires the dodder MCP server.
# Requires pivy-agent UNLOCKED. See examples/dodder-consumer/README.md.
#
# run the dodder-consumer example's end-to-end harness
[group('explore')]
explore-dodder-e2e:
    examples/dodder-consumer/e2e.sh

# [explore] Build the clown circus from the dodder-consumer example —
# clown's mkJuggler (renamed from mkCircus, clown#183) bundling the
# dodder-pinned spinclass + dodder + madder clown plugins (modeled after
# ~/eng/lib/circus.nix). Composition smoke test; building proves the
# plugins resolve. See examples/dodder-consumer.
#
# build the clown circus from the dodder-consumer example
[group('explore')]
explore-dodder-circus:
    cd examples/dodder-consumer && nix build .#circus --print-build-logs

# [debug] Build just the bats-default lane, without the full `test-nix` (nix
# flake check) run — already fully covered by `test-nix`/`checks.bats-default`;
# this is a drill-down for iterating on zz-tests_bats without waiting for the
# unit-test checkPhase or the other bats lanes (mirrors tommy's
# debug-bats-nix-tag).
#
# build just the bats-default lane
[group('debug')]
debug-bats-default:
    nix build .#bats-default --no-link --print-build-logs

# [debug] Build just the race-instrumented bats lane. Slower than
# bats-default (race-detector overhead); already covered by `test-nix` —
# deliberately NOT wired into the `test` aggregate as a first-class step so
# `just`/`just test` stays fast, matching this repo's other opt-in slow lanes.
#
# build just the race-instrumented bats lane
[group('debug')]
debug-bats-race:
    nix build .#bats-race --no-link --print-build-logs

# [debug] Build just the madder-pinned bats lane: runs zz-tests_bats against a
# spinclass built with a madder pin so the tap-ndjson tests in hooks.bats run
# instead of skipping (#85). Already covered by `test-nix` via the
# bats-madder flake check; this is the single-lane drill-down.
#
# build just the madder-pinned bats lane
[group('debug')]
debug-bats-madder:
    nix build .#bats-madder --no-link --print-build-logs

# [debug] Pipe a synthetic PreToolUse payload for merge-this-session through
# the installed plugin handler, then print exit code, stdout, and stderr.
#
# pipe a synthetic PreToolUse payload through the installed plugin handler
[group('debug')]
debug-hook-pretooluse:
    #!/usr/bin/env bash
    set -uo pipefail
    handler="/nix/store/wchfwfh5vb3s3a4dwb6qj9axcrmiza2g-spinclass-0.1.6/share/purse-first/spinclass/hooks/handler"
    payload='{"hook_event_name":"PreToolUse","session_id":"synth-test","tool_name":"mcp__plugin_spinclass_spinclass__merge-this-session","tool_input":{},"cwd":"'"$(pwd)"'"}'
    echo "handler: $handler"
    echo "payload: $payload"
    out=$(mktemp); err=$(mktemp)
    printf '%s' "$payload" | "$handler" >"$out" 2>"$err"
    rc=$?
    echo "exit: $rc"
    echo "--- stdout ---"; cat "$out"
    echo "--- stderr ---"; cat "$err"
    rm -f "$out" "$err"

# [debug] Map every live process that has a SPINCLASS_SESSION_ID to its
# CLOWN_SESSION_ID vs SPINCLASS_SESSION_ID. Confirms the spawn env-leak
# (spinclass#169): a spawned worker shows CLOWN_SESSION_ID = the DRIVER's
# key while SPINCLASS_SESSION_ID = the worker's key (they should match).
#
# Exercise #250's base-branch gate against the INSTALLED binary in a throwaway
# repo, rather than the nix-built one the bats lane uses. The distinction is the
# point: a merge validates code the merging process is not itself running, so
# "the tests pass" and "the thing on your PATH does this" are separate claims.
#
# Sets up the two defects #250 fixes at once — a checkout parked on an unrelated
# branch AND behind its origin — so a session cut from HEAD would miss the
# upstream commit twice over. Everything lives under .tmp/ and is removed after.
#
# verify #250's freshened base end-to-end against the installed sc
[group('debug')]
debug-stale-base:
    #!/usr/bin/env bash
    set -uo pipefail
    bin="${SPINCLASS_BIN:-spinclass}"
    root="$(mktemp -d "$PWD/.tmp/stale-base-XXXXXX")"
    trap 'rm -rf "$root"' EXIT

    git init -q --bare --initial-branch=master "$root/upstream.git"
    git init -q --initial-branch=master "$root/seed"
    git -C "$root/seed" commit -q --allow-empty -m "initial"
    git -C "$root/seed" remote add origin "$root/upstream.git"
    git -C "$root/seed" push -q -u origin master

    git clone -q "$root/upstream.git" "$root/checkout"

    # Another session lands work upstream.
    git clone -q "$root/upstream.git" "$root/other"
    git -C "$root/other" commit -q --allow-empty -m "upstream work"
    git -C "$root/other" push -q origin master
    tip=$(git -C "$root/other" rev-parse HEAD)

    # The operator leaves their checkout somewhere else entirely.
    git -C "$root/checkout" checkout -q -b unrelated
    git -C "$root/checkout" commit -q --allow-empty -m "work nobody asked for"
    unrelated=$(git -C "$root/checkout" rev-parse HEAD)

    echo "upstream tip:      $tip"
    echo "checkout HEAD:     $unrelated (on branch 'unrelated', behind origin)"
    echo
    ( cd "$root/checkout" && "$bin" --format tap start --no-attach )
    echo

    wt=$(ls -d "$root/checkout/.worktrees"/*/ 2>/dev/null | head -1)
    [ -n "$wt" ] || { echo "FAIL: no worktree created"; exit 1; }

    if git -C "$wt" merge-base --is-ancestor "$tip" HEAD; then
      echo "PASS: session contains the upstream commit"
    else
      echo "FAIL: session is missing the upstream commit — it was cut from a stale base"
      exit 1
    fi
    if git -C "$wt" merge-base --is-ancestor "$unrelated" HEAD; then
      echo "FAIL: session inherited the 'unrelated' branch — it was cut from HEAD"
      exit 1
    else
      echo "PASS: session did not inherit the checkout's parked branch"
    fi
    if [ "$(git -C "$root/checkout" rev-parse refs/heads/master)" = "$tip" ]; then
      echo "PASS: local master was fast-forwarded, not merely read"
    else
      echo "FAIL: local master did not move"
      exit 1
    fi
    if [ "$(git -C "$root/checkout" branch --show-current)" = "unrelated" ]; then
      echo "PASS: the checkout stayed where the operator left it"
    else
      echo "FAIL: the checkout was moved off 'unrelated'"
      exit 1
    fi

# Inspect what a spinclass async job looks like from ringmaster's side rather
# than spinclass's own job.json — the two are different surfaces and #251 turns
# on whether the ringmaster one is good enough to retire ours. Checks both
# halves of #251 piece 2 at once: a populated spool (`status --tail` shows live
# hook output instead of the `spool_bytes: 0` the issue reports) and a terminal
# record carrying its result by reference.
#
# Note plain `ringmaster read` renders attachments as a count, so the URIs only
# appear under --json; the blob fetch below is what proves the reference
# actually resolves rather than merely being recorded.
#
# inspect a ringmaster job: spool tail, journal records, and any attached blobs
[group('debug')]
debug-ringmaster-job job tail="20":
    #!/usr/bin/env bash
    set -uo pipefail
    echo "=== ringmaster status (spool tail {{ tail }}) ==="
    ringmaster status {{ quote(job) }} --tail {{ tail }} || echo "(status failed)"
    echo
    echo "=== journal records (--json) ==="
    records=$(ringmaster read --job {{ quote(job) }} --json) || { echo "(read failed)"; exit 1; }
    printf '%s\n' "$records" | jq . 2>/dev/null || printf '%s\n' "$records"
    echo
    echo "=== attached resources ==="
    # Scraped rather than jq-pathed so this keeps working if the record shape
    # shifts — the point is whether a URI is there and resolves, not its key.
    uris=$(printf '%s\n' "$records" | grep -oE 'madder://blobs/[A-Za-z0-9-]+' | sort -u)
    if [ -z "$uris" ]; then
      echo "(none attached)"
      exit 0
    fi
    for uri in $uris; do
      echo "--- $uri ---"
      madder cat "${uri#madder://blobs/}" || echo "(blob fetch FAILED — the wake recorded a reference that does not resolve)"
      echo
    done

# map live processes' SPINCLASS_SESSION_ID against their CLOWN_SESSION_ID
[group('debug')]
debug-session-env-map:
    #!/usr/bin/env bash
    set -uo pipefail
    for d in /proc/[0-9]*; do
      pid=${d#/proc/}
      env=$(tr '\0' '\n' < "$d/environ" 2>/dev/null) || continue
      sp=$(printf '%s\n' "$env" | grep -E '^SPINCLASS_SESSION_ID=') || continue
      cl=$(printf '%s\n' "$env" | grep -E '^CLOWN_SESSION_ID=' || echo 'CLOWN_SESSION_ID=(unset)')
      cmd=$(tr '\0' ' ' < "$d/cmdline" 2>/dev/null | cut -c1-50)
      flag=""
      [ "${sp#SPINCLASS_SESSION_ID=}" != "${cl#CLOWN_SESSION_ID=}" ] && flag="  <-- DIVERGENT"
      echo "pid $pid [$cmd]$flag"
      echo "   $sp"
      echo "   $cl"
    done

# [debug] Pipe a cold `prompts/get` (no `initialize`) into `spinclass serve` and
# print the dynamic system-prompt fragment it returns, extracted from the
# JSON-RPC result. Serves the internal/sysprompt dev-loop (spinclass#187): edit a
# template, re-run, eyeball the rendered markdown. With mode=worktree, sets the
# SPINCLASS_* identity env so the worktree variant renders; default resolves
# whatever the cwd maps to (a git checkout -> the main-checkout variant).
#
# print the dynamic system-prompt fragment `spinclass serve` returns
[group('debug')]
debug-prompt-fragment mode="":
    #!/usr/bin/env bash
    set -uo pipefail
    req='{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"system-prompt-append"}}'
    if [ "{{ mode }}" = "worktree" ]; then
      export SPINCLASS_WORKTREE="$PWD" SPINCLASS_SESSION_ID="demo/branch" SPINCLASS_BRANCH="branch"
    fi
    printf '%s\n' "$req" \
      | go run ./cmd/spinclass serve 2>/dev/null \
      | jq -r 'select(.id == 1) | .result.messages[0].content.text'

# [debug] Render an `sc` subcommand through a REAL PTY and dump the screen, so
# TTY-only output — the pretty `sc list` table's colored status dots, 🤡 clown
# badges, and description wrapping — can be eyeballed the way color-stripped
# `go test` logs and piped stdout cannot. Builds a throwaway binary, resizes a
# transient detached `posh` session to <cols> columns (so wrapping can be tested
# at any width), runs `sc <args>` in it, prints the rendered scrollback, then
# tears the session down. Renders your LIVE sessions. Swap the `history` line for
# `posh history "$sess" --vt` to inspect colors/attributes as an escape stream.
# Serves the cmd/spinclass/list_view rendering dev-loop. Requires `posh` on PATH.
#
# render an `sc` subcommand through a real PTY and dump the screen
[group('debug')]
debug-render-tty cols="100" *args="list":
    #!/usr/bin/env bash
    set -uo pipefail
    command -v posh >/dev/null || { echo "debug-render-tty needs 'posh' on PATH"; exit 1; }
    bin="$(mktemp -d)/sc"
    go build -o "$bin" ./cmd/spinclass || exit 1
    sess="sc-render-$$"
    trap 'posh kill "$sess" >/dev/null 2>&1' EXIT
    # posh `run` space-joins argv and feeds it to the session shell, so a bare
    # `;` argv becomes a real command separator: resize the PTY, then run sc.
    posh run "$sess" -- stty cols {{ cols }} rows 50 ';' "$bin" {{ args }}
    sleep 2
    posh history "$sess"

# [debug] Scaffold a throwaway repo wired (via direnv PATH_add) to a
# devshell-built spinclass binary, then drop into $SHELL inside it — a
# real-worktree-free sandbox for manually exercising `sc start`/etc. against
# uncommitted spinclass changes.
#
# scaffold a throwaway repo on a devshell-built binary and drop into $SHELL
[group('debug')]
debug-dev-repo:
    #!/usr/bin/env bash
    set -euo pipefail
    build_dir="$(pwd)/build"
    mkdir -p "$build_dir"
    nix develop --command go build -o "$build_dir/spinclass" ./cmd/spinclass
    dir=$(mktemp -d)
    trap 'rm -rf "$dir"' EXIT
    git -C "$dir" init -b main
    git -C "$dir" -c commit.gpgsign=false commit --allow-empty -m "initial commit"
    printf 'PATH_add "%s"\n' "$build_dir" > "$dir/.envrc"
    direnv allow "$dir"
    cd "$dir"
    "$SHELL"

# [debug] Faithful end-to-end #196 repro against the INSTALLED sc binary, in a
# throwaway repo so nothing real is touched. Mirrors the reporter: a global
# sweatfile contributes an inherited dotenv key via the bare [direnv] form; the
# repo sweatfile adds keys via the scalar-before-subtable form ([direnv] with
# envrc, then [direnv.dotenv]). `sc start --no-attach` reads the repo sweatfile
# via LoadHierarchy and writes .spinclass/env — so a dropped key is visible.
# Delete once #196 is closed.
#
# reproduce issue #196 end-to-end against the installed sc binary
[group('debug')]
debug-issue196-scratch:
    #!/usr/bin/env bash
    set -uo pipefail
    scratch=$(mktemp -d)
    trap 'rm -rf "$scratch"' EXIT
    export HOME="$scratch"
    export XDG_STATE_HOME="$scratch/.local/state"
    export XDG_CONFIG_HOME="$scratch/.config"
    # Inherited parent key via the bare [direnv] form (known-good shape).
    mkdir -p "$scratch/.config/spinclass"
    cat > "$scratch/.config/spinclass/sweatfile" <<'EOF'
    [direnv]

    [direnv.dotenv]
    INHERITED_KEY = "/parent"
    EOF
    # Repo with the scalar-before-subtable form (the failing shape).
    repo="$scratch/repo"
    mkdir -p "$repo"
    git -C "$repo" init -q -b main
    git -C "$repo" config user.email "test@test"
    git -C "$repo" config user.name "test"
    git -C "$repo" config commit.gpgsign false
    git -C "$repo" commit -q --allow-empty -m initial
    cat > "$repo/sweatfile" <<'EOF'
    [direnv]
    envrc = ["source_up"]

    [direnv.dotenv]
    INHERITED_KEY = "/repo"
    PROBE_KEY = "/probe"
    EOF
    cd "$repo"
    sc start --no-attach "issue196 repro" 2>&1 | sed 's/^/  /'
    wt=$(find "$repo/.worktrees" -mindepth 1 -maxdepth 1 -type d | head -1)
    env_file="$wt/.spinclass/env"
    echo "--- $env_file ---"
    if [[ -f "$env_file" ]]; then cat "$env_file"; else echo "(missing)"; ls -la "$wt" 2>&1; fi
    echo "--- expected: INHERITED_KEY + PROBE_KEY both present ---"

# [debug] Run the issue #196 regression test (scalar-before-subtable decode
# drop) against the BRIDGED tommy via the devshell go — the same tommy rev the
# nix-built binary links, unlike a raw `go test` which resolves go.mod. Delete
# once #196 is closed.
#
# run the issue #196 regression test against the bridged tommy
[group('debug')]
debug-issue196:
    nix develop --command go test -run 'TestScalarBeforeSubtable|TestScalarBeforeSubtableWithPrecedingTable|TestScalarBeforeSubtableHierarchy|TestStandaloneDottedHeadersConsumed' -v ./internal/sweatfile/

# Tag a spinclass release. The "v" prefix is added for you, so pass
# the semver without it. Usage: just tag 0.1.0 "feat: initial release"
#
# sign and push a spinclass release tag
tag version $message:
    #!/usr/bin/env bash
    set -euo pipefail
    tag="v{{version}}"
    prev=$(git tag --sort=-v:refname -l "v*" | head -1)
    if [[ -n "$prev" ]]; then
      gum log --level info "Previous: $prev"
      git log --oneline "$prev"..HEAD
    fi
    git tag -s -m "$message" "$tag"
    gum log --level info "Created tag: $tag"
    git push origin "$tag"
    gum log --level info "Pushed $tag"
    git tag -v "$tag"

# Sed-rewrite SPINCLASS_VERSION in version.env to the given semver
# (eng-versioning(7) single source of truth) — the fork's buildGoApplication
# auto-reads it and burns it into the binary at build time via -ldflags. No-op
# if already at the target version. Usage: just bump-version 0.1.1
#
# rewrite SPINCLASS_VERSION in version.env to the given semver
bump-version new_version:
    #!/usr/bin/env bash
    set -euo pipefail
    . ./version.env
    if [[ "${SPINCLASS_VERSION:-}" == "{{new_version}}" ]]; then
      gum log --level info "already at {{new_version}}"
      exit 0
    fi
    sed -E -i "s/^(export SPINCLASS_VERSION)=.*/\\1={{new_version}}/" version.env
    gum log --level info "bumped SPINCLASS_VERSION: ${SPINCLASS_VERSION:-<unset>} → {{new_version}}"

# Cut a release: must be run on master. Bumps SPINCLASS_VERSION in
# version.env, commits the bump with a changelog-style message built
# from commits since the last v* tag, pushes master, then signs and
# pushes the v{{version}} tag. The "v" prefix is added for you, so
# pass the semver without it. Usage: just release 0.1.1
#
# Use `just tag <version> <message>` directly if you want to control
# the commit message yourself without bumping.
#
# cut a release: bump version.env, commit, push master, then sign and push the tag
release version:
    #!/usr/bin/env bash
    set -euo pipefail
    current_branch=$(git rev-parse --abbrev-ref HEAD)
    if [[ "$current_branch" != "master" ]]; then
      gum log --level error "just release must be run on master (currently on $current_branch)"
      exit 1
    fi
    prev=$(git tag --sort=-v:refname -l "v*" | head -1)
    header="release v{{version}}"
    if [[ -n "$prev" ]]; then
      summary=$(git log --format='- %s' "$prev"..HEAD)
      if [[ -n "$summary" ]]; then
        msg="$header"$'\n\n'"$summary"
      else
        msg="$header"
      fi
    else
      msg="$header"
    fi
    just bump-version "{{version}}"
    if ! git diff --quiet version.env; then
      git add version.env
      git commit -m "chore: release v{{version}}"
      git push origin master
      gum log --level info "pushed version.env bump to master"
    fi
    just tag "{{version}}" "$msg"

# [explore] Spike for the per-commit-repair-hook design
# (docs/plans/2026-06-16-per-commit-repair-hook-design.md). Verifies the
# load-bearing isolation mechanism: extensions.worktreeConfig + a
# per-worktree core.hooksPath confines a pre-commit hook to ONE worktree.
# Scratch repo, marker hook, six checks. Delete once the installer lands.
#
# verify a per-worktree core.hooksPath confines a pre-commit hook to one worktree
[group('explore')]
explore-worktree-hooks:
    #!/usr/bin/env bash
    set -uo pipefail
    scratch=$(mktemp -d)
    trap 'rm -rf "$scratch"' EXIT
    repo="$scratch/repo"
    marker="$scratch/fired.log"
    pass=0
    g() { git -C "$1" -c commit.gpgsign=false "${@:2}"; }

    git init -q -b main "$repo"
    g "$repo" commit -q --allow-empty -m initial

    wt="$repo/.worktrees/feat"
    g "$repo" worktree add -q -b feat "$wt" >/dev/null 2>&1

    # Enable per-worktree config + a worktree-scoped hooksPath, in the WORKTREE only.
    git -C "$wt" config extensions.worktreeConfig true
    hooks="$wt/.spinclass/hooks"
    mkdir -p "$hooks"
    printf '#!/usr/bin/env bash\necho "FIRED:$(git rev-parse --show-toplevel)" >> "%s"\nexit 0\n' "$marker" > "$hooks/pre-commit"
    chmod +x "$hooks/pre-commit"
    git -C "$wt" config --worktree core.hooksPath "$hooks"

    echo "## 1: commit in worktree fires the hook"
    : > "$marker"; g "$wt" commit -q --allow-empty -m in-wt
    if grep -q FIRED "$marker"; then echo "  PASS"; else echo "  FAIL: hook did not fire in worktree"; pass=1; fi

    echo "## 2: commit in MAIN checkout does NOT fire the worktree hook"
    : > "$marker"; g "$repo" commit -q --allow-empty -m in-main
    if [[ -s "$marker" ]]; then echo "  FAIL: main fired worktree hook"; cat "$marker"; pass=1; else echo "  PASS"; fi

    echo "## 3: main checkout sees no core.hooksPath"
    hp=$(git -C "$repo" config --get core.hooksPath || true)
    if [[ -z "$hp" ]]; then echo "  PASS"; else echo "  FAIL: main core.hooksPath=$hp"; pass=1; fi

    echo "## 4: a second worktree without the config does not inherit it"
    wt2="$repo/.worktrees/feat2"
    g "$repo" worktree add -q -b feat2 "$wt2" >/dev/null 2>&1
    hp2=$(git -C "$wt2" config --get core.hooksPath || true)
    if [[ -z "$hp2" ]]; then echo "  PASS"; else echo "  FAIL: second worktree core.hooksPath=$hp2"; pass=1; fi

    echo "## 5: extensions.worktreeConfig is repo-global (informational)"
    echo "  main sees extensions.worktreeConfig=$(git -C "$repo" config --get extensions.worktreeConfig || true)"

    echo "## 6: cleanup on git worktree remove"
    cfg="$repo/.git/worktrees/feat/config.worktree"
    echo "  per-worktree config before remove: $([[ -f "$cfg" ]] && echo present || echo absent)"
    g "$repo" worktree remove --force "$wt"
    after=$([[ -f "$cfg" ]] && echo STILL-PRESENT || echo gone)
    echo "  per-worktree config after remove:  $after"
    [[ "$after" == gone ]] || { echo "  FAIL: per-worktree config leaked after remove"; pass=1; }

    if [[ $pass -eq 0 ]]; then echo "VERDICT: per-worktree hooksPath isolation WORKS"; else echo "VERDICT: isolation FAILED (see above)"; fi
    exit $pass
