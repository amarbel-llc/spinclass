#! /usr/bin/env bats

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  setup_stubs
  create_repo
}

function spinclass_start_creates_worktree { # @test
  cd "$TEST_REPO" || return
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
  cd "$TEST_REPO" || return
  run_sc start --no-attach

  assert_success
  # Should have created a worktree dir — at least one entry in .worktrees/
  run ls "$TEST_REPO/.worktrees/"
  assert_success
  assert [ -n "$output" ]
}

function spinclass_start_no_attach_skips_session { # @test
  cd "$TEST_REPO" || return
  run_sc start --no-attach
  assert_success

  local wt_path
  wt_path=$(extract_wt_path "$output")
  assert [ -d "$wt_path" ]
  # No session state file should be created with --no-attach
  assert_no_session_state
}

function spinclass_start_idempotent { # @test
  cd "$TEST_REPO" || return
  local bin="${SPINCLASS_BIN:-spinclass}"

  # First start — capture the worktree path
  local first_output
  first_output=$("$bin" --format tap start --no-attach 2>&1)
  local wt_path
  wt_path=$(extract_wt_path "$first_output")
  local branch
  branch=$(basename "$wt_path")

  # Second start to same worktree (by cd'ing into it) should succeed with SKIP
  cd "$wt_path" || return
  run_sc start --no-attach
  assert_success
  assert_output --partial "SKIP"
}

function spinclass_list_shows_sessions { # @test
  cd "$TEST_REPO" || return
  local bin="${SPINCLASS_BIN:-spinclass}"

  # Create some worktrees
  "$bin" --format tap start --no-attach
  "$bin" --format tap start --no-attach

  # list without active sessions should produce empty output (no-attach doesn't write state)
  run_sc list
  assert_success
}

function spinclass_merge_fast_forwards { # @test
  cd "$TEST_REPO" || return
  local bin="${SPINCLASS_BIN:-spinclass}"
  local attach_output
  attach_output=$("$bin" --format tap start --no-attach 2>&1)

  local wt
  wt=$(extract_wt_path "$attach_output")
  local branch
  branch=$(basename "$wt")

  # Make a commit on the worktree branch
  echo "new content" >"$wt/new-file.txt"
  git -C "$wt" add new-file.txt
  git -C "$wt" commit -m "add new file"

  # Clean untracked files created by sweatfile apply so worktree remove succeeds
  git -C "$wt" clean -fd

  # Merge from the main repo. --local-only: push is the default now (#126)
  # but this sandbox repo has no remote, so exercise the local ff-merge.
  run_sc_crap merge "$branch" --local-only
  assert_success

  # Commit should now be on main
  run git -C "$TEST_REPO" log --oneline --all
  assert_output --partial "add new file"

  # Worktree should be removed
  assert [ ! -d "$wt" ]
}

function spinclass_autoclose_assume_yes_removes_worktree { # @test
  # #66: SPINCLASS_AUTOCLOSE_ASSUME=yes bypasses the huh.Confirm so the
  # auto-close-on-fully-merged branch fires without a TTY. A fresh `sc
  # start` worktree has 0 commits ahead of main and (with the default
  # sweatfile's .spinclass/ exclude plus the test-local .envrc exclude
  # below) a clean porcelain, which is exactly the condition the auto-
  # close prompt gates on. The .envrc is written by `sweatfile.Apply`
  # because the bats stub directory puts a `direnv` shim on PATH; it
  # is untracked content, so without the exclude the porcelain check
  # would fail and the auto-close branch would not fire.
  create_session_sweatfile_with_envrc_exclude
  cd "$TEST_REPO" || return
  local bin="${SPINCLASS_BIN:-spinclass}"

  local attach_output
  attach_output=$(SPINCLASS_AUTOCLOSE_ASSUME=yes timeout --preserve-status 10s "$bin" --format tap start 2>&1)
  local wt
  wt=$(extract_wt_path "$attach_output")
  assert [ -n "$wt" ]

  # The auto-close branch should have torn the worktree down.
  assert [ ! -d "$wt" ]
}

function spinclass_autoclose_assume_no_keeps_worktree { # @test
  # #66: SPINCLASS_AUTOCLOSE_ASSUME=no explicitly declines the auto-close
  # so the worktree stays in place. This is the no-TTY analogue of the
  # user picking "Keep" at the huh prompt. Excluding `.envrc` matters
  # here too: if it weren't excluded the porcelain would be dirty and
  # the auto-close branch would skip on the wrong condition, masking
  # whether the env var was actually consulted.
  create_session_sweatfile_with_envrc_exclude
  cd "$TEST_REPO" || return
  local bin="${SPINCLASS_BIN:-spinclass}"

  local attach_output
  attach_output=$(SPINCLASS_AUTOCLOSE_ASSUME=no timeout --preserve-status 10s "$bin" --format tap start 2>&1)
  local wt
  wt=$(extract_wt_path "$attach_output")
  assert [ -n "$wt" ]

  # The worktree should still exist — env said "no".
  assert [ -d "$wt" ]
}

function spinclass_close_by_id_removes_worktree { # @test
  # #40 regression guard: `sc close <id>` (the explicit worktree-id arg
  # path, distinct from cwd auto-detect and the interactive picker) must
  # resolve the session via session.FindByID and tear the worktree down.
  # This path regressed once after the session-resolution unification
  # (commit 03a9265) and is now wired in internal/close: resolveCloseTarget
  # -> FindByID(target) -> closeShop. Drive it end-to-end so the regression
  # cannot return unnamed. Uses a real `start` (not --no-attach) so a
  # session state entry exists for FindByID to match on.
  create_session_sweatfile
  cd "$TEST_REPO" || return
  local bin="${SPINCLASS_BIN:-spinclass}"

  local start_output
  start_output=$(timeout --preserve-status 10s "$bin" --format tap start 2>&1)
  local wt
  wt=$(extract_wt_path "$start_output")
  assert [ -n "$wt" ]
  assert [ -d "$wt" ]
  local id
  id=$(basename "$wt")

  # Close by the explicit id arg from OUTSIDE the worktree (cwd is the
  # repo root), so this exercises FindByID, not the cwd-autodetect branch.
  run_sc close --force "$id"
  assert_success

  # The worktree is gone, and a non-existent id is rejected with the
  # bare-worktree hint rather than silently succeeding.
  assert [ ! -d "$wt" ]
  run_sc close --force "$id"
  assert_failure
}

function spinclass_clean_removes_merged { # @test
  # #51: re-verify #33 scenario 1 — merged worktree removal. Uses
  # `--yes` to skip the huh.Confirm so the path is non-TTY-driveable
  # (the `--yes` bypass for #45's prompt issue). `git clean -fd`
  # before each git-managed remove is the same defensive scrub
  # `spinclass_merge_fast_forwards` uses: `sc merge` and `sc clean`
  # both call non-force `git worktree remove`, which refuses on
  # untracked content like the sweatfile-installed `.envrc`.
  cd "$TEST_REPO" || return
  local bin="${SPINCLASS_BIN:-spinclass}"

  local attach1_output
  attach1_output=$("$bin" --format tap start --no-attach 2>&1)
  local wt1
  wt1=$(extract_wt_path "$attach1_output")
  local branch1
  branch1=$(basename "$wt1")

  # Remove sweatfile-installed untracked content so `sc merge`'s
  # non-force `git worktree remove` step succeeds.
  git -C "$wt1" clean -fd

  # Give branch1 a real commit so `sc merge` has something to merge —
  # otherwise the merge short-circuits with "nothing to merge".
  echo "merged content" >"$wt1/merged.txt"
  git -C "$wt1" add merged.txt
  git -C "$wt1" commit -m "commit on branch1"

  # Merge the worktree first (makes the branch fully merged; TAP is
  # retired for merge/check, so this speaks the ndjson-crap wire).
  # --local-only: push is the default now (#126) but this sandbox has no
  # remote.
  "$bin" --format ndjson merge "$branch1" --local-only

  # Create another worktree that IS merged (no extra commits)
  local attach2_output
  attach2_output=$("$bin" --format tap start --no-attach 2>&1)
  local wt2
  wt2=$(extract_wt_path "$attach2_output")

  # Same scrub before `sc clean`'s non-force worktree remove.
  git -C "$wt2" clean -fd

  run_sc clean --yes
  assert_success
  # The noop worktree with zero commits ahead should be cleaned
  assert [ ! -d "$wt2" ]
}

function spinclass_clean_dry_run_keeps_worktree { # @test
  # #51: re-verify #33 scenario 4 — `--dry-run` must NOT remove
  # anything. Set up the same merged worktree as the removal test but
  # use `-n --yes` and assert the worktree still exists afterwards.
  cd "$TEST_REPO" || return
  local bin="${SPINCLASS_BIN:-spinclass}"

  local attach_output
  attach_output=$("$bin" --format tap start --no-attach 2>&1)
  local wt
  wt=$(extract_wt_path "$attach_output")

  run_sc clean --dry-run --yes
  assert_success
  # Dry-run should leave the worktree in place.
  assert [ -d "$wt" ]
}

function spinclass_clean_reaps_abandoned_sessions { # @test
  # #51: re-verify #33 scenario 2 — when a session's worktree is
  # removed externally, the dangling index symlink is reaped without
  # touching tombstones. Uses `run_sc_session start` (entrypoint
  # `true`) to land an inactive index symlink, then deletes the
  # worktree behind spinclass's back.
  create_session_sweatfile
  cd "$TEST_REPO" || return

  run_sc_session start
  assert_success
  assert_session_state

  local index_dir="$XDG_STATE_HOME/spinclass/index"
  local entries=("$index_dir"/*.json)
  local entry="${entries[0]}"
  assert [ -L "$entry" ]
  local target
  target=$(readlink "$entry")
  local wt_dir
  wt_dir=$(dirname "$(dirname "$target")")
  assert [ -d "$wt_dir" ]

  # Force the symlink to dangle. `git worktree remove` won't do this
  # cleanly because removing the worktree dir also yanks the .git
  # link, so just rm the dir; the abandoned-session reap path is
  # what we're exercising.
  rm -rf "$wt_dir"
  run_sc clean --yes
  assert_success

  # The dangling symlink should be gone; no tombstone left behind.
  assert [ ! -e "$entry" ]
}

function spinclass_clean_gcs_stale_tombstones { # @test
  # #51: re-verify #33 scenario 3 — tombstone GC removes regular
  # files at the index path whose `exited_at` is older than the
  # configured retention. Set retention to 1s, close a session
  # (writes a tombstone with `exited_at` ~= now), backdate the
  # tombstone JSON, then run `sc clean --yes`.
  create_session_sweatfile
  # Append a tiny tombstone-retention so any backdated tombstone is
  # immediately past its cutoff.
  cat >>"$HOME/.config/spinclass/sweatfile" <<'EOF'
tombstone-retention = "1s"
EOF

  cd "$TEST_REPO" || return
  local bin="${SPINCLASS_BIN:-spinclass}"

  local start_output
  start_output=$(timeout --preserve-status 10s "$bin" --format tap start 2>&1)
  local wt
  wt=$(extract_wt_path "$start_output")
  local branch
  branch=$(basename "$wt")

  # Close the session to write a tombstone (regular file at the
  # index path). `--force` skips the unintegrated/dirty huh prompt
  # (the worktree here is clean and zero commits ahead, so the
  # prompt wouldn't fire — but --force keeps the test from hanging
  # if that ever changes).
  run_sc close --force "$branch"
  assert_success

  local index_dir="$XDG_STATE_HOME/spinclass/index"
  local tombs=("$index_dir"/*.json)
  local tomb="${tombs[0]}"
  # Tombstone is a regular file (close promotes the symlink).
  assert [ -f "$tomb" ]
  assert [ ! -L "$tomb" ]

  # Backdate exited_at into 2020 so it is past the 1s cutoff.
  jq '.exited_at = "2020-01-01T00:00:00Z"' "$tomb" >"$tomb.new"
  mv "$tomb.new" "$tomb"

  run_sc clean --yes
  assert_success
  # Tombstone GC'd by clean.
  assert [ ! -e "$tomb" ]
}
