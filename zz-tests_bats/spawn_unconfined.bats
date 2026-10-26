#! /usr/bin/env bats

# `sc spawn --unconfined` asks the human itself (FDR 0035 "A confined spawn is
# not always-ask"): the spawn-session MCP tool asks through the PreToolUse
# floor, which the CLI never reaches. The CLI refuses unless stdin and stdout
# are terminals and the operator types "unconfined". The gate runs before any
# repo is resolved, so a bogus repo proves nothing was spawned.
#
# The pty case runs under util-linux `script`, as grit_escape.bats does; that
# is a pty, not the operator's real terminal, and it skips cleanly when
# `script` is absent (the nix bats lanes).

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  unset SPINCLASS_SESSION_ID CLOWN_SESSION_ID CLOWN_UNIT
  SPAWN_LOG="$XDG_STATE_HOME/spinclass/spawn-unconfined.log"
  export SPAWN_LOG
}

require_pty() {
  command -v script >/dev/null ||
    skip "no 'script' (util-linux) on PATH: a pty cannot be allocated"
  printf '' | timeout 5 script -qefc 'test -t 0 && test -t 1' /dev/null >/dev/null 2>&1 ||
    skip "'script' is present but cannot allocate a pty with isatty(0) and isatty(1) here"
}

function unconfined_spawn_from_a_recipe_with_null_stdin_is_refused { # @test
  run --separate-stderr "$SPINCLASS_BIN" spawn --repo no-such-repo --brief b --unconfined </dev/null
  assert_failure
  assert_equal "$output" ""
  [[ $stderr == *"--unconfined needs a human at a terminal"* ]]
  run grep -c '"outcome":"refused: no terminal"' "$SPAWN_LOG"
  assert_output 1
}

function unconfined_spawn_at_a_pty_with_a_wrong_answer_is_refused { # @test
  require_pty
  local errf="$BATS_TEST_TMPDIR/pty.stderr"
  local cmd
  cmd="$(printf '%q ' "$SPINCLASS_BIN" spawn --repo no-such-repo --brief b --unconfined)"
  run bash -c '{ printf "y\n"; sleep 0.3; } | timeout 20 script -qefc "{ $1 ; } 2>\"$2\"" /dev/null' _ "$cmd" "$errf"
  assert_failure
  run cat "$errf"
  assert_output --partial "type unconfined to confirm"
  assert_output --partial "not confirmed; nothing was spawned"
  run grep -c '"outcome":"declined"' "$SPAWN_LOG"
  assert_output 1
}
