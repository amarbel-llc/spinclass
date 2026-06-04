default: lint build test

build:
    nix build --show-trace

test:
    nix flake check --print-build-logs

test-bats:
    nix build .#bats-default --no-link --print-build-logs

test-bats-race:
    nix build .#bats-race --no-link --print-build-logs

# madder-pinned bats lane: runs zz-tests_bats against a spinclass built
# with a madder pin so the tap-ndjson tests in hooks.bats run instead of
# skipping (#85). Folded into `just test` via the bats-madder flake check.
test-bats-madder:
    nix build .#bats-madder --no-link --print-build-logs

# Format all source files via conformist (the treefmt successor): Go
# (goimports → gofumpt), Nix (nixfmt), shell/bats (shfmt). Config lives
# in ./conformist.toml. The read-only counterpart is `lint-fmt`.
fmt:
    nix develop --command conformist

lint: lint-vet lint-fmt

lint-vet:
    nix develop --command go vet ./...

# Read-only format + lint gate via conformist: fails on formatter drift
# (Go/Nix/shell, per ./conformist.toml) plus shellcheck. `just fmt` is
# the corresponding write mode. Folded into `just lint` → `just default`,
# so the pre-merge `just` hook enforces fmt-cleanliness on every merge.
lint-fmt:
    nix develop --command conformist check

clean:
    rm -rf result

deps:
    nix develop --command gomod2nix

# Regenerate the tommy-generated sweatfile codec (sweatfile_tommy.go) after a
# tommy bump or a Sweatfile struct change. Builds the tommy CLI from the
# pinned module version into a temp dir, puts it on PATH, then runs
# `go generate` (the //go:generate directive needs `tommy` on PATH and sets
# $GOFILE). Run after `just deps`.
gen-tommy:
    #!/usr/bin/env bash
    set -euo pipefail
    nix develop --command bash -c '
      bindir=$(mktemp -d)
      trap "rm -rf \"$bindir\"" EXIT
      go build -o "$bindir/tommy" github.com/amarbel-llc/tommy/cmd/tommy
      PATH="$bindir:$PATH" go generate ./internal/sweatfile/
    '
    nix develop --command gofumpt -w internal/sweatfile/sweatfile_tommy.go

# [explore] Inspect nix-store --gc --print-roots output for entries pointing
# into the spinclass repo. Used to investigate issue #67 — what does
# print-roots show before vs after worktree removal?
[group('explore')]
explore-gcroots-spinclass:
    #!/usr/bin/env bash
    set -uo pipefail
    nix-store --gc --print-roots 2>/dev/null | grep -F 'spinclass' || echo "(no spinclass-rooted entries found)"

# [explore] List ALL dangling auto-root links on the system (regardless of
# what they used to point at). These are zero-byte symlinks with broken
# targets — leftovers from removed checkouts/worktrees.
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
[group('explore')]
explore-dodder-e2e:
    examples/dodder-consumer/e2e.sh

# [explore] Build the clown circus from the dodder-consumer example —
# clown's mkCircus bundling the dodder-pinned spinclass + dodder + madder
# clown plugins (modeled after ~/eng/lib/circus.nix). Composition smoke
# test; building proves the plugins resolve. See examples/dodder-consumer.
[group('explore')]
explore-dodder-circus:
    cd examples/dodder-consumer && nix build .#circus --print-build-logs

# [debug] Pipe a synthetic PreToolUse payload for merge-this-session through
# the installed plugin handler, then print exit code, stdout, and stderr.
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

# Verify that the nix-built binary has version+commit burnt in via the
# fork's buildGoApplication ldflags, and that the prefix matches the
# spinclassVersion literal in flake.nix.
verify-version-burnin: build
    #!/usr/bin/env bash
    set -euo pipefail
    got="$(./result/bin/spinclass version)"
    echo "spinclass version: $got"
    [[ "$got" =~ ^[^+]+\+[^+]+$ ]] || { echo "bad shape: $got" >&2; exit 1; }
    [[ "$got" != "dev+unknown" ]]   || { echo "ldflags did not fire" >&2; exit 1; }
    flake_version="$(grep 'spinclassVersion = ' flake.nix | sed 's/.*"\(.*\)".*/\1/')"
    prefix="${got%%+*}"
    [[ "$prefix" == "$flake_version" ]] || \
        { echo "version prefix '$prefix' != flake.nix '$flake_version'" >&2; exit 1; }
    echo "OK: shape, non-default, prefix match"

dev-repo:
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

# [explore] End-to-end smoke test for the cross-session chat prototype
# (FDR 0009). Builds the binary, points XDG_STATE_HOME at a scratch dir,
# starts `chat-watch` in the background as a fake session B, then sends a
# broadcast and a directed message as session A via `chat-send`. Asserts the
# watcher pushed exactly the messages addressed to B. Proves the real binary
# round-trips send->watch; does NOT prove the Claude Code plugin-monitor push
# (that needs a live two-session trial). Identity comes from
# $SPINCLASS_SESSION_ID, so no real worktree is needed.
[group('explore')]
explore-chat-roundtrip:
    #!/usr/bin/env bash
    set -uo pipefail
    scratch=$(mktemp -d)
    bin="$scratch/spinclass"
    trap 'kill "$watch_pid" 2>/dev/null; rm -rf "$scratch"' EXIT
    nix develop --command go build -o "$bin" ./cmd/spinclass || { echo "build failed"; exit 1; }
    export XDG_STATE_HOME="$scratch/state"

    watch_out="$scratch/watch.out"
    SPINCLASS_SESSION_ID="repo-b/feat-b" "$bin" chat-watch >"$watch_out" 2>&1 &
    watch_pid=$!
    sleep 1   # let the watcher snapshot the (empty) room as its baseline

    SPINCLASS_SESSION_ID="repo-a/feat-a" "$bin" chat-send --message "hello everyone"
    SPINCLASS_SESSION_ID="repo-a/feat-a" "$bin" chat-send --message "psst, just you" --to "repo-b/feat-b"
    SPINCLASS_SESSION_ID="repo-a/feat-a" "$bin" chat-send --message "not for B"      --to "repo-c/feat-c"

    sleep 2   # poll interval is 1s; give the watcher two ticks
    kill "$watch_pid" 2>/dev/null; wait "$watch_pid" 2>/dev/null

    echo "## watcher output:"; sed 's/^/    /' "$watch_out"; echo
    pass=0
    grep -qF "from repo-a/feat-a: hello everyone" "$watch_out" || { echo "FAIL: broadcast not delivered"; pass=1; }
    grep -qF "from repo-a/feat-a: psst, just you" "$watch_out" || { echo "FAIL: DM to B not delivered"; pass=1; }
    if grep -qF "not for B" "$watch_out"; then echo "FAIL: message for C leaked to B"; pass=1; fi
    if [[ $pass -eq 0 ]]; then echo "VERDICT: chat send->watch round-trip WORKS"; else echo "VERDICT: round-trip FAILED"; fi
    exit $pass

# [explore] End-to-end smoke for the chat-read polling path (#98). Sends a
# broadcast + a DM as session A, then reads as session B: firehose sees both,
# a second read sees none (cursor advanced), --peek does not advance, and the
# to_me / from / repo filters each narrow correctly. Identity via
# $SPINCLASS_SESSION_ID; temp XDG_STATE_HOME, no real worktree needed.
[group('explore')]
explore-chat-read:
    #!/usr/bin/env bash
    set -uo pipefail
    scratch=$(mktemp -d)
    bin="$scratch/spinclass"
    trap 'rm -rf "$scratch"' EXIT
    nix develop --command go build -o "$bin" ./cmd/spinclass || { echo "build failed"; exit 1; }
    export XDG_STATE_HOME="$scratch/state"
    A="alpha/feat-a"; B="beta/feat-b"
    pass=0

    SPINCLASS_SESSION_ID="$A" "$bin" chat-send --message "to-all"
    SPINCLASS_SESSION_ID="$A" "$bin" chat-send --message "to-b" --to "$B"

    echo "## firehose read as B (peek):"
    out=$(SPINCLASS_SESSION_ID="$B" "$bin" chat-read --peek); echo "$out" | sed 's/^/    /'
    echo "$out" | grep -qF "to-all" || { echo "FAIL: firehose missing broadcast"; pass=1; }
    echo "$out" | grep -qF "to-b"   || { echo "FAIL: firehose missing DM"; pass=1; }

    echo "## peek must not have advanced — real read still sees both:"
    out=$(SPINCLASS_SESSION_ID="$B" "$bin" chat-read)
    echo "$out" | grep -qF "to-all" || { echo "FAIL: peek wrongly advanced cursor"; pass=1; }

    echo "## second real read: cursor advanced, nothing new:"
    out=$(SPINCLASS_SESSION_ID="$B" "$bin" chat-read)
    [[ "$out" == "no new messages" ]] || { echo "FAIL: expected 'no new messages', got: $out"; pass=1; }

    echo "## filters (peek so cursor state is irrelevant) as B:"
    SPINCLASS_SESSION_ID="$A" "$bin" chat-send --message "f-all"
    SPINCLASS_SESSION_ID="$A" "$bin" chat-send --message "f-b" --to "$B"
    SPINCLASS_SESSION_ID="gamma/x" "$bin" chat-send --message "f-other" --to "other/y"
    to_me=$(SPINCLASS_SESSION_ID="$B" "$bin" chat-read --peek --to_me)
    echo "$to_me" | grep -qF "f-other" && { echo "FAIL: to_me leaked a non-addressed msg"; pass=1; }
    from=$(SPINCLASS_SESSION_ID="$B" "$bin" chat-read --peek --from "gamma/x")
    echo "$from" | grep -qF "f-other" || { echo "FAIL: --from gamma/x missed its msg"; pass=1; }
    echo "$from" | grep -qF "f-all"   && { echo "FAIL: --from leaked another sender"; pass=1; }
    repo=$(SPINCLASS_SESSION_ID="$B" "$bin" chat-read --peek --repo "alpha")
    echo "$repo" | grep -qF "f-all" || { echo "FAIL: --repo alpha missed its msg"; pass=1; }
    echo "$repo" | grep -qF "f-other" && { echo "FAIL: --repo alpha leaked gamma"; pass=1; }

    if [[ $pass -eq 0 ]]; then echo "VERDICT: chat-read polling WORKS"; else echo "VERDICT: chat-read FAILED"; fi
    exit $pass

# Tag a spinclass release. The "v" prefix is added for you, so pass
# the semver without it. Usage: just tag 0.1.0 "feat: initial release"
tag version message:
    #!/usr/bin/env bash
    set -euo pipefail
    tag="v{{version}}"
    prev=$(git tag --sort=-v:refname -l "v*" | head -1)
    if [[ -n "$prev" ]]; then
      gum log --level info "Previous: $prev"
      git log --oneline "$prev"..HEAD
    fi
    git tag -s -m "{{message}}" "$tag"
    gum log --level info "Created tag: $tag"
    git push origin "$tag"
    gum log --level info "Pushed $tag"
    git tag -v "$tag"

# Sed-rewrite spinclassVersion in flake.nix to the given semver. The
# version string is burnt into the binary at build time via -ldflags
# (auto-injected by buildGoApplication), so flake.nix is the single
# source of truth. No-op if already at the target version. Usage:
# just bump-version 0.1.1
bump-version new_version:
    #!/usr/bin/env bash
    set -euo pipefail
    current=$(grep 'spinclassVersion = ' flake.nix | sed 's/.*"\(.*\)".*/\1/')
    if [[ "$current" == "{{new_version}}" ]]; then
      gum log --level info "already at {{new_version}}"
      exit 0
    fi
    sed -i.bak 's/spinclassVersion = "'"$current"'"/spinclassVersion = "{{new_version}}"/' flake.nix && rm flake.nix.bak
    gum log --level info "bumped spinclassVersion: $current → {{new_version}}"

# Cut a release: must be run on master. Bumps spinclassVersion in
# flake.nix, commits the bump with a changelog-style message built
# from commits since the last v* tag, pushes master, then signs and
# pushes the v{{version}} tag. The "v" prefix is added for you, so
# pass the semver without it. Usage: just release 0.1.1
#
# Use `just tag <version> <message>` directly if you want to control
# the commit message yourself without bumping.
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
    if ! git diff --quiet flake.nix; then
      git add flake.nix
      git commit -m "chore: release v{{version}}"
      git push origin master
      gum log --level info "pushed flake.nix bump to master"
    fi
    just tag "{{version}}" "$msg"
