#! /usr/bin/env bats

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  create_repo
}

function tool_use_log_writes_to_xdg_log_home { # @test
  local bin="${SPINCLASS_BIN:-spinclass}"

  # Create a worktree so hooks can detect worktree context
  cd "$TEST_REPO"
  local attach_output
  attach_output=$("$bin" --format tap start --no-attach 2>&1)
  local wt
  wt=$(extract_wt_path "$attach_output")
  local branch
  branch=$(basename "$wt")
  local repo_name
  repo_name=$(basename "$TEST_REPO")
  export SPINCLASS_SESSION_ID="$repo_name/$branch"

  # Pipe a PostToolUse hook payload to spinclass hooks
  cd "$wt"
  run bash -c 'echo '"'"'{"hook_event_name":"PostToolUse","session_id":"test","tool_name":"Edit","tool_input":{"file_path":"/some/file.go"},"cwd":"'"$wt"'"}'"'"' | '"$bin"' hooks'
  # hooks should not produce output or error
  assert_success

  # Log file should exist at XDG_LOG_HOME default: ~/.local/log
  # Session key slashes are replaced with -- in the filename
  local log_file="$HOME/.local/log/spinclass/tool-uses/${repo_name}--${branch}.jsonl"
  assert [ -f "$log_file" ]

  # Should contain the tool name
  run cat "$log_file"
  assert_output --partial '"tool_name":"Edit"'
}

function tool_use_log_respects_xdg_log_home { # @test
  local bin="${SPINCLASS_BIN:-spinclass}"
  local custom_log="$BATS_TEST_TMPDIR/custom-logs"
  export XDG_LOG_HOME="$custom_log"

  cd "$TEST_REPO"
  local attach_output
  attach_output=$("$bin" --format tap start --no-attach 2>&1)
  local wt
  wt=$(extract_wt_path "$attach_output")
  local branch
  branch=$(basename "$wt")
  local repo_name
  repo_name=$(basename "$TEST_REPO")
  export SPINCLASS_SESSION_ID="$repo_name/$branch"

  cd "$wt"
  run bash -c 'echo '"'"'{"hook_event_name":"PostToolUse","session_id":"test","tool_name":"Bash","tool_input":{},"cwd":"'"$wt"'"}'"'"' | '"$bin"' hooks'
  assert_success

  local log_file="$custom_log/spinclass/tool-uses/${repo_name}--${branch}.jsonl"
  assert [ -f "$log_file" ]

  run cat "$log_file"
  assert_output --partial '"tool_name":"Bash"'
}

function tool_use_log_silent_without_session { # @test
  local bin="${SPINCLASS_BIN:-spinclass}"
  unset SPINCLASS_SESSION_ID

  cd "$TEST_REPO"
  run bash -c 'echo '"'"'{"hook_event_name":"PostToolUse","session_id":"test","tool_name":"Read","tool_input":{},"cwd":"'"$TEST_REPO"'"}'"'"' | '"$bin"' hooks'
  assert_success

  # No log dir should be created
  local log_dir="$HOME/.local/log/spinclass/tool-uses"
  assert [ ! -d "$log_dir" ]
}
