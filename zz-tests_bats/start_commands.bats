#! /usr/bin/env bats

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  create_session_sweatfile
  create_repo
}

function start_command_creates_worktree { # @test
  cat >"$TEST_REPO/sweatfile" <<'EOF'
[session-entry]
start = ["true"]
resume = ["true"]

[[start-commands]]
name = "test"
exec-start = ["printf", "{\"context\":\"# Test context for {arg}\"}"]
EOF

  cd "$TEST_REPO"
  run_sc start-test --no-attach myvalue
  assert_success

  local wt_path
  wt_path=$(extract_wt_path "$output")
  assert [ -d "$wt_path" ]
}

function start_command_branch_checkout { # @test
  # Create a branch with a commit in the test repo
  git -C "$TEST_REPO" checkout -b feature-x
  echo "feature content" >"$TEST_REPO/feature.txt"
  git -C "$TEST_REPO" add feature.txt
  git -C "$TEST_REPO" commit -m "feature commit"
  git -C "$TEST_REPO" checkout main

  cat >"$TEST_REPO/sweatfile" <<'EOF'
[session-entry]
start = ["true"]
resume = ["true"]

[[start-commands]]
name = "branch-test"
exec-start = ["printf", "{\"branch\":\"feature-x\",\"context\":\"# Branch test\"}"]
EOF

  cd "$TEST_REPO"
  run_sc start-branch-test --no-attach dummy
  assert_success

  local wt_path
  wt_path=$(extract_wt_path "$output")
  assert [ -d "$wt_path" ]

  # Verify the worktree is on the expected branch
  run git -C "$wt_path" branch --show-current
  assert_output "feature-x"
}

function start_command_description_from_json { # @test
  cat >"$TEST_REPO/sweatfile" <<'EOF'
[session-entry]
start = ["true"]
resume = ["true"]

[[start-commands]]
name = "desc-test"
exec-start = ["printf", "{\"description\":\"custom desc\",\"context\":\"# Context\"}"]
EOF

  cd "$TEST_REPO"
  local bin="${SPINCLASS_BIN:-spinclass}"
  local start_output
  start_output=$(timeout --preserve-status 10s "$bin" --format tap start-desc-test --no-attach myarg 2>&1)

  local wt_path
  wt_path=$(extract_wt_path "$start_output")

  # Find the session state file (worktree-local or tombstone) and check description.
  local state_file
  state_file=$(first_session_state_path 2>/dev/null || true)
  if [ -n "$state_file" ]; then
    run jq -r '.description' "$state_file"
    assert_output "custom desc"
  fi
}

function start_command_arg_regex_rejects_bad_input { # @test
  cat >"$TEST_REPO/sweatfile" <<'EOF'
[session-entry]
start = ["true"]
resume = ["true"]

[[start-commands]]
name = "strict"
arg-regex = "^[0-9]+$"
exec-start = ["printf", "{\"context\":\"ok\"}"]
EOF

  cd "$TEST_REPO"
  run_sc start-strict --no-attach abc
  assert_failure
  assert_output --partial "does not match"
}

function start_command_user_overrides_builtin { # @test
  cat >"$TEST_REPO/sweatfile" <<'EOF'
[session-entry]
start = ["true"]
resume = ["true"]

[[start-commands]]
name = "gh_issue"
arg-regex = "^[0-9]+$"
exec-start = ["printf", "{\"context\":\"user override\"}"]
EOF

  cd "$TEST_REPO"
  run_sc start-gh_issue --no-attach 1
  assert_success

  local wt_path
  wt_path=$(extract_wt_path "$output")
  assert [ -d "$wt_path" ]
}

function validate_warns_shell_without_regex { # @test
  cat >"$TEST_REPO/sweatfile" <<'EOF'
[[start-commands]]
name = "risky"
exec-start = ["sh", "-c", "echo {arg}"]
EOF

  cd "$TEST_REPO"
  run_sc validate
  assert_output --partial "shell interpreter without arg-regex"
}
