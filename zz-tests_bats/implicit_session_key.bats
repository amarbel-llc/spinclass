#! /usr/bin/env bats

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  create_repo
}

function implicit_session_key_prints_bare_key_despite_global_format { # @test
  run_sc implicit-session-key --claude-session-id 0b6a1f7e-3c2d-4e5f-9a8b-7c6d5e4f3a2b --cwd "$TEST_REPO"
  assert_success
  assert_output --regexp "^repo/[0-9a-f]{16}$"
  [ ! -e "$TEST_REPO/.spinclass" ]
}

function implicit_session_key_is_stable_for_same_pair { # @test
  run_sc implicit-session-key --claude-session-id same-id --cwd "$TEST_REPO"
  assert_success
  local first="$output"
  run_sc implicit-session-key --claude-session-id same-id --cwd "$TEST_REPO"
  assert_output "$first"
}

function implicit_session_key_refuses_subdirectory_with_exit_three { # @test
  mkdir -p "$TEST_REPO/sub"
  run_sc implicit-session-key --claude-session-id sid --cwd "$TEST_REPO/sub"
  assert_failure 3
  assert_output --partial "refused: not-toplevel"
}

function implicit_session_key_refuses_when_disabled { # @test
  printf '[hooks]\ndisable-implicit-sessions = true\n' >"$TEST_REPO/sweatfile"
  run_sc implicit-session-key --claude-session-id sid --cwd "$TEST_REPO"
  assert_failure 3
  assert_output --partial "refused: disabled"
}
