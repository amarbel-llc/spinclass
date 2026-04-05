#! /usr/bin/env bats

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  create_session_sweatfile
  create_repo
}

function spinclass_start_writes_session_state { # @test
  cd "$TEST_REPO"
  run_sc_session start
  assert_success

  # Session state dir should exist with exactly one state file
  local state_dir="$XDG_STATE_HOME/spinclass/sessions"
  assert [ -d "$state_dir" ]
  local state_file
  state_file=$(find "$state_dir" -name '*-state.json' | head -1)
  assert [ -n "$state_file" ]

  # Verify key fields in the state JSON
  run jq -r '.state' "$state_file"
  assert_output "inactive"

  run jq -r '.worktree_path' "$state_file"
  assert_success
  # Worktree path should be under .worktrees/
  assert_output --partial ".worktrees/"

  run jq -r '.branch' "$state_file"
  assert_success
  assert [ -n "$output" ]
}

function spinclass_resume_by_id { # @test
  cd "$TEST_REPO"
  local bin="${SPINCLASS_BIN:-spinclass}"

  # Start a session — writes state and exits (entrypoint is "true")
  local start_output
  start_output=$(timeout --preserve-status 10s "$bin" --format tap start 2>&1)

  # Extract the worktree dirname (the ID used by resume)
  local wt_path
  wt_path=$(extract_wt_path "$start_output")
  local wt_id
  wt_id=$(basename "$wt_path")

  # Resume by ID should succeed
  run_sc_session resume "$wt_id"
  assert_success
}

function spinclass_resume_from_cwd { # @test
  cd "$TEST_REPO"
  local bin="${SPINCLASS_BIN:-spinclass}"

  # Start a session — writes state and exits
  local start_output
  start_output=$(timeout --preserve-status 10s "$bin" --format tap start 2>&1)

  local wt_path
  wt_path=$(extract_wt_path "$start_output")

  # cd into the worktree and resume with no args (auto-detect from cwd)
  cd "$wt_path"
  run_sc_session resume
  assert_success
}

function spinclass_resume_no_session_fails { # @test
  cd "$TEST_REPO"

  # Create a worktree with --no-attach (no session state written)
  run_sc start --no-attach
  assert_success

  local wt_path
  wt_path=$(extract_wt_path "$output")

  # cd into the worktree and try to resume — should fail
  cd "$wt_path"
  run_sc_session resume
  assert_failure
  assert_output --partial "no session"
}

function spinclass_resume_from_main_repo_by_id { # @test
  cd "$TEST_REPO"
  local bin="${SPINCLASS_BIN:-spinclass}"

  # Start a session — writes state and exits
  local start_output
  start_output=$(timeout --preserve-status 10s "$bin" --format tap start 2>&1)
  local wt_path
  wt_path=$(extract_wt_path "$start_output")
  local wt_id
  wt_id=$(basename "$wt_path")

  # Resume by ID from main repo (not inside worktree) should succeed
  cd "$TEST_REPO"
  run_sc_session resume "$wt_id"
  assert_success
}

function spinclass_resume_from_main_repo_no_args_lists_ids { # @test
  cd "$TEST_REPO"
  local bin="${SPINCLASS_BIN:-spinclass}"

  # Start a session — writes state and exits
  local start_output
  start_output=$(timeout --preserve-status 10s "$bin" --format tap start 2>&1)
  local wt_path
  wt_path=$(extract_wt_path "$start_output")
  local wt_id
  wt_id=$(basename "$wt_path")

  # Resume with no args from main repo (non-TTY) should fail but list IDs
  cd "$TEST_REPO"
  run_sc_session resume
  assert_failure
  assert_output --partial "available sessions"
  assert_output --partial "$wt_id"
}

function spinclass_resume_no_attach_shows_dry_run { # @test
  cd "$TEST_REPO"
  local bin="${SPINCLASS_BIN:-spinclass}"

  # Start a session — writes state and exits
  local start_output
  start_output=$(timeout --preserve-status 10s "$bin" --format tap start 2>&1)
  local wt_path
  wt_path=$(extract_wt_path "$start_output")
  local wt_id
  wt_id=$(basename "$wt_path")

  # Resume with --no-attach should succeed and show SKIP
  run_sc_session resume --no-attach "$wt_id"
  assert_success
  assert_output --partial "SKIP"
}
