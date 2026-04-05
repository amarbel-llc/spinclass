#! /usr/bin/env bats

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  setup_stubs
  create_repo
}

function spinclass_start_creates_worktree { # @test
  cd "$TEST_REPO"
  run_sc start --no-attach
  assert_success

  local wt_path
  wt_path=$(extract_wt_path "$output")
  assert [ -d "$wt_path" ]
  # Should be a git worktree (has .git file, not directory)
  assert [ -f "$wt_path/.git" ]
  # Branch should exist (extract from path)
  local branch
  branch=$(basename "$wt_path")
  run git -C "$TEST_REPO" rev-parse --verify "refs/heads/$branch"
  assert_success
}

function spinclass_start_auto_name { # @test
  cd "$TEST_REPO"
  run_sc start --no-attach

  assert_success
  # Should have created a worktree dir — at least one entry in .worktrees/
  run ls "$TEST_REPO/.worktrees/"
  assert_success
  assert [ -n "$output" ]
}

function spinclass_start_no_attach_skips_session { # @test
  cd "$TEST_REPO"
  run_sc start --no-attach
  assert_success

  local wt_path
  wt_path=$(extract_wt_path "$output")
  assert [ -d "$wt_path" ]
  # No session state file should be created with --no-attach
  local state_dir="$XDG_STATE_HOME/spinclass/sessions"
  if [ -d "$state_dir" ]; then
    local count
    count="$(find "$state_dir" -name '*-state.json' | wc -l)"
    assert [ "$count" -eq 0 ]
  fi
}

function spinclass_start_idempotent { # @test
  cd "$TEST_REPO"
  local bin="${SPINCLASS_BIN:-spinclass}"

  # First start — capture the worktree path
  local first_output
  first_output=$("$bin" --format tap start --no-attach 2>&1)
  local wt_path
  wt_path=$(extract_wt_path "$first_output")
  local branch
  branch=$(basename "$wt_path")

  # Second start to same worktree (by cd'ing into it) should succeed with SKIP
  cd "$wt_path"
  run_sc start --no-attach
  assert_success
  assert_output --partial "SKIP"
}

function spinclass_list_shows_sessions { # @test
  cd "$TEST_REPO"
  local bin="${SPINCLASS_BIN:-spinclass}"

  # Create some worktrees
  "$bin" --format tap start --no-attach
  "$bin" --format tap start --no-attach

  # list without active sessions should produce empty output (no-attach doesn't write state)
  run_sc list
  assert_success
}

function spinclass_merge_fast_forwards { # @test
  cd "$TEST_REPO"
  local bin="${SPINCLASS_BIN:-spinclass}"
  local attach_output
  attach_output=$("$bin" --format tap start --no-attach 2>&1)

  local wt
  wt=$(extract_wt_path "$attach_output")
  local branch
  branch=$(basename "$wt")

  # Make a commit on the worktree branch
  echo "new content" > "$wt/new-file.txt"
  git -C "$wt" add new-file.txt
  git -C "$wt" commit -m "add new file"

  # Clean untracked files created by sweatfile apply so worktree remove succeeds
  git -C "$wt" clean -fd

  # Merge from the main repo
  run_sc merge "$branch"
  assert_success

  # Commit should now be on main
  run git -C "$TEST_REPO" log --oneline --all
  assert_output --partial "add new file"

  # Worktree should be removed
  assert [ ! -d "$wt" ]
}

function spinclass_clean_removes_merged { # @test
  cd "$TEST_REPO"
  local bin="${SPINCLASS_BIN:-spinclass}"

  local attach1_output
  attach1_output=$("$bin" --format tap start --no-attach 2>&1)
  local wt1
  wt1=$(extract_wt_path "$attach1_output")
  local branch1
  branch1=$(basename "$wt1")

  # Clean untracked files so worktree remove succeeds
  git -C "$wt1" clean -fd

  # Merge the worktree first (makes the branch fully merged)
  "$bin" --format tap merge "$branch1"

  # Create another worktree that IS merged (no extra commits)
  local attach2_output
  attach2_output=$("$bin" --format tap start --no-attach 2>&1)
  local wt2
  wt2=$(extract_wt_path "$attach2_output")

  # Clean untracked files from sweatfile apply
  git -C "$wt2" clean -fd

  run_sc clean
  assert_success
  # The noop worktree with zero commits ahead should be cleaned
  assert [ ! -d "$wt2" ]
}
