#! /usr/bin/env bats

# Locks the contract clown's stdio bridge depends on: `spinclass serve` answers
# a `prompts/get` for the dynamic system-prompt fragment issued BEFORE any
# `initialize` (RFC-0002 §5; spinclass#187). go-mcp does not gate prompts/get on
# initialize, so the cold request must return a `result`, never an `error`.

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  create_repo
  # The outer session that runs this suite has SPINCLASS_*/CLOWN_* set; clear
  # them so each test controls the mode it exercises rather than inheriting it.
  unset SPINCLASS_SESSION_ID SPINCLASS_REPO SPINCLASS_BRANCH SPINCLASS_WORKTREE \
    SPINCLASS_DESCRIPTION CLOWN_SESSION_ID CLOWN_GROUP_ID
}

PROMPT_REQ='{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"system-prompt-append"}}'

# Pipe a single JSON-RPC line into `spinclass serve` with no preceding
# `initialize`; stdin closes after it, so serve reads the request, answers, then
# shuts down on EOF.
run_cold_prompt() {
  run bash -c "printf '%s\n' '$PROMPT_REQ' | timeout --preserve-status 10s '${SPINCLASS_BIN:-spinclass}' serve 2>/dev/null"
}

function serve_answers_cold_prompts_get_main_checkout { # @test
  cd "$TEST_REPO" || return
  run_cold_prompt
  assert_success
  assert_output --partial '"result"'
  refute_output --partial '"error"'
  # Main checkout (no SPINCLASS_WORKTREE): the implicit-session variant.
  assert_output --partial 'main checkout'
  refute_output --partial 'Worktree management'
}

function serve_answers_cold_prompts_get_worktree { # @test
  export SPINCLASS_WORKTREE
  SPINCLASS_WORKTREE="$(cd "$TEST_REPO" && pwd -P)"
  export SPINCLASS_SESSION_ID="repo/feat-x" SPINCLASS_BRANCH="feat-x"
  cd "$TEST_REPO" || return
  run_cold_prompt
  assert_success
  assert_output --partial '"result"'
  refute_output --partial '"error"'
  # Worktree variant carries the worktree-management guidance and the live key.
  assert_output --partial 'Worktree management'
  assert_output --partial 'repo/feat-x'
}

function serve_rejects_unknown_prompt { # @test
  cd "$TEST_REPO" || return
  local req='{"jsonrpc":"2.0","id":2,"method":"prompts/get","params":{"name":"no-such-prompt"}}'
  run bash -c "printf '%s\n' '$req' | timeout --preserve-status 10s '${SPINCLASS_BIN:-spinclass}' serve 2>/dev/null"
  assert_success
  assert_output --partial '"error"'
}
