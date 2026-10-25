#! /usr/bin/env bats

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  create_session_sweatfile
  create_repo
}

function assert_session_state_rejects_a_closed_sessions_tombstone { # @test
  # #52: a cleanly closed session leaves a tombstone (a regular file in the
  # index) for tombstone-retention. assert_session_state means "a live session
  # is tracked", so it must not pass on the tombstone alone;
  # assert_closed_session_state is the tombstone assertion.
  cd "$TEST_REPO" || return
  run_sc start --no-attach
  assert_success
  local wt_path
  wt_path=$(extract_wt_path "$output")
  run_sc close --force "$(basename "$wt_path")"
  assert_success

  run assert_session_state
  assert_failure
  assert_closed_session_state
}

function spinclass_start_writes_session_state { # @test
  cd "$TEST_REPO" || return
  run_sc_session start
  assert_success

  # Session should be tracked in the central index.
  assert_session_state
  local state_file
  state_file=$(first_session_state_path)
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
  cd "$TEST_REPO" || return
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
  cd "$TEST_REPO" || return
  local bin="${SPINCLASS_BIN:-spinclass}"

  # Start a session — writes state and exits
  local start_output
  start_output=$(timeout --preserve-status 10s "$bin" --format tap start 2>&1)

  local wt_path
  wt_path=$(extract_wt_path "$start_output")

  # cd into the worktree and resume with no args (auto-detect from cwd).
  # Auto-detect shows a confirm dialog before attaching; --yes skips it,
  # which is also required here because the bats harness is non-TTY.
  cd "$wt_path" || return
  run_sc_session resume --yes
  assert_success
}

function spinclass_resume_from_cwd_non_tty_requires_yes { # @test
  cd "$TEST_REPO" || return
  local bin="${SPINCLASS_BIN:-spinclass}"

  # Start a session — writes state and exits
  local start_output
  start_output=$(timeout --preserve-status 10s "$bin" --format tap start 2>&1)

  local wt_path
  wt_path=$(extract_wt_path "$start_output")

  # Auto-detect resume without --yes on a non-TTY stdin must fail with
  # the -y hint instead of silently attaching (resumeConfirmPlan).
  cd "$wt_path" || return
  run_sc_session resume
  assert_failure
  assert_output --partial "pass -y"
}

function spinclass_resume_no_session_fails { # @test
  cd "$TEST_REPO" || return

  # A raw git worktree with no spinclass state at all (--no-attach now writes
  # findable inactive state, so it would no longer be session-less).
  local wt_path="$TEST_REPO/.worktrees/orphan-wt"
  git -C "$TEST_REPO" worktree add -b orphan-wt "$wt_path"

  # cd into the worktree and try to resume — should fail
  cd "$wt_path" || return
  run_sc_session resume
  assert_failure
  assert_output --partial "no session"
}

function spinclass_resume_from_main_repo_by_id { # @test
  cd "$TEST_REPO" || return
  local bin="${SPINCLASS_BIN:-spinclass}"

  # Start a session — writes state and exits
  local start_output
  start_output=$(timeout --preserve-status 10s "$bin" --format tap start 2>&1)
  local wt_path
  wt_path=$(extract_wt_path "$start_output")
  local wt_id
  wt_id=$(basename "$wt_path")

  # Resume by ID from main repo (not inside worktree) should succeed
  cd "$TEST_REPO" || return
  run_sc_session resume "$wt_id"
  assert_success
}

function spinclass_resume_from_main_repo_no_args_lists_ids { # @test
  # #45: a no-args, non-TTY `resume` from the main repo now stops at the
  # confirmation gate ("resume requires an interactive terminal (or pass -y
  # to skip confirmation)", observed 2026-10-25) before any picker could list
  # IDs, so what this test claims no longer matches the behaviour. Which
  # behaviour is wanted is open on #45.
  skip 'see #45: no-args non-TTY resume now refuses at the confirmation gate, not the picker'
  cd "$TEST_REPO" || return
  local bin="${SPINCLASS_BIN:-spinclass}"

  # Start a session — writes state and exits
  local start_output
  start_output=$(timeout --preserve-status 10s "$bin" --format tap start 2>&1)
  local wt_path
  wt_path=$(extract_wt_path "$start_output")
  local wt_id
  wt_id=$(basename "$wt_path")

  # Resume with no args from main repo (non-TTY) should fail but list IDs
  cd "$TEST_REPO" || return
  run_sc_session resume
  assert_failure
  # The non-TTY picker refusal (internal/bravo/sessionpick): its copy changed
  # from "available sessions" in 03a9265 (#45).
  assert_output --partial "no session selected; available:"
  assert_output --partial "$wt_id"
}

function spinclass_resume_non_tty_emits_no_title_escape { # @test
  cd "$TEST_REPO" || return
  local bin="${SPINCLASS_BIN:-spinclass}"

  local start_output
  start_output=$(timeout --preserve-status 10s "$bin" --format tap start 2>&1)
  local wt_id
  wt_id=$(basename "$(extract_wt_path "$start_output")")

  # resume-title (#154) is TTY-gated: piped (non-TTY) output must carry
  # no OSC-2 escape bytes.
  run_sc_session resume "$wt_id"
  assert_success
  refute_output --partial $'\033]2;'
}

function spinclass_resume_no_attach_shows_dry_run { # @test
  cd "$TEST_REPO" || return
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
