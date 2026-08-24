#! /usr/bin/env bats

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  setup_stubs
  create_run_sweatfile
  create_repo
  install_passthrough_direnv
}

# A sweatfile for `sc run` tests: a trivial pre-merge hook (so the merge VERIFY
# step is instant, not the project's real `just`) plus the sweatfile-installed
# untracked-file excludes (so the post-merge non-force worktree remove succeeds).
create_run_sweatfile() {
  local sweatfile_dir="$HOME/.config/spinclass"
  mkdir -p "$sweatfile_dir"
  cat >"$sweatfile_dir/sweatfile" <<'EOF'
[session-entry]
start = ["true"]
resume = ["true"]

[hooks]
pre-merge = "true"

[git]
excludes = [".envrc", ".claude/"]
EOF
}

# install_passthrough_direnv is provided by common.bash (shared with exec.bats
# and hooks.bats).

# Run `sc run` on the ndjson-crap wire (its output uses the merge/check present
# stack; TAP is rejected). Mirrors run_sc_crap. Usage: run_sc_run [args...]
run_sc_run() {
  local bin="${SPINCLASS_BIN:-spinclass}"
  run timeout --preserve-status 30s "$bin" --format ndjson run "$@"
}

# Count entries under the repo's .worktrees/ (0 when empty/absent).
worktree_count() {
  local d="$TEST_REPO/.worktrees"
  [ -d "$d" ] || {
    echo 0
    return
  }
  find "$d" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' '
}

@test "run: default merges the commit and tears the session down" {
  cd "$TEST_REPO" || return
  run_sc_run --local-only -- sh -c 'echo hi > run-file.txt && git add run-file.txt && git commit -qm "add run-file"'
  assert_success
  # Step verdict + a merge stage are present on the wire.
  assert_crap 'any(.[]; .type == "node_end" and .exit_code == 0)'

  # Commit landed on the default branch.
  run git -C "$TEST_REPO" log --oneline main
  assert_output --partial "add run-file"
  # Worktree removed (merge teardown) and the index entry dropped.
  assert_equal "$(worktree_count)" "0"
  assert_no_session_state
}

@test "run: empty run (no commits) is a clean success" {
  cd "$TEST_REPO" || return
  run_sc_run --local-only -- true
  assert_success
  assert_crap 'any(.[]; .type == "test" and .ok == true and (.description | test("nothing to merge")))'
  # Closed: no worktree, no tracked session.
  assert_equal "$(worktree_count)" "0"
  assert_no_session_state
}

@test "run: a failing step leaves the session intact and exits nonzero" {
  cd "$TEST_REPO" || return
  run_sc_run --local-only -- sh -c 'exit 7'
  # shellcheck disable=SC2154
  assert_equal "$status" 7
  # The step phase is recorded as a failure carrying the util's exit code in
  # its diagnostic (crap synthesizes a generic node_end.exit_code; the real
  # code rides the diagnostic).
  assert_crap 'any(.[]; .type == "node_end" and .diagnostic.exit_code == 7)'
  # Session left for inspection.
  assert_equal "$(worktree_count)" "1"
  assert_session_state
}

@test "run: --no-close merges but leaves the worktree" {
  cd "$TEST_REPO" || return
  run_sc_run --local-only --no-close -- sh -c 'echo x > nc.txt && git add nc.txt && git commit -qm "nc-commit"'
  assert_success
  # Merged to main.
  run git -C "$TEST_REPO" log --oneline main
  assert_output --partial "nc-commit"
  # But the worktree remains (--no-close).
  assert_equal "$(worktree_count)" "1"
}

@test "run: --no-merge with commits leaves the session (never discards work)" {
  cd "$TEST_REPO" || return
  run_sc_run --no-merge -- sh -c 'echo y > nm.txt && git add nm.txt && git commit -qm "nm-commit"'
  assert_success
  # NOT merged to main.
  run git -C "$TEST_REPO" log --oneline main
  refute_output --partial "nm-commit"
  # Session left intact because the run produced commits.
  assert_equal "$(worktree_count)" "1"
  assert_session_state
}

@test "run: --no-merge with no commits closes the session" {
  cd "$TEST_REPO" || return
  run_sc_run --no-merge -- true
  assert_success
  # Nothing committed, nothing merged → session closed.
  assert_equal "$(worktree_count)" "0"
}

@test "run: stdin shebang script runs under its interpreter and merges" {
  cd "$TEST_REPO" || return
  local bin="${SPINCLASS_BIN:-spinclass}"
  # #197: the script shebang MUST be #!/bin/sh, not #!/usr/bin/env bash. This
  # lane runs in the hermetic linux nix build sandbox, which exposes /bin/sh but
  # NOT /usr/bin/env — so `sc run` exec'ing /usr/bin/env fails with exit 127.
  # /bin/sh is the one interpreter nix guarantees; the body below is POSIX. (On
  # darwin's permissive sandbox /usr/bin/env resolves, which is why this only
  # ever went red on the linux lane.)
  run timeout --preserve-status 30s sh -c \
    "printf '#!/bin/sh\nset -e\necho via-stdin > s.txt\ngit add s.txt\ngit commit -qm stdin-commit\n' | '$bin' --format ndjson run --local-only"
  assert_success
  run git -C "$TEST_REPO" log --oneline main
  assert_output --partial "stdin-commit"
}

@test "run: no command and no stdin script errors" {
  cd "$TEST_REPO" || return
  run timeout --preserve-status 10s sh -c \
    "printf '' | '${SPINCLASS_BIN:-spinclass}' --format ndjson run"
  assert_failure
  assert_output --partial "nothing to run"
}

@test "run: --post-merge hook runs after merge lands" {
  cd "$TEST_REPO" || return
  local flag_file="$TEST_REPO/post-merge-ran"
  run_sc_run --local-only \
    --post-merge "touch '$flag_file'" \
    -- sh -c 'echo pm > pm.txt && git add pm.txt && git commit -qm "pm-commit"'
  assert_success
  # Commit landed.
  run git -C "$TEST_REPO" log --oneline main
  assert_output --partial "pm-commit"
  # Dynamic hook ran.
  [ -f "$flag_file" ] || fail "--post-merge hook did not create $flag_file"
}

@test "run: --post-merge hook receives SPINCLASS_MERGED_SHA env" {
  cd "$TEST_REPO" || return
  local sha_file="$TEST_REPO/merged-sha"
  run_sc_run --local-only \
    --post-merge "echo \$SPINCLASS_MERGED_SHA > '$sha_file'" \
    -- sh -c 'echo sha > sha.txt && git add sha.txt && git commit -qm "sha-commit"'
  assert_success
  local recorded
  recorded="$(cat "$sha_file" 2>/dev/null | tr -d '[:space:]')"
  [ -n "$recorded" ] || fail "SPINCLASS_MERGED_SHA was empty"
  local landed
  landed="$(git -C "$TEST_REPO" rev-parse main)"
  assert_equal "$recorded" "$landed"
}

@test "run: multiple --post-merge hooks run in order" {
  cd "$TEST_REPO" || return
  local log_file="$TEST_REPO/hook-log"
  run_sc_run --local-only \
    --post-merge "echo first >> '$log_file'" \
    --post-merge "echo second >> '$log_file'" \
    -- sh -c 'echo multi > m.txt && git add m.txt && git commit -qm "multi-hook"'
  assert_success
  run cat "$log_file"
  assert_output "$(printf 'first\nsecond')"
}

@test "run: failing --post-merge hook does not fail the merge" {
  cd "$TEST_REPO" || return
  run_sc_run --local-only \
    --post-merge "exit 42" \
    -- sh -c 'echo fail > fail.txt && git add fail.txt && git commit -qm "fail-hook-commit"'
  # Merge succeeds despite the failing hook.
  assert_success
  run git -C "$TEST_REPO" log --oneline main
  assert_output --partial "fail-hook-commit"
  # The wire carries a warn-severity diagnostic for the failed hook.
  assert_crap 'any(.[]; .type == "node_end" and .exit_code != 0 and .diagnostic.severity == "warn")'
}
