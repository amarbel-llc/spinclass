#! /usr/bin/env bats

# Session restart (#419, FDR 0037) over a fake `clown` on PATH. Spinclass
# authorizes, refuses and records; the hand-off in the global sweatfile
# ([session-entry].restart, a layer above the repo) is the only thing it execs,
# and the fake clown appends its argv to $CLOWN_LOG. The session's principal
# is bound the way the harness binds it, by a SessionStart hook run with
# CLOWN_SESSION_ID set; `sc restart-preflight` is then what clown's frontend
# calls at idle.

PRINCIPAL=7d3c1f52-4e8a-4b6d-9a20-5c1e8f3b6a90

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  setup_stubs
  create_repo

  CLOWN_LOG="$BATS_TEST_TMPDIR/clown.log"
  export CLOWN_LOG
  : >"$CLOWN_LOG"
  cat >"$BATS_TEST_TMPDIR/stubs/clown" <<'EOF'
#!/bin/sh
printf 'argv:' >>"$CLOWN_LOG"
printf ' <%s>' "$@" >>"$CLOWN_LOG"
printf '\n' >>"$CLOWN_LOG"
EOF
  chmod +x "$BATS_TEST_TMPDIR/stubs/clown"
  unset SPINCLASS_SESSION_ID CLOWN_SESSION_ID CLOWN_UNIT
}

# Write the global sweatfile; with restart_toml set it carries the hand-off.
# Usage: write_global_sweatfile [restart-toml-line]
write_global_sweatfile() {
  mkdir -p "$HOME/.config/spinclass"
  {
    printf '[session-entry]\nstart = ["true"]\nresume = ["true"]\n'
    if [[ -n ${1-} ]]; then
      printf '%s\n' "$1"
    fi
  } >"$HOME/.config/spinclass/sweatfile"
}

RESTART_HANDOFF='restart = ["clown", "restart", "--session", "{principal}", "--when", "{when}", "--reason", "{reason}"]'

# Start a session and bind PRINCIPAL to it as its SessionStart hook would.
# Sets WT_PATH.
start_bound_session() {
  cd "$TEST_REPO" || return
  run_sc start --no-attach
  assert_success
  WT_PATH=$(extract_wt_path "$output")
  export WT_PATH
  cd "$WT_PATH" || return
  printf '{"hook_event_name":"SessionStart","session_id":"bats-restart","cwd":"%s"}' "$WT_PATH" |
    CLOWN_SESSION_ID=$PRINCIPAL "$SPINCLASS_BIN" hook >/dev/null
}

function restart_this_session_hands_off_the_expanded_argv { # @test
  write_global_sweatfile "$RESTART_HANDOFF"
  start_bound_session

  CLOWN_SESSION_ID=$PRINCIPAL run_sc restart-this-session --reason "need new tools"
  assert_success
  assert_output --partial "end your turn"

  run cat "$CLOWN_LOG"
  assert_output "argv: <restart> <--session> <$PRINCIPAL> <--when> <idle> <--reason> <need new tools>"
}

function restart_this_session_refuses_without_a_hand_off { # @test
  write_global_sweatfile
  start_bound_session

  CLOWN_SESSION_ID=$PRINCIPAL run_sc restart-this-session
  assert_failure
  assert_output --partial "[session-entry]"
  assert_output --partial "restart"

  run cat "$CLOWN_LOG"
  assert_output ""
}

function a_repo_layer_hand_off_is_never_run { # @test
  write_global_sweatfile
  printf '[session-entry]\nrestart = ["clown", "evil"]\n' >>"$TEST_REPO/sweatfile"
  start_bound_session

  CLOWN_SESSION_ID=$PRINCIPAL run_sc restart-this-session
  assert_failure
  run cat "$CLOWN_LOG"
  assert_output ""
}

function restart_preflight_reports_none_then_restart_then_none { # @test
  write_global_sweatfile "$RESTART_HANDOFF"
  start_bound_session

  CLOWN_SESSION_ID=$PRINCIPAL run --separate-stderr "$SPINCLASS_BIN" restart-preflight
  assert_success
  echo "$output" | jq -e '.verdict == "none" and .when == ""'

  CLOWN_SESSION_ID=$PRINCIPAL run_sc restart-this-session --reason r
  assert_success

  CLOWN_SESSION_ID=$PRINCIPAL run --separate-stderr "$SPINCLASS_BIN" restart-preflight
  assert_success
  # shellcheck disable=SC2154  # $lines is set by bats' run
  assert_equal "${#lines[@]}" 1
  echo "$output" | jq -e '.verdict == "restart" and .when == "idle"'

  # The request was consumed: the relaunched session must not restart again.
  CLOWN_SESSION_ID=$PRINCIPAL run --separate-stderr "$SPINCLASS_BIN" restart-preflight
  assert_success
  echo "$output" | jq -e '.verdict == "none"'
}

function restart_preflight_refuses_once_the_hand_off_is_revoked { # @test
  write_global_sweatfile "$RESTART_HANDOFF"
  start_bound_session
  CLOWN_SESSION_ID=$PRINCIPAL run_sc restart-this-session
  assert_success

  write_global_sweatfile
  CLOWN_SESSION_ID=$PRINCIPAL run --separate-stderr "$SPINCLASS_BIN" restart-preflight
  assert_success
  echo "$output" | jq -e '.verdict == "refused" and (.reason | contains("[session-entry]"))'

  CLOWN_SESSION_ID=$PRINCIPAL run --separate-stderr "$SPINCLASS_BIN" restart-preflight
  echo "$output" | jq -e '.verdict == "none"'
}

function restart_preflight_outside_any_session_is_none { # @test
  write_global_sweatfile "$RESTART_HANDOFF"
  cd "$TEST_REPO" || return

  run --separate-stderr "$SPINCLASS_BIN" restart-preflight
  assert_success
  echo "$output" | jq -e '.verdict == "none"'
}
