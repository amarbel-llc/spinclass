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
