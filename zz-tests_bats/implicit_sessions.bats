#! /usr/bin/env bats

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  setup_stubs
}

# create_origin_checkout now lives in common.bash — the stale-base suite needs
# the same bare-upstream-plus-clone fixture.

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

@test "merge from implicit main-checkout session is refused and moves nothing" {
  # spinclass#317: implicit (main-checkout) merge support was removed. The
  # refusal must happen before the pre-merge hook or any push.
  create_origin_checkout

  local marker="$BATS_TEST_TMPDIR/hook-ran.marker"
  # shellcheck disable=SC2016
  cat >"$TEST_CHECKOUT/sweatfile" <<EOF
[hooks]
pre-merge = "touch '$marker'"
EOF

  materialize_implicit_session "$TEST_CHECKOUT"

  echo "change" >"$TEST_CHECKOUT/file.txt"
  git -C "$TEST_CHECKOUT" add file.txt
  git -C "$TEST_CHECKOUT" commit -m "implicit work"
  local upstream_before
  upstream_before=$(git -C "$TEST_UPSTREAM" rev-parse master)

  cd "$TEST_CHECKOUT" || return
  run_sc_crap merge
  assert_failure
  assert_output --partial "spinclass#317"
  assert_output --partial "sc start"

  # Nothing ran, nothing moved.
  assert [ ! -f "$marker" ]
  assert_equal "$(git -C "$TEST_UPSTREAM" rev-parse master)" "$upstream_before"
}

@test "check from implicit main-checkout session runs the hook" {
  create_origin_checkout

  local marker="$BATS_TEST_TMPDIR/check-hook-ran.marker"
  # shellcheck disable=SC2016
  cat >"$TEST_CHECKOUT/sweatfile" <<EOF
[hooks]
pre-merge = "touch '$marker'"
EOF

  materialize_implicit_session "$TEST_CHECKOUT"

  # `sc check` is gate-free (no attestation), so it runs the hook against cwd
  # for an implicit checkout exactly as it would for a worktree.
  cd "$TEST_CHECKOUT" || return
  run_sc_crap check
  assert_success

  # Hook ran.
  assert [ -f "$marker" ]
  # The hook is the stream's only stage: a passing node_end, no failing
  # test records (phase-only since go-crap v2.2.1 / crap#22).
  assert_crap '([.[] | select(.type == "node_end")] | length == 1 and all(.exit_code == 0))
    and ([.[] | select(.type == "test" and .ok == false)] | length == 0)'
  # Did not hit the worktree-only reject.
  refute_output --partial "not inside a worktree session"
}

@test "merge with no implicit session and not a worktree still resolves normally" {
  # Sanity: without a materialized implicit session, `sc merge` from a plain
  # main checkout does NOT take the implicit route (FindImplicitAtCwd → nil),
  # so it falls through to the normal resolution path. We only assert it does
  # not erroneously short-circuit as implicit (no push of an unmerged tree).
  create_origin_checkout
  cd "$TEST_CHECKOUT" || return
  run_sc_crap merge
  # No implicit session and no worktrees to choose → normal path. We don't
  # pin the exact outcome (it depends on worktree resolution), only that the
  # implicit branch was not taken (it would have pushed/hooked silently).
  refute_output --partial "push master"
}
