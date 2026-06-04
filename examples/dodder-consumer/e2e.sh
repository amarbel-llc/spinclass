#!/usr/bin/env bash
#
# End-to-end test for the dodder-pinned spinclass (FDR 0008).
#
# Builds the consumer flake's dodder-pinned spinclass, drives a real
# `sc start` in a throwaway git repo, and asserts the per-worktree dodder
# repository + madder store + MCP wiring landed.
#
# REQUIRES pivy-agent UNLOCKED: dodder signs the new repo with your agent
# key (resolved via `dodder info-ssh_agent`). A locked/empty agent makes
# `dodder init` hard-fail by design — that is the documented behaviour,
# not a harness bug.
#
# This cannot run as a `nix flake check`: the build sandbox has no agent.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
cd "$here"

echo "# building dodder-pinned spinclass (fetches github inputs on first run)…"
out=$(nix build --no-link --print-out-paths ".#default")
spin="$out/bin/spinclass"
echo "# spinclass: $spin"

home=$(mktemp -d)
repo=$(mktemp -d)
cleanup() { rm -rf "$home" "$repo"; }
trap cleanup EXIT
export HOME="$home" # isolate sweatfile cascade + ~/.local/state/spinclass

git init -q -b main "$repo"
git -C "$repo" \
  -c user.email=e2e@test -c user.name=e2e -c commit.gpgsign=false \
  commit -q --allow-empty -m init

# Non-interactive entrypoint: sc start configures the worktree (madder +
# dodder init, .mcp.json, excludes, allow) then execs `true` and exits.
cat >"$repo/sweatfile" <<'EOF'
[session-entry]
start = ["true"]
EOF

echo '# sc start "dodder e2e"…'
(cd "$repo" && "$spin" start "dodder e2e")

# Fresh repo => exactly one worktree.
wt=$(echo "$repo"/.worktrees/*)
echo "# worktree: $wt"

fail=0
check() {
  local desc="$1"
  shift
  if "$@" >/dev/null 2>&1; then
    echo "ok - $desc"
  else
    echo "not ok - $desc"
    fail=1
  fi
}
has() { grep -qF "$2" "$1"; }

check ".dodder repository (config-seed)" test -f "$wt/.dodder/local/share/config-seed"
check ".madder default store reused" test -f "$wt/.madder/local/share/blob_stores/default/blob_store-config"
check ".dodder/ git-excluded" has "$repo/.git/info/exclude" ".dodder/"
check ".madder/ git-excluded" has "$repo/.git/info/exclude" ".madder/"
check "Bash(dodder:*) claude-allowed" has "$wt/.claude/settings.local.json" "Bash(dodder:*)"
check "Bash(madder:*) claude-allowed" has "$wt/.claude/settings.local.json" "Bash(madder:*)"
check "dodder shim symlink" test -L "$repo/.git/spinclass/bin/dodder"
check "madder shim symlink" test -L "$repo/.git/spinclass/bin/madder"
check ".mcp.json registers dodder" has "$wt/.mcp.json" '"dodder"'
check ".mcp.json dodder uses mcp arg" has "$wt/.mcp.json" '"mcp"'

# Bonus (soft): the repo's signatures verify against the agent key. sc
# start succeeding already proves signing ran (it would have hard-failed
# otherwise); fsck is extra confirmation and is not allowed to fail the
# suite on its own quirks.
dodderbin="$repo/.git/spinclass/bin/dodder"
if (cd "$wt" && DODDER_CEILING_DIRECTORIES="$wt" MADDER_CEILING_DIRECTORIES="$wt" \
  "$dodderbin" fsck) >/dev/null 2>&1; then
  echo "ok - dodder fsck verifies (bonus)"
else
  echo "# note: dodder fsck did not pass cleanly (bonus check, not fatal)"
fi

echo
if [ "$fail" -eq 0 ]; then
  echo "# ALL PASS"
else
  echo "# FAILURES — see 'not ok' lines above"
  exit 1
fi
