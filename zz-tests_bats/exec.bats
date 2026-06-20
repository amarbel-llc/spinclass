#! /usr/bin/env bats

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  setup_stubs
  create_session_sweatfile
  create_repo
}

# Replace the logging direnv stub (setup_stubs) with one that actually
# execs the wrapped command, so a test can observe the env/cwd `sc exec`
# set. direnv is invoked as `direnv exec <dir> <util...>`; drop the first
# two args and exec the rest in <dir>.
install_passthrough_direnv() {
  cat >"$BATS_TEST_TMPDIR/stubs/direnv" <<'STUB'
#!/bin/sh
dir="$2"
shift 2
cd "$dir" || exit 1
exec "$@"
STUB
  chmod +x "$BATS_TEST_TMPDIR/stubs/direnv"
}

# Start a session via the "true" entrypoint (create_session_sweatfile) and
# echo its worktree path. Writes findable session state (unlike --no-attach).
start_session() {
  local bin="${SPINCLASS_BIN:-spinclass}"
  local out
  # cd into TEST_REPO in a subshell so the worktree lands in the isolated
  # temp repo, not whatever real repo the test process happens to sit in.
  out=$(cd "$TEST_REPO" && timeout --preserve-status 10s "$bin" --format tap start 2>&1)
  extract_wt_path "$out"
}

function exec_in_session_sets_identity_env_and_cwd { # @test
  local wt
  wt=$(start_session)
  install_passthrough_direnv

  cd "$wt" || return
  # shellcheck disable=SC2016  # single quotes intentional: the inner `sh -c` expands the env sc exec sets
  run "${SPINCLASS_BIN:-spinclass}" exec -- sh -c 'echo "ID=$SPINCLASS_SESSION_ID"; echo "WT=$SPINCLASS_WORKTREE"; pwd'
  assert_success
  # Session id is <repo-dirname>/<branch>; repo dir is "repo".
  assert_output --partial "ID=repo/"
  assert_output --partial "WT=$wt"
  # The util ran in the worktree dir.
  assert_output --partial "$wt"
}

function exec_wraps_with_direnv_targeting_worktree { # @test
  # Uses the default logging direnv stub: assert sc exec invoked
  # `direnv exec <worktree> <util>`.
  local wt
  wt=$(start_session)

  cd "$wt" || return
  run "${SPINCLASS_BIN:-spinclass}" exec -- echo hi
  assert_success

  run cat "$BATS_TEST_TMPDIR/stubs/direnv.log"
  assert_success
  assert_output --partial "exec"
  assert_output --partial "$wt"
}

function exec_target_by_id_from_main_repo { # @test
  local wt
  wt=$(start_session)
  local wt_id
  wt_id=$(basename "$wt")
  install_passthrough_direnv

  # From the main repo (not inside the worktree), address by id.
  cd "$TEST_REPO" || return
  run "${SPINCLASS_BIN:-spinclass}" exec --session "$wt_id" -- pwd
  assert_success
  assert_output --partial "$wt"
}

function exec_explicit_target_miss_errors { # @test
  cd "$TEST_REPO" || return
  run "${SPINCLASS_BIN:-spinclass}" exec --session does-not-exist -- true
  assert_failure
  assert_output --partial "no session"
}

function exec_implicit_miss_degrades_to_cwd { # @test
  install_passthrough_direnv

  # The main repo checkout is not an sc worktree session -> implicit miss.
  cd "$TEST_REPO" || return
  # shellcheck disable=SC2016  # single quotes intentional: the inner `sh -c` expands the env sc exec sets
  run "${SPINCLASS_BIN:-spinclass}" exec -- sh -c 'echo "ID=[$SPINCLASS_SESSION_ID]"; pwd'
  assert_success
  # Degraded: no identity env, ran in cwd, with a notice.
  assert_output --partial "ID=[]"
  assert_output --partial "$TEST_REPO"
  assert_output --partial "not inside a spinclass session"
}

function exec_propagates_util_exit_code { # @test
  local wt
  wt=$(start_session)
  install_passthrough_direnv

  cd "$wt" || return
  run "${SPINCLASS_BIN:-spinclass}" exec -- sh -c 'exit 7'
  # shellcheck disable=SC2154  # $status is set by bats' run
  assert_equal "$status" 7
}

function exec_defaults_to_shell { # @test
  local wt
  wt=$(start_session)
  install_passthrough_direnv

  # A bare `sc exec` runs $SHELL in the worktree (devshell-wrapped).
  cat >"$BATS_TEST_TMPDIR/stubs/myshell" <<'EOF'
#!/bin/sh
echo "SHELL_RAN_IN=$(pwd)"
EOF
  chmod +x "$BATS_TEST_TMPDIR/stubs/myshell"

  cd "$wt" || return
  SHELL="$BATS_TEST_TMPDIR/stubs/myshell" run "${SPINCLASS_BIN:-spinclass}" exec
  assert_success
  assert_output --partial "SHELL_RAN_IN=$wt"
}
