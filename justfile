default: lint build test verify

# --- lint ---

[group("pre-build")]
lint: lint-fmt lint-golangci lint-layout lint-worktree lint-invisible-chars

# Fail on any literal invisible or control character in a tracked text file:
# Unicode category Cf (bidi overrides, zero-width spaces, BOM, soft hyphen, ...),
# C0/C1 controls other than tab, newline and CR, and U+2028/U+2029. Downstream
# linters in the update cascade flag these in Go sources, so they halted it
# twice (U+202E, then U+200B and U+2028). Write them as escapes ("\u200b") instead.
# Repo-owned, so it does not depend on which linters the conformist preset enables.
# ripgrep comes from the flake's nixpkgs, because the pre-merge hook may run
# outside the devShell.
#
# fail on literal invisible (Unicode Cf) or control characters in tracked files
[group("pre-build")]
lint-invisible-chars:
    #!/usr/bin/env bash
    set -euo pipefail
    pattern='[\p{Cf}\x{00}-\x{08}\x{0B}\x{0C}\x{0E}-\x{1F}\x{7F}\x{80}-\x{9F}\x{2028}\x{2029}]'
    if git ls-files -z | xargs -0 nix shell --inputs-from . nixpkgs#ripgrep -c rg --no-heading --line-number --max-columns 120 -- "$pattern"; then
      echo "literal invisible/control characters found (above): write them as escape sequences" >&2
      exit 1
    fi
    echo "no literal invisible or control characters in tracked files"

# Read-only format + lint gate via the sandboxed checks.formatting
# derivation: formatter drift (Go/Nix/shell/TOML, per ./conformist.nix) plus
# the linters — shellcheck, statix/deadnix, and the
# eng-convention linters (eng-versioning, flake-outputs/lock, justfile-*) from
# conformist.lib.presets.eng. `just codemod-fmt` is the corresponding write
# mode. Folded into `just lint` → `just default`, so the pre-merge `just` hook
# enforces it on every merge.
#
# check formatting and the eng-convention lints, read-only
[group("pre-build")]
lint-fmt:
    #!/usr/bin/env bash
    set -euo pipefail
    system=$(nix eval --raw --impure --expr 'builtins.currentSystem')
    nix build ".#checks.${system}.formatting" --no-link --print-build-logs

# Pure, sandboxed golangci-lint via the nix-native `checks.<system>.lint`
# derivation (igloo's buildGoLint, warm-cache seeded). Replaces the golangci-lint
# that used to run in the impure lint-worktree lane below — a sandboxed build
# has a per-build scratch GOLANGCI_LINT_CACHE, so it can't replay stale `.merge-*`
# cache paths whose suppressions fail open (spinclass#294). Folded into `just
# lint`, so the pre-merge `just` hook runs it on every merge.
#
# run the pure warm-cache nix-native golangci-lint check
[group("pre-build")]
lint-golangci:
    #!/usr/bin/env bash
    set -euo pipefail
    system=$(nix eval --raw --impure --expr 'builtins.currentSystem')
    nix build ".#checks.${system}.lint" --no-link --print-build-logs

# The dagnabit package-layout check (#414): fails, listing the would-move
# lines, when a package under internal/ sits at a level other than the one its
# imports put it at. Part of `just lint`, so the pre-merge gate runs it
# (circus's lint-layout-circus-cli is the model). A new package goes at the
# level this prints; `just debug-dagnabit-move-level <level>` applies a move.
#
# check the internal/ package layout with dagnabit
[group("pre-build")]
lint-layout:
    #!/usr/bin/env bash
    set -euo pipefail
    system=$(nix eval --raw --impure --expr 'builtins.currentSystem')
    nix build ".#checks.${system}.layout" --no-link --print-build-logs

# The impure eng-convention checks (git remotes, git-default-branch, sweatfile,
# agents-md) against the working tree, where .git is available. Runs conformist
# from the devShell PATH (direnv `use flake`). Go lint runs in the pure
# `lint-golangci` / `checks.lint` (spinclass#294).
#
# run the impure eng-convention checks against the working tree
[group("pre-build")]
lint-worktree:
    #!/usr/bin/env bash
    set -euo pipefail
    cfg=$(nix build --no-link --print-out-paths '.#conformist-impure-config')
    conformist check --config-file "$cfg" --tree-root .

# --- build ---

[group("build")]
build: build-nix

# Build the spinclass binary via nix (burns version.env + commit into the
# binary via -ldflags — see mkSpinclass in flake.nix).
#
# build the spinclass binary via nix
[group("build")]
build-nix:
    nix build --show-trace

# Regenerate the tommy-generated sweatfile codec (sweatfile_tommy.go) after a
# tommy bump or a Sweatfile struct change. Runs `go generate` in godyn's
# rendered module tree, the same derivation `verify-tommy-codegen` gates on
# (checks.<system>.tommy-codegen), and applies its passthru.codegenPatch to
# the index (igloo FDR 0008), so only the generated file changes. The former
# `godyn-go -- go generate` escape hatch re-resolved the module graph on every
# run and rewrote go.nix with an unrequested dependency bump (spinclass#410);
# the patch route never touches go.nix. The patch is applied to the working
# tree only (its preimage is the tracked files as they stand in the worktree,
# dirty or not), so stage the result yourself. The codec header stamps tommy's
# rev, so regenerate after every tommy bump. Not part of the gate.
#
# regenerate the tommy-generated sweatfile codec
[group("build")]
build-tommy-codegen:
    #!/usr/bin/env bash
    set -euo pipefail
    system=$(nix eval --raw --impure --expr 'builtins.currentSystem')
    out=$(nix build --no-link --print-out-paths --print-build-logs ".#checks.${system}.tommy-codegen.passthru.codegenPatch")
    if [ -s "$out/patch" ]; then
        git apply -p2 "$out/patch"
        git diff --stat -- internal/0/sweatfile/sweatfile_tommy.go | tail -n 3
    else
        echo "sweatfile_tommy.go is already up to date"
    fi

# --- test ---

[group("post-build")]
test: test-nix

# Go unit tests plus every bats integration lane via `nix flake check` (the
# [checks] output: the unit tests (godyn per-package `spinclass-tests` on
# x86_64-linux, the buildGoApplication `go test ./...` elsewhere), the bats
# lanes, lint/vet, tommy-codegen and formatting).
#
# run the Go unit tests and every bats lane via `nix flake check`
[group("post-build")]
test-nix:
    nix flake check --print-build-logs

# --- verify ---

[group("post-build")]
verify: verify-version-burnin verify-tommy-codegen

# Verify that the nix-built binary has version+commit burnt in via the
# fork's buildGoApplication ldflags (auto-reading version.env —
# eng-versioning(7)), and that the version prefix matches version.env.
# `spinclass version` emits the blessed self-as-row component table
# (eng-versioning(7) "version subcommand output"), so the self version
# is the VERSION column of the first `spinclass-*` row, not raw stdout.
#
# verify version and commit are burnt into the nix-built binary
[group("post-build")]
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

# Drift guard (#159): the pure `checks.<system>.tommy-codegen` (godyn
# passthru.codegenCheck) runs `go generate` in the vendored module tree and
# fails if the result differs from the committed source — catches a `tommy`
# pin bump landed without a matching `just build-tommy-codegen`.
#
# fail if the committed tommy codec differs from a fresh regen
[group("post-build")]
verify-tommy-codegen:
    #!/usr/bin/env bash
    set -euo pipefail
    system=$(nix eval --raw --impure --expr 'builtins.currentSystem')
    nix build ".#checks.${system}.tommy-codegen" --no-link --print-build-logs

# The residual check circus's gate-guard runs after it skips the pre-merge
# `just` for a docs-only merge (.gate-skip-allow). gate-guard looks for exactly
# this name. lint-invisible-chars is the only gate check that reads docs/, so
# it keeps running when the rest of the gate is skipped.
#
# run the checks a docs-only gate skip keeps (invisible characters)
[group("post-build")]
verify-gate-skip-residual: lint-invisible-chars

# --- codemod ---

[group("codemod")]
codemod-fmt: codemod-fmt-tree

# Format the tree in place (repair mode) via `nix fmt`: Go (goimports →
# gofumpt), Nix (nixfmt), shell/bats (shfmt), TOML (tommy fmt). Config is
# Nix-generated from ./conformist.nix + presets.{eng,eng-go}. The read-only
# counterpart is `lint-fmt`.
#
# format the tree in place via `nix fmt`
[group("codemod")]
codemod-fmt-tree:
    nix fmt

# --- clean ---

[group("maintenance")]
clean: clean-build

# remove the nix build result symlink
[group("maintenance")]
clean-build:
    rm -rf result

# --- maintenance ---

# [debug] Fast single-package Go test loop via godyn-test (igloo FDR 0008):
# builds one package's test run from a git+file: ref (tracked files as in the
# working tree; `git add -N` a NEW file first or it is invisible); only the
# edited cone rebuilds. x86_64-linux only: elsewhere `default` is the
# buildGoApplication build, which has no passthru.testWith (igloo#33).
# `just` is still the gate that counts.
#
# Pass a -run regex via `run`, NOT via flags: just interpolates variadic args
# as raw text, so 'A|B' would reach the shell with a live pipe. Flags use the
# test binary's spelling:
#
#     just debug-go-test internal/charlie/perms 'TestA|TestB' -test.v
#
# [debug] Harness B, fast local lane: build .#spinclass-serve-integration,
# whose checkPhase runs only the four serve integration tests
# (cmd/spinclass/serve_integration_test.go). godyn's inner loop skips them (no
# `go` on PATH), so this catches a serve-path regression before the pre-merge
# lane does. Opt-in, outside the gate. An unchanged tree is a store hit and
# runs nothing; add `--rebuild` by hand to rerun the tests.
#
# run the serve integration tests in the sandbox (harness B)
[group("debug")]
debug-serve-integration:
    nix build .#spinclass-serve-integration --print-build-logs --no-link

# run go test for one package via godyn-test (fast inner loop)
[group("debug")]
debug-go-test dir run='' *flags='':
    nix run --inputs-from . igloo#godyn-test -- {{ dir }} -- {{ if run == '' { '' } else { quote('-test.run=' + run) } }} {{ flags }}

# measure the #438 policy broker's per-check latency (evaluator alone, then a unix-socket round trip), reporting mean, p50 and p99 per check
[group("debug")]
debug-bench-policy-broker iterations='5000x':
    just debug-go-test internal/bravo/folioeval '^$' -test.bench=. -test.benchtime={{ iterations }}
    just debug-go-test internal/0/policybroker '^$' -test.bench=. -test.benchtime={{ iterations }}

# regenerate internal/0/sshsig/testdata: real `ssh-keygen -Y sign` vectors from throwaway keys (private keys are never copied out)
[group("debug")]
debug-sshsig-fixtures:
    #!/usr/bin/env bash
    set -euo pipefail
    out="$PWD/internal/0/sshsig/testdata"
    scratch=$(mktemp -d)
    trap 'rm -rf "$scratch"' EXIT
    mkdir -p "$out"
    printf 'piggy-test: grant merge to master\nrequest piggy-record-digest-v1@sha256-example\n' > "$out/message.txt"
    for kind in ed25519 ecdsa; do
      args=(-t "$kind")
      [ "$kind" = ecdsa ] && args+=(-b 256)
      ssh-keygen -q "${args[@]}" -N '' -C sshsig-fixture -f "$scratch/$kind"
      cp "$out/message.txt" "$scratch/msg"
      ssh-keygen -q -Y sign -f "$scratch/$kind" -n piggy-escalation-v1 "$scratch/msg"
      cp "$scratch/$kind.pub" "$out/$kind.pub"
      cp "$scratch/msg.sig" "$out/$kind.sig"
      rm "$scratch/msg.sig"
    done
    # the default hash is sha512; also cover sha256 with the ed25519 key
    ssh-keygen -q -Y sign -O hashalg=sha256 -f "$scratch/ed25519" -n piggy-escalation-v1 "$scratch/msg"
    cp "$scratch/msg.sig" "$out/ed25519-sha256.sig"
    ls -l "$out"

# [debug] Time the godyn-test inner loop (igloo FDR 0008) on a real package:
# a first run, a no-op rerun, then a run after appending a comment to one of
# the package's _test.go files (restored on exit from a $TMPDIR backup).
#
# time the godyn-test inner loop (first, no-op, and edited runs)
[group("debug")]
debug-godyn-test-loop dir='internal/charlie/perms':
    #!/usr/bin/env bash
    set -euo pipefail
    target=$(find {{ dir }} -maxdepth 1 -name '*_test.go' | sort | head -1)
    bak="${TMPDIR:-/tmp}/godyn-test-loop.bak"
    cp "$target" "$bak"
    trap 'cp "$bak" "$target"' EXIT
    timeit() {
      local t0
      t0=$(date +%s%N)
      just debug-go-test {{ dir }} >/dev/null 2>&1 || { echo "godyn-test failed ($1)" >&2; exit 1; }
      echo "$1: $(( ($(date +%s%N) - t0) / 1000000 )) ms"
    }
    timeit first
    timeit no-op
    printf '\n// godyn-test-loop edit marker %s\n' "$(date +%s%N)" >> "$target"
    timeit "edited $target"

# [debug] Prove checks.<system>.tommy-codegen catches drift: append a comment
# to the committed codec, expect the check to FAIL, restore the codec on exit.
#
# verify the tommy codegen check fails on a perturbed codec
[group("debug")]
debug-codegen-drift:
    #!/usr/bin/env bash
    set -euo pipefail
    target=internal/0/sweatfile/sweatfile_tommy.go
    bak="${TMPDIR:-/tmp}/codegen-drift.bak"
    cp "$target" "$bak"
    trap 'cp "$bak" "$target"' EXIT
    printf '\n// codegen-drift marker %s\n' "$(date +%s%N)" >> "$target"
    if just verify-tommy-codegen 2>&1 | tail -n 15; then
      echo "UNEXPECTED: codegen check passed on a perturbed codec" >&2
      exit 1
    fi
    echo "OK: codegen check failed on drift"

# POC (spinclass#284 → godyn): build the opt-in per-package godyn binary
# (`.#spinclass-native`) with full build logs. The fast inner loop / A-B target
# for the incremental-edit timing comparison against the default buildGoApplication
# build (`just build-nix`). x86_64-linux only (godynSystem). Content-addressed
# per-package outputs — needs the ca-derivations experimental feature.
#
# build the opt-in godyn (native, per-package) spinclass binary
[group("debug")]
debug-godyn-build:
    nix build .#spinclass-native --no-link --print-build-logs

# POC (parked, spinclass#284 → godyn): incremental-edit rebuild timing —
# godyn (per-package CA, `.#spinclass-native`) vs buildGoApplication (whole
# module, `.#spinclass-native.passthru.bga`). The bga target is the native
# build's OWN passthru — same src/subPackages, doCheck=false — so it is a pure
# build-time A/B with no test-suite confound. Warms both on the unedited tree, appends a
# comment to a LEAF internal package (internal/foxtrot/run — imported only by
# cmd/spinclass), git-adds it so the dirty-tree `nix build` sees the edit
# (tracked/staged only), then times each backend's rebuild. Expectation
# (godyn(7)): godyn recompiles only the changed cone (run + cmd relink) while bga
# rebuilds the whole module. Restores internal/foxtrot/run/run.go on exit. A content-only
# comment edit needs no graph regen. x86_64-linux only.
#
# time an incremental one-package edit: godyn vs buildGoApplication rebuild
[group("debug")]
debug-godyn-bench:
    #!/usr/bin/env bash
    set -euo pipefail
    native='.#spinclass-native'
    bga='.#spinclass-native.passthru.bga'
    target=internal/foxtrot/run/run.go
    echo "warming both backends on the unedited tree…" >&2
    nix build "$native" --no-link >/dev/null 2>&1
    nix build "$bga" --no-link >/dev/null 2>&1
    cp "$target" "$target.benchbak"
    trap 'mv -f "$target.benchbak" "$target"; git add "$target" >/dev/null 2>&1 || true' EXIT
    printf '\n// godyn-bench incremental-edit marker %s\n' "$(date +%s%N)" >> "$target"
    git add "$target"
    timeit() {
        local t0 t1
        t0=$(date +%s%N)
        nix build "$1" --no-link >/dev/null 2>&1 || { echo "build failed: $1" >&2; exit 1; }
        t1=$(date +%s%N)
        echo $(( (t1 - t0) / 1000000 ))
    }
    n=$(timeit "$native")
    b=$(timeit "$bga")
    echo "incremental edit ($target — leaf, dependents: cmd/spinclass only), rebuild wall-clock:"
    echo "  godyn (spinclass-native):          ${n} ms"
    echo "  buildGoApplication (passthru.bga): ${b} ms"

# [explore] Run the flake-pinned dagnabit (purse-first) with arbitrary args, to
# learn its CLI and dry-run package-layout moves (#414).
#
# run the pinned dagnabit with arbitrary args
[group("explore")]
explore-dagnabit *args:
    nix run --inputs-from . purse-first#dagnabit -- {{ args }}

# [debug] Apply ONE level of the dagnabit -initial layout move (#414) without a
# go.mod in the checkout: in godyn's rendered module tree (codegenCheck's
# passthru.codegenPatch, igloo FDR 0008) git-init the tree (dagnabit's mover
# shells out to `git mv`, purse-first#196), plan with `dagnabit -initial -n
# internal`, run `dagnabit move` (git mv + import rewrite + gofmt) for each
# package planned into internal/<level>/, and apply the resulting patch here
# with `git apply --index` (staged). Run levels in order: 0, alfa, bravo, ...
#
# move the packages dagnabit plans for one level into internal/<level>/
[group("debug")]
debug-dagnabit-move-level level:
    #!/usr/bin/env bash
    set -euo pipefail
    export DAGNABIT_LEVEL={{ quote(level) }} DAGNABIT_TOP="$(git rev-parse --show-toplevel)"
    read -r -d '' expr <<'EOF' || true
    let
      f = builtins.getFlake "git+file:${builtins.getEnv "DAGNABIT_TOP"}";
      s = builtins.currentSystem;
      p = f.inputs.nixpkgs-master.legacyPackages.${s};
      level = builtins.getEnv "DAGNABIT_LEVEL";
      check = f.packages.${s}.default.passthru.codegenCheck {
        nativeBuildInputs = [ f.inputs.purse-first.packages.${s}.dagnabit p.git p.jq ];
        exclude = [ ".git" ];
        command = ''
          export HOME=$TMPDIR
          git init -q . && git add -A
          dagnabit -initial -n internal > $TMPDIR/plan.ndjson
          cat $TMPDIR/plan.ndjson >&2
          # Never a level directory itself: on a partly tiered tree -initial
          # trims internal/0/<pkg> to the node internal/0 and plans
          # `internal/0 -> internal/0/0` (purse-first#220); only a flat
          # internal/<pkg> source (two path components) is a package move.
          jq -r --arg lvl ${level} 'select(.event == "would-move") | select(.dst | startswith("internal/" + $lvl + "/")) | select(.src | split("/") | length == 2) | select(.src | test("^internal/(0|alfa|bravo|charlie|delta|echo|foxtrot)$") | not) | "\(.src) \(.dst)"' $TMPDIR/plan.ndjson > $TMPDIR/moves
          [ -s $TMPDIR/moves ] || { echo "no moves planned into internal/${level}/" >&2; exit 1; }
          while read -r src dst; do
            echo "moving $src -> $dst" >&2
            dagnabit move -module code.linenisgreat.com/spinclass "$src" "$dst"
          done < $TMPDIR/moves
          rm -rf .git
        '';
      };
    in check.passthru.codegenPatch
    EOF
    out=$(nix build --impure --no-link --print-out-paths --print-build-logs --expr "$expr")
    echo "patch: $out/patch" >&2
    git apply -p2 --index "$out/patch"
    git diff --cached --stat | tail -n 3

# [debug] After debug-dagnabit-move-level, rewrite the non-Go references
# (docs, manpages, recipes, flake comments, bats, Go comments) to the packages
# that level just moved: the package list is read from the staged renames into
# internal/<level>/, and every `internal/<pkg>` path naming one of them becomes
# `internal/<level>/<pkg>`. FDRs, plans and epics keep the old paths as history
# (#414 ruling). Prints the files it touched; the edits are staged.
#
# rewrite references to the packages one dagnabit level moved
[group("debug")]
debug-dagnabit-rewrite-refs level:
    #!/usr/bin/env bash
    set -euo pipefail
    level={{ quote(level) }}
    mapfile -t pkgs < <(git diff --cached -M --name-status |
        awk -v pre="internal/$level/" '$1 ~ /^R/ && index($3, pre) == 1 { split($2, a, "/"); print a[2] }' | sort -u)
    [ "${#pkgs[@]}" -gt 0 ] || { echo "no staged renames into internal/$level/" >&2; exit 1; }
    alt=$(IFS='|'; echo "${pkgs[*]}")
    echo "packages: ${pkgs[*]}" >&2
    # Symlinks are skipped: `sed -i` would replace the link (CLAUDE.md ->
    # AGENTS.md) with a regular copy; their targets are rewritten directly.
    mapfile -t files < <(git ls-files -s -- . ':!docs/features' ':!docs/plans' ':!docs/epics' |
        awk '$1 != "120000" { sub(/^[^\t]*\t/, ""); print }' |
        xargs grep -lE "(^|[^[:alnum:]_.-])internal/($alt)([^[:alnum:]_-]|$)" || true)
    [ "${#files[@]}" -gt 0 ] || { echo "no references to rewrite" >&2; exit 0; }
    sed -E -i "s#(^|[^[:alnum:]_.-])internal/($alt)([^[:alnum:]_-]|\$)#\1internal/$level/\2\3#g" "${files[@]}"
    git add -- "${files[@]}"
    printf '%s\n' "${files[@]}"

# [explore] Print dagnabit's plan (one NDJSON would-move line per package) for
# the internal/ tree, in godyn's rendered module tree (#414): `mode=initial`
# runs `dagnabit -initial -n internal` (the flat-layout plan the per-level
# moves follow), `mode=layout` the plain `-n internal` that lint-layout gates.
# A time nonce in the command defeats the cache so every run prints its plan.
#
# print dagnabit's internal/ move plan (initial or layout mode)
[group("explore")]
explore-dagnabit-plan mode='initial':
    #!/usr/bin/env bash
    set -euo pipefail
    case {{ quote(mode) }} in
      initial) export DAGNABIT_FLAGS="-initial -n" ;;
      layout) export DAGNABIT_FLAGS="-n" ;;
      *) echo "mode must be initial or layout" >&2; exit 2 ;;
    esac
    export DAGNABIT_TOP="$(git rev-parse --show-toplevel)"
    read -r -d '' expr <<'EOF' || true
    let
      f = builtins.getFlake "git+file:${builtins.getEnv "DAGNABIT_TOP"}";
      s = builtins.currentSystem;
      check = f.packages.${s}.default.passthru.codegenCheck {
        nativeBuildInputs = [ f.inputs.purse-first.packages.${s}.dagnabit ];
        command = "dagnabit ${builtins.getEnv "DAGNABIT_FLAGS"} internal >&2 # ${toString builtins.currentTime}";
      };
    in check.passthru.codegenPatch
    EOF
    nix build --impure --no-link --print-build-logs --expr "$expr" 2>&1 | grep -F '{"' || true

# [explore] Report which of the given repo paths exist in godyn's rendered
# module tree (the src codegenCheck/codegenPatch run in), and how many .go and
# _test.go files it holds per top directory. Tests purse-first#220's theory that
# the dagnabit level moves missed imports in files absent from that tree. A
# time nonce defeats the cache so every run prints.
#
# list which paths godyn's rendered codegenCheck tree contains
[group("explore")]
explore-codegen-src paths="cmd/spinclass/close_child_cmd.go cmd/spinclass/resurrect_cmd.go internal/foxtrot/resurrect/resurrect_test.go":
    #!/usr/bin/env bash
    set -euo pipefail
    export CODEGEN_PATHS={{ quote(paths) }} CODEGEN_TOP="$(git rev-parse --show-toplevel)"
    read -r -d '' expr <<'EOF' || true
    let
      f = builtins.getFlake "git+file:${builtins.getEnv "CODEGEN_TOP"}";
      s = builtins.currentSystem;
      check = f.packages.${s}.default.passthru.codegenCheck {
        command = ''
          for p in ${builtins.getEnv "CODEGEN_PATHS"}; do
            if [ -e "$p" ]; then echo "SRC present $p" >&2; else echo "SRC MISSING $p" >&2; fi
          done
          for d in cmd internal; do
            echo "SRC count $d go=$(find $d -name '*.go' ! -name '*_test.go' | wc -l) test=$(find $d -name '*_test.go' | wc -l)" >&2
          done
          # ${toString builtins.currentTime}
        '';
      };
    in check.passthru.codegenPatch
    EOF
    nix build --impure --no-link --print-build-logs --expr "$expr" 2>&1 | grep -F 'SRC ' || true
    for d in cmd internal; do
        printf 'GIT count %s go=%s test=%s\n' "$d" \
            "$(git ls-files "$d" | grep '\.go$' | grep -vc '_test\.go$')" \
            "$(git ls-files "$d" | grep -c '_test\.go$')"
    done

# [explore] Inspect nix-store --gc --print-roots output for entries pointing
# into the spinclass repo. Used to investigate issue #67 — what does
# print-roots show before vs after worktree removal?
#
# list nix gc roots pointing into the spinclass repo
[group("explore")]
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
[group("explore")]
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

# [explore] Dump the raw roff around each sampled manpage's NAME section, so the
# sysprompt manpage-index parser is designed against the formats actually on
# this host rather than a guess. Both dialects appear in a nix profile: man(7)
# (`.SH NAME` + `name \- desc`) and mdoc(7) (`.Sh NAME` + `.Nm`/`.Nd`). Pages may
# be gzipped. Input to the FDR 0030 manpage-index design.
#
# dump raw NAME-section roff from sampled manpages to design the parser against
[group("explore")]
explore-manpage-name-formats glob=(home_directory() / '.nix-profile/share/man/man*/*'):
    #!/usr/bin/env bash
    set -uo pipefail
    shown=0
    for f in {{ glob }}; do
      [ -f "$f" ] || continue
      case "$f" in *.gz) body=$(gzip -cd "$f" 2>/dev/null) ;; *) body=$(cat "$f" 2>/dev/null) ;; esac
      [ -n "$body" ] || continue
      snippet=$(printf '%s\n' "$body" | grep -A3 -iE '^\.S[Hh] +"?NAME' | head -6)
      [ -n "$snippet" ] || snippet="(no NAME section matched)"
      echo "=== $(basename "$f")"
      printf '%s\n' "$snippet" | sed 's/^/    /'
      shown=$((shown+1))
      [ "$shown" -ge 12 ] && break
    done
    echo
    echo "total page files under the glob: $(ls -1 {{ glob }} 2>/dev/null | wc -l)"
    echo
    echo "=== dialect census (which NAME shapes the parser must handle)"
    man_sh=0; mdoc_nd=0; none=0; esc=0; plain=0
    for f in {{ glob }}; do
      [ -f "$f" ] || continue
      case "$f" in *.gz) body=$(gzip -cd "$f" 2>/dev/null) ;; *) body=$(cat "$f" 2>/dev/null) ;; esac
      [ -n "$body" ] || continue
      if printf '%s\n' "$body" | grep -qE '^\.Nd '; then mdoc_nd=$((mdoc_nd+1))
      elif printf '%s\n' "$body" | grep -qiE '^\.SH +"?NAME'; then
        man_sh=$((man_sh+1))
        nameline=$(printf '%s\n' "$body" | grep -iA4 -E '^\.SH +"?NAME' | grep -vE '^\.|^--$' | head -1)
        case "$nameline" in *' \- '*) esc=$((esc+1)) ;; *' - '*) plain=$((plain+1)) ;; esac
      else none=$((none+1)); fi
    done
    echo "  man(7) .SH NAME : $man_sh   (of which ' \\- ' separator: $esc, plain ' - ': $plain)"
    echo "  mdoc(7) .Nd     : $mdoc_nd"
    echo "  no NAME found   : $none"
    exit 0

# [explore] Reproduce what a session rooted at `dir` actually resolves: runs the
# INSTALLED sc validate from there, so the sweatfile chain it prints is the one
# a real session sees, and any `unknown field` line proves the tommy decoder
# dropped that table. Written for the FDR 0030 report that an eng main-checkout
# session renders no Manpage/Repository index; answers both halves at once
# (is <dir>/sweatfile in the chain, and did [sysprompt] decode).
#
# run the installed sc validate from an arbitrary directory
[group("explore")]
explore-validate-from dir=(home_directory() / 'eng'):
    #!/usr/bin/env bash
    set -uo pipefail
    # the installed sc: the home-manager profile on darwin, the system profile on NixOS (same resolution as debug-sc)
    bin="$HOME/.nix-profile/bin/sc"
    [ -x "$bin" ] || bin=$(command -v sc)
    echo "=== validating from {{ dir }} with $bin"
    cd "{{ dir }}" && "$bin" validate
    exit 0

# [explore] Run the exact `nix develop` the frozen per-commit hook's resolver
# runs (internal/bravo/apply/frozenhook.go, spinclass#459) against THIS repo's
# landed default branch: evaluate git+file://<main checkout>?rev=<ref's sha>
# without touching the lock and print the absolute path the devShell gives
# `bin`. Proves the git+file fetch of a worktree repo, the no-lock-update
# posture and the resolution shape live, where the sandboxed tests cannot.
#
# resolve the pre-commit formatter from the landed default branch as the frozen hook does
[group("explore")]
explore-frozen-hook-resolve bin='conformist-pre-commit' ref='origin/master':
    #!/usr/bin/env bash
    set -euo pipefail
    common=$(git rev-parse --path-format=absolute --git-common-dir)
    repo=$(dirname "$common")
    sha=$(git rev-parse {{ quote(ref) }})
    echo "base: {{ ref }} = $sha" >&2
    echo "repo: $repo" >&2
    nix develop "git+file://$repo?rev=$sha" --no-update-lock-file --no-write-lock-file \
        --command sh -c 'command -v -- "$1"' sh {{ quote(bin) }}

# [explore] Reproduce the dynamic system-prompt fragment a REAL session at `dir`
# receives, using the INSTALLED sc (not `go run`, which cannot build
# cmd/spinclass — spinclass#292). This is the counterpart to
# debug-prompt-fragment for cases where the installed binary is the thing under
# test. Written for the FDR 0030 report that an eng main-checkout session renders
# no Manpage/Repository index sections.
#
# print the dynamic system-prompt fragment the INSTALLED sc returns for a directory
[group("explore")]
explore-prompt-fragment-from dir=(home_directory() / 'eng'):
    #!/usr/bin/env bash
    set -uo pipefail
    bin="$HOME/.nix-profile/bin/sc"
    [ -x "$bin" ] || bin=$(command -v sc)   # NixOS installs sc in the system profile, not ~/.nix-profile
    req='{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"system-prompt-append"}}'
    cd "{{ dir }}" || exit 1
    frag=$(printf '%s\n' "$req" \
      | "$bin" serve 2>/dev/null \
      | jq -r 'select(.id == 1) | .result.messages[0].content.text')
    printf '%s\n' "$frag"
    # Size to stderr so stdout stays the fragment alone. A consumer with its
    # own fragment size cap needs these numbers, not a line count.
    printf '\n=== %d bytes, %d lines, ~%d tokens (chars/4 heuristic)\n' \
      "$(printf '%s' "$frag" | wc -c)" \
      "$(printf '%s\n' "$frag" | wc -l)" \
      "$(( $(printf '%s' "$frag" | wc -c) / 4 ))" >&2
    exit 0

# [explore] Estimate the system-prompt token cost of the FDR 0030 indexes before
# a sweatfile switches them on. Renders the exact row text each index emits
# ("- `name(section)` — desc" per page, "- `repo` — desc" per checkout) and
# reports chars / estimated tokens (chars÷4 — a rough heuristic, not a
# tokenizer). Pass a manpage glob and a directory of checkouts.
#
# estimate the prompt-token cost of the sysprompt manpage and repo indexes
[group("explore")]
explore-index-prompt-cost glob=(home_directory() / '.nix-profile/share/man/man*/eng*') repos=(home_directory() / 'eng/repos'):
    #!/usr/bin/env bash
    set -uo pipefail
    man_rows=""; n=0
    for f in {{ glob }}; do
      [ -f "$f" ] || continue
      case "$f" in *.gz) body=$(gzip -cd "$f" 2>/dev/null) ;; *) body=$(cat "$f" 2>/dev/null) ;; esac
      base=$(basename "$f"); base=${base%.gz}; sect=${base##*.}; name=${base%.*}
      desc=$(printf '%s\n' "$body" | grep -iA4 -E '^\.SH +"?NAME' | grep -vE '^\.|^--$' | head -1 \
             | sed -E 's/\\f[BIRP(]?[A-Za-z]?//g; s/^.* \\?- //; s/\\-/-/g')
      man_rows="${man_rows}- \`${name}(${sect})\` — ${desc}"$'\n'; n=$((n+1))
    done
    repo_rows=""; r=0
    for d in "{{ repos }}"/*/; do
      d=${d%/}; [ -d "$d/.git" ] || continue
      desc=$(grep -oE 'description[[:space:]]*=[[:space:]]*"[^"]*"' "$d/flake.nix" 2>/dev/null \
             | head -1 | sed -E 's/.*"([^"]*)"/\1/')
      repo_rows="${repo_rows}- \`$(basename "$d")\` — ${desc}"$'\n'; r=$((r+1))
    done
    mc=$(printf '%s' "$man_rows" | wc -c); rc=$(printf '%s' "$repo_rows" | wc -c)
    printf '%-16s %8s %8s %8s\n' index rows chars '~tokens'
    printf '%-16s %8d %8d %8d\n' manpage "$n" "$mc" $((mc/4))
    printf '%-16s %8d %8d %8d\n' repository "$r" "$rc" $((rc/4))
    printf '%-16s %8d %8d %8d\n' TOTAL $((n+r)) $((mc+rc)) $(((mc+rc)/4))
    echo
    echo "=== manpage rows"; printf '%s' "$man_rows"
    echo "=== repository rows"; printf '%s' "$repo_rows"
    exit 0

# [explore] Inventory the sibling repos a root-level sweatfile entry would reach:
# for each dir under `repos`, its configured origin host (what [auth] would
# derive SPINCLASS_FORGE_HOST from) and its conformist flake-input rev (the
# git-remotes(#8) fix, 7baec98, must be pinned before [auth] turns on there).
# Input to the FDR 0028 fleet-placement design.
#
# list each sibling repo's origin host and pinned conformist rev
[group("explore")]
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
[group("debug")]
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
[group("debug")]
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
[group("debug")]
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
[group("explore")]
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
[group("explore")]
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
[group("explore")]
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
[group("explore")]
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
[group("explore")]
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
[group("explore")]
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
[group("explore")]
explore-dodder-e2e:
    examples/dodder-consumer/e2e.sh

# [explore] Build the clown circus from the dodder-consumer example —
# clown's mkJuggler (renamed from mkCircus, clown#183) bundling the
# dodder-pinned spinclass + dodder + madder clown plugins (modeled after
# ~/eng/lib/circus.nix). Composition smoke test; building proves the
# plugins resolve. See examples/dodder-consumer.
#
# build the clown circus from the dodder-consumer example
[group("explore")]
explore-dodder-circus:
    cd examples/dodder-consumer && nix build .#circus --print-build-logs

# [debug] Build just the bats-default lane, without the full `test-nix` (nix
# flake check) run — already fully covered by `test-nix`/`checks.bats-default`;
# this is a drill-down for iterating on zz-tests_bats without waiting for the
# unit-test checkPhase or the other bats lanes (mirrors tommy's
# debug-bats-nix-tag).
#
# build just the bats-default lane
[group("debug")]
debug-bats-default:
    nix build .#bats-default --no-link --print-build-logs

# [debug] Build just the race-instrumented bats lane. Slower than
# bats-default (race-detector overhead); already covered by `test-nix` —
# deliberately NOT wired into the `test` aggregate as a first-class step so
# `just`/`just test` stays fast, matching this repo's other opt-in slow lanes.
#
# build just the race-instrumented bats lane
[group("debug")]
debug-bats-race:
    nix build .#bats-race --no-link --print-build-logs

# [debug] Build just the madder-pinned bats lane: runs zz-tests_bats against a
# spinclass built with a madder pin so the tap-ndjson tests in hooks.bats run
# instead of skipping (#85). Already covered by `test-nix` via the
# bats-madder flake check; this is the single-lane drill-down.
#
# build just the madder-pinned bats lane
[group("debug")]
debug-bats-madder:
    nix build .#bats-madder --no-link --print-build-logs

# [debug] Pipe a synthetic PreToolUse payload for merge-this-session through
# the installed plugin handler, then print exit code, stdout, and stderr.
#
# pipe a synthetic PreToolUse payload through the installed plugin handler
[group("debug")]
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

# [debug] Ask the worktree build's PreToolUse hook (`sc hook`) for its verdict
# on one synthetic store-tool call (FDR 0036 perms.StoreVerdict), as the given
# principal against the live session store. Judges only: the hook never runs
# the tool, so nothing changes. Pass the caller's own CLOWN_SESSION_ID.
#
#     just debug-store-verdict mcp__plugin_spinclass_spinclass__grit_stage '{"subcommand":"status"}' <principal>
#
# print the PreToolUse hook's verdict for one synthetic store-tool call
[group("debug")]
debug-store-verdict tool input principal:
    #!/usr/bin/env bash
    set -uo pipefail
    payload='{"hook_event_name":"PreToolUse","session_id":"synth-store-verdict","tool_name":"{{ tool }}","tool_input":{{ input }},"cwd":"'"$PWD"'"}'
    echo "payload: $payload" >&2
    echo "env: HOME=$HOME XDG_STATE_HOME=${XDG_STATE_HOME:-}" >&2
    echo "--- whoami (same binary, same principal) ---" >&2
    CLOWN_SESSION_ID='{{ principal }}' nix run --inputs-from . .#default -- whoami >&2
    echo "--- hook verdict ---" >&2
    printf '%s' "$payload" | CLOWN_SESSION_ID='{{ principal }}' nix run --inputs-from . .#default -- hook
    echo "exit: $?" >&2

# print the last n (default 20) PreToolUse verdicts this worktree's hook logged (jq-pretty when available)
[group("debug")]
debug-hook-verdicts n='20':
    #!/usr/bin/env bash
    set -uo pipefail
    f=.spinclass/hook-verdicts.ndjson
    [ -f "$f" ] || { echo "no $f (no spinclass tool call decided here yet)" >&2; exit 0; }
    if command -v jq >/dev/null 2>&1; then tail -n {{ n }} "$f" | jq .; else tail -n {{ n }} "$f"; fi

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
[group("debug")]
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
[group("debug")]
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

# list the operator's troupe runtime dir and account password files with
# mode and owner, contents never read (F14 root cause, spinclass#438 grill)
[group("debug")]
debug-troupe-accounts-ls:
    #!/usr/bin/env bash
    set -uo pipefail
    d="${XDG_RUNTIME_DIR:?}/troupe"
    echo "== $d"
    ls -la "$d" 2>&1
    echo "== $d/accounts"
    ls -la "$d/accounts" 2>&1
    echo "== stat"
    stat -c '%U %a %F %n' "$d" "$d/accounts" "$d"/accounts/* 2>&1
    echo "== count: $(ls -1 "$d/accounts" 2>/dev/null | wc -l) account file(s)"

# posture of a live session's processes: CLOWN_UNATTENDED (value) and the permission-mode words in each command line, nothing else is printed (restart posture checks, spinclass#476)
[group("explore")]
explore-live-session-posture key:
    #!/usr/bin/env bash
    set -uo pipefail
    want="SPINCLASS_SESSION_ID={{key}}"
    found=0
    for d in /proc/[0-9]*; do
      pid=${d#/proc/}
      env=$({ tr '\0' '\n' < "$d/environ"; } 2>/dev/null) || continue
      printf '%s\n' "$env" | grep -qxF "$want" || continue
      found=$((found+1))
      comm=$(cat "$d/comm" 2>/dev/null || echo '?')
      started=$(stat -c '%y' "$d" 2>/dev/null | cut -c1-19)
      unatt=$(printf '%s\n' "$env" | sed -n 's/^CLOWN_UNATTENDED=//p')
      words=$(tr '\0' '\n' < "$d/cmdline" 2>/dev/null | grep -iE 'permission|dontask|unattended|resume' | tr '\n' ' ')
      echo "pid $pid comm=$comm started=$started CLOWN_UNATTENDED=${unatt:-(unset)} cmdline-words=[${words}]"
    done
    [ "$found" -gt 0 ] || echo "(no live process names $want)"

# map live processes' SPINCLASS_SESSION_ID against their CLOWN_SESSION_ID
[group("debug")]
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

# list every live process whose environment names session KEY, with its ppid,
# process group, session id, cwd and command (spinclass#270: what a close of
# KEY leaves running)
[group("debug")]
debug-session-procs key:
    #!/usr/bin/env bash
    set -uo pipefail
    want="SPINCLASS_SESSION_ID={{key}}"
    for d in /proc/[0-9]*; do
      pid=${d#/proc/}
      { tr '\0' '\n' < "$d/environ"; } 2>/dev/null | grep -qxF "$want" || continue
      stat=$(cat "$d/stat" 2>/dev/null) || continue
      read -r _state ppid pgid sid _rest <<<"${stat##*) }"
      cwd=$(readlink "$d/cwd" 2>/dev/null || echo '?')
      cmd=$(tr '\0' ' ' < "$d/cmdline" 2>/dev/null | cut -c1-80)
      echo "pid=$pid ppid=$ppid pgid=$pgid sid=$sid cwd=$cwd cmd=[$cmd]"
    done

# [debug] `lint-golangci` stops at the first package whose lint derivation fails.
# This builds checks.<system>.lint with --keep-going, then prints the full log of
# every failed per-package lint derivation, so one run collects every finding.
# Written for the cascade's newer-linter halts (2026-10-26): run it after a plain
# `nix flake update` to see what the cascade will see.
#
# run the golangci lint check with --keep-going and print every failing package's findings
[group("debug")]
debug-lint-all-findings:
    #!/usr/bin/env bash
    set -uo pipefail
    system=$(nix eval --raw --impure --expr 'builtins.currentSystem')
    log=$(mktemp)
    nix build ".#checks.${system}.lint" --no-link --keep-going 2>"$log"
    status=$?
    grep -o "Cannot build '/nix/store/[^']*godyn-lint[^']*\.drv'" "$log" | sed "s/Cannot build '//; s/'$//" | sort -u | while read -r drv; do
      echo "=== $drv"
      nix log "$drv" 2>/dev/null | grep -E '\.go:[0-9]+:[0-9]+:' | sed 's#/nix/store/[^/]*/##'
    done
    exit "$status"

# [debug] The restart watchdog (#419) arms a one-shot user timer with the same
# flag shape as restartWatchArgv: --on-active, a sanitized --unit name, and
# --setenv. This arms one with a 5s delay that writes an env value to a file,
# then checks that the file appeared with the value, against the real user
# manager.
#
# arm a 5s systemd-run --user --on-active probe and check it fired with its env
[group("debug")]
debug-restart-watch-timer:
    #!/usr/bin/env bash
    set -euo pipefail
    out=$(mktemp -d "${TMPDIR:-/tmp}/restart-watch-probe.XXXXXX")/fired
    unit="spinclass-restart-watch-probe-$(date +%s%N)"
    systemd-run --user --on-active=5s --unit="$unit" --setenv=PROBE_VALUE=probe-ok /bin/sh -c "printf '%s' \"\$PROBE_VALUE\" > $out"
    systemctl --user list-timers --all "$unit.timer" --no-pager || true
    # systemd coalesces timers within AccuracySec (default 1min), so a 5s timer
    # may fire up to a minute late.
    for _ in $(seq 1 75); do [ -s "$out" ] && break; sleep 1; done
    if [ "$(cat "$out" 2>/dev/null)" = probe-ok ]; then echo "OK: timer fired with its --setenv value"; exit 0; fi
    echo "FAIL: timer did not fire or env missing"
    systemctl --user status "$unit.service" "$unit.timer" --no-pager || true
    journalctl --user -u "$unit.service" --no-pager -n 20 || true
    exit 1

# [explore] Prerequisites for the first live restart of an UNCONFINED throwaway
# worker run unattended (docs/plans/2026-10-26-first-live-restart-unattended-
# runbook.md). Read-only: it reads versions and config and runs
# `restart-preflight --peek`, which never writes. The clown commit checks need a
# clown checkout (CLOWN_REPO, default ~/eng/repos/clown) to test ancestry; without
# one they print MANUAL. A global-sweatfile read prints only whether a key is set.
#
# check the clown/spinclass/config/systemd prerequisites for the first live restart
[group("explore")]
explore-live-restart-prereqs:
    #!/usr/bin/env bash
    set -euo pipefail
    fails=0
    pass() { printf 'PASS    %s\n' "$*"; }
    fail() { printf 'FAIL    %s\n' "$*"; fails=$((fails + 1)); }
    info() { printf 'INFO    %s\n' "$*"; }
    manual() { printf 'MANUAL  %s\n' "$*"; }

    command -v jq >/dev/null 2>&1 && pass "jq on PATH" || fail "jq not on PATH (the baseline and verify recipes need it)"

    # clown: version, the restart verb, and the two required commits.
    if command -v clown >/dev/null 2>&1; then
      cv=$(clown --version 2>&1 | head -n1) || cv=""
      if [ -z "$cv" ] || ! clown --version >/dev/null 2>&1; then cv=$(clown version 2>&1 | head -n1) || cv=""; fi
      [ -n "$cv" ] || cv="(neither clown --version nor clown version printed anything)"
      pass "clown on PATH: $cv"
      # `clown restart --help` is rejected as an unknown argument by the deployed clown
      # (observed 2026-10-26); the bare verb prints its usage line, which is the probe.
      restart_usage=$(clown restart 2>&1 || true)
      case "$restart_usage" in
        *"usage: clown restart"*) pass "clown has the restart verb ($(printf '%s' "$restart_usage" | grep -m1 'usage: clown restart'))" ;;
        *) fail "clown restart printed no usage line: this clown predates the restart verb ($restart_usage)" ;;
      esac
      commit=$(printf '%s' "$cv" | grep -oE '[0-9a-f]{7,40}' | tail -n1 || true)
      clown_repo=${CLOWN_REPO:-$HOME/eng/repos/clown}
      if [ -n "$commit" ] && git -C "$clown_repo" cat-file -e "$commit^{commit}" 2>/dev/null; then
        for need in 613bf9a 1f5e57c5; do
          if git -C "$clown_repo" merge-base --is-ancestor "$need" "$commit" 2>/dev/null; then
            pass "clown $commit contains $need"
          else
            fail "clown $commit does not contain $need ($( [ "$need" = 613bf9a ] && echo 'restart verb and --peek wiring' || echo 'CLOWN_UNATTENDED, clown#323' ))"
          fi
        done
      else
        manual "cannot test clown ancestry from '$cv' (no checkout at $clown_repo or no commit in the version): confirm the build contains 613bf9a (restart verb, --peek) and 1f5e57c5 (CLOWN_UNATTENDED, #323)"
      fi
    else
      fail "clown not on PATH"
    fi

    # spinclass: version and the read-only peek.
    if command -v spinclass >/dev/null 2>&1; then
      pass "spinclass on PATH: $(spinclass version 2>&1 | sed -n 2p)"
      peek=$(spinclass restart-preflight --peek 2>/dev/null) || peek=""
      if [ "$(printf '%s\n' "$peek" | wc -l)" -eq 1 ] && printf '%s' "$peek" | jq -e 'has("state")' >/dev/null 2>&1; then
        pass "restart-preflight --peek prints one JSON line: $peek"
      else
        fail "restart-preflight --peek did not print exactly one JSON line with a state key (got: ${peek:-nothing}); this spinclass predates --peek"
      fi
    else
      fail "spinclass not on PATH"
    fi

    # sweatfile layers: global first, then each ancestor of the main checkout,
    # outermost first. The repo's own sweatfile is not a trusted layer.
    global="$HOME/.config/spinclass/sweatfile"
    layers="$global"
    common=$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)
    if [ -n "$common" ]; then
      d=$(dirname "$(dirname "$common")")
      above=""
      while [ "$d" != / ]; do above="$d/sweatfile"$'\n'"$above"; d=$(dirname "$d"); done
      layers="$layers"$'\n'"$above"
    fi
    last_value() { # key regex of the form key = "value"; prints the last trusted layer's value
      printf '%s\n' "$layers" | while IFS= read -r f; do
        [ -f "$f" ] && sed -nE "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*\"([^\"]*)\".*/\1/p" "$f"
      done | tail -n1
    }
    if [ -f "$global" ] && grep -Eq '^[[:space:]]*restart[[:space:]]*=' "$global"; then
      pass "trusted [session-entry] restart hand-off is set in the global sweatfile (value not printed)"
    else
      fail "no 'restart =' in the global sweatfile: every restart is refused with 'no restart hand-off is configured' (a repo-level value is ignored)"
    fi
    mode=$(last_value unattended-permission-mode || true)
    if [ "$mode" = dontAsk ]; then
      pass "[session-entry] unattended-permission-mode = dontAsk (global or a layer above the repo)"
    else
      fail "[session-entry] unattended-permission-mode is '${mode:-unset}', want dontAsk: spawn-session would launch the worker in auto mode with no CLOWN_UNATTENDED marker"
    fi

    # systemd user manager: the restart-watch watchdog arms a user timer.
    sysd=$(systemctl --user is-system-running 2>&1 || true)
    case "$sysd" in
      running|degraded) pass "systemd user manager reachable ($sysd)" ;;
      *) fail "systemd user manager not usable (is-system-running: ${sysd:-no output}): no restart-watch watchdog would be armed" ;;
    esac
    command -v systemd-run >/dev/null 2>&1 && pass "systemd-run on PATH" || fail "systemd-run not on PATH"

    manual "troupe credential: spinclass cannot observe it; see the runbook's manual step"
    echo
    if [ "$fails" -gt 0 ]; then echo "SUMMARY: $fails FAIL"; exit 1; fi
    echo "SUMMARY: all automated prerequisites PASS (MANUAL lines remain)"

# [explore] Shared by explore-live-restart-baseline and -verify. TARGET is a
# session key (<repo>/<branch>), a worktree basename, or an absolute worktree
# path. Finds the frontend record by the same hash the session package uses
# (sha256 of the cleaned worktree path, first 8 bytes, hex) or, for a key, by
# scanning the record files for a matching session_key (only the match is read
# into the snapshot). Never prints credentials; the troupe credential is not
# something spinclass can see. Writes .tmp/live-restart/<sanitized-key>/LABEL.json
# and prints that path on its last line.
#
# write a JSON snapshot of one session's restart-relevant state and processes
[group("explore")]
_explore-live-restart-snapshot target label:
    #!/usr/bin/env bash
    set -euo pipefail
    target='{{ target }}'
    label='{{ label }}'
    command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }
    records="${XDG_STATE_HOME:-$HOME/.local/state}/spinclass/records"
    rec=""
    case "$target" in
      /*)
        h=$(printf '%s' "$(realpath -sm "$target")" | sha256sum | cut -c1-16)
        rec="$records/$h.json"
        ;;
      *)
        for f in "$records"/*.json; do
          [ -f "$f" ] || continue
          if jq -e --arg t "$target" '[.session_key, (.worktree_path | split("/") | last)] | any(.[]; . == $t)' "$f" >/dev/null 2>&1; then rec=$f; break; fi
        done
        ;;
    esac
    [ -f "$rec" ] || { echo "no frontend record for '$target' under $records (live sessions only)" >&2; exit 1; }
    key=$(jq -r '.session_key' "$rec")
    principal=$(jq -r '.principal // ""' "$rec")
    dir=".tmp/live-restart/$(printf '%s' "$key" | sed 's#[^A-Za-z0-9._-]#_#g')"
    mkdir -p "$dir"
    peek=$(spinclass restart-preflight --peek --target "$key" 2>&1 || true)
    onpath=$(readlink -f "$(command -v spinclass)")
    pf=$(mktemp)
    trap 'rm -f "$pf"' EXIT
    pat_c="CLOWN_SESSION_ID=${principal:-__no_principal_recorded__}"
    pat_s="SPINCLASS_SESSION_ID=$key"
    for d in /proc/[0-9]*; do
      pid=${d#/proc/}
      # The braces matter: a failed `< file` redirection is reported by the shell
      # before a trailing 2>/dev/null applies, which flooded every run's stderr.
      env=$({ tr '\0' '\n' < "$d/environ"; } 2>/dev/null) || continue
      printf '%s\n' "$env" | grep -xF -e "$pat_c" -e "$pat_s" >/dev/null || continue
      args=$({ tr '\0' '\n' < "$d/cmdline"; } 2>/dev/null) || continue
      [ -n "$args" ] || continue
      exe=$(readlink "$d/exe" 2>/dev/null || echo '?')
      exe_real=$(readlink -f "$d/exe" 2>/dev/null || echo '?')
      argv0=$(printf '%s\n' "$args" | head -n1)
      kind=other
      if printf '%s\n' "$args" | grep -xF serve >/dev/null; then kind=serve
      elif [ "$(basename "$exe")" = clown ]; then kind=clown
      elif printf '%s\n' "$args" | grep -E '^--permission-mode' >/dev/null; then kind=agent
      else case "$exe $argv0" in *claude*) kind=agent ;; esac
      fi
      [ "$kind" != other ] || continue
      unatt=false
      printf '%s\n' "$env" | grep -xF 'CLOWN_UNATTENDED=1' >/dev/null && unatt=true
      dontask=false
      printf '%s\n' "$args" | awk '$0 == "--permission-mode=dontAsk" { f = 1 } prev == "--permission-mode" && $0 == "dontAsk" { f = 1 } { prev = $0 } END { exit !f }' && dontask=true
      # clown#323 applies dontAsk through an inline --settings JSON instead of the
      # argv flag; it survives a relaunch where the argv splice may not.
      settings_dontask=false
      printf '%s\n' "$args" | awk 'prev == "--settings" && /"defaultMode" *: *"dontAsk"/ { f = 1 } /^--settings=.*"defaultMode" *: *"dontAsk"/ { f = 1 } { prev = $0 } END { exit !f }' && settings_dontask=true
      profile=$(printf '%s\n' "$args" | awk 'prev == "--profile" { print; exit } /^--profile=/ { sub(/^--profile=/, ""); print; exit } { prev = $0 }')
      start=$(sed 's/.*) //' "$d/stat" 2>/dev/null | cut -d' ' -f20 || true)
      jq -nc --argjson pid "$pid" --arg kind "$kind" --arg exe "$exe" --arg exe_real "$exe_real" --arg start "$start" \
        --argjson unattended "$unatt" --argjson dontask "$dontask" --argjson settings_dontask "$settings_dontask" --arg profile "$profile" \
        '{pid: $pid, kind: $kind, exe: $exe, exe_real: $exe_real, start_ticks: $start, unattended: $unattended, dontask: $dontask, settings_dontask: $settings_dontask, profile_argv: $profile}' >> "$pf"
    done
    jq --slurpfile procs "$pf" --arg peek "$peek" --arg onpath "$onpath" --arg taken "$(date -u +%FT%TZ)" --arg label "$label" '{
      label: $label, taken_at: $taken,
      session_key: .session_key, principal: (.principal // ""), worktree: .worktree_path,
      state: .state, kind: (.kind // ""), recorded_pid: (.pid // 0),
      restarting_at: .restarting_at, restarted_at: .restarted_at, restarting_by: .restarting_by,
      restart_request_queued: (.restart_request != null),
      profile: (.profile // ""), profile_digest: (.profile_digest // ""),
      spawned_by: (.spawned_by // ""),
      peek: ($peek | try fromjson catch $peek),
      spinclass_on_path: $onpath,
      procs: $procs,
      troupe_credential: "not observable by spinclass: check manually (runbook)"
    }' "$rec" > "$dir/$label.json"
    echo "$dir/$label.json"

# [explore] Read-only snapshot of the throwaway worker BEFORE the restart, plus
# the preconditions the unattended run needs. Writes
# .tmp/live-restart/<key>/baseline.json (no credential contents; processes whose
# environment names the worker's principal or key, by pid/exe/flags only). A
# stale serve binary is INFO here: the STALE check after the restart is only
# meaningful if the spinclass on PATH changes between this baseline and the
# restart (the runbook says how to arrange that).
#
# snapshot a worker before restarting it and check the unattended preconditions
[group("explore")]
explore-live-restart-baseline target:
    #!/usr/bin/env bash
    set -euo pipefail
    snap=$("{{ just_executable() }}" --quiet --justfile "{{ justfile() }}" _explore-live-restart-snapshot '{{ target }}' baseline | tail -n1)
    echo "snapshot: $snap"
    fails=0
    pass() { printf 'PASS    %s\n' "$*"; }
    fail() { printf 'FAIL    %s\n' "$*"; fails=$((fails + 1)); }
    info() { printf 'INFO    %s\n' "$*"; }
    check() { if jq -e "$2" "$snap" >/dev/null; then pass "$1"; else fail "$1"; fi; }
    jq -r '"INFO    key=\(.session_key) principal=\(.principal) state=\(.state) profile=\(.profile)", "INFO    restarting_at=\(.restarting_at) restarted_at=\(.restarted_at)", "INFO    peek=\(.peek | tojson)", (.procs[] | "INFO    \(.kind) pid=\(.pid) unattended=\(.unattended) dontAsk=\(.dontask) profile_argv=\(.profile_argv) exe=\(.exe)")' "$snap"
    check "principal recorded and UUID-shaped (restart refuses otherwise)" '.principal | test("^[0-9a-fA-F-]{36}$")'
    check "restart-preflight --peek reports state live" '.peek.state == "live"'
    check "a worker agent process was found by its principal/key" 'any(.procs[]; .kind == "agent")'
    check "agent environment has CLOWN_UNATTENDED=1" 'any(.procs[]; .kind == "agent" and .unattended)'
    check "agent runs dontAsk (--permission-mode argv or clown --settings defaultMode)" 'any(.procs[]; (.kind == "agent" or .kind == "clown") and (.dontask or .settings_dontask))'
    check "a spinclass serve process was found" 'any(.procs[]; .kind == "serve")'
    if jq -e '.spinclass_on_path as $p | all(.procs[] | select(.kind == "serve" and (.exe_real | endswith("/spinclass"))); .exe_real == $p and (.exe | endswith("(deleted)") | not))' "$snap" >/dev/null; then
      info "serve binary is the spinclass on PATH now: the post-restart STALE check is vacuous unless you change the spinclass on PATH before restarting"
    else
      info "serve binary already differs from the spinclass on PATH (STALE before the restart); the worker's own version tool is authoritative (it also recognises same-build-different-path)"
    fi
    echo
    if [ "$fails" -gt 0 ]; then echo "SUMMARY: $fails FAIL"; exit 1; fi
    echo "SUMMARY: baseline PASS (MANUAL: note the troupe credential and send the nonce DM now)"

# [explore] Read-only check of the pre-minted token pool (FDR 0028 "Token pool")
# WITHOUT claiming a slot: the directory is 0700, manifest.json parses at version
# 1 with an https forge, every slot's file is a regular 0600 file (the escaped
# file name itself is checked by the claim), and each slot's hours left. Prints no
# token contents. Run it after the operator's batch mint and before leaving;
# [auth].forge-hosts must also list the forge host (`sc validate`). Written
# because the papi runbook's placeholder --out path differs from the fixed pool
# path ($XDG_STATE_HOME/spinclass/token-pool).
#
# check the token pool directory, manifest and slot files without claiming one
[group("explore")]
explore-pool-check pool='' repos='':
    #!/usr/bin/env bash
    set -uo pipefail
    repos={{ quote(repos) }}
    dir={{ quote(pool) }}
    [ -n "$dir" ] || dir="${XDG_STATE_HOME:-$HOME/.local/state}/spinclass/token-pool"
    fails=0
    pass() { printf 'PASS    %s\n' "$*"; }
    fail() { printf 'FAIL    %s\n' "$*"; fails=$((fails + 1)); }
    [ -d "$dir" ] && [ ! -L "$dir" ] || { fail "no pool directory at $dir (papi must mint with --out set to exactly this path)"; echo "SUMMARY: $fails FAIL"; exit 1; }
    mode=$(stat -c %a "$dir")
    [ "$mode" = 700 ] && pass "pool dir mode 700" || fail "pool dir mode $mode, want 700"
    man="$dir/manifest.json"
    jq -e '.version == 1' "$man" >/dev/null 2>&1 && pass "manifest.json parses, version 1" || { fail "manifest.json missing, unparseable or not version 1"; echo "SUMMARY: $fails FAIL"; exit 1; }
    jq -e '.forge | test("^https://[^/]+")' "$man" >/dev/null && pass "forge is https: $(jq -r .forge "$man")" || fail "forge is not an https URL"
    now=$(date +%s)
    while IFS=$'\t' read -r session repo file deadline; do
      case "$file" in */* | "") fail "slot $session: file name '$file' is empty or has a slash"; continue ;; esac
      if [ -e "$dir/$file" ]; then
        { [ -f "$dir/$file" ] && [ ! -L "$dir/$file" ] && [ "$(stat -c %a "$dir/$file")" = 600 ]; } && st="unclaimed" || { fail "slot $session: $file is not a regular 0600 file"; st="bad"; }
      else st="claimed"; fi
      left="no deadline"
      if [ -n "$deadline" ]; then
        d=$(date -d "$deadline" +%s 2>/dev/null || echo 0)
        left="$(( (d - now) / 3600 ))h left"
        [ "$d" -gt "$now" ] || { [ "$st" = unclaimed ] && fail "slot $session: deadline $deadline has passed"; }
      fi
      printf 'INFO    %-28s %-28s %-9s %s\n' "$session" "$repo" "$st" "$left"
    done < <(jq -r '.tokens[] | [.session, .repo, .file, (.deadline // "")] | @tsv' "$man")
    jq -r '.tokens[].repo' "$man" | sort | uniq -c | sed 's/^/INFO    slots per repo: /'
    # The claim compares the session's repo, taken from the origin URL's PATH
    # (".../spinclass.git" -> "spinclass" on the owner-less vanity plane, "o/n" on
    # GitHub), with the slot's repo; papi records the resolved owner/name. An
    # owner-less origin matches the slot's last segment (poolRepoMatches).
    # Pass `repos` as space-separated checkout dirs to test them.
    for r in $repos; do
      url=$(git -C "$r" config --get remote.origin.url 2>/dev/null || true)
      [ -n "$url" ] || { fail "repo $r: no origin url"; continue; }
      case "$url" in
        *://*) path=${url#*://}; path=${path#*/} ;;
        *) path=${url#*:} ;;
      esac
      path=${path#/}; path=${path%.git}
      if jq -e --arg p "$path" '[.tokens[] | select(.repo == $p or (($p | contains("/") | not) and (.repo | split("/") | last) == $p))] | length > 0' "$man" >/dev/null; then
        pass "repo $r (origin path '$path') has a matching pool slot"
      else
        fail "repo $r (origin path '$path'): NO MATCH among the slots' repos: a session there will not claim a slot"
      fi
    done
    [ "$fails" -eq 0 ] && { echo "SUMMARY: pool PASS (MANUAL: [auth].forge-hosts lists the forge host; allow-stale-base for the trip repos)"; exit 0; }
    echo "SUMMARY: $fails FAIL"; exit 1

# [explore] After `close-child-session` on a restarted worker: fail while ANY
# process still carries the session key (SPINCLASS_SESSION_ID) in its
# environment (claude, clown, ringmaster, moxy, the MCP children...). Allow a few
# seconds for the tree to exit. Read-only.
#
# fail if any process of a closed session survives
[group("explore")]
explore-live-close-leftovers key:
    #!/usr/bin/env bash
    set -uo pipefail
    pat="SPINCLASS_SESSION_ID={{ key }}"
    for attempt in 1 2 3 4 5 6; do
      left=""
      for d in /proc/[0-9]*; do
        pid=${d#/proc/}
        [ "$pid" = "$$" ] && continue
        { tr '\0' '\n' < "$d/environ"; } 2>/dev/null | grep -xF -e "$pat" >/dev/null || continue
        left="$left $pid:$(tr '\0' ' ' < "$d/cmdline" 2>/dev/null | cut -c1-80)"$'\n'
      done
      [ -z "$left" ] && { echo "PASS    no process carries $pat"; exit 0; }
      sleep 2
    done
    printf 'FAIL    processes of the closed session survive:\n%s' "$left"
    exit 1

# [explore] PoC for the #449 review's F1 theory: does the guarded gate's default
# command (`nix flake check --no-update-lock-file`, under the gate's scrubbed env
# and pinned NIX_CONFIG) fetch a flake INPUT that names a path outside the build
# worktree into the world-readable /nix/store before it refuses the stale lock?
# Uses scratch flakes in a temp dir and a DUMMY marker file as the "secret",
# a fresh marker and store snapshot per variant; never a real secret. Variants:
# no lock; a worker-committed lock with a wrong narHash, unused and used by an
# output; and the review's F1' shapes, where the lock is complete for what it
# names but flake.nix declares an input the lock lacks that carries ?narHash=
# (a path: input, a tarball+file: input, and the same one level down in a
# git+file: subflake), inputs that differ between an impure enumeration and
# pure evaluation (nix refuses a non-literal inputs set before any fetch), and
# the third review's F1'' shapes (a lock with a remote node under the name
# flake.nix declares as path:, and a root override of a locked subflake's
# remote input to path:), and the same with nix's own allowed-uris limited to
# https:// (it does not stop the lock-time path: fetch). Reports per variant
# whether a store path added during the run holds the marker. Also prints the
# daemon's sandbox setting.
# Read-only apart from the temp dir and whatever nix adds to the store.
#
# PoC: does the guarded gate copy an out-of-tree flake input into /nix/store? (dummy marker only)
[group("explore")]
explore-guarded-gate-input-leak:
    #!/usr/bin/env bash
    set -uo pipefail
    work=$(mktemp -d)
    trap 'rm -rf "$work"' EXIT
    gatepath="$(dirname "$(command -v nix)"):$(dirname "$(command -v git)"):$(dirname "$(command -v sh)")"
    sys=$(nix eval --impure --raw --expr builtins.currentSystem 2>/dev/null || echo x86_64-linux)
    badhash="sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
    rootonly='{ "nodes": { "root": { } }, "root": "root", "version": 7 }'
    commit() { git -C "$1" add -A; git -C "$1" -c user.name=t -c user.email=t@t commit -qm "$2" >/dev/null; }
    # touches NAME: an outputs expression whose check forces input NAME.
    touches() { printf '{ checks.%s.touch = derivation { name = "touch"; system = "%s"; builder = "/bin/sh"; args = [ "-c" "echo ${%s} > $out" ]; }; }' "$sys" "$sys" "$1"; }
    extra_nix_config=""   # a variant may append to the gate's NIX_CONFIG
    gate() {  # $1: flake dir
      env -i PATH="$gatepath" HOME="$HOME" ${NIX_REMOTE:+NIX_REMOTE="$NIX_REMOTE"} \
        NIX_CONFIG=$'allow-unsafe-native-code-during-evaluation = false\naccept-flake-config = false\nsandbox = true\n'"$extra_nix_config" \
        GIT_ALLOW_PROTOCOL=file:https SPINCLASS_GUARDED_GATE=1 \
        sh -c 'cd "$1" && nix flake check --no-update-lock-file' sh "$1" 2>&1 | tail -6
      echo "exit: ${PIPESTATUS[0]}"
    }
    results=""
    # variant NAME DESC: a fresh marker dir and token, a fresh flake; setup_NAME
    # FLAKE OUTSIDE writes the flake; then the gate runs and every store path
    # added meanwhile is searched for the token.
    variant() {
      local name=$1 desc=$2 outside flake token leaked=0 p
      outside="$work/$name-outside"; mkdir -p "$outside"
      token="SPINCLASS-DUMMY-MARKER-$name-$(date +%s)-$RANDOM"
      printf '%s\n' "$token" > "$outside/marker.txt"
      flake="$work/$name-flake"; mkdir -p "$flake"; git -C "$flake" init -q
      "setup_$name" "$flake" "$outside"
      ls /nix/store > "$work/$name-before"
      echo "== $name: $desc"; gate "$flake"
      ls /nix/store > "$work/$name-after"
      while IFS= read -r p; do
        p="/nix/store/$p"
        if [ -f "$p/marker.txt" ] && grep -qxF "$token" "$p/marker.txt"; then echo "LEAKED: $p ($(stat -c %a "$p"))"; leaked=1
        elif [ -f "$p" ] && grep -qF "$token" "$p" 2>/dev/null; then echo "LEAKED (file): $p"; leaked=1; fi
      done < <(comm -13 "$work/$name-before" "$work/$name-after")
      [ "$leaked" = 0 ] && echo "NOT IN STORE"
      results="$results$name leaked=$leaked"$'\n'
    }
    setup_nolock() { printf '{ inputs.leak = { url = "path:%s"; flake = false; }; outputs = { self, leak }: %s; }\n' "$2" "$(touches leak)" > "$1/flake.nix"; commit "$1" flake; }
    setup_wronghash_unused() {
      printf '{ inputs.leak = { url = "path:%s"; flake = false; }; outputs = { self, leak }: { checks = { }; }; }\n' "$2" > "$1/flake.nix"
      printf '{ "nodes": { "leak": { "flake": false, "locked": { "lastModified": 1, "narHash": "%s", "path": "%s", "type": "path" }, "original": { "path": "%s", "type": "path" } }, "root": { "inputs": { "leak": "leak" } } }, "root": "root", "version": 7 }\n' "$badhash" "$2" "$2" > "$1/flake.lock"
      commit "$1" flake
    }
    setup_wronghash_used() {
      setup_wronghash_unused "$1" "$2"
      printf '{ inputs.leak = { url = "path:%s"; flake = false; }; outputs = { self, leak }: %s; }\n' "$2" "$(touches leak)" > "$1/flake.nix"; commit "$1" flake
    }
    setup_f1prime_path() {
      printf '{ inputs.leak = { url = "path:%s?narHash=%s"; flake = false; }; outputs = { self, leak }: %s; }\n' "$2" "$badhash" "$(touches leak)" > "$1/flake.nix"
      printf '%s\n' "$rootonly" > "$1/flake.lock"; commit "$1" flake
    }
    setup_f1prime_tarball() {
      tar -cf "$2.tar" -C "$2" marker.txt
      printf '{ inputs.leak = { url = "tarball+file://%s.tar?narHash=%s"; flake = false; }; outputs = { self, leak }: %s; }\n' "$2" "$badhash" "$(touches leak)" > "$1/flake.nix"
      printf '%s\n' "$rootonly" > "$1/flake.lock"; commit "$1" flake
    }
    setup_f1prime_transitive() {
      local sub="$1-sub" meta
      mkdir -p "$sub"; git -C "$sub" init -q
      printf '{ inputs.leak = { url = "path:%s?narHash=%s"; flake = false; }; outputs = { self, leak }: %s; }\n' "$2" "$badhash" "$(touches leak)" > "$sub/flake.nix"
      commit "$sub" sub
      meta=$(nix flake prefetch --json "git+file://$sub" 2>/dev/null)
      local hash rev
      hash=$(nix eval --impure --raw --expr "(builtins.fromJSON ''$meta'').hash")
      rev=$(nix eval --impure --raw --expr "(builtins.fromJSON ''$meta'').locked.rev")
      printf '{ inputs.sub.url = "git+file://%s"; outputs = { self, sub }: { checks.%s.touch = sub.checks.%s.touch; }; }\n' "$sub" "$sys" "$sys" > "$1/flake.nix"
      printf '{ "nodes": { "sub": { "locked": { "lastModified": 1, "narHash": "%s", "ref": "refs/heads/master", "rev": "%s", "type": "git", "url": "file://%s" }, "original": { "type": "git", "url": "file://%s" } }, "root": { "inputs": { "sub": "sub" } } }, "root": "root", "version": 7 }\n' "$hash" "$rev" "$sub" "$sub" > "$1/flake.lock"
      commit "$1" flake
    }
    # The second review's C: inputs that differ between an impure, restricted
    # enumeration and nix's own pure evaluation of flake.nix.
    setup_conditional_getenv() {
      printf '{ inputs = if builtins.getEnv "HOME" == "" then { leak = { url = "path:%s?narHash=%s"; flake = false; }; } else { }; outputs = args: { checks.%s.touch = derivation { name = "touch"; system = "%s"; builder = "/bin/sh"; args = [ "-c" "echo ${args.leak or "none"} > $out" ]; }; }; }\n' "$2" "$badhash" "$sys" "$sys" > "$1/flake.nix"
      printf '%s\n' "$rootonly" > "$1/flake.lock"; commit "$1" flake
    }
    setup_conditional_currenttime() {
      printf '{ inputs = if (builtins.tryEval (builtins.currentTime or (throw "none"))).success then { } else { leak = { url = "path:%s?narHash=%s"; flake = false; }; }; outputs = args: { checks.%s.touch = derivation { name = "touch"; system = "%s"; builder = "/bin/sh"; args = [ "-c" "echo ${args.leak or "none"} > $out" ]; }; }; }\n' "$2" "$badhash" "$sys" "$sys" > "$1/flake.nix"
      printf '%s\n' "$rootonly" > "$1/flake.lock"; commit "$1" flake
    }
    # The third review's F1'': completeness judged on names. The lock has a
    # remote node named leak; flake.nix redeclares leak as a path input.
    setup_namematch() {
      printf '{ inputs.leak = { url = "path:%s?narHash=%s"; flake = false; }; outputs = { self, leak }: %s; }\n' "$2" "$badhash" "$(touches leak)" > "$1/flake.nix"
      printf '{ "nodes": { "leak": { "flake": false, "locked": { "lastModified": 1, "narHash": "sha256-x", "owner": "NixOS", "repo": "nix", "rev": "0000000000000000000000000000000000000000", "type": "github" }, "original": { "owner": "NixOS", "repo": "nix", "type": "github" } }, "root": { "inputs": { "leak": "leak" } } }, "root": "root", "version": 7 }\n' > "$1/flake.lock"
      commit "$1" flake
    }
    # Same, through a nested override: the root lock names a git+file: subflake
    # whose own input sub is remote and locked; the root overrides sub's input
    # to a path.
    setup_nested_override() {
      local sub="$1-sub" meta hash rev
      mkdir -p "$sub"; git -C "$sub" init -q
      printf '{ inputs.leak = { url = "github:NixOS/nix"; flake = false; }; outputs = { self, leak }: %s; }\n' "$(touches leak)" > "$sub/flake.nix"
      printf '{ "nodes": { "leak": { "flake": false, "locked": { "lastModified": 1, "narHash": "sha256-x", "owner": "NixOS", "repo": "nix", "rev": "0000000000000000000000000000000000000000", "type": "github" }, "original": { "owner": "NixOS", "repo": "nix", "type": "github" } }, "root": { "inputs": { "leak": "leak" } } }, "root": "root", "version": 7 }\n' > "$sub/flake.lock"
      commit "$sub" sub
      meta=$(nix flake prefetch --json "git+file://$sub" 2>/dev/null)
      hash=$(nix eval --impure --raw --expr "(builtins.fromJSON ''$meta'').hash")
      rev=$(nix eval --impure --raw --expr "(builtins.fromJSON ''$meta'').locked.rev")
      printf '{ inputs.sub.url = "git+file://%s"; inputs.sub.inputs.leak.url = "path:%s?narHash=%s"; outputs = { self, sub }: { checks.%s.touch = sub.checks.%s.touch; }; }\n' "$sub" "$2" "$badhash" "$sys" "$sys" > "$1/flake.nix"
      printf '{ "nodes": { "leak": { "flake": false, "locked": { "lastModified": 1, "narHash": "sha256-x", "owner": "NixOS", "repo": "nix", "rev": "0000000000000000000000000000000000000000", "type": "github" }, "original": { "owner": "NixOS", "repo": "nix", "type": "github" } }, "sub": { "inputs": { "leak": "leak" }, "locked": { "lastModified": 1, "narHash": "%s", "ref": "refs/heads/master", "rev": "%s", "type": "git", "url": "file://%s" }, "original": { "type": "git", "url": "file://%s" } }, "root": { "inputs": { "sub": "sub" } } }, "root": "root", "version": 7 }\n' "$hash" "$rev" "$sub" "$sub" > "$1/flake.lock"
      commit "$1" flake
    }
    setup_nolock_alloweduris() { setup_nolock "$1" "$2"; }
    setup_f1prime_path_alloweduris() { setup_f1prime_path "$1" "$2"; }
    echo "== daemon sandbox setting (nix config show)"; nix config show sandbox 2>/dev/null || nix show-config 2>/dev/null | grep '^sandbox'
    variant nolock "no flake.lock committed, an output references the path input"
    variant wronghash_unused "worker-committed lock with a wrong narHash, outputs never touch the input"
    variant wronghash_used "same wrong-narHash lock, an output references the input"
    variant f1prime_path "lock complete for what it names; flake.nix declares path:...?narHash= the lock lacks, referenced"
    variant f1prime_tarball "lock complete; flake.nix declares tarball+file:...?narHash= the lock lacks, referenced"
    variant f1prime_transitive "root lock names a git+file: subflake; the subflake declares path:...?narHash= its lock lacks"
    variant conditional_getenv "inputs declared only when getEnv HOME is empty (pure eval), referenced"
    variant conditional_currenttime "inputs declared only when currentTime is unavailable (pure eval), referenced"
    variant namematch "lock has a remote node named leak; flake.nix redeclares leak as path:...?narHash=, referenced"
    variant nested_override "root lock complete; root flake.nix overrides a locked subflake's remote input to path:...?narHash="
    # Does nix's own allowed-uris (restricted or pure mode) stop the lock-time
    # fetch of a path: input? If so a NIX_CONFIG line closes the class.
    extra_nix_config=$'allowed-uris = https://\n'
    variant nolock_alloweduris "no lock, path input referenced, NIX_CONFIG allowed-uris = https:// only"
    variant f1prime_path_alloweduris "F1' path input, NIX_CONFIG allowed-uris = https:// only"
    extra_nix_config=""
    printf '== SUMMARY\n%s' "$results"

# [explore] PoC for the guarded gate's lock policy (#449 F1', F1''): the
# `nix eval` the gate uses to read the inputs flake.nix declares, run against
# hostile flake.nix shapes (readFile of a dummy marker outside the flake and
# inside it, getEnv, readDir, pathExists, currentSystem, an attrName computed
# from the marker, fetchurl and fetchGit with hashes, storePath, infinite
# recursion, a huge list), each in its own temp dir, under the gate's scrubbed
# env, in one or more evaluation modes (the ones that cannot import a file at
# all on this nix are kept for the record; restrict-stdin is what the gate
# runs). Then the gate's canonicalisation of every input (the `canon` apply
# expression, a copy of lockpolicy.go's) against url, attribute, indirect,
# follows, nested, path, relative, mixed and string specs, the same inside a
# systemd scope with MemoryMax, and side by side with the `original` a real
# `nix flake lock` writes for local dummy inputs. Prints per shape: exit
# code, whether the marker's content showed up in stdout or stderr, wall
# time, output size. Dummy marker only.
#
# PoC: is the guarded gate's inputs enumeration pure and bounded on this nix? (dummy marker only)
[group("explore")]
explore-guarded-gate-inputs-eval:
    #!/usr/bin/env bash
    set -uo pipefail
    work=$(mktemp -d)
    trap 'rm -rf "$work"' EXIT
    gatepath="$(dirname "$(command -v nix)"):$(dirname "$(command -v git)"):$(dirname "$(command -v sh)")"
    secret="DUMMY-MARKER-NOT-A-SECRET-$RANDOM"
    marker="$work/marker"; printf '%s' "$secret" > "$marker"
    export LEAK_ME="$secret"
    # modeargs MODE DIR: how the flake.nix is handed to nix eval. `-f` refuses
    # --pure-eval, so the candidates are --expr with an absolute import under
    # pure eval, an -I search-path entry under pure eval, and restrict-eval
    # (no allowed URIs) with -f or -I.
    modeargs() {
      case "$1" in
        pure-expr) printf '%s\n' --pure-eval --expr "import $2/flake.nix" ;;
        pure-I) printf '%s\n' --pure-eval -I "f=$2" --expr 'import <f>/flake.nix' ;;
        restrict-f) printf '%s\n' --option restrict-eval true --option allowed-uris "" -f ./flake.nix ;;
        restrict-I) printf '%s\n' --option restrict-eval true --option allowed-uris "" -I "f=$2" --expr 'import <f>/flake.nix' ;;
        # What flakes do: the file is copied into the store first, and pure
        # evaluation imports the store path (store paths are always readable).
        pure-store) local sp; sp=$(nix store add --mode flat "$2/flake.nix" 2>/dev/null) || sp=$(nix store add-file "$2/flake.nix")
                    printf '%s\n' --pure-eval --option allowed-uris "" --expr "import $sp" ;;
        # restrict-eval allows the Nix search path: the store copy as the only
        # entry, no URIs, and every other absolute path refused.
        restrict-store) local sp; sp=$(nix store add --mode flat "$2/flake.nix" 2>/dev/null) || sp=$(nix store add-file "$2/flake.nix")
                    printf '%s\n' --option restrict-eval true --option allowed-uris "" -I "f=$sp" --expr 'import <f>' ;;
        # The new CLI's nix eval is pure by default and refuses every file
        # import; --impure with restrict-eval keeps reads to the search path
        # (the store copy) and the store, and allowed-uris "" forbids fetches.
        restrict-impure) local sp; sp=$(nix store add --mode flat "$2/flake.nix" 2>/dev/null) || sp=$(nix store add-file "$2/flake.nix")
                    printf '%s\n' --impure --option restrict-eval true --option allowed-uris "" -I "f=$sp" --expr 'import <f>' ;;
        # The content on stdin: no temporary file and no store copy at all
        # (the third review's A(b)); the base directory of a relative path
        # literal is the working directory, which restricted mode refuses.
        restrict-stdin) printf '%s\n' --impure --option restrict-eval true --option allowed-uris "" -f - ;;
      esac
    }
    # The lock policy's canonicalisation (lockpolicy.go, declaredInputsApply;
    # keep the two in step): every input's spec as nix would read it, the
    # url form through builtins.parseFlakeRef, nested overrides included.
    canon='f: let
      canon = nested: name: spec:
        if !(builtins.isAttrs spec) then throw "input ${name} is not an attribute set"
        else let
          rest = removeAttrs spec [ "inputs" "flake" "follows" ];
          inheritsRef = nested && !(spec ? follows) && !(spec ? type) && !(spec ? url);
        in {
          original =
            if spec ? follows then (if rest == { } then null else throw "input ${name} mixes follows with a reference")
            else if spec ? type then rest
            else if spec ? url then (if builtins.attrNames rest == [ "url" ] then builtins.parseFlakeRef spec.url else throw "input ${name} mixes url with other attributes")
            else if inheritsRef then (if rest == { } then null else throw "input ${name} mixes overrides with a reference")
            else { type = "indirect"; id = name; };
          inherits = inheritsRef;
          flake = spec.flake or true;
          follows = spec.follows or null;
          inputs = builtins.mapAttrs (canon true) (spec.inputs or { });
        };
      in builtins.mapAttrs (canon false) (f.inputs or { })'
    run() {  # $1 mode, $2 name, $3 flake.nix body, $4 optional --apply expression
      local dir="$work/$1-$2" out start end names='f: builtins.attrNames (f.inputs or { })' apply
      apply="${4:-$names}"
      mkdir -p "$dir"; printf '%s\n' "$3" > "$dir/flake.nix"
      # The eval output goes to a file, never a shell variable: a multi-megabyte
      # result (huge-attrs) in a variable made every command after it fail with
      # "Argument list too long" and the marker check with it.
      out="$dir/out"
      local -a args; mapfile -t args < <(modeargs "$1" "$dir")
      start=$(date +%s)
      (cd "$dir" && env -i PATH="$gatepath" HOME="$HOME" LEAK_ME="$secret" ${NIX_REMOTE:+NIX_REMOTE="$NIX_REMOTE"} \
        NIX_CONFIG=$'allow-unsafe-native-code-during-evaluation = false\naccept-flake-config = false\nsandbox = true\n' \
        timeout 20 nix eval --json --option allow-import-from-derivation false --option max-call-depth 2000 \
          "${args[@]}" --apply "$apply" < "$dir/flake.nix" 2>&1 | head -c 2000000 > "$out"; exit "${PIPESTATUS[0]}"); code=$?
      end=$(date +%s)
      leaked=no; grep -qF "$secret" "$out" && leaked=YES
      printf '%-16s %-20s exit=%-3s marker-in-output=%-3s %ss bytes=%s  %s\n' "$1" "$2" "$code" "$leaked" "$((end - start))" "$(wc -c < "$out")" "$(head -c 300 "$out" | tr '\n' ' ')"
    }
    for mode in restrict-impure restrict-stdin; do
      run "$mode" honest '{ inputs.a.url = "github:x/y"; inputs.b = { url = "github:x/z"; flake = false; }; outputs = _: {}; }'
      run "$mode" throw-readfile "{ inputs = throw (builtins.readFile $marker); outputs = _: {}; }"
      run "$mode" attrname-from-file "{ inputs = { \${builtins.readFile $marker} = { }; }; outputs = _: {}; }"
      run "$mode" getenv '{ inputs = { ${builtins.getEnv "LEAK_ME"} = { }; }; outputs = _: {}; }'
      run "$mode" readdir-outside "{ inputs = builtins.readDir $work; outputs = _: {}; }"
      run "$mode" currentsystem '{ inputs = { ${builtins.currentSystem} = { }; }; outputs = _: {}; }'
      run "$mode" pathexists "{ inputs = { \${toString (builtins.pathExists $marker)} = { }; }; outputs = _: {}; }"
      run "$mode" readfile-in-flake '{ inputs = { ${builtins.readFile ./flake.nix} = { }; }; outputs = _: {}; }'
    done
    run restrict-impure fetchurl '{ inputs = { ${builtins.readFile (builtins.fetchurl { url = "file:///etc/hostname"; sha256 = "0000000000000000000000000000000000000000000000000000"; })} = { }; }; outputs = _: {}; }'
    run restrict-impure fetchgit '{ inputs = { ${builtins.readFile ((builtins.fetchGit { url = "file:///home"; rev = "0000000000000000000000000000000000000000"; }) + "/x")} = { }; }; outputs = _: {}; }'
    run restrict-impure storepath '{ inputs = { ${toString (builtins.storePath /nix/store)} = { }; }; outputs = _: {}; }'
    run restrict-impure recursion '{ inputs = let f = x: f (x + 1); in f 0; outputs = _: {}; }'
    run restrict-impure huge-attrs '{ inputs = builtins.listToAttrs (map (i: { name = toString i; value = { }; }) (builtins.genList (i: i) 10000000)); outputs = _: {}; }'
    # The canonicalisation against the spec shapes flake.nix can hold (the
    # third review's F1''): compare each `original` with what `nix flake
    # lock` writes for the same spec.
    # What lockpolicy.go runs: the same evaluation inside a transient systemd
    # scope with a memory bound (stdin must pass through systemd-run --scope,
    # and MemoryMax must be accepted on a scope unit).
    echo "== scoped (systemd-run --user --scope --property=MemoryMax=2G, stdin)"
    d="$work/scoped"; mkdir -p "$d"; printf '{ inputs.a.url = "github:x/y"; outputs = _: {}; }\n' > "$d/flake.nix"
    (cd "$d" && env -i PATH="$gatepath:$(dirname "$(command -v systemd-run)")" HOME="$HOME" ${NIX_REMOTE:+NIX_REMOTE="$NIX_REMOTE"} \
      XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-}" DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-}" \
      NIX_CONFIG=$'allow-unsafe-native-code-during-evaluation = false\naccept-flake-config = false\nsandbox = true\n' \
      systemd-run --user --scope --quiet --unit="spinclass-inputs-probe-$$" --property=KillMode=control-group --property=MemoryMax=2G -- \
      nix eval --json --impure --option restrict-eval true --option allowed-uris "" --option allow-import-from-derivation false --option max-call-depth 2000 \
      -f - --apply "$canon" < flake.nix 2>&1 | head -c 600; echo " exit=${PIPESTATUS[0]}")
    echo "== canonicalisation (restrict-stdin)"
    run restrict-stdin canon-url '{ inputs.a.url = "github:x/y?ref=main"; outputs = _: {}; }' "$canon"
    run restrict-stdin canon-attrs '{ inputs.a = { type = "github"; owner = "x"; repo = "y"; }; outputs = _: {}; }' "$canon"
    run restrict-stdin canon-indirect '{ inputs.a = { }; inputs.b.url = "nixpkgs"; outputs = _: {}; }' "$canon"
    run restrict-stdin canon-follows '{ inputs.a.url = "github:x/y"; inputs.a.inputs.n.follows = "b"; inputs.b.url = "github:x/z"; outputs = _: {}; }' "$canon"
    run restrict-stdin canon-nested '{ inputs.a.url = "github:x/y"; inputs.a.inputs.n.url = "path:/nowhere?narHash=sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="; outputs = _: {}; }' "$canon"
    # A nested override that names no reference (clown's tommy.inputs.tap.inputs.X.follows):
    # inherits=true for tap and gomod2nix-style follows below it, never indirect.
    run restrict-stdin canon-inherit '{ inputs.a.url = "github:x/y"; inputs.a.inputs.t.inputs.g.follows = "b"; inputs.b.url = "github:x/z"; outputs = _: {}; }' "$canon"
    run restrict-stdin canon-path '{ inputs.a = { url = "path:/nowhere"; flake = false; }; outputs = _: {}; }' "$canon"
    run restrict-stdin canon-relative '{ inputs.a.url = "./sub"; outputs = _: {}; }' "$canon"
    run restrict-stdin canon-git-file '{ inputs.a.url = "git+file:///nowhere"; outputs = _: {}; }' "$canon"
    run restrict-stdin canon-mixed '{ inputs.a = { url = "github:x/y"; owner = "z"; }; outputs = _: {}; }' "$canon"
    run restrict-stdin canon-string '{ inputs.a = "github:x/y"; outputs = _: {}; }' "$canon"
    run restrict-stdin canon-tarball '{ inputs.a.url = "https://example.invalid/x.tar.gz?narHash=sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="; outputs = _: {}; }' "$canon"
    # What nix writes as `original` for the same specs, from a real lock of a
    # flake whose inputs are local dummies (no network): the url form, the
    # attribute form, a git+file: with ?dir=, a nested override, and a
    # follows. Then the canonicalisation of that same flake.nix, side by side.
    echo "== nix flake lock original for comparison (local dummy inputs)"
    d="$work/lockshape"; mkdir -p "$d/sub" "$d/gsub/d"; git -C "$d" init -q; git -C "$d/gsub" init -q
    printf '{ outputs = _: {}; }\n' > "$d/sub/flake.nix"
    printf '{ inputs.n.url = "path:%s"; outputs = _: {}; }\n' "$d/sub" > "$d/gsub/d/flake.nix"
    git -C "$d/gsub" add -A; git -C "$d/gsub" -c user.name=t -c user.email=t@t commit -qm g
    printf '{ inputs.a.url = "path:%s"; inputs.b = { type = "path"; path = "%s"; flake = false; }; inputs.c.url = "git+file://%s?dir=d"; inputs.c.inputs.n.url = "path:%s?narHash=%s"; inputs.e.follows = "a"; outputs = _: {}; }\n' \
      "$d/sub" "$d/sub" "$d/gsub" "$d/sub" "$(nix hash path "$d/sub")" > "$d/flake.nix"
    git -C "$d" add -A
    (cd "$d" && nix flake lock 2>&1 | tail -3; jq -c '.nodes | to_entries[] | {(.key): {original: .value.original, inputs: .value.inputs, flake: .value.flake}}' flake.lock 2>/dev/null)
    run restrict-stdin canon-lockshape "$(cat "$d/flake.nix")" "$canon"

# [debug] Block for `seconds` doing nothing. NOT a hang fixture (drill 3,
# 2026-10-27): called through run_recipe the harness returns control after 120 s
# ("moved to the background"), the model's turn ENDS and the session is idle with a
# background task, so session-health reads ok/idle and restart-hung-child refuses
# ("not hung"). Useful only as a harmless long-running recipe; see
# docs/plans/2026-10-26-live-proofs-plan.md item 5 for drills that do stall a turn.
#
# sleep for N seconds (a deliberately stalled tool call for the hung-session drill)
[group("debug")]
debug-sleep seconds='900':
    sleep {{ seconds }}

# Key holder probe A (docs: the per-session key holder design note): start a throwaway fibby HOLDER build plus
# `piggy agent --holder` (scope `any`) under .tmp, mint a key (no card, no PIN: a virtual card), list it with
# ssh-add -L, sign and verify one message, then test whether a certificate from a throwaway CA can be offered
# through the holder and verified the way hogtie verifies (`-Y verify ... namespaces="hogtie-v1"`). Prints one
# `probe:` line per step with its exit status and never touches a real card or the operator's agent. FIBBY and
# PIGGY default to the store paths found on this host; TIMEOUT bounds every external command (seconds).
#
# probe a throwaway fibby holder plus piggy holder agent: mint, sign, certificate offer, verify (no card)
[group("debug")]
debug-keyholder-probe fibby="" piggy="" timeout="60":
    #!/usr/bin/env bash
    set -uo pipefail
    export LC_ALL=C
    to() { timeout "{{ timeout }}" "$@"; }
    say() { printf 'probe: %s\n' "$*"; }
    piggy="{{ piggy }}"; [ -n "$piggy" ] || piggy="$(command -v piggy)" || { say "no piggy on PATH"; exit 1; }
    fibby="{{ fibby }}"
    if [ -z "$fibby" ]; then
      for c in /nix/store/*-fibby-holder-0.1.0/bin/fibby; do fibby="$c"; done
    fi
    [ -x "$fibby" ] || { say "no fibby holder build found"; exit 1; }
    say "piggy=$piggy ($("$piggy" --version 2>&1 | head -1))"
    say "fibby=$fibby"
    base="$(mktemp -d "$PWD/.tmp/kh-probe.XXXXXX")"
    chmod 0700 "$base"
    mkdir -m 0700 "$base/fibby" "$base/agent" "$base/work"
    ctl="$base/fibby/ctl.sock"; pcsc="$base/fibby/pcscd.comm"; agent="$base/agent/agent.sock"
    # a PIN prompt anywhere is a failure, not a question
    printf '#!/bin/sh\necho "REFUSING to prompt" >&2\nexit 1\n' >"$base/askpass"; chmod 0700 "$base/askpass"
    export SSH_ASKPASS="$base/askpass" SSH_ASKPASS_REQUIRE=force DISPLAY="" PCSCLITE_CSOCK_NAME="$pcsc"
    unset WAYLAND_DISPLAY SSH_AUTH_SOCK PIGGY_AUTH_SOCK
    pids=()
    cleanup() {
      kill "${pids[@]}" 2>/dev/null || true
      sleep 1
      left="$(pgrep -f "$base" || true)"
      if [ -n "$left" ]; then say "LEFTOVER processes naming $base: $left"; else say "no leftover process names $base"; fi
      rm -rf "$base"
    }
    trap cleanup EXIT
    "$fibby" --socket "$pcsc" --control-socket "$ctl" --model yk5.7 >"$base/fibby.log" 2>&1 &
    pids+=($!)
    for _ in $(seq 1 50); do [ -S "$ctl" ] && [ -S "$pcsc" ] && break; sleep 0.1; done
    say "fibby profile: $(to "$fibby" ctl --socket "$ctl" profile 2>&1)"
    to "$fibby" ctl --socket "$ctl" destroy "Virtual PCD piggy fibby 00 00" >/dev/null 2>&1 || true
    "$piggy" agent --holder --holder-fibby-ctl "$ctl" --scope probe-v1=any -a "$agent" >"$base/agent.log" 2>&1 &
    pids+=($!)
    for _ in $(seq 1 50); do [ -S "$agent" ] && break; sleep 0.1; done
    [ -S "$agent" ] || { say "agent socket never appeared"; sed 's/^/agent.log: /' "$base/agent.log"; exit 1; }
    say "socket dirs: fibby $(stat -c '%a %U' "$base/fibby") agent $(stat -c '%a %U' "$base/agent") socket $(stat -c '%a' "$agent")"
    export SSH_AUTH_SOCK="$agent"
    cd "$base/work"
    to "$piggy" holder mint --scope probe-v1 >holder.pub 2>mint.err; rc=$?
    say "mint rc=$rc pub: $(cat holder.pub) err: $(head -c 300 mint.err)"
    [ "$rc" = 0 ] || { sed 's/^/agent.log: /' "$base/agent.log"; exit 1; }
    say "ssh-add -L: $(to ssh-add -L 2>&1)"
    say "fingerprint: $(ssh-keygen -lf holder.pub 2>&1)"
    say "public key line: $(awk '{print NF " fields, type=" $1 ", comment=" $3}' holder.pub)"
    echo "probe-a message" >msg
    to ssh-keygen -Y sign -U -f holder.pub -n probe msg >sign.out 2>&1; say "sign (plain key) rc=$? $(head -c 200 sign.out)"
    read -r t k _ <holder.pub; printf 'probe %s %s\n' "$t" "$k" >signers
    to ssh-keygen -Y verify -f signers -I probe -n probe -s msg.sig <msg >verify.out 2>&1; say "verify (plain key) rc=$? $(head -c 200 verify.out)"
    # certificate: a throwaway CA, a certificate for the holder's key
    ssh-keygen -q -t ed25519 -N '' -f ca -C probe-ca >/dev/null
    ssh-keygen -q -s ca -I probe-cert -n flic:restic-snapshots -V +1h holder.pub 2>cert.err; say "cert sign rc=$? $(head -c 200 cert.err)"
    ssh-keygen -L -f holder-cert.pub 2>&1 | sed -n 's/^ *\(Type\|Public key\|Key ID\|Valid\|Principals\).*/probe: cert: &/p'
    say "ssh-add of the certificate: $(to ssh-add holder-cert.pub 2>&1) rc=$?"
    say "ssh-add -L after: $(to ssh-add -L 2>&1)"
    echo "probe-a hogtie message" >hmsg
    to ssh-keygen -Y sign -U -f holder-cert.pub -n hogtie-v1 hmsg >csign.out 2>&1; crc=$?
    say "sign with the certificate (-U -f holder-cert.pub) rc=$crc $(head -c 300 csign.out)"
    printf '* cert-authority,namespaces="hogtie-v1" %s\n' "$(cut -d' ' -f1,2 ca.pub)" >allowed
    to ssh-keygen -Y verify -f allowed -I flic:restic-snapshots -n hogtie-v1 -s hmsg.sig <hmsg >cverify.out 2>&1
    say "verify like hogtie (cert-authority, -I host:action, namespace hogtie-v1) rc=$? $(head -c 300 cverify.out)"
    if [ -s hmsg.sig ]; then
      say "signature embeds: $(ssh-keygen -Y check-novalidate -n hogtie-v1 -s hmsg.sig <hmsg 2>&1 | head -c 200)"
    fi
    say "agent.log tail:"; tail -n 8 "$base/agent.log" | sed 's/^/probe:   /'

# Print what ssh-keygen -L and -l say about a certificate and its plain key (throwaway CA under .tmp, no holder, no card):
# the fixture shape `sc session-key` parses (K4). Read-only apart from its own scratch directory.
#
# print ssh-keygen -L / -l output for a throwaway certificate
[group("explore")]
explore-cert-format:
    #!/usr/bin/env bash
    set -uo pipefail
    export LC_ALL=C
    d="$(mktemp -d "$PWD/.tmp/cert-format.XXXXXX")"; chmod 0700 "$d"; trap 'rm -rf "$d"' EXIT
    cd "$d"
    ssh-keygen -q -t ed25519 -N '' -f ca -C test-ca >/dev/null
    ssh-keygen -q -t ed25519 -N '' -f key -C 'holder:test 1' >/dev/null
    ssh-keygen -q -s ca -I id-1 -n flic:restic-snapshots -V +1h key.pub
    echo "== TZ=$(date +%Z)"; echo "== ssh-keygen -L -f key-cert.pub"; ssh-keygen -L -f key-cert.pub
    echo "== ssh-keygen -lf key-cert.pub"; ssh-keygen -lf key-cert.pub
    echo "== ssh-keygen -lf key.pub"; ssh-keygen -lf key.pub
    echo "== cert.pub first field and size"; cut -d' ' -f1 key-cert.pub; wc -c <key-cert.pub

# Key holder live proof (item A): build this tree's spinclass, start ONE real session pair (fibby + holder as
# transient `systemd-run --user` units) through the hidden `sc keyholder-pair`, check `ssh-add -L` against its
# socket lists exactly one key and `sc session-key`-style facts hold, stop the pair, and assert zero leftover units,
# processes and directories. Refuses while the host's per-cpu 1-minute load is 1.0 or more (one real run at a time).
# Uses a virtual card inside fibby: no real card, no PIN, and the operator's agent is never inherited.
#
# start, list, stop one real key holder pair and assert nothing is left behind
[group("debug")]
debug-keyholder-pair-live fibby="" piggy="" session="live/keyholder-proof":
    #!/usr/bin/env bash
    set -uo pipefail
    export LC_ALL=C
    say() { printf 'live: %s\n' "$*"; }
    load="$(cut -d' ' -f1 /proc/loadavg)"; cpus="$(nproc)"
    if awk -v l="$load" -v n="$cpus" 'BEGIN{exit !(l/n >= 1.0)}'; then say "REFUSED: load $load over $cpus cpus is per-cpu >= 1.0"; exit 1; fi
    # `.#default` carries the tool pins (systemd-run, systemctl, ssh-add, ssh-keygen); `.#spinclass-native` has none and
    # the key holder refuses to look any of them up on PATH.
    out="$(nix build .#default --no-link --print-out-paths 2>/dev/null | tail -1)"
    sc="$out/bin/spinclass"; [ -x "$sc" ] || { say "no spinclass built at $out"; exit 1; }
    piggy="{{ piggy }}"; [ -n "$piggy" ] || piggy="$(command -v piggy)" || { say "no piggy on PATH"; exit 1; }
    fibby="{{ fibby }}"
    if [ -z "$fibby" ]; then for c in /nix/store/*-fibby-holder-0.1.0/bin/fibby; do fibby="$c"; done; fi
    [ -x "$fibby" ] || { say "no fibby holder build found"; exit 1; }
    base="$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/khl.XXXXXX")"; chmod 0700 "$base"
    mkdir -m 0700 "$base/state"
    export XDG_STATE_HOME="$base/state"
    unset SSH_AUTH_SOCK PIGGY_AUTH_SOCK
    s="{{ session }}"
    stopped=0
    # The launcher never looks a program up on PATH: name both explicitly (absolute paths).
    # Without a terminal the command refuses start and stop unless a recipe says it is one (not a boundary: see the manpage).
    pair() { SPINCLASS_KEYHOLDER_PAIR_RECIPE=1 "$sc" keyholder-pair --fibby "$fibby" --piggy "$piggy" "$@"; }
    cleanup() {
      [ "$stopped" = 1 ] || pair --action stop --session "$s" >/dev/null 2>&1 || true
      rm -rf "$base"
    }
    trap cleanup EXIT
    say "spinclass=$sc"; say "state=$XDG_STATE_HOME"
    pair --action start --session "$s" >"$base/start.out" 2>"$base/start.err"; rc=$?
    say "start rc=$rc"; sed 's/^/live:   /' "$base/start.out"; sed 's/^/live:   err: /' "$base/start.err"
    [ "$rc" = 0 ] || exit 1
    sock="$(sed -n 's/^agent_sock //p' "$base/start.out")"
    say "socket mode: $(stat -c '%a %U' "$sock")"
    keys="$(SSH_AUTH_SOCK="$sock" timeout 30 ssh-add -L 2>&1)"; n="$(printf '%s\n' "$keys" | grep -c '^ssh-')"
    say "ssh-add -L lists $n key(s): $keys"
    [ "$n" = 1 ] || { say "FAIL: want exactly one key"; exit 1; }
    pair --action start --session "$s" >"$base/start2.out" 2>&1
    if cmp -s "$base/start.out" "$base/start2.out"; then say "second start: same pair, same key (idempotent)"; else say "FAIL: second start differs"; sed 's/^/live:   /' "$base/start2.out"; exit 1; fi
    say "status: $(pair --action status --session "$s" 2>&1 | head -1)"
    # The holder's unit is bound to fibby's (BindsTo=): stop fibby alone and the agent must go with it.
    fu="$(sed -n 's/^fibby_unit //p' "$base/start.out")"; au="$(sed -n 's/^agent_unit //p' "$base/start.out")"
    systemctl --user stop "$fu" >/dev/null 2>&1 || true
    tied=no; for _ in 1 2 3 4 5 6 7 8 9 10; do systemctl --user is-active --quiet "$au" || { tied=yes; break; }; sleep 0.5; done
    say "fibby stopped alone: the agent unit stopped with it: $tied"
    [ "$tied" = yes ] || { say "FAIL: the agent unit outlived fibby (BindsTo= missing?)"; exit 1; }
    pair --action stop --session "$s"; rc=$?; stopped=1
    say "stop rc=$rc"
    sleep 1
    units="$(systemctl --user list-units --all --no-legend 'spinclass-keyholder-*' 2>&1 | grep -c spinclass-keyholder || true)"
    procs="$(pgrep -f "$base" || true)"
    dirs="$(find "$XDG_STATE_HOME" -mindepth 3 -maxdepth 3 -path '*keyholder/*' 2>/dev/null | wc -l)"
    say "after stop: units=$units processes='${procs}' directories=$dirs"
    if [ "$units" = 0 ] && [ -z "$procs" ] && [ "$dirs" = 0 ]; then say "LIVE-OK one pair started, listed one key, stopped, nothing left"; else say "FAIL: leftovers"; find "$XDG_STATE_HOME" -mindepth 3 -maxdepth 3 -path '*keyholder/*' -printf 'live:   left: %P (%y %m)\n' 2>/dev/null; exit 1; fi

# Regenerate the `sc session-key` golden fixtures from the REAL binary and a REAL pair: a throwaway repo and session
# in an isolated HOME and state dir, one fibby + holder pair (virtual card, no real card, no PIN, a throwaway minted
# key: only its public key and fingerprint appear), then `sc session-key` in both forms. The state directory is
# rewritten to /state in cert_path so the files carry no host path. Writes cmd/spinclass/testdata/session-key.json.golden
# and session-key.txt.golden. Refuses while per-cpu 1-minute load is 1.0 or more; one real pair, torn down afterwards.
#
# capture the real `sc session-key` output into the golden fixtures krusty's installer cites
[group("debug")]
debug-session-key-capture fibby="" piggy="":
    #!/usr/bin/env bash
    set -uo pipefail
    export LC_ALL=C
    say() { printf 'capture: %s\n' "$*"; }
    load="$(cut -d' ' -f1 /proc/loadavg)"; cpus="$(nproc)"
    if awk -v l="$load" -v n="$cpus" 'BEGIN{exit !(l/n >= 1.0)}'; then say "REFUSED: load $load over $cpus cpus is per-cpu >= 1.0"; exit 1; fi
    here="$PWD"
    # `.#default` carries the tool pins (systemd-run, systemctl, ssh-add, ssh-keygen); `.#spinclass-native` has none and
    # the key holder refuses to look any of them up on PATH.
    out="$(nix build .#default --no-link --print-out-paths 2>/dev/null | tail -1)"
    sc="$out/bin/spinclass"; [ -x "$sc" ] || { say "no spinclass built at $out"; exit 1; }
    piggy="{{ piggy }}"; [ -n "$piggy" ] || piggy="$(command -v piggy)" || { say "no piggy on PATH"; exit 1; }
    fibby="{{ fibby }}"
    if [ -z "$fibby" ]; then for c in /nix/store/*-fibby-holder-0.1.0/bin/fibby; do fibby="$c"; done; fi
    [ -x "$fibby" ] || { say "no fibby holder build found"; exit 1; }
    base="$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/skc.XXXXXX")"; chmod 0700 "$base"
    mkdir -m 0700 "$base/state" "$base/home" "$base/config"
    export HOME="$base/home" XDG_STATE_HOME="$base/state" XDG_CONFIG_HOME="$base/config" GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
    unset SSH_AUTH_SOCK PIGGY_AUTH_SOCK SPINCLASS_SESSION_ID CLOWN_SESSION_ID
    pair() { SPINCLASS_KEYHOLDER_PAIR_RECIPE=1 "$sc" keyholder-pair --fibby "$fibby" --piggy "$piggy" "$@"; }
    key=""
    cleanup() { [ -z "$key" ] || pair --action stop --session "$key" >/dev/null 2>&1 || true; rm -rf "$base"; }
    trap cleanup EXIT
    git init -q "$base/repo" && cd "$base/repo" \
      && git -c user.name=capture -c user.email=capture@example.invalid -c commit.gpgsign=false commit -q --allow-empty -m init \
      || { say "could not make the throwaway repo"; exit 1; }
    "$sc" start --no-attach >"$base/start.out" 2>&1 || { say "sc start failed"; cat "$base/start.out"; exit 1; }
    branch="$(ls "$base/repo/.worktrees" | head -1)"; [ -n "$branch" ] || { say "no worktree made"; cat "$base/start.out"; exit 1; }
    key="repo/$branch"
    say "session $key"
    pair --action start --session "$key" >"$base/pair.out" 2>"$base/pair.err" || { say "pair start failed"; cat "$base/pair.err"; exit 1; }
    "$sc" session-key "$key" >"$base/key.txt" 2>"$base/key.txt.err" || { say "session-key failed"; cat "$base/key.txt.err"; exit 1; }
    "$sc" session-key "$branch" --json >"$base/key.json" 2>"$base/key.json.err" || { say "session-key --json failed"; cat "$base/key.json.err"; exit 1; }
    td="$here/cmd/spinclass/testdata"; mkdir -p "$td"
    sed "s#$base/state#/state#g" "$base/key.txt" >"$td/session-key.txt.golden"
    sed "s#$base/state#/state#g" "$base/key.json" >"$td/session-key.json.golden"
    say "wrote $td/session-key.txt.golden ($(wc -l <"$td/session-key.txt.golden") lines) and session-key.json.golden"
    sed 's/^/capture:   txt: /' "$td/session-key.txt.golden"; sed 's/^/capture:   json: /' "$td/session-key.json.golden"
    pair --action stop --session "$key" >/dev/null 2>&1; key=""; sleep 1
    say "after stop: $(systemctl --user list-units --all --no-legend 'spinclass-keyholder-*' 2>&1 | grep -c spinclass-keyholder || true) key holder units left"

# Key holder probe B: a holder key minted OUTSIDE a unit is usable from INSIDE a real full unit. Starts a
# throwaway fibby + holder pair (virtual card, no real card, no PIN), mints one key, then runs clown's
# `agent-scope-run` with a spec whose sockets[] binds ONLY the holder's agent socket and whose env sets
# SSH_AUTH_SOCK to it; inside the unit it prints `ssh-add -L`, makes one signature, and reports whether the operator's
# real agent path is visible (present/absent only; the operator's socket is never used). Tears down the unit and
# the pair and asserts no leftover. Refuses while per-cpu 1-minute load is 1.0 or more; ONE unit per run.
# CLOWN_REV defaults to the clown commit carrying the Validate rule (SSH_AUTH_SOCK only to a bound socket).
#
# run one real full unit with the holder's socket bound in and prove a key minted outside is usable inside
[group("debug")]
debug-keyholder-unit-probe fibby="" piggy="" clown="" clown_rev="f502df21ae7d5420c73ca62ebd94cc272566b2bf":
    #!/usr/bin/env bash
    set -uo pipefail
    export LC_ALL=C
    say() { printf 'unit-probe: %s\n' "$*"; }
    load="$(cut -d' ' -f1 /proc/loadavg)"; cpus="$(nproc)"
    if awk -v l="$load" -v n="$cpus" 'BEGIN{exit !(l/n >= 1.0)}'; then say "REFUSED: load $load over $cpus cpus is per-cpu >= 1.0"; exit 1; fi
    operator_sock="${SSH_AUTH_SOCK:-}"; operator_piggy="${PIGGY_AUTH_SOCK:-}"
    piggy="{{ piggy }}"; [ -n "$piggy" ] || piggy="$(command -v piggy)" || { say "no piggy on PATH"; exit 1; }
    fibby="{{ fibby }}"
    if [ -z "$fibby" ]; then for c in /nix/store/*-fibby-holder-0.1.0/bin/fibby; do fibby="$c"; done; fi
    [ -x "$fibby" ] || { say "no fibby holder build found"; exit 1; }
    clown="{{ clown }}"
    if [ -z "$clown" ]; then
      ref="https://code.linenisgreat.com/clown/archive/{{ clown_rev }}.tar.gz#default"
      say "building clown from $ref"
      clown="$(timeout 1500 nix build --no-link --print-out-paths "$ref" 2>"$PWD/.tmp/clown-build.err" | tail -1)"
    fi
    [ -x "$clown/bin/clown" ] || { say "no clown at '$clown'"; tail -n 5 "$PWD/.tmp/clown-build.err" 2>/dev/null; exit 1; }
    say "clown=$clown"
    base="$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/khu.XXXXXX")"; chmod 0700 "$base"
    mkdir -m 0700 "$base/fibby" "$base/agent" "$base/res"
    wt="$(mktemp -d "$PWD/.tmp/khu-wt.XXXXXX")"
    ctl="$base/fibby/ctl.sock"; pcsc="$base/fibby/pcscd.comm"; agent="$base/agent/agent.sock"
    printf '#!/bin/sh\necho "REFUSING to prompt" >&2\nexit 1\n' >"$base/askpass"; chmod 0700 "$base/askpass"
    export SSH_ASKPASS="$base/askpass" SSH_ASKPASS_REQUIRE=force DISPLAY="" PCSCLITE_CSOCK_NAME="$pcsc"
    unset WAYLAND_DISPLAY SSH_AUTH_SOCK PIGGY_AUTH_SOCK
    pids=()
    cleanup() {
      kill "${pids[@]}" 2>/dev/null || true
      sleep 1
      left="$(pgrep -f "$base" || true)"
      units="$(systemctl --user list-units --all --no-legend 2>/dev/null | grep -c 'keyholder-probe' || true)"
      say "after teardown: processes naming the pair dir='${left}' units naming keyholder-probe=$units"
      rm -rf "$base" "$wt"
    }
    trap cleanup EXIT
    "$fibby" --socket "$pcsc" --control-socket "$ctl" --model yk5.7 >"$base/fibby.log" 2>&1 &
    pids+=($!)
    for _ in $(seq 1 50); do [ -S "$ctl" ] && [ -S "$pcsc" ] && break; sleep 0.1; done
    timeout 60 "$fibby" ctl --socket "$ctl" destroy "Virtual PCD piggy fibby 00 00" >/dev/null 2>&1 || true
    "$piggy" agent --holder --holder-fibby-ctl "$ctl" --scope probe-v1=any -a "$agent" >"$base/agent.log" 2>&1 &
    pids+=($!)
    for _ in $(seq 1 50); do [ -S "$agent" ] && break; sleep 0.1; done
    [ -S "$agent" ] || { say "agent socket never appeared"; sed 's/^/agent.log: /' "$base/agent.log"; exit 1; }
    SSH_AUTH_SOCK="$agent" timeout 60 "$piggy" holder mint --scope probe-v1 >"$wt/holder.pub" 2>"$base/mint.err" || { say "mint failed"; head -c 300 "$base/mint.err"; exit 1; }
    say "outside: ssh-add -L: $(SSH_AUTH_SOCK="$agent" timeout 30 ssh-add -L 2>&1 | awk '{print $1}')"
    say "outside: fingerprint: $(ssh-keygen -lf "$wt/holder.pub" 2>&1)"
    jq -n --arg wt "$wt" --arg home "$HOME" --arg res "$base/res/result.json" --arg sock "$agent" \
      '{version:1, worktree:$wt, operator_home:$home, unit_name_hint:"keyholder-probe", result_path:$res,
        sockets:[{path:$sock, purpose:"key-holder"}], env:{SSH_AUTH_SOCK:$sock}}' >"$base/spec.json"
    inner='echo "sock=$SSH_AUTH_SOCK"; echo "inside: ssh-add -L:"; ssh-add -L 2>&1 | awk '"'"'{print "  " $1 " " $NF}'"'"'; ssh-add -l 2>&1 | sed "s/^/  fp: /";'
    inner+=' printf hello > '"$wt"'/m; ssh-keygen -Y sign -U -f '"$wt"'/holder.pub -n probe '"$wt"'/m 2>&1 | sed "s/^/  sign: /"; echo "  sign rc=${PIPESTATUS[0]}";'
    # The negative must not be vacuous: enumerate the operator's REAL candidate agent sockets (names of
    # socket files only, never their contents; none is opened or connected to), hand each path to the unit and
    # test it there. The run fails if there are no candidates or if any one is visible inside.
    cand="$wt/candidates.txt"; : >"$cand"
    for p in "$operator_sock" "$operator_piggy"; do [ -z "$p" ] || printf '%s\n' "$p" >>"$cand"; done
    rt="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
    {
      [ -d "$rt" ] && find "$rt" -maxdepth 2 -type s \( -name 'piggy*' -o -name 'ssh*' -o -name '*agent*' -o -name 'S.gpg-agent*' -o -name 'S.gpg*' \) 2>/dev/null
      [ -d "$HOME/.local/state/ssh" ] && find "$HOME/.local/state/ssh" -maxdepth 2 -type s 2>/dev/null
      [ -d "$HOME/.local/state/piggy" ] && find "$HOME/.local/state/piggy" -maxdepth 2 -type s 2>/dev/null
      find /tmp -maxdepth 3 -type s -path '/tmp/ssh-*/agent.*' 2>/dev/null
    } | grep -v -F "$base" >>"$cand" || true
    sort -u "$cand" -o "$cand"
    ncand="$(wc -l <"$cand")"
    say "operator agent candidates ($ncand; names only, ~ is HOME):"; sed "s#$HOME#~#; s/^/unit-probe:   /" "$cand"
    if [ "$ncand" -lt 1 ]; then say "FAIL: no operator agent socket candidate exists on this host, so an 'absent' answer would prove nothing"; exit 1; fi
    inner+=' while IFS= read -r p; do if [ -e "$p" ]; then echo "VISIBLE $p"; else echo "absent $p"; fi; done < '"$cand"';'
    say "running the unit (one run)"
    # agent-scope-run refuses a working directory outside the worktree
    cd "$wt"
    timeout 300 "$clown/bin/clown" agent-scope-run --spec "$base/spec.json" -- bash -c "$inner" </dev/null 2>&1 | grep -v 'agent scope: environment:' | tee "$base/unit.out" | sed "s#$HOME#~#g; s/^/unit: /"
    rc="${PIPESTATUS[0]}"
    say "agent-scope-run rc=$rc"
    nvis="$(grep -c '^VISIBLE ' "$base/unit.out" || true)"; nabs="$(grep -c '^absent ' "$base/unit.out" || true)"
    say "inside the unit: candidates visible=$nvis absent=$nabs of $ncand"
    if [ "$nvis" != 0 ]; then say "FAIL: an operator agent socket is VISIBLE inside the unit"; grep '^VISIBLE ' "$base/unit.out" | sed "s#$HOME#~#"; exit 2; fi
    if [ "$nabs" != "$ncand" ]; then say "FAIL: the unit tested $nabs of $ncand candidates (the check did not run to the end)"; exit 1; fi
    say "NEGATIVE-OK none of $ncand operator agent socket candidates is visible inside the unit"
    [ -f "$base/res/result.json" ] && say "result: $(jq -c '{unit, exit_code, main_pid}' "$base/res/result.json" 2>&1)"
    say "systemctl --user clown-agent units now: $(systemctl --user list-units --all --no-legend 2>/dev/null | grep -c 'clown-agent' || true)"

# Key holder end-to-end certificate proof: krusty's route through THIS repo's real holder pair. Two real pairs
# (fibby + holder, virtual cards, no real card), a THROWAWAY CA standing in for slot 83, a PLAIN-key signature made
# through the holder (`ssh-keygen -Y sign -U`), `circus cert rewrap` (the merged circus tool), and the verify
# command hogtie runs (`ssh-keygen -Y verify` against ONE `cert-authority,namespaces="hogtie-v1"` allowed-signers
# line). Negative controls: the plain signature, another key's certificate, the wrong principal, an expired
# certificate, another session's holder. Nothing outside a 0700 scratch directory is touched (no card, no
# /etc/hogtie, no hogtie service, no grant); both pairs are stopped and nothing may be left. Refuses while per-cpu
# 1-minute load is 1.0 or more. Prints HOGTIE-PROOF-OK only when every positive passed and every negative failed.
# STAND-IN: the CA, and hogtie's own envelope pre-parse (certificate type, configured CA, window, critical options).
#
# prove a holder-signed, circus-rewrapped signature verifies the way hogtie verifies, with negative controls
[group("debug")]
debug-keyholder-hogtie-proof fibby="" piggy="" circus="/nix/store/srisylv7g0sms1na3mbjw0dms0f14wsl-circus-cli/bin/circus":
    #!/usr/bin/env bash
    set -uo pipefail
    export LC_ALL=C
    say() { printf 'hogtie-proof: %s\n' "$*"; }
    load="$(cut -d' ' -f1 /proc/loadavg)"; cpus="$(nproc)"
    if awk -v l="$load" -v n="$cpus" 'BEGIN{exit !(l/n >= 1.0)}'; then say "REFUSED: load $load over $cpus cpus is per-cpu >= 1.0"; exit 1; fi
    out="$(nix build .#default --no-link --print-out-paths 2>/dev/null | tail -1)"
    sc="$out/bin/spinclass"; [ -x "$sc" ] || { say "no spinclass built at $out"; exit 1; }
    piggy="{{ piggy }}"; [ -n "$piggy" ] || piggy="$(command -v piggy)" || { say "no piggy on PATH"; exit 1; }
    fibby="{{ fibby }}"
    if [ -z "$fibby" ]; then for c in /nix/store/*-fibby-holder-0.1.0/bin/fibby; do fibby="$c"; done; fi
    [ -x "$fibby" ] || { say "no fibby holder build found"; exit 1; }
    circus="{{ circus }}"; [ -x "$circus" ] || { say "no circus binary at $circus"; exit 1; }
    base="$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/khp.XXXXXX")"; chmod 0700 "$base"
    mkdir -m 0700 "$base/state" "$base/work"
    export XDG_STATE_HOME="$base/state"
    unset SSH_AUTH_SOCK PIGGY_AUTH_SOCK
    s1="proof/one"; s2="proof/two"
    stopped=0
    pair() { SPINCLASS_KEYHOLDER_PAIR_RECIPE=1 "$sc" keyholder-pair --fibby "$fibby" --piggy "$piggy" "$@"; }
    cleanup() {
      if [ "$stopped" = 0 ]; then pair --action stop --session "$s1" >/dev/null 2>&1 || true; pair --action stop --session "$s2" >/dev/null 2>&1 || true; fi
      rm -rf "$base"
    }
    trap cleanup EXIT
    fails=0
    pass() { say "PASS $*"; }
    fail() { say "FAIL $*"; fails=$((fails + 1)); }
    say "REAL: spinclass=$sc circus=$circus"; say "REAL: two holder pairs (virtual cards); STAND-IN: throwaway CA for slot 83"
    for s in "$s1" "$s2"; do
      pair --action start --session "$s" >"$base/$(echo "$s" | tr / _).out" 2>"$base/$(echo "$s" | tr / _).err" || { say "pair start failed for $s"; cat "$base/$(echo "$s" | tr / _).err"; exit 1; }
    done
    sock1="$(sed -n 's/^agent_sock //p' "$base/proof_one.out")"; sock2="$(sed -n 's/^agent_sock //p' "$base/proof_two.out")"
    cd "$base/work" || exit 1
    SSH_AUTH_SOCK="$sock1" timeout 30 ssh-add -L >holder1.pub 2>/dev/null; SSH_AUTH_SOCK="$sock2" timeout 30 ssh-add -L >holder2.pub 2>/dev/null
    [ "$(grep -c '^ssh-' holder1.pub)" = 1 ] && [ "$(grep -c '^ssh-' holder2.pub)" = 1 ] || { say "FAIL: each holder must list exactly one key"; exit 1; }
    say "holder 1: $(ssh-keygen -lf holder1.pub)"; say "holder 2: $(ssh-keygen -lf holder2.pub)"
    ssh-keygen -q -t ed25519 -N '' -f ca -C throwaway-ca
    ssh-keygen -q -s ca -I proof -n flic:restic-snapshots -O clear -V +1h holder1.pub
    ssh-keygen -q -s ca -I proof2 -n flic:restic-snapshots -O clear -V +1h holder2.pub
    mkdir exp && cp holder1.pub exp/holder1.pub && (cd exp && ssh-keygen -q -s ../ca -I expired -n flic:restic-snapshots -O clear -V -2h:-1h holder1.pub)
    printf 'proof message\n' >msg
    printf '* cert-authority,namespaces="hogtie-v1" %s %s\n' "$(cut -d' ' -f1 ca.pub)" "$(cut -d' ' -f2 ca.pub)" >allowed
    say "allowed-signers: $(wc -l <allowed) line"
    # 1. a PLAIN-key signature through the holder (the call that works), then krusty's rewrap, then hogtie's verify command.
    SSH_AUTH_SOCK="$sock1" ssh-keygen -Y sign -U -f holder1.pub -n hogtie-v1 msg >sign.out 2>&1; rc=$?
    if [ "$rc" = 0 ] && [ -s msg.sig ]; then pass "1 plain-key signature through holder 1 (rc 0)"; else fail "1 plain-key signature through holder 1: rc=$rc: $(head -c 200 sign.out)"; fi
    "$circus" cert rewrap --cert holder1-cert.pub <msg.sig >msg.cert.sig 2>rewrap.err; rc=$?
    if [ "$rc" = 0 ] && [ -s msg.cert.sig ]; then pass "2 circus cert rewrap with holder 1's certificate (rc 0)"; else fail "2 circus cert rewrap: rc=$rc: $(head -c 200 rewrap.err)"; fi
    vout="$(ssh-keygen -Y verify -f allowed -I flic:restic-snapshots -n hogtie-v1 -s msg.cert.sig <msg 2>&1)"; rc=$?
    if [ "$rc" = 0 ] && printf '%s' "$vout" | grep -q '^Good'; then pass "3 verify (hogtie's command) accepts the holder-signed, rewrapped signature: $vout"; else fail "3 verify rc=$rc: $vout"; fi
    # Negative controls: each must FAIL.
    vout="$(ssh-keygen -Y verify -f allowed -I flic:restic-snapshots -n hogtie-v1 -s msg.sig <msg 2>&1)"; rc=$?
    if [ "$rc" != 0 ]; then pass "a negative: the PLAIN signature (no rewrap) is refused: rc=$rc ${vout:0:100}"; else fail "a negative: the plain signature VERIFIED"; fi
    "$circus" cert rewrap --cert holder2-cert.pub <msg.sig >msg.other.sig 2>other.err; rc=$?
    if [ "$rc" != 0 ]; then pass "b negative: rewrap with ANOTHER key's certificate is refused by the tool: rc=$rc $(head -c 160 other.err)"; else fail "b negative: rewrap with another key's certificate was ACCEPTED"; fi
    vout="$(ssh-keygen -Y verify -f allowed -I flic:other -n hogtie-v1 -s msg.cert.sig <msg 2>&1)"; rc=$?
    if [ "$rc" != 0 ]; then pass "c negative: the wrong principal is refused: rc=$rc ${vout:0:100}"; else fail "c negative: the wrong principal VERIFIED"; fi
    "$circus" cert rewrap --cert exp/holder1-cert.pub <msg.sig >msg.exp.sig 2>exp.err; rc=$?
    if [ "$rc" != 0 ]; then pass "d negative: the expired certificate is refused by the tool already: rc=$rc $(head -c 120 exp.err)"
    else
      vout="$(ssh-keygen -Y verify -f allowed -I flic:restic-snapshots -n hogtie-v1 -s msg.exp.sig <msg 2>&1)"; rc=$?
      if [ "$rc" != 0 ]; then pass "d negative: the tool wrapped the expired certificate and verify REFUSES it: rc=$rc ${vout:0:100}"; else fail "d negative: the expired certificate VERIFIED"; fi
    fi
    # e: ssh-keygen writes msg.sig; keep holder 1's, then sign with holder 2 and keep that one apart.
    cp msg.sig msg1.sig; rm -f msg.sig
    SSH_AUTH_SOCK="$sock2" ssh-keygen -Y sign -U -f holder2.pub -n hogtie-v1 msg >sign2.out 2>&1 || { say "FAIL: holder 2 could not sign: $(head -c 200 sign2.out)"; fails=$((fails + 1)); }
    mv msg.sig msg2.sig
    if cmp -s msg1.sig msg2.sig; then say "FAIL: holder 2's signature is byte-identical to holder 1's (setup)"; fails=$((fails + 1)); else say "INFO: holder 2's signature differs from holder 1's (setup check)"; fi
    "$circus" cert rewrap --cert holder1-cert.pub <msg2.sig >msg2.on1.sig 2>e.err; rc=$?
    if [ "$rc" != 0 ]; then pass "e negative: holder 2's signature rewrapped with holder 1's certificate is refused by the tool: rc=$rc $(head -c 160 e.err)"; else fail "e negative: another session's signature was wrapped with holder 1's certificate"; fi
    "$circus" cert rewrap --cert holder2-cert.pub <msg2.sig >msg2.cert.sig 2>/dev/null && say "INFO: holder 2's own rewrapped signature verifies as its own principal: $(ssh-keygen -Y verify -f allowed -I flic:restic-snapshots -n hogtie-v1 -s msg2.cert.sig <msg 2>&1 | head -c 100) (hogtie binds the principal, not the session: the per-session separation is that each key has its own certificate)"
    pair --action stop --session "$s1" >/dev/null 2>&1; pair --action stop --session "$s2" >/dev/null 2>&1; stopped=1
    sleep 1
    units="$(systemctl --user list-units --all --no-legend 'spinclass-keyholder-*' 2>&1 | grep -c spinclass-keyholder || true)"
    procs="$(pgrep -f "$base" || true)"
    dirs="$(find "$XDG_STATE_HOME" -mindepth 3 -maxdepth 3 -path '*keyholder/*' 2>/dev/null | wc -l)"
    say "after stop: units=$units processes='${procs}' directories=$dirs"
    if [ "$units" != 0 ] || [ -n "$procs" ] || [ "$dirs" != 0 ]; then fail "leftovers after stopping both pairs"; fi
    if [ "$fails" = 0 ]; then say "HOGTIE-PROOF-OK 3 positives passed, 5 negatives failed as expected, nothing left"; else say "FAILED: $fails step(s) failed"; exit 1; fi

# [explore] Poll a worker's record every few seconds and print ONE line per change
# (UTC time, request queued, restarting_at, restarted_at, claude pid, recorded pid,
# peek state) until restarted_at is set after restarting_at, or `limit` seconds
# pass. The timeline for a mid-turn restart drill (how long a queued restart
# waited for the turn to end). Read-only.
#
# print a timeline of a worker's restart state until it returns or the limit passes
[group("explore")]
explore-live-restart-watch target limit='600' every='5':
    #!/usr/bin/env bash
    set -uo pipefail
    prev=""
    end=$(( $(date +%s) + {{ limit }} ))
    while [ "$(date +%s)" -lt "$end" ]; do
      snap=$("{{ just_executable() }}" --quiet --justfile "{{ justfile() }}" _explore-live-restart-snapshot '{{ target }}' watch 2>/dev/null | tail -n1) || snap=""
      if [ -n "$snap" ] && [ -f "$snap" ]; then
        line=$(jq -r '"queued=\(.restart_request_queued) restarting_at=\(.restarting_at // "-") restarted_at=\(.restarted_at // "-") claude=\([.procs[] | select(.kind == "agent" and (.exe | test("claude"))) | .pid] | join(",")) recorded_pid=\(.recorded_pid) peek=\(.peek.state // "?")"' "$snap")
        if [ "$line" != "$prev" ]; then printf '%s %s\n' "$(date -u +%FT%TZ)" "$line"; prev=$line; fi
        if jq -e '.restarting_at != null and .restarted_at != null and (.restarted_at >= .restarting_at)' "$snap" >/dev/null; then echo "returned"; exit 0; fi
      fi
      sleep {{ every }}
    done
    echo "limit reached"
# The troupe credential and the conversation resuming cannot be observed from
# the process table or the record; those stay MANUAL. The serve-binary check
# compares the resolved exe with the spinclass on PATH and can false-FAIL when
# the clown plugin runs its own build of the same revision ("same build,
# different path"): the worker's own version tool is the authority there.
#
# snapshot a restarted worker and compare it with its baseline
[group("explore")]
explore-live-restart-verify target:
    #!/usr/bin/env bash
    set -euo pipefail
    after=$("{{ just_executable() }}" --quiet --justfile "{{ justfile() }}" _explore-live-restart-snapshot '{{ target }}' after | tail -n1)
    base="$(dirname "$after")/baseline.json"
    [ -f "$base" ] || { echo "no baseline at $base: run explore-live-restart-baseline before the restart" >&2; exit 1; }
    echo "baseline: $base"
    echo "after:    $after"
    fails=0
    pass() { printf 'PASS    %s\n' "$*"; }
    fail() { printf 'FAIL    %s\n' "$*"; fails=$((fails + 1)); }
    info() { printf 'INFO    %s\n' "$*"; }
    manual() { printf 'MANUAL  %s\n' "$*"; }
    check() { if jq -e --slurpfile b "$base" "$2" "$after" >/dev/null; then pass "$1"; else fail "$1"; fi; }
    jq -r '"INFO    restarting_by=\(.restarting_by) restarting_at=\(.restarting_at) restarted_at=\(.restarted_at)", "INFO    peek=\(.peek | tojson)", (.procs[] | "INFO    \(.kind) pid=\(.pid) unattended=\(.unattended) dontAsk=\(.dontask) profile_argv=\(.profile_argv) exe=\(.exe)")' "$after"

    check "same session key" '.session_key == $b[0].session_key'
    check "same principal (the troupe identity the restart exists to keep)" '.principal != "" and .principal == $b[0].principal'
    check "same worktree" '.worktree == $b[0].worktree'
    ra=$(jq -r '.restarting_at // ""' "$after"); rd=$(jq -r '.restarted_at // ""' "$after"); rb=$(jq -r '.restarting_at // ""' "$base")
    if [ -z "$ra" ]; then fail "RestartingAt is unset: restart-preflight never returned a restart verdict"
    elif [ -n "$rb" ] && [ "$(date -d "$ra" +%s.%N)" = "$(date -d "$rb" +%s.%N)" ]; then fail "RestartingAt did not move since the baseline: this is not a new restart"
    else pass "RestartingAt set ($ra)"; fi
    if [ -z "$rd" ]; then fail "RestartedAt is unset: the relaunched session's SessionStart did not mark its return (or it has not come back)"
    elif [ -n "$ra" ] && awk -v a="$(date -d "$rd" +%s.%N)" -v b="$(date -d "$ra" +%s.%N)" 'BEGIN { exit !(a >= b) }'; then pass "RestartedAt ($rd) >= RestartingAt"
    else fail "RestartedAt ($rd) is older than RestartingAt ($ra)"; fi
    check "restart-preflight --peek reports state live" '.peek.state == "live"'
    # Close-after-restart gap: the relaunch's SessionStart must re-record the pid, or
    # close-child-session cannot prove the recorded pid is the session's and leaves it running.
    check "the recorded pid is one of the session's live processes (close can stop it)" '.recorded_pid as $r | $r > 0 and any(.procs[]; .pid == $r)'
    check "the recorded pid is not a pid from the baseline (re-recorded on relaunch)" '.recorded_pid as $r | all($b[0].procs[]; .pid != $r)'
    # Only the claude process is relaunched: the outer posh/clown frontend that
    # carries --permission-mode survives a restart (observed 2026-10-26), so it is not
    # an "agent" here. A baseline taken AFTER the preflight verdict (mid-countdown) is
    # still valid for this check as long as the old claude had not exited yet.
    check "a NEW claude process exists (no claude pid or start time survived from the baseline)" '[.procs[] | select(.kind == "agent" and (.exe | test("claude")))] as $a | ($a | length) >= 1 and all($a[]; . as $n | all($b[0].procs[] | select(.kind == "agent" and (.exe | test("claude"))); .pid != $n.pid or .start_ticks != $n.start_ticks))'
    if jq -e --slurpfile b "$base" 'any($b[0].procs[]; .kind == "agent" and .unattended)' "$after" >/dev/null; then
      check "relaunched agent environment has CLOWN_UNATTENDED=1" 'any(.procs[]; .kind == "agent" and .unattended)'
    else info "baseline agent had no CLOWN_UNATTENDED=1: unattended check skipped (the baseline recipe FAILs this)"; fi
    if jq -e --slurpfile b "$base" 'any($b[0].procs[]; (.kind == "agent" or .kind == "clown") and (.dontask or .settings_dontask))' "$after" >/dev/null; then
      # dontAsk survives through the argv splice or, since clown#323, an inline
      # --settings defaultMode; either keeps the worker from hanging on a prompt.
      check "relaunched agent runs dontAsk (--permission-mode argv or clown --settings defaultMode)" 'any(.procs[]; (.kind == "agent" or .kind == "clown") and (.dontask or .settings_dontask))'
      jq -e 'any(.procs[]; .kind == "agent" and .dontask)' "$after" >/dev/null || info "dontAsk is not in the agent argv after the relaunch: the argv splice did not survive, only the settings path counts"
    else info "baseline agent was not dontAsk: dontAsk check skipped"; fi
    if jq -e '.restart_request_queued' "$after" >/dev/null; then info "a restart request is STILL queued in the record: the preflight never consumed it (clown 'exiting with a restart queued and never asked about')"; fi
    # Only processes whose real exe is a spinclass binary are spinclass serves;
    # moxy, madder-mcp and the stdio bridges also carry a `serve` argument.
    onpath=$(jq -r '.spinclass_on_path' "$after")
    srv=$(jq -r '[.procs[] | select(.kind == "serve" and (.exe_real | endswith("/spinclass"))) | .exe_real] | unique | .[]' "$after")
    if [ -z "$srv" ]; then fail "no spinclass serve process after the relaunch"
    else
      stale=0
      for s in $srv; do
        [ "$s" = "$onpath" ] && continue
        rs=$("$s" version 2>/dev/null | awk '$1 ~ /^spinclass-/ { print $3; exit }')
        rp=$("$onpath" version 2>/dev/null | awk '$1 ~ /^spinclass-/ { print $3; exit }')
        if [ -n "$rs" ] && [ "$rs" = "$rp" ]; then info "serve $s is the same build ($rs) as the spinclass on PATH, at a different path"
        else stale=1; fail "serve $s (build ${rs:-unknown}) is not the spinclass on PATH (build ${rp:-unknown}): STALE"; fi
      done
      [ "$stale" = 0 ] && pass "spinclass serve is the current build (no STALE)"
    fi
    check "profile pin intact in the record (name and digest)" '.profile == $b[0].profile and .profile_digest == $b[0].profile_digest'
    if [ -n "$(jq -r '.profile' "$base")" ]; then
      check "relaunched agent argv carries the same --profile as before" '([.procs[] | select(.kind == "agent") | .profile_argv] | unique) == ([$b[0].procs[] | select(.kind == "agent") | .profile_argv] | unique)'
    else info "no profile pinned: argv --profile check skipped"; fi
    manual "conversation resumed: ask the worker to recall the nonce sent before the restart"
    manual "troupe credential not revoked: spinclass cannot observe it; confirm the worker still chats as the same JID (chat_list) and per the runbook"
    echo
    if [ "$fails" -gt 0 ]; then echo "SUMMARY: $fails FAIL"; exit 1; fi
    echo "SUMMARY: verify PASS (MANUAL lines remain)"

# [explore] Spinclass#372 auto-allows read-only moxy folio/grit tools on the
# caller's own and held children's worktrees. The hook's verdict log
# (<worktree>/.spinclass/hook-verdicts.ndjson) only records spinclass's OWN tools
# (internal/delta/hooks/verdictlog.go, verdictToolPrefix), never moxy ones, so a
# folio_read allow leaves no line there. This therefore (1) greps the DRIVER's
# log for lines naming the child (restart-child-session and friends; an allow
# whose reason names "held child KEY" would appear here only if the log's scope
# changes) and (2) replays synthetic moxy PreToolUse payloads through the
# installed `spinclass hook` as the driver's principal against the live session
# store, with controls that must NOT be allowed. Judges only; it runs no tool.
# Replaying may append a line to the driver's hook-trace.log. The driver's
# principal comes from $CLOWN_SESSION_ID, else the driver's frontend record.
#
# show what the hook decides for moxy reads of a held child's worktree, live
[group("explore")]
explore-live-moxy-allow driver-worktree child-key:
    #!/usr/bin/env bash
    set -euo pipefail
    driver=$(realpath -sm '{{ driver-worktree }}')
    child_key='{{ child-key }}'
    records="${XDG_STATE_HOME:-$HOME/.local/state}/spinclass/records"
    log="$driver/.spinclass/hook-verdicts.ndjson"
    echo "=== driver verdict log lines naming $child_key ==="
    if [ -f "$log" ]; then
      jq -r --arg k "$child_key" 'select((.reason | contains("held child " + $k)) or (.input | contains($k))) | [.ts, .tool, .decision, (.reason | .[0:80]), (try (.input | fromjson | keys | join(",")) catch "(input truncated)")] | @tsv' "$log" || true
      echo "allow lines whose reason names 'held child $child_key': $(jq -r --arg k "$child_key" 'select(.reason | contains("held child " + $k)) | .tool' "$log" | wc -l)"
    else
      echo "(no $log)"
    fi
    echo
    echo "=== replay through the installed spinclass hook ==="
    child_rec=""
    for f in "$records"/*.json; do
      [ -f "$f" ] || continue
      if jq -e --arg t "$child_key" '.session_key == $t' "$f" >/dev/null 2>&1; then child_rec=$f; break; fi
    done
    [ -n "$child_rec" ] || { echo "no frontend record for child '$child_key'" >&2; exit 1; }
    child_wt=$(jq -r '.worktree_path' "$child_rec")
    principal=${CLOWN_SESSION_ID:-}
    if [ -z "$principal" ]; then
      h=$(printf '%s' "$driver" | sha256sum | cut -c1-16)
      principal=$(jq -r '.principal // ""' "$records/$h.json" 2>/dev/null || true)
    fi
    [ -n "$principal" ] || { echo "no driver principal: set CLOWN_SESSION_ID or point at a driver worktree with a record" >&2; exit 1; }
    echo "child worktree: $child_wt"
    fails=0
    replay() { # label tool input-json expect (allow|none)
      payload=$(jq -nc --arg tool "mcp__plugin_moxy_moxy__$2" --argjson input "$3" --arg cwd "$driver" '{hook_event_name: "PreToolUse", session_id: "synth-moxy-allow", tool_name: $tool, tool_input: $input, cwd: $cwd}')
      out=$(printf '%s' "$payload" | CLOWN_SESSION_ID="$principal" spinclass hook 2>/dev/null || true)
      got=$(printf '%s' "$out" | jq -r '.hookSpecificOutput.permissionDecision // empty' 2>/dev/null || true)
      reason=$(printf '%s' "$out" | jq -r '.hookSpecificOutput.permissionDecisionReason // empty' 2>/dev/null || true)
      [ -n "$got" ] || got=none
      keys=$(printf '%s' "$3" | jq -r 'keys | join(",")')
      # expect "none" means any verdict but allow (the hook may ask or stay silent).
      if { [ "$4" = allow ] && [ "$got" = allow ] && printf '%s' "$reason" | grep -F "held child $child_key" >/dev/null; } || { [ "$4" = none ] && [ "$got" != allow ]; }; then
        printf 'PASS    %s: %s [args: %s] -> %s %s\n' "$1" "$2" "$keys" "$got" "$reason"
      else
        printf 'FAIL    %s: %s [args: %s] -> %s %s (wanted %s)\n' "$1" "$2" "$keys" "$got" "$reason" "$4"
        fails=$((fails + 1))
      fi
    }
    replay "ls child worktree" folio_ls "$(jq -nc --arg p "$child_wt" '{path: $p}')" allow
    replay "read inside child worktree" folio_read "$(jq -nc --arg p "$child_wt/.git" '{file_path: $p}')" allow
    replay "glob inside child worktree" folio_glob "$(jq -nc --arg p "$child_wt" '{pattern: "*", path: $p}')" allow
    replay "grit_branch log of child" grit_branch "$(jq -nc --arg p "$child_wt" '{subcommand: "log", repo_path: $p, max_count: 1}')" allow
    replay "grit_stage status of child" grit_stage "$(jq -nc --arg p "$child_wt" '{subcommand: "status", repo_path: $p}')" allow
    replay "CONTROL child .spinclass" folio_read "$(jq -nc --arg p "$child_wt/.spinclass/state.json" '{file_path: $p}')" none
    replay "CONTROL outside any worktree" folio_ls '{"path":"/etc"}' none
    replay "CONTROL mutating grit_branch" grit_branch "$(jq -nc --arg p "$child_wt" '{subcommand: "reset-hard", repo_path: $p, ref: "HEAD"}')" none
    echo
    if [ "$fails" -gt 0 ]; then echo "SUMMARY: $fails FAIL (the driver must hold an ACCEPTED handle on the child: spawn mints one; list-handles with accept exercises pending grants)"; exit 1; fi
    echo "SUMMARY: the installed hook allows the read-only moxy shapes on the held child and refuses the controls"

# [debug] internal/0/madder.UserStoreReady assumes an unprefixed store id lands
# at $XDG_DATA_HOME/madder/blob_stores/<id>/blob_store-config (madder(1) FILES).
# This inits and writes the durable `spinclass` store against a throwaway
# XDG_DATA_HOME, exactly as InitUserStore/WriteUserStore invoke madder, and
# shows where the config landed.
#
# run the pinned madder's user-store init + write in a throwaway XDG tree
[group("debug")]
debug-madder-user-store:
    #!/usr/bin/env bash
    set -euo pipefail
    root=$(mktemp -d "${TMPDIR:-/tmp}/madder-user-store.XXXXXX")
    export HOME="$root/home" XDG_DATA_HOME="$root/data" XDG_STATE_HOME="$root/state" XDG_CACHE_HOME="$root/cache" XDG_CONFIG_HOME="$root/config" XDG_LOG_HOME="$root/log"
    mkdir -p "$HOME" "$root/cwd"
    cd "$root/cwd"
    MADDER_XDG_USER_LOCATION_ONLY=1 madder init -encryption none spinclass
    printf 'probe' | MADDER_XDG_USER_LOCATION_ONLY=1 madder write -format json spinclass -
    echo "--- blob_store-config files under $root ---"
    find "$root" -name blob_store-config
    test -f "$XDG_DATA_HOME/madder/blob_stores/spinclass/blob_store-config" && echo "OK: config at the path UserStoreReady checks"

# [debug] Without a sweatfile [metrics].statsd-address, internal/0/statsd emits
# only when STATSD_HOST or STATSD_PORT is in the emitting process's environment,
# so a `serve` started without them is silently off (spinclass#363). Also
# reports whether anything listens on the UDP port.
#
# show each live `spinclass serve` process's statsd opt-in environment
[group("debug")]
debug-statsd-env port="8125":
    #!/usr/bin/env bash
    set -uo pipefail
    for d in /proc/[0-9]*; do
      pid=${d#/proc/}
      cmd=$(tr '\0' ' ' < "$d/cmdline" 2>/dev/null) || continue
      case "$cmd" in *spinclass*serve*|*"sc serve"*) ;; *) continue ;; esac
      env=$(tr '\0' '\n' < "$d/environ" 2>/dev/null) || { echo "pid $pid: environ unreadable"; continue; }
      echo "pid $pid [$(printf '%s' "$cmd" | cut -c1-70)]"
      printf '%s\n' "$env" | grep -E '^(STATSD_HOST|STATSD_PORT|SPINCLASS_DISABLE_STATSD)=' | sed 's/^/   /' || echo "   (no STATSD_* variables: emission off)"
    done
    echo "--- UDP listeners on port {{ port }} ---"
    ss -ulnp 2>/dev/null | grep -E ":{{ port }}\b" || echo "(none found)"

# NAMES only (never values) of the environment of this session's moxy and spinclass-serve processes, with the ones that look credential-bearing marked; the evidence for what env_get would disclose
[group("explore")]
explore-moxy-env-names:
    #!/usr/bin/env bash
    set -uo pipefail
    me="${CLOWN_SESSION_ID:-}"
    [ -n "$me" ] || { echo "CLOWN_SESSION_ID unset: cannot pick this session's processes" >&2; exit 1; }
    for d in /proc/[0-9]*; do
      env=$(tr '\0' '\n' < "$d/environ" 2>/dev/null) || continue
      printf '%s\n' "$env" | grep -qx "CLOWN_SESSION_ID=$me" || continue
      exe=$(readlink "$d/exe" 2>/dev/null || echo '?')
      case "$exe" in */moxy*|*/spinclass|*/clown-stdio-bridge) ;; *) continue ;; esac
      echo "pid ${d#/proc/} exe=$exe"
      printf '%s\n' "$env" | sed 's/=.*//' | sort | tr '\n' ' ' | fold -s -w 110 | sed 's/^/   /'
      echo
      printf '%s\n' "$env" | sed 's/=.*//' | grep -Ei 'token|secret|key|pass|cred|auth|sock' | sed 's/^/   SENSITIVE-LOOKING: /' | sort -u
    done

# for each live confined unit process: its HOME against <worktree>/.tmp/agent-home, the convention confinement.AgentHomeOf copies from clown (#443); exit 1 on any mismatch
[group("explore")]
explore-live-agent-home:
    #!/usr/bin/env bash
    set -uo pipefail
    seen=0; bad=0
    for d in /proc/[0-9]*; do
      pid=${d#/proc/}
      env=$(tr '\0' '\n' < "$d/environ" 2>/dev/null) || continue
      unit=$(printf '%s\n' "$env" | sed -n 's/^CLOWN_UNIT=//p')
      [ -n "$unit" ] || continue
      wt=$(printf '%s\n' "$env" | sed -n 's/^SPINCLASS_WORKTREE=//p')
      home=$(printf '%s\n' "$env" | sed -n 's/^HOME=//p')
      [ -n "$wt" ] || continue
      want="${wt%/}/.tmp/agent-home"
      seen=$((seen+1))
      if [ "$home" = "$want" ]; then
        echo "ok   pid $pid unit=$unit HOME=$home"
      else
        echo "MISMATCH pid $pid unit=$unit HOME=$home want=$want"
        bad=$((bad+1))
      fi
    done
    [ "$seen" -gt 0 ] || echo "(no confined unit process is running; nothing to compare)"
    [ "$bad" -eq 0 ]

# for each live `spinclass serve`: its session, binary, CLOWN_POLICY_BROKER and whether the broker socket exists (the #438 deploy check)
[group("explore")]
explore-live-broker-env:
    #!/usr/bin/env bash
    set -uo pipefail
    for d in /proc/[0-9]*; do
      pid=${d#/proc/}
      cmd=$(tr '\0' ' ' < "$d/cmdline" 2>/dev/null) || continue
      case "$cmd" in *spinclass*serve*|*"sc serve"*) ;; *) continue ;; esac
      env=$(tr '\0' '\n' < "$d/environ" 2>/dev/null) || { echo "pid $pid: environ unreadable"; continue; }
      session=$(printf '%s\n' "$env" | sed -n 's/^SPINCLASS_SESSION_ID=//p')
      broker=$(printf '%s\n' "$env" | sed -n 's/^CLOWN_POLICY_BROKER=//p')
      exe=$(readlink "$d/exe" 2>/dev/null || echo '?')
      echo "pid $pid session=${session:-?} exe=$exe"
      if [ -z "$broker" ]; then
        echo "   CLOWN_POLICY_BROKER unset: clown named no broker socket (old clown, or no serve discovered at launch)"
      elif [ -S "$broker" ]; then
        echo "   CLOWN_POLICY_BROKER=$broker: socket EXISTS (serve listens: a trusted [folio] stanza is configured)"
      else
        echo "   CLOWN_POLICY_BROKER=$broker: no socket (no stanza, or serve predates the broker): moxy's hook dials and falls to the floor"
      fi
    done

# for each live `spinclass serve`: whether its binary contains the host-queue admission code ("gate slots taken": without it every gate runs unqueued, BYPASS) and the first-come ordering ("position %d of %d", spinclass#482: without it its gates are queued but UNORDERED, their markers have no place and sort ahead of numbered ones). Stale binary, spinclass#399. Exit 1 if any bypass or are unordered
[group("explore")]
explore-live-queue-bypass:
    #!/usr/bin/env bash
    set -uo pipefail
    bad=0; seen=0
    for d in /proc/[0-9]*; do
      pid=${d#/proc/}
      # Only a process whose own program is spinclass/sc and whose first argument
      # is "serve": a bridge or a posh wrapper that merely names it is not a serve.
      args=$({ tr '\0' '\n' < "$d/cmdline"; } 2>/dev/null) || continue
      prog=$(printf '%s\n' "$args" | sed -n '1p'); prog=${prog##*/}
      first=$(printf '%s\n' "$args" | sed -n '2p')
      case "$prog" in spinclass|sc|.spinclass-wrapped) ;; *) continue ;; esac
      [ "$first" = serve ] || continue
      env=$({ tr '\0' '\n' < "$d/environ"; } 2>/dev/null) || { echo "pid $pid: environ unreadable"; continue; }
      session=$(printf '%s\n' "$env" | sed -n 's/^SPINCLASS_SESSION_ID=//p')
      exe=$(readlink "$d/exe" 2>/dev/null || echo '?')
      seen=$((seen+1))
      if grep -q -a -F 'gate slots taken' "$d/exe" 2>/dev/null; then
        if grep -q -a -F 'position %d of %d' "$d/exe" 2>/dev/null; then
          echo "ordered  pid $pid session=${session:-?} exe=$exe"
        else
          echo "UNORDERED  pid $pid session=${session:-?} exe=$exe (queued, but no first-come ordering: its waiters have no place and sort ahead of numbered ones)"
          bad=$((bad+1))
        fi
      else
        echo "BYPASS  pid $pid session=${session:-?} exe=$exe (no host-queue code: its gates run unqueued)"
        bad=$((bad+1))
      fi
    done
    [ "$seen" -gt 0 ] || echo "(no spinclass serve is running)"
    [ "$bad" -eq 0 ]

# what direnv adds to a launcher's environment for a session worktree (the environment a confined spawn carries across the unit boundary today): variable NAMES added or changed, never values, and the PATH entries added, by store name; the direnv-less confined pilot's step 1 (docs/plans/2026-11-04-confined-direnv-less-pilot.md)
[group("explore")]
explore-direnv-env-delta wt=".":
    #!/usr/bin/env bash
    set -uo pipefail
    export LC_ALL=C   # sort and comm must agree on the order
    wt=$(cd "{{wt}}" && pwd -P)
    command -v direnv >/dev/null || { echo "direnv not on PATH"; exit 1; }
    # name<TAB>sha256(value) per variable, so a changed value shows without being printed
    names_and_hashes() {
      env -0 | tr '\0' '\n' | awk -F= 'NF>1{n=$1; v=substr($0, length(n)+2); printf "%s\t%s\n", n, v}' | while IFS=$'\t' read -r n v; do
        printf '%s\t%s\n' "$n" "$(printf '%s' "$v" | sha256sum | cut -c1-12)"
      done | sort
    }
    base=$(direnv exec / bash -c "$(declare -f names_and_hashes); names_and_hashes")
    full=$(direnv exec "$wt" bash -c "$(declare -f names_and_hashes); names_and_hashes" 2>/dev/null)
    echo "== worktree: $wt"
    echo "== variables direnv ADDS (names only)"
    comm -13 <(printf '%s\n' "$base" | cut -f1) <(printf '%s\n' "$full" | cut -f1)
    echo "== variables direnv CHANGES (names only)"
    comm -12 <(printf '%s\n' "$base" | cut -f1) <(printf '%s\n' "$full" | cut -f1) | while read -r n; do
      b=$(printf '%s\n' "$base" | awk -F'\t' -v n="$n" '$1==n{print $2}')
      f=$(printf '%s\n' "$full" | awk -F'\t' -v n="$n" '$1==n{print $2}')
      [ "$b" = "$f" ] || echo "$n"
    done
    echo "== PATH entries direnv ADDS (store paths by name, other dirs verbatim)"
    bp=$(direnv exec / bash -c 'printf "%s" "$PATH"' | tr ':' '\n' | sort -u)
    fp=$(direnv exec "$wt" bash -c 'printf "%s" "$PATH"' 2>/dev/null | tr ':' '\n' | sort -u)
    comm -13 <(printf '%s\n' "$bp") <(printf '%s\n' "$fp") | sed -E 's|^/nix/store/[a-z0-9]{32}-|store: |'
    echo "== counts: base=$(printf '%s\n' "$base" | wc -l) with-direnv=$(printf '%s\n' "$full" | wc -l)"

# for each live confined unit: the environment its main process carries, variable NAMES only (values never printed) plus PATH entries by store name; one block per unit; the direnv-less confined pilot's step 3 comparison (docs/plans/2026-11-04-confined-direnv-less-pilot.md)
[group("explore")]
explore-live-unit-env:
    #!/usr/bin/env bash
    set -uo pipefail
    seen=""
    for d in /proc/[0-9]*; do
      pid=${d#/proc/}
      [ -r "$d/environ" ] || continue   # other users' processes: the open itself fails, not tr
      env=$(tr '\0' '\n' < "$d/environ" 2>/dev/null) || continue
      unit=$(printf '%s\n' "$env" | sed -n 's/^CLOWN_UNIT=//p')
      [ -n "$unit" ] || continue
      case " $seen " in *" $unit "*) continue ;; esac
      seen="$seen $unit"
      session=$(printf '%s\n' "$env" | sed -n 's/^SPINCLASS_SESSION_ID=//p')
      echo "== unit $unit session=${session:-?} (first pid seen: $pid, $(tr '\0' ' ' < "$d/cmdline" 2>/dev/null | cut -c1-60))"
      echo "-- variables (names)"
      printf '%s\n' "$env" | awk -F= 'NF>1{print $1}' | sort -u | tr '\n' ' '; echo
      echo "-- PATH entries"
      printf '%s\n' "$env" | sed -n 's/^PATH=//p' | tr ':' '\n' | sed -E 's|^/nix/store/[a-z0-9]{32}-|store: |'
    done
    [ -n "$seen" ] || echo "(no confined unit process is running; spawn the pilot worker first)"

# step 3 of the direnv-less confined pilot (docs/plans/2026-11-04-confined-direnv-less-pilot.md): create ~/eng/pilot with its own sweatfile layer (the loader entry) and the throwaway repo sc-pilot-env (the canary's no-input flake), print the global spawn-entry for comparison, then `sc validate` in the repo; operator-run (writes outside any worktree)
[group("debug")]
debug-pilot-env-scaffold:
    #!/usr/bin/env bash
    set -euo pipefail
    root="$HOME/eng/pilot"
    [ -e "$root" ] && { echo "$root exists; run debug-pilot-env-teardown first"; exit 1; }
    mkdir -p "$root" && cd "$root"
    git init -q --bare sc-pilot-env.git
    git init -q -b master sc-pilot-env && cd sc-pilot-env
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
    git add -A && git commit -qm "init: the canary flake, no inputs"
    git remote add origin "$root/sc-pilot-env.git" && git push -q -u origin master
    cd "$root"
    just --justfile "{{justfile()}}" debug-pilot-env-layer loader
    echo "== the global spawn-entry, for comparison (everything after clown must match the pilot entry's tail)"
    sed -n '/spawn-entry/,/]/p' "$HOME/.config/spinclass/sweatfile" || true
    echo "== sc validate in the pilot repo (the pilot layer must be listed)"
    cd "$root/sc-pilot-env" && sc validate

# write ~/eng/pilot/sweatfile, the pilot layer: variant `loader` (sources <worktree>/.spinclass/env, then execs clown; the pilot entry) or `bare` (clown alone; the control that is expected to lose chat)
[group("debug")]
debug-pilot-env-layer variant="loader":
    #!/usr/bin/env bash
    set -euo pipefail
    root="$HOME/eng/pilot"
    [ -d "$root" ] || { echo "$root missing; run debug-pilot-env-scaffold"; exit 1; }
    case "{{variant}}" in
      loader)
        cat > "$root/sweatfile" <<'EOF'
    # direnv-less confined pilot layer (spinclass docs/plans/2026-11-04-confined-direnv-less-pilot.md).
    # The entry sources the session dotenv ([direnv.dotenv], written by sc start) and execs clown:
    # no source_up, no use flake, no PATH_add. Delete this directory when the pilot is over.
    [session-entry]
    spawn-entry = ["sh", "-c", "set -a; [ -f \"$1/.spinclass/env\" ] && . \"$1/.spinclass/env\"; set +a; shift; exec clown \"$@\"", "sh", "{dir}", "--clown-attach=spawn", "--", "{prompt}"]
    EOF
        ;;
      bare)
        cat > "$root/sweatfile" <<'EOF'
    # direnv-less confined pilot layer, CONTROL variant: clown alone, no dotenv (expected to boot and lose chat).
    [session-entry]
    spawn-entry = ["clown", "--clown-attach=spawn", "--", "{prompt}"]
    EOF
        ;;
      *) echo "variant must be loader or bare"; exit 2 ;;
    esac
    echo "== wrote $root/sweatfile ({{variant}})"; cat "$root/sweatfile"

# step 4 of the direnv-less confined pilot: spawn the confined pilot worker from inside sc-pilot-env with the terminal's direnv chain unloaded (direnv exec /), then list it; KEY is the operator's session key the worker reports to; run `just explore-live-unit-env` while it is live
[group("debug")]
debug-pilot-env-spawn key profile="canary-spinclass":
    #!/usr/bin/env bash
    set -euo pipefail
    cd "$HOME/eng/pilot/sc-pilot-env"
    brief='You are a confined pilot worker for a spinclass environment experiment. Do exactly this: (1) send one chat message to the session key below listing the tools you have by name; (2) read README.md and append the line "pilot: airside" to it with the Edit tool; (3) if a grit_stage and grit_commit tool exist, stage README.md and commit with the message "pilot: airside", otherwise say so; (4) send a second chat message with whether you committed and the HEAD commit if you know it. Do not merge, do not run recipes, do not create other files. Operator session key: {{key}}'
    direnv exec / sc spawn --confined --profile "{{profile}}" --hello-timeout 3m \
      --description "direnv-less confined pilot" --brief "$brief"
    echo "== sessions"; sc list 2>&1 | grep -i pilot || echo "(no pilot session listed)"

# end the direnv-less confined pilot: refuse while a worker session is listed (sc close it first), then remove ~/eng/pilot (repo, bare origin and the pilot layer). The "(main)" row a terminal `sc spawn` leaves is an implicit main-checkout record (FDR 0014) with a dead PID: `sc close` only matches a LIVE implicit session, and the record's local state file goes with the checkout, after which its index entry dangles and `sc list` hides it (`sc clean` reaps dangling entries)
[group("debug")]
debug-pilot-env-teardown:
    #!/usr/bin/env bash
    set -uo pipefail
    # worker sessions have a worktree branch; the implicit record shows as "(main)" and needs no close
    if sc list 2>/dev/null | grep -i 'sc-pilot-env/' | grep -qv '(main)'; then
      echo "a pilot worker session is still listed; sc close it first:"; sc list | grep -i 'sc-pilot-env/' | grep -v '(main)'; exit 1
    fi
    rm -rf "$HOME/eng/pilot" && echo "removed $HOME/eng/pilot"
    if sc list 2>/dev/null | grep -qi 'sc-pilot-env/'; then
      echo "a sc-pilot-env row is still listed (an index entry whose file is gone); run: sc clean"
    else
      echo "no sc-pilot-env session listed"
    fi

# [debug] Render the dynamic system-prompt fragment exactly as a session's own
# serve would: the INSTALLED sc's `serve`, cwd set to the session's worktree,
# with SPINCLASS_WORKTREE / SPINCLASS_SESSION_ID / SPINCLASS_REPO /
# SPINCLASS_BRANCH set for that worktree (the worktree variant), and this
# caller's own SPINCLASS_* / CLOWN_* identity cleared. Read-only. Prints the
# fragment, then the principles lines found, to answer "does this session's
# serve render the principles" without touching the session.
#
# render the prompt fragment as the session in worktree would
[group("debug")]
debug-prompt-fragment-as worktree:
    #!/usr/bin/env bash
    set -uo pipefail
    bin=$(command -v sc)
    wt={{ quote(worktree) }}
    repo=$(git -C "$wt" rev-parse --path-format=absolute --git-common-dir | xargs dirname)
    branch=$(git -C "$wt" rev-parse --abbrev-ref HEAD)
    key="$(basename "$repo")/$branch"
    echo "sc: $bin; worktree: $wt; session key: $key; HOME: $HOME" >&2
    req='{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"system-prompt-append"}}'
    frag=$(cd "$wt" && printf '%s\n' "$req" |
      env -u CLOWN_SESSION_ID -u CLOWN_GROUP_ID -u SPINCLASS_DESCRIPTION \
        SPINCLASS_WORKTREE="$wt" SPINCLASS_SESSION_ID="$key" \
        SPINCLASS_REPO="$(basename "$repo")" SPINCLASS_BRANCH="$branch" \
        "$bin" serve 2>/dev/null |
      jq -r 'select(.id == 1) | .result.messages[0].content.text')
    printf '%s\n' "$frag"
    echo "=== principles lines:" >&2
    printf '%s\n' "$frag" | grep -nE '^principles |circus-principles|principles file' >&2 || echo "(none)" >&2

# [debug] Pipe a cold `prompts/get` (no `initialize`) into `spinclass serve` and
# print the dynamic system-prompt fragment it returns, extracted from the
# JSON-RPC result. Serves the internal/bravo/sysprompt dev-loop (spinclass#187): edit a
# template, re-run, eyeball the rendered markdown. With mode=worktree, sets the
# SPINCLASS_* identity env so the worktree variant renders; default resolves
# whatever the cwd maps to (a git checkout -> the main-checkout variant).
#
# Runs the nix-built binary, not `go run`: cmd/spinclass does not build under
# plain go tooling in the devshell (the gomod.nix-bridged dewey/pkgs/mesa
# resolves only under nix build — spinclass#292). The dirty-tree nix build sees
# TRACKED files only; `git add -N` a new file (or template) first.
#
# print the dynamic system-prompt fragment `spinclass serve` returns
[group("debug")]
debug-prompt-fragment mode="":
    #!/usr/bin/env bash
    set -euo pipefail
    req='{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"system-prompt-append"}}'
    out=$(nix build --no-link --print-out-paths)
    if [ "{{ mode }}" = "worktree" ]; then
      export SPINCLASS_WORKTREE="$PWD" SPINCLASS_SESSION_ID="demo/branch" SPINCLASS_BRANCH="branch"
    fi
    printf '%s\n' "$req" \
      | "$out/bin/spinclass" serve 2>/dev/null \
      | jq -r 'select(.id == 1) | .result.messages[0].content.text'

# Runs `sc confinement-check` from a nix-built binary against THIS host: the
# FDR 0035 session-confinement table for a session (default: this worktree's),
# one TAP line per fact. The integration check for internal/bravo/confinement — it
# probes the real /proc, cgroups and sockets, so most facts fail until
# clown#244's agent unit lands. Exits nonzero while any fact fails; the
# trailing `|| true` keeps the table visible as a recipe result. Run it from
# outside the session's agent unit (the frontend). Tracked files only: `git
# add -N` new sources first (spinclass#292, as debug-prompt-fragment).
#
# print the FDR 0035 confinement table for a session on this host (for a
# session other than this one, pass its principal or the recorded one is
# used, with a warning; the caller's CLOWN_SESSION_ID never is)
[group("debug")]
debug-confinement-check session="" principal="":
    #!/usr/bin/env bash
    set -euo pipefail
    out=$(nix build --no-link --print-out-paths)
    args=()
    [ -n "{{ session }}" ] && args+=(--session "{{ session }}")
    [ -n "{{ principal }}" ] && args+=(--principal "{{ principal }}")
    "$out/bin/spinclass" confinement-check "${args[@]}" || true

# Shows how the installed claude treats the permission-mode flags a spawn
# splices (spinclass#385): its --permission-mode choices, whether the retired
# --enable-auto-mode is rejected or ignored (probed with --help, so no model
# call), and the auto-mode subcommand's help. Makes no network call.
#
# show the installed claude's permission-mode flags and auto-mode help
[group("explore")]
explore-claude-permission-modes:
    #!/usr/bin/env bash
    set -uo pipefail
    command -v claude || { echo "no claude on PATH"; exit 0; }
    claude --version
    echo "--- --permission-mode in --help:"
    claude --help | grep -n -A3 -- '--permission-mode'
    echo "--- claude --enable-auto-mode --help (exit status, first lines):"
    claude --enable-auto-mode --help >/dev/null 2>"${TMPDIR:-/tmp}/eam.err"; echo "exit=$?"
    head -5 "${TMPDIR:-/tmp}/eam.err"
    echo "--- claude auto-mode --help:"
    claude auto-mode --help 2>&1 | head -40

# [explore] Observe, in throwaway `claude -p` sessions, how Claude Code orders a
# PreToolUse hook's deny against a settings allow rule (#422 Layer 1 relies on
# the hook's deny winning). Each case runs in its own temp git repo with ONLY
# project settings (--setting-sources project: no user plugins, so neither the
# spinclass nor any other plugin hook is loaded), --permission-mode dontAsk (an
# unallowed tool is denied, never prompted), and a probe hook that logs every
# Bash call and denies any command containing HOOKDENY. A case "ran" iff its
# sentinel file exists afterwards. Allow rules are passed with --allowedTools,
# since an untrusted temp workspace's project permissions.allow is ignored.
# Cases:
#   D  allow Bash(touch:*); `touch CONTROL_D`, no deny  (control: the allow works)
#   A  allow Bash(touch:*); hook denies `touch HOOKDENY_A`        (deny vs allow)
#   B  allow Bash(git status:*) only; no hook deny;
#      `git status && touch COMPOUND_B`        (does a first-word allow cover a chain?)
#   C  allow Bash(git status:*) and Bash(touch:*); hook denies
#      `git status && touch HOOKDENY_C`        (deny vs allow on a chain)
# Makes real model calls (haiku), one per case. Leaves the temp dirs for
# inspection and prints their paths.
#
# observe hook deny vs settings allow ordering in throwaway claude sessions
[group("explore")]
explore-hook-deny-vs-allow model="haiku":
    #!/usr/bin/env bash
    set -uo pipefail
    command -v claude >/dev/null || { echo "no claude on PATH"; exit 1; }
    claude --help | grep -q -- '--setting-sources' || { echo "this claude has no --setting-sources; refusing (user plugins would load)"; exit 1; }
    run_case() {
        local name=$1 allow=$2 cmd=$3 sentinel=$4 dir
        dir=$(mktemp -d "${TMPDIR:-/tmp}/hookprec-$name.XXXXXX")
        git -C "$dir" init -q
        mkdir -p "$dir/.claude"
        cat >"$dir/deny-hook.sh" <<'EOF'
    #!/bin/sh
    input=$(cat)
    printf '%s\n' "$input" >>"$(dirname "$0")/hook.log"
    case "$input" in
    *HOOKDENY*) printf '%s\n' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"probe hook deny"}}' ;;
    esac
    EOF
        chmod +x "$dir/deny-hook.sh"
        printf '{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"%s"}]}]}}\n' \
            "$dir/deny-hook.sh" >"$dir/.claude/settings.json"
        # The allow rules go on --allowedTools: an untrusted temp workspace's
        # project permissions.allow is ignored (observed), its hooks are not.
        # The prompt comes right after -p: --allowedTools is variadic and would
        # swallow a trailing positional.
        (cd "$dir" && env -u CLOWN_SESSION_ID -u SPINCLASS_SESSION_ID claude \
            -p "Run exactly this one Bash command, once, verbatim, and nothing else: $cmd" \
            --setting-sources project --permission-mode dontAsk --model {{ quote(model) }} \
            --allowedTools "$allow" \
            >"$dir/claude.out" 2>&1)
        local ran=no hooked=no
        [ -e "$dir/$sentinel" ] && ran=yes
        [ -s "$dir/hook.log" ] && hooked=yes
        printf 'case %s: allow=%s cmd=%q -> ran=%s hook_called=%s dir=%s\n' "$name" "$allow" "$cmd" "$ran" "$hooked" "$dir"
    }
    claude --version
    run_case D 'Bash(touch:*)' 'touch CONTROL_D' CONTROL_D
    run_case A 'Bash(touch:*)' 'touch HOOKDENY_A' HOOKDENY_A
    run_case B 'Bash(git status:*)' 'git status && touch COMPOUND_B' COMPOUND_B
    run_case C 'Bash(git status:*) Bash(touch:*)' 'git status && touch HOOKDENY_C' HOOKDENY_C

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
[group("debug")]
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

# [debug] Drive the `sc grit` escape hatch (FDR 0036 section 7) end to end
# under a pty so isatty(stdin/stdout) is true: runs zz-tests_bats/grit_escape.bats
# against the nix-built binary of this worktree (`just build-nix`'s .#default),
# printing one `# CASE ... exit=... stdout=... stderr=...` line per case on fd 3.
# Everything runs in a bats throwaway repo with HOME, XDG_*, GIT_CONFIG_GLOBAL
# redirected into the test tmpdir, a LOCAL bare remote, no network, no real
# sessions. The pty is util-linux `script`, NOT the operator's real terminal.
# Optional <filter> is a bats --filter regex, e.g. `just debug-grit-escape-pty
# 'stash|tags'`. Serves the sc-grit dev loop.
#
# run the sc grit escape hatch end to end under a pty
[group("debug")]
debug-grit-escape-pty filter="":
    #!/usr/bin/env bash
    set -euo pipefail
    command -v script >/dev/null || { echo "debug-grit-escape-pty needs util-linux 'script' on PATH"; exit 1; }
    out=$(nix build --no-link --print-out-paths .#default)
    export SPINCLASS_BIN="$out/bin/spinclass"
    echo "SPINCLASS_BIN=$SPINCLASS_BIN" >&2
    cd zz-tests_bats
    filter={{ quote(filter) }}
    BATS_TEST_TIMEOUT=10 bats --jobs 1 ${filter:+--filter "$filter"} grit_escape.bats

# [debug] Time one zz-tests_bats file against the race-instrumented binary
# (.#spinclass-race), outside the sandbox and without the gate's parallel
# lanes, so a race-lane BATS_TEST_TIMEOUT failure can be measured rather than
# guessed at (#419's restart.bats). Per-test durations come from bats --timing;
# BATS_TEST_TIMEOUT is unset so a slow test reports its time instead of dying.
#
# time one bats file against the race build
[group("debug")]
debug-bats-race-timing file filter="":
    #!/usr/bin/env bash
    set -euo pipefail
    out=$(nix build --no-link --print-out-paths .#spinclass-race)
    export SPINCLASS_BIN="$out/bin/spinclass"
    echo "SPINCLASS_BIN=$SPINCLASS_BIN" >&2
    cd zz-tests_bats
    filter={{ quote(filter) }}
    unset BATS_TEST_TIMEOUT
    # A private runtime dir, so nothing the run starts joins the host's real
    # gate queue (its ledger lives under XDG_RUNTIME_DIR).
    XDG_RUNTIME_DIR=$(mktemp -d)
    export XDG_RUNTIME_DIR
    trap 'rm -rf "$XDG_RUNTIME_DIR"' EXIT
    bats --timing --jobs 1 ${filter:+--filter "$filter"} {{ quote(file) }}

# [debug] Scaffold a throwaway repo wired (via direnv PATH_add) to a
# devshell-built spinclass binary, then drop into $SHELL inside it — a
# real-worktree-free sandbox for manually exercising `sc start`/etc. against
# uncommitted spinclass changes.
#
# scaffold a throwaway repo on a devshell-built binary and drop into $SHELL
[group("debug")]
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
[group("debug")]
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
# drop) against the bridged tommy via godyn-test — the same tommy rev the
# nix-built binary links. Delete once #196 is closed.
#
# run the issue #196 regression test against the bridged tommy
[group("debug")]
debug-issue196:
    just debug-go-test internal/0/sweatfile 'TestScalarBeforeSubtable|TestScalarBeforeSubtableWithPrecedingTable|TestScalarBeforeSubtableHierarchy|TestStandaloneDottedHeadersConsumed' -test.v

# Tag a spinclass release. The "v" prefix is added for you, so pass
# the semver without it. Usage: just tag 0.1.0 "feat: initial release"
#
# sign and push a spinclass release tag
[group("maintenance")]
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
[group("maintenance")]
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
[group("maintenance")]
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
[group("explore")]
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

# Export the tracked tree at <rev> (git archive, no .git) into .tmp/<name>/tree/ so a
# reviewer reads exactly that commit while the branch keeps moving, then verify the
# export is complete (debug-tree-export-check) and fail if it is not. Serves the review
# loop; writes only under .tmp/<name>.
[group("debug")]
debug-tree-export name rev: && (debug-tree-export-check name rev)
    #!/usr/bin/env bash
    set -euo pipefail
    out=".tmp/{{ name }}/tree"
    rm -rf "$out"
    mkdir -p "$out"
    git archive "{{ rev }}" | tar -x -C "$out"
    echo "exported $(git rev-parse "{{ rev }}") to $out"

# Verify an existing .tmp/<name>/tree/ export against <rev>: every regular file and
# symlink in `git ls-tree -r <rev>` exists with the same blob id (a symlink: the same
# target), the counts agree, nothing extra is in the tree (stray files, empty
# directories), and no submodule was left out. Prints `TREE-EXPORT-OK <rev> <n> files`,
# or up to 10 problems and exits non-zero. Serves the review loop: run it later on a
# review package to know it still is the commit. Read-only.
[group("debug")]
debug-tree-export-check name rev:
    #!/usr/bin/env bash
    set -euo pipefail
    export LC_ALL=C
    out=".tmp/{{ name }}/tree"
    rev="{{ rev }}"
    [ -d "$out" ] || { echo "TREE-EXPORT-FAIL $rev: $out does not exist" >&2; exit 1; }
    commit=$(git rev-parse --verify "$rev^{commit}")
    scratch=$(mktemp -d)
    trap 'rm -rf "$scratch"' EXIT
    problems="$scratch/problems"
    want="$scratch/want"
    : > "$problems"
    : > "$want"
    git ls-tree -r -z --full-tree "$commit" > "$scratch/tree.z"
    nwant=0
    while IFS= read -r -d '' rec; do
      meta=${rec%%$'\t'*}
      path=${rec#*$'\t'}
      read -r mode _ sha <<< "$meta"
      case "$path" in
        *$'\n'*) echo "path with a newline cannot be checked: ${path//$'\n'/\\n}" >> "$problems"; continue ;;
      esac
      if [ "$mode" = 160000 ]; then
        echo "submodule not exported: $path ($sha)" >> "$problems"
        continue
      fi
      printf '%s\n' "$path" >> "$want"
      nwant=$((nwant + 1))
      f="$out/$path"
      if [ "$mode" = 120000 ]; then
        if [ ! -L "$f" ]; then echo "missing or not a symlink: $path" >> "$problems"; continue; fi
        got=$(printf '%s' "$(readlink -- "$f")" | git hash-object --stdin)
      else
        if [ -L "$f" ] || [ ! -f "$f" ]; then echo "missing or not a regular file: $path" >> "$problems"; continue; fi
        got=$(git hash-object --no-filters -- "$f")
      fi
      if [ "$got" != "$sha" ]; then
        echo "content differs: $path (exported blob $got, $rev has $sha)" >> "$problems"
      fi
    done < "$scratch/tree.z"
    sort "$want" > "$scratch/want.sorted"
    { find "$out" ! -type d -printf '%P\n'; find "$out" -mindepth 1 -type d -empty -printf '%P/ (empty directory)\n'; } | sort > "$scratch/have.sorted"
    nhave=$(find "$out" ! -type d | wc -l)
    if [ "$nhave" -ne "$nwant" ]; then
      echo "file count: $out holds $nhave files and symlinks, $rev has $nwant" >> "$problems"
    fi
    comm -13 "$scratch/want.sorted" "$scratch/have.sorted" | sed "s|^|extra, not in $rev: |" >> "$problems"
    if [ -s "$problems" ]; then
      echo "TREE-EXPORT-FAIL $rev ($commit): $(wc -l < "$problems") problem(s) in $out; first up to 10:" >&2
      head -n 10 "$problems" | sed 's/^/  /' >&2
      exit 1
    fi
    echo "TREE-EXPORT-OK $rev $nwant files"

# Build a review package for a fresh reviewer under .tmp/<name>/: full.diff (base..HEAD),
# src/ (touched non-test Go sources whole), tests/ (touched test files), docs/ (touched
# docs and the justfile), and context/ (the extra files named, whole). Files are copied
# as .txt with the path's slashes as `__`. Serves the review loop for security-boundary
# changes; no tool writes outside .tmp/<name>.
[group("debug")]
debug-review-package name base *context:
    #!/usr/bin/env bash
    set -euo pipefail
    out=".tmp/{{ name }}"
    rm -rf "$out"
    mkdir -p "$out/src" "$out/tests" "$out/docs" "$out/context"
    git diff "{{ base }}..HEAD" > "$out/full.diff"
    flat() { printf '%s' "${1//\//__}.txt"; }
    while IFS= read -r f; do
      [ -f "$f" ] || continue
      case "$f" in
        *_tommy.go) ;;
        *_test.go) cp "$f" "$out/tests/$(flat "$f")" ;;
        *.go) cp "$f" "$out/src/$(flat "$f")" ;;
        *) cp "$f" "$out/docs/$(flat "$f")" ;;
      esac
    done < <(git diff --name-only "{{ base }}..HEAD")
    for f in {{ context }}; do
      [ -f "$f" ] && cp "$f" "$out/context/$(flat "$f")"
    done
    echo "package at $out: $(ls "$out/src" | wc -l) sources, $(ls "$out/tests" | wc -l) tests, $(ls "$out/docs" | wc -l) docs, $(ls "$out/context" | wc -l) context files, $(wc -l < "$out/full.diff") diff lines"

# [debug] The two real halves of the bridge against each other (FDR 0048, J-H1
# start order): spinclass's real `spinclass serve` listener and clown's real shim
# (`clown bridge-listen`, from a PRE-BUILT clown named by the clown_bin argument) in a real transient unit
# under clown-agent.slice, both start orders: A serves first, then the unit; B
# starts the unit first, then serve; C sends a mismatched hello from inside the
# admitted unit. bridge.cgroup and the unit record are written by clown's REAL
# writers (bridgewire.WriteCgroupFile, agentscope.WriteResult) through clown's
# separate `clown-bridge-seam` binary (the seam_bin argument: a PRE-BUILT store
# path, never in production clown); the principal mapping line is written
# by this recipe (spinclass has no CLI for its confined-launch writer, pinned to
# the real writer by TestRecipeMappingLineIsWhatTheRealWriterWrites).
# Scratch under a 0700 mktemp; nothing of the operator's is touched; one run at a
# time (host-load per-cpu < 1.0).
#
# run both real halves against each other in both start orders
[group("debug")]
debug-bridge-halves-proof clown_bin seam_bin:
    #!/usr/bin/env bash
    set -uo pipefail
    export LC_ALL=C
    say() { printf 'halves: %s\n' "$*"; }
    failed=0
    pass() { say "PASS: $*"; }
    fail() { say "FAIL: $*"; failed=1; }
    load="$(cut -d' ' -f1 /proc/loadavg)"; cpus="$(nproc)"
    if awk -v l="$load" -v n="$cpus" 'BEGIN{exit !(l/n >= 1.0)}'; then say "REFUSED: load $load over $cpus cpus is per-cpu >= 1.0"; exit 1; fi
    command -v socat >/dev/null || { say "socat is needed"; exit 1; }
    out="$(nix build .#default --no-link --print-out-paths 2>/dev/null | tail -1)"
    sc="$out/bin/spinclass"; [ -x "$sc" ] || { say "no spinclass built"; exit 1; }
    # The clown binary is a PRE-BUILT store path the operator or the clown session
    # names: this recipe never builds a flake outside this worktree.
    clown="{{ clown_bin }}"
    case "$clown" in /nix/store/*/bin/clown) ;; *) say "clown_bin must be an absolute /nix/store/.../bin/clown path, got '$clown'"; exit 1;; esac
    [ -x "$clown" ] || { say "no executable clown at $clown"; exit 1; }
    seam="{{ seam_bin }}"
    case "$seam" in /nix/store/*/bin/clown-bridge-seam) ;; *) say "seam_bin must be an absolute /nix/store/.../bin/clown-bridge-seam path, got '$seam'"; exit 1;; esac
    [ -x "$seam" ] || { say "no executable clown-bridge-seam at $seam"; exit 1; }
    say "spinclass=$sc"; say "clown=$clown"; say "seam=$seam"
    base="$(mktemp -d "${XDG_RUNTIME_DIR:-/tmp}/bh.XXXXXX")"; chmod 0700 "$base"
    ua="clown-agent-bh$$a.service"; ub="clown-agent-bh$$b.service"; uc="clown-agent-bh$$c.service"
    serve_pids=()
    cleanup() {
      for u in "$ua" "$ub" "$uc"; do systemctl --user stop "$u" >/dev/null 2>&1 || true; done
      for p in "${serve_pids[@]}"; do kill "$p" >/dev/null 2>&1 || true; done
      exec 7>&- 8>&- 9>&- 2>/dev/null || true
      sleep 0.3
      rm -rf "$base"
    }
    trap cleanup EXIT
    export HOME="$base/home" XDG_STATE_HOME="$base/state" XDG_CONFIG_HOME="$base/cfg"
    mkdir -p "$HOME" "$XDG_STATE_HOME" "$XDG_CONFIG_HOME"
    export GIT_AUTHOR_NAME=halves GIT_AUTHOR_EMAIL=h@example.invalid GIT_COMMITTER_NAME=halves GIT_COMMITTER_EMAIL=h@example.invalid GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
    git init -q --bare --initial-branch=master "$base/up.git"
    git init -q --initial-branch=master "$base/seed"
    git -C "$base/seed" -c commit.gpgsign=false commit -q --allow-empty -m initial
    git -C "$base/seed" remote add origin "$base/up.git"
    git -C "$base/seed" push -q -u origin master
    git clone -q "$base/up.git" "$base/proj"
    ( cd "$base/proj" && "$sc" --format tap start --no-attach >"$base/start.out" 2>&1 ) || { say "sc start failed"; tail -5 "$base/start.out"; exit 1; }
    wt="$(ls -d "$base/proj/.worktrees"/*/ 2>/dev/null | head -1)"; wt="${wt%/}"
    [ -d "$wt" ] || { say "no worktree"; exit 1; }
    key="proj/$(basename "$wt")"; principal="halves-$$"
    h="$(printf %s "$(realpath "$wt")" | sha256sum | cut -c1-16)"
    rec="$XDG_STATE_HOME/spinclass/units/$h.json"
    map="$XDG_STATE_HOME/spinclass/principals/$principal.json"
    say "session=$key principal=$principal"
    init='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"halves","version":"0"}}}'
    who='{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"whoami","arguments":{}}}'
    rpc() { printf '%s\n%s\n' "$2" "$3" | timeout 10 socat -t3 - UNIX-CONNECT:"$1" 2>/dev/null; }
    raw() { printf '%s\n' "$2" | timeout 5 socat -t2 - UNIX-CONNECT:"$1" 2>/dev/null; }
    waitfor() { # seconds, test command...
      local n="$1"; shift; local end=$((SECONDS + n))
      while [ "$SECONDS" -lt "$end" ]; do "$@" && return 0; sleep 0.1; done; return 1
    }
    start_serve() { # label fd unit
      bd="$base/b$1"; cgf="$base/cg$1"; mkdir -m 0700 "$bd"
      mkfifo "$base/in$1"
      case "$2" in 9) exec 9<>"$base/in$1";; 7) exec 7<>"$base/in$1";; *) exec 8<>"$base/in$1";; esac
      ( cd "$wt" && exec env SPINCLASS_SESSION_ID="$key" CLOWN_SESSION_ID="$principal" CLOWN_BRIDGE_DIR="$bd" CLOWN_BRIDGE_UNIT="$3" CLOWN_BRIDGE_CGROUP_FILE="$cgf" "$sc" serve <"$base/in$1" >"$base/serve$1.out" 2>"$base/serve$1.err" ) &
      serve_pids+=("$!")
    }
    start_unit() { # label unit
      local nm="${2%.service}"
      systemd-run --user --quiet --collect --slice=clown-agent.slice --unit="$nm" \
        -p StandardOutput=file:"$base/shim$1.out" -p StandardError=file:"$base/shim$1.err" \
        "$clown" bridge-listen --socket "$base/b$1/bridge.sock" --unix "$base/ep$1.sock=session" >/dev/null 2>&1
    }
    write_cg() { # label unit: the launcher's write once the unit is active
      local cg; cg="$(systemctl --user show -p ControlGroup --value "$2")"
      case "$cg" in */clown-agent.slice/"$2") ;; *) fail "unit cgroup $cg does not end in /clown-agent.slice/$2"; return 1;; esac
      # clown's REAL writer (bridgewire.WriteCgroupFile) through its hidden verb.
      "$seam" cgroup-file --path "$base/cg$1" --cgroup "$cg" >"$base/wcg$1.out" 2>&1 \
        || { fail "clown-bridge-seam cgroup-file failed: $(head -c 200 "$base/wcg$1.out")"; return 1; }
      cgval="$cg"
    }
    write_record_and_mapping() { # unit
      mkdir -p "$(dirname "$map")"
      # The unit record is clown's REAL writer (agentscope.WriteResult over unitresult.WriteResult),
      # through its hidden verb, written after the shim is ready as in a launch.
      "$seam" result --path "$rec" --unit "$1" --cgroup "$cgval" --principal "$principal" --bridge recorded >"$base/wres.out" 2>&1 \
        || { fail "clown-bridge-seam result failed: $(head -c 200 "$base/wres.out")"; return 1; }
      # The principal mapping: spinclass has no CLI that runs its confined-launch writer
      # (RecordPrincipalFromUnit runs inside `sc spawn`), so the recipe writes this one line;
      # The line's template lives in ONE file the test also reads, and
      # TestRecipeMappingLineIsWhatTheRealWriterWrites pins it to that writer's output.
      # The file is DATA: its four @PLACEHOLDER@ fields are replaced as plain text, never
      # used as a printf format string.
      shopt -u patsub_replacement 2>/dev/null || true
      mapline="$(cat internal/bravo/confinement/testdata/halves-mapping-line.json)"
      mapline="${mapline//@PRINCIPAL@/$principal}"
      mapline="${mapline//@SESSION_KEY@/$key}"
      mapline="${mapline//@WORKTREE@/$wt}"
      mapline="${mapline//@UNIT@/$1}"
      printf '%s\n' "$mapline" >"$map"
      # claude's SessionStart hook, run for real (it stamps the principal into the session record).
      ( cd "$wt" && printf '{"hook_event_name":"SessionStart","session_id":"halves","source":"startup","cwd":"%s"}' "$wt" | env CLOWN_SESSION_ID="$principal" "$sc" hook >"$base/hook.out" 2>&1 ) || say "SessionStart hook exited nonzero: $(head -c 200 "$base/hook.out")"
    }
    shim_ready() { grep -q '^bridge-listen: ready control' "$base/shim$1.out" 2>/dev/null; }
    sock_up() { [ -S "$1" ]; }

    say "=== ORDER A: serve up before the unit ==="
    start_serve a 9 "$ua"
    waitfor 10 sock_up "$base/ba/bridge.sock" && pass "A: serve bound bridge.sock at once, with no mapping, no record, no cgroup file" || fail "A: serve never bound bridge.sock"
    r="$(raw "$base/ba/bridge.sock" 'BRIDGE/1 control')"
    case "$r" in "ERR refused"*) pass "A: control answers ERR refused while the cgroup file is missing";; *) fail "A: control before the cgroup file got: $r";; esac
    start_unit a "$ua" && say "unit $ua started (the shim is its main process)" || fail "A: systemd-run failed"
    waitfor 10 systemctl --user is-active --quiet "$ua"
    write_cg a "$ua" && say "cgroup file written: $cgval"
    if waitfor 15 shim_ready a; then pass "A: the shim holds control (ready control) before any mapping or record exists"; else fail "A: no 'ready control' in 15 s"; cat "$base/shima.out" 2>/dev/null; fi
    r="$(rpc "$base/epa.sock" "$init" "$who")"
    case "$r" in *'"result"'*) fail "A: a session call before the mapping was answered: $r";; *) pass "A: a session call before the mapping/record is not served (no result)";; esac
    if grep -qi 'refused' "$base/shima.err" 2>/dev/null; then pass "A: the shim logged the refusal ($(grep -i refused "$base/shima.err" | head -1 | cut -c1-120))"; else fail "A: the shim logged no refusal"; fi
    write_record_and_mapping "$ua"
    r="$(rpc "$base/epa.sock" "$init" "$who")"
    case "$r" in *"$key"*'"result"'*|*'"result"'*"$key"*) pass "A: whoami through the shim answers for $key after the record and mapping";; *) fail "A: whoami after the mapping got: $r";; esac
    # Bridged ESCALATION REFUSALS, from the admitted unit's shim (its endpoint dials the bridge from
    # inside the unit's cgroup): a tool outside the five allow-listed ones is -32602, a refused
    # argument of an allowed tool is -32001 `refused: ...`, and no restart request is queued.
    tool_call() { printf '{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"%s","arguments":%s}}' "$1" "$2" "$3"; }
    rpc_calls() { local sock="$1"; shift; printf '%s\n' "$@" | timeout 10 socat -t3 - UNIX-CONNECT:"$sock" 2>/dev/null; }
    restart_marks() { { grep -rl '"restart_request"' "$XDG_STATE_HOME/spinclass/records" "$wt/.spinclass" 2>/dev/null || true; } | wc -l | tr -d ' '; }
    marks_before="$(restart_marks)"
    r="$(rpc_calls "$base/epa.sock" "$init" "$(tool_call 3 stop-this-session '{}')")"
    case "$r" in *'-32602'*) pass "A: a tool outside the five (stop-this-session) over the bridge is -32602: $(printf %s "$r" | tail -1 | cut -c1-140)";; *) fail "A: stop-this-session over the bridge got: $r";; esac
    r="$(rpc_calls "$base/epa.sock" "$init" "$(tool_call 4 restart-this-session '{"force":true,"reason":"halves proof"}')")"
    case "$r" in *'-32001'*'refused: '*) pass "A: restart-this-session with force over the bridge is -32001 refused: $(printf %s "$r" | tail -1 | cut -c1-140)";; *) fail "A: restart-this-session force got: $r";; esac
    marks_after="$(restart_marks)"
    if [ "$marks_before" = "$marks_after" ] && [ "$marks_after" = 0 ]; then pass "A: no restart request was queued by the refused calls (records carry no restart_request)"; else fail "A: restart_request marks before=$marks_before after=$marks_after"; fi
    r="$(raw "$base/ba/bridge.sock" 'BRIDGE/1 control')"
    case "$r" in "ERR refused"*) pass "A: a foreign caller (a process outside the unit) gets ERR refused";; *) fail "A: a foreign caller got: $r";; esac
    rm -f "$base/cga"
    sleep 5
    if ! sock_up "$base/ba/bridge.sock" || [ -z "$(rpc "$base/epa.sock" "$init" "$who")" ]; then pass "A: the cgroup file removed after being valid ends the listener's service"; else fail "A: the listener still serves after the cgroup file was removed"; fi
    if grep -qi 'down' "$base/shima.err" 2>/dev/null; then pass "A: the shim said the bridge is down ($(grep -i down "$base/shima.err" | tail -1 | cut -c1-140))"; else fail "A: the shim never said down"; fi
    systemctl --user stop "$ua" >/dev/null 2>&1 || true
    kill "${serve_pids[0]}" >/dev/null 2>&1 || true
    exec 9>&-

    say "=== ORDER B: the unit up before serve ==="
    mkdir -m 0700 "$base/bb"
    t0=$SECONDS
    start_unit b "$ub" || fail "B: systemd-run failed"
    waitfor 10 systemctl --user is-active --quiet "$ub"
    write_cg b "$ub"
    sleep 3
    if shim_ready b; then fail "B: the shim claimed ready control with no serve at all"; else pass "B: with no serve the shim is not ready (it retries control)"; fi
    rmdir "$base/bb" 2>/dev/null; start_serve b 8 "$ub"
    if waitfor 15 shim_ready b; then pass "B: the shim was admitted $((SECONDS - t0)) s after the unit started, once serve bound (inside the 15 s window, no deadlock)"; else fail "B: no 'ready control' after serve bound"; cat "$base/shimb.err" 2>/dev/null | tail -3; say "bb: $(ls -la "$base/bb" 2>&1 | tr '\n' ' ')"; say "serve b alive: $(kill -0 "${serve_pids[1]}" 2>&1 && echo yes || echo no)"; fi
    write_record_and_mapping "$ub"
    r="$(rpc "$base/epb.sock" "$init" "$who")"
    case "$r" in *"$key"*'"result"'*|*'"result"'*"$key"*) pass "B: whoami through the shim answers for $key";; *) fail "B: whoami got: $r";; esac
    systemctl --user stop "$ub" >/dev/null 2>&1 || true
    kill "${serve_pids[1]}" >/dev/null 2>&1 || true
    exec 8>&-
    sleep 0.5

    say "=== ORDER C: a mismatched hello from a client INSIDE the admitted unit ==="
    start_serve c 7 "$uc"
    waitfor 10 sock_up "$base/bc/bridge.sock" && pass "C: serve bound bridge.sock" || fail "C: serve never bound bridge.sock"
    socat_bin="$(command -v socat)"
    systemd-run --user --quiet --collect --slice=clown-agent.slice --unit="${uc%.service}" \
      sh -c "sleep 4; printf 'BRIDGE/9 session\n' | $socat_bin -t2 - UNIX-CONNECT:$base/bc/bridge.sock >$base/verc.out 2>&1" >/dev/null 2>&1 \
      && say "unit $uc started (a client that sends BRIDGE/9, inside the unit's cgroup)" || fail "C: systemd-run failed"
    waitfor 10 systemctl --user is-active --quiet "$uc"
    write_cg c "$uc" && say "cgroup file written: $cgval"
    waitfor 15 test -s "$base/verc.out"
    r="$(cat "$base/verc.out" 2>/dev/null)"
    case "$r" in "ERR version"*) pass "C: a mismatched hello from inside the admitted unit gets: $(printf %s "$r" | head -1 | cut -c1-120)";; *) fail "C: the mismatched hello got: '$r'";; esac
    say "C: the shim's own unusable-message text was NOT exercised (the shim sends a correct hello; the real serve cannot be made to answer a mismatch to it without changing it)"
    systemctl --user stop "$uc" >/dev/null 2>&1 || true
    kill "${serve_pids[2]}" >/dev/null 2>&1 || true
    exec 7>&-
    sleep 0.5

    left_units="$(systemctl --user list-units --all --no-legend 'clown-agent-bh*' 2>/dev/null | grep -c "bh$$")"
    left_procs="$(pgrep -f "$base" | grep -vc "^$$\$" || true)"
    if [ "${left_units:-0}" = 0 ]; then pass "no transient unit left"; else fail "$left_units unit(s) left"; fi
    if [ "${left_procs:-0}" = 0 ]; then pass "no process left naming the scratch directory"; else fail "$left_procs process(es) left"; pgrep -af "$base" | head -3; fi
    if [ "$failed" = 0 ]; then say "HALVES-PROOF-OK both orders: serve first and unit first"; else
      say "serve a err:"; tail -5 "$base/servea.err" 2>/dev/null; say "shim a err:"; tail -5 "$base/shima.err" 2>/dev/null
      say "serve b err:"; tail -5 "$base/serveb.err" 2>/dev/null; say "shim b err:"; tail -5 "$base/shimb.err" 2>/dev/null
      exit 1
    fi
