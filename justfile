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
