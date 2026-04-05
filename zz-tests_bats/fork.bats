#! /usr/bin/env bats

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  setup_stubs
  create_repo
}

function fork_creates_new_branch { # @test
  cd "$TEST_REPO"
  local bin="${SPINCLASS_BIN:-spinclass}"
  local attach_output
  attach_output=$("$bin" --format tap start --no-attach 2>&1)
  local wt_path
  wt_path=$(extract_wt_path "$attach_output")

  # Fork from inside the worktree (cwd-based)
  cd "$wt_path"
  run_sc fork new_branch
  assert_success

  # New worktree should exist
  assert [ -d "$TEST_REPO/.worktrees/new_branch" ]
  assert [ -f "$TEST_REPO/.worktrees/new_branch/.git" ]
}

function fork_creates_branch_with_from_flag { # @test
  cd "$TEST_REPO"
  local bin="${SPINCLASS_BIN:-spinclass}"
  local attach_output
  attach_output=$("$bin" --format tap start --no-attach 2>&1)
  local wt_path
  wt_path=$(extract_wt_path "$attach_output")

  # Fork using --from flag (can run from anywhere)
  run_sc fork --from "$wt_path" from_dst
  assert_success

  assert [ -d "$TEST_REPO/.worktrees/from_dst" ]
  assert [ -f "$TEST_REPO/.worktrees/from_dst/.git" ]
}

function fork_auto_names_branch { # @test
  cd "$TEST_REPO"
  local bin="${SPINCLASS_BIN:-spinclass}"
  local attach_output
  attach_output=$("$bin" --format tap start --no-attach 2>&1)
  local wt_path
  wt_path=$(extract_wt_path "$attach_output")
  local branch
  branch=$(basename "$wt_path")

  cd "$wt_path"
  run_sc fork
  assert_success

  # Should have created <branch>-1
  assert [ -d "$TEST_REPO/.worktrees/${branch}-1" ]
}

function fork_fails_outside_worktree { # @test
  cd "$TEST_REPO"
  local bin="${SPINCLASS_BIN:-spinclass}"
  "$bin" --format tap start --no-attach

  # Running from main repo (not a worktree) without --from should fail
  # because the main branch won't have a .worktrees/<branch> layout
  cd "$TEST_REPO"
  run_sc fork some_branch
  assert_failure
}
