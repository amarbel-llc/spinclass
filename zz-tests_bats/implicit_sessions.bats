#! /usr/bin/env bats

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  setup_stubs
}

# Create a bare upstream and a clone checked out on master, with origin
# tracking. Sets TEST_UPSTREAM (bare) and TEST_CHECKOUT (working clone on
# master). The clone is the implicit "main checkout" the session attaches to.
create_origin_checkout() {
  export TEST_UPSTREAM="$BATS_TEST_TMPDIR/upstream.git"
  git init --bare --initial-branch=master "$TEST_UPSTREAM"

  local seed="$BATS_TEST_TMPDIR/seed"
  git init --initial-branch=master "$seed"
  echo "initial" >"$seed/file.txt"
  git -C "$seed" add file.txt
  git -C "$seed" commit -m "initial commit"
  git -C "$seed" remote add origin "$TEST_UPSTREAM"
  git -C "$seed" push -u origin master

  export TEST_CHECKOUT="$BATS_TEST_TMPDIR/checkout"
  git clone "$TEST_UPSTREAM" "$TEST_CHECKOUT"
  git -C "$TEST_CHECKOUT" config branch.master.remote origin
  git -C "$TEST_CHECKOUT" config branch.master.merge refs/heads/master
}

# Materialize a live implicit session at the given checkout. Writes the
# per-randID state file directly (mirroring session.WriteImplicit's on-disk
# shape) with the bats test process PID so FindImplicitAtCwd's IsAlive(PID)
# check passes for the whole test — driving the materialization through
# `spinclass hooks` SessionStart instead would record the (immediately dead)
# PID of the bash subprocess, and FindImplicitAtCwd would skip it.
materialize_implicit_session() {
  local checkout="$1"
  local rand="deadbeef"
  local repo_name
  repo_name=$(basename "$checkout")
  mkdir -p "$checkout/.spinclass"
  cat >"$checkout/.spinclass/state-$rand.json" <<EOF
{
  "state": "active",
  "repo_path": "$checkout",
  "worktree_path": "$checkout",
  "branch": "master",
  "session_key": "$repo_name/master-$rand",
  "kind": "implicit",
  "pid": $$
}
EOF
}

@test "merge from implicit main-checkout session runs hook then pushes, no rebase" {
  create_origin_checkout

  local marker="$BATS_TEST_TMPDIR/hook-ran.marker"
  # Pre-merge hook touches a marker so we can prove it executed. A plain
  # `touch` needs neither nix nor just, so the real hook path runs in bats.
  # shellcheck disable=SC2016
  cat >"$TEST_CHECKOUT/sweatfile" <<EOF
[hooks]
pre-merge = "touch '$marker'"
EOF

  materialize_implicit_session "$TEST_CHECKOUT"

  # Commit a change on master in the checkout (work already on the default
  # branch — the implicit session's defining property).
  echo "change" >"$TEST_CHECKOUT/file.txt"
  git -C "$TEST_CHECKOUT" add file.txt
  git -C "$TEST_CHECKOUT" commit -m "implicit work"
  local head
  head=$(git -C "$TEST_CHECKOUT" rev-parse HEAD)

  # Merge from inside the checkout, no target → implicit route.
  cd "$TEST_CHECKOUT" || return
  run_sc merge
  assert_success

  # Hook ran.
  assert [ -f "$marker" ]

  # Pushed: bare upstream master == checkout HEAD.
  local upstream_head
  upstream_head=$(git -C "$TEST_UPSTREAM" rev-parse master)
  assert [ "$upstream_head" = "$head" ]

  # No rebase step in the output (implicit path is hook-then-push only).
  refute_output --partial "rebase"
  # Reached the push path, not the worktree-only reject.
  refute_output --partial "not inside a worktree session"
}

@test "merge with no implicit session and not a worktree still resolves normally" {
  # Sanity: without a materialized implicit session, `sc merge` from a plain
  # main checkout does NOT take the implicit route (FindImplicitAtCwd → nil),
  # so it falls through to the normal resolution path. We only assert it does
  # not erroneously short-circuit as implicit (no push of an unmerged tree).
  create_origin_checkout
  cd "$TEST_CHECKOUT" || return
  run_sc merge
  # No implicit session and no worktrees to choose → normal path. We don't
  # pin the exact outcome (it depends on worktree resolution), only that the
  # implicit branch was not taken (it would have pushed/hooked silently).
  refute_output --partial "push master"
}
