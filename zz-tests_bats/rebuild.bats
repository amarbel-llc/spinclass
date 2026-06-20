#! /usr/bin/env bats

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  setup_stubs
  create_session_sweatfile
  create_repo
}

# Start a session via the "true" entrypoint and echo its worktree path. Runs
# `sc start` cd'd into TEST_REPO so the worktree lands in the isolated repo.
start_session() {
  local bin="${SPINCLASS_BIN:-spinclass}"
  local out
  out=$(cd "$TEST_REPO" && timeout --preserve-status 10s "$bin" --format tap start 2>&1)
  extract_wt_path "$out"
}

# Overwrite the recorded setup fingerprint to simulate drift, so the freshly
# recomputed fingerprint no longer matches and the worktree reads as stale.
corrupt_fingerprint() {
  local state tmp
  state=$(first_session_state_path)
  tmp=$(mktemp)
  jq '.setup_fingerprint = "STALE-TEST"' "$state" >"$tmp" && mv "$tmp" "$state"
}

function start_records_setup_fingerprint { # @test
  start_session >/dev/null
  local state
  state=$(first_session_state_path)

  run jq -r '.setup_fingerprint' "$state"
  assert_success
  assert [ -n "$output" ]
  [ "$output" != "null" ] || fail "setup_fingerprint not recorded"

  run jq -r '.setup_scheme' "$state"
  assert_success
  assert_output "1"
}

function rebuild_check_fresh_exits_zero { # @test
  local wt_id
  wt_id=$(basename "$(start_session)")

  run_sc rebuild --check "$wt_id"
  assert_success
  assert_output --partial "up to date"
}

function rebuild_check_stale_exits_nonzero { # @test
  local wt_id
  wt_id=$(basename "$(start_session)")
  corrupt_fingerprint

  run_sc rebuild --check "$wt_id"
  assert_failure
  assert_output --partial "stale"
}

function rebuild_refreshes_fingerprint { # @test
  local wt_id state
  wt_id=$(basename "$(start_session)")
  state=$(first_session_state_path)
  corrupt_fingerprint

  run_sc rebuild "$wt_id"
  assert_success
  assert_output --partial "rebuilt"

  # The bogus fingerprint was replaced by a freshly-computed one.
  run jq -r '.setup_fingerprint' "$state"
  assert_success
  [ "$output" != "STALE-TEST" ] || fail "fingerprint not refreshed by rebuild"

  # And the worktree now reads as fresh.
  run_sc rebuild --check "$wt_id"
  assert_success
}

function resume_warns_when_stale { # @test
  local wt_id
  wt_id=$(basename "$(start_session)")
  corrupt_fingerprint

  run_sc_session resume "$wt_id"
  assert_success
  assert_output --partial "stale"
}

function resume_auto_rebuilds_when_enabled { # @test
  # Enable opt-in auto-rebuild, then start fresh under it.
  cat >"$HOME/.config/spinclass/sweatfile" <<'EOF'
[session-entry]
start = ["true"]
resume = ["true"]

[hooks]
auto-rebuild-on-resume = true
EOF

  local wt_id state
  wt_id=$(basename "$(start_session)")
  state=$(first_session_state_path)
  corrupt_fingerprint

  run_sc_session resume "$wt_id"
  assert_success
  assert_output --partial "auto-rebuilt"

  # Auto-rebuild refreshed the recorded fingerprint.
  run jq -r '.setup_fingerprint' "$state"
  assert_success
  [ "$output" != "STALE-TEST" ] || fail "fingerprint not refreshed by auto-rebuild"
}
