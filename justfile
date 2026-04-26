default: build test

build:
    nix build --show-trace

test:
    nix develop --command tap-dancer go-test -skip-empty ./...

fmt:
    nix develop --command gofumpt -w .

lint:
    nix develop --command go vet ./...

clean:
    rm -rf result

deps:
    nix develop --command gomod2nix

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
