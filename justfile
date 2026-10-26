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
# tommy bump or a Sweatfile struct change, through godyn's escape hatch
# (godyn-go, igloo FDR 0008; no ambient go; tommy comes from goRunInputs).
# The codec header stamps tommy's rev, so regenerate after every tommy bump.
# Not part of the gate: `verify-tommy-codegen` is the pure drift check.
#
# regenerate the tommy-generated sweatfile codec
[group("build")]
build-tommy-codegen:
    nix run --inputs-from . igloo#godyn-go -- -- go generate ./internal/0/sweatfile/

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
    bin="$HOME/.nix-profile/bin/sc"
    echo "=== validating from {{ dir }}"
    cd "{{ dir }}" && "$bin" validate
    exit 0

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
      if clown restart --help >/dev/null 2>&1; then pass "clown restart --help exists"; else fail "clown restart --help failed: this clown predates the restart verb"; fi
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
      env=$(tr '\0' '\n' < "$d/environ" 2>/dev/null) || continue
      printf '%s\n' "$env" | grep -xF -e "$pat_c" -e "$pat_s" >/dev/null || continue
      args=$(tr '\0' '\n' < "$d/cmdline" 2>/dev/null) || continue
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
      profile=$(printf '%s\n' "$args" | awk 'prev == "--profile" { print; exit } /^--profile=/ { sub(/^--profile=/, ""); print; exit } { prev = $0 }')
      start=$(sed 's/.*) //' "$d/stat" 2>/dev/null | cut -d' ' -f20 || true)
      jq -nc --argjson pid "$pid" --arg kind "$kind" --arg exe "$exe" --arg exe_real "$exe_real" --arg start "$start" \
        --argjson unattended "$unatt" --argjson dontask "$dontask" --arg profile "$profile" \
        '{pid: $pid, kind: $kind, exe: $exe, exe_real: $exe_real, start_ticks: $start, unattended: $unattended, dontask: $dontask, profile_argv: $profile}' >> "$pf"
    done
    jq --slurpfile procs "$pf" --arg peek "$peek" --arg onpath "$onpath" --arg taken "$(date -u +%FT%TZ)" --arg label "$label" '{
      label: $label, taken_at: $taken,
      session_key: .session_key, principal: (.principal // ""), worktree: .worktree_path,
      state: .state, kind: (.kind // ""),
      restarting_at: .restarting_at, restarted_at: .restarted_at, restarting_by: .restarting_by,
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
    check "agent argv has --permission-mode dontAsk" 'any(.procs[]; .kind == "agent" and .dontask)'
    check "a spinclass serve process was found" 'any(.procs[]; .kind == "serve")'
    if jq -e '.spinclass_on_path as $p | all(.procs[] | select(.kind == "serve"); .exe_real == $p and (.exe | endswith("(deleted)") | not))' "$snap" >/dev/null; then
      info "serve binary is the spinclass on PATH now: the post-restart STALE check is vacuous unless you change the spinclass on PATH before restarting"
    else
      info "serve binary already differs from the spinclass on PATH (STALE before the restart); the worker's own version tool is authoritative (it also recognises same-build-different-path)"
    fi
    echo
    if [ "$fails" -gt 0 ]; then echo "SUMMARY: $fails FAIL"; exit 1; fi
    echo "SUMMARY: baseline PASS (MANUAL: note the troupe credential and send the nonce DM now)"

# [explore] The same snapshot AFTER the relaunch, compared with baseline.json.
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
    check "a NEW agent process exists (no agent pid or start time survived from the baseline)" '[.procs[] | select(.kind == "agent")] as $a | ($a | length) >= 1 and all($a[]; . as $n | all($b[0].procs[] | select(.kind == "agent"); .pid != $n.pid or .start_ticks != $n.start_ticks))'
    if jq -e --slurpfile b "$base" 'any($b[0].procs[]; .kind == "agent" and .unattended)' "$after" >/dev/null; then
      check "relaunched agent environment has CLOWN_UNATTENDED=1" 'any(.procs[]; .kind == "agent" and .unattended)'
    else info "baseline agent had no CLOWN_UNATTENDED=1: unattended check skipped (the baseline recipe FAILs this)"; fi
    if jq -e --slurpfile b "$base" 'any($b[0].procs[]; .kind == "agent" and .dontask)' "$after" >/dev/null; then
      check "relaunched agent argv has --permission-mode dontAsk" 'any(.procs[]; .kind == "agent" and .dontask)'
    else info "baseline agent was not dontAsk: dontAsk check skipped"; fi
    check "a spinclass serve process exists after the relaunch" 'any(.procs[]; .kind == "serve")'
    check "serve binary is the spinclass on PATH (no STALE)" '.spinclass_on_path as $p | any(.procs[]; .kind == "serve") and all(.procs[] | select(.kind == "serve"); .exe_real == $p and (.exe | endswith("(deleted)") | not))'
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
# print the FDR 0035 confinement table for a session on this host
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
