#! /usr/bin/env bats

# The host admission queue (#424 steps 3-4): with a trusted [host-queue]
# enable = true and max-gates = 1, two concurrent `sc check` runs in two
# sessions run one at a time; the second names its wait. With no section the
# queue is off and nothing waits.

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  setup_stubs
  create_repo
  export XDG_RUNTIME_DIR="$BATS_TEST_TMPDIR/run"
  mkdir -p "$XDG_RUNTIME_DIR"
}

# start_checked_session NAME: a session whose pre-merge sleeps, so two checks
# overlap. Prints the worktree path.
start_checked_session() {
  cd "$TEST_REPO" || return
  run_sc start --no-attach "$1"
  assert_success
  extract_wt_path "$output"
}

# A policy that only the gate count can hold: every probe term is set so the
# real host never holds the test.
enable_host_queue() {
  mkdir -p "$HOME/.config/spinclass"
  cat >>"$HOME/.config/spinclass/sweatfile" <<'EOF'

[host-queue]
enable = true
max-gates = 1
memory-floor = "0"
memory-resume = "0"
pressure-memory-avg10 = 100.0
swap-ceiling = "1TiB"
disk-floor = "0"
load-threshold = 1000.0
settle = "0s"
poll = "100ms"
max-wait = "2m"
idle-grace = "0"
EOF
}

two_concurrent_checks() {
  local a b
  a=$(start_checked_session host_queue_a)
  b=$(start_checked_session host_queue_b)
  install_passthrough_direnv
  commit_repo_sweatfile "$TEST_REPO" '[hooks]
pre-merge = "sleep 2"
'
  (
    cd "$a" && "${SPINCLASS_BIN:-spinclass}" --format plain check >"$BATS_TEST_TMPDIR/a.out" 2>&1
    echo $? >"$BATS_TEST_TMPDIR/a.rc"
  ) &
  local pid_a=$!
  (
    cd "$b" && "${SPINCLASS_BIN:-spinclass}" --format plain check >"$BATS_TEST_TMPDIR/b.out" 2>&1
    echo $? >"$BATS_TEST_TMPDIR/b.rc"
  ) &
  local pid_b=$!
  wait "$pid_a" "$pid_b"
}

function host_queue_serializes_two_concurrent_checks { # @test
  enable_host_queue
  two_concurrent_checks
  run cat "$BATS_TEST_TMPDIR/a.rc" "$BATS_TEST_TMPDIR/b.rc"
  assert_output $'0\n0'
  # Exactly one of the two waited for the other's slot.
  run sh -c 'cat "$1"/a.out "$1"/b.out | grep -c "host queue wait .*(gates)"' sh "$BATS_TEST_TMPDIR"
  assert_output 1
  # Both slots were released: no live marker is left.
  run sh -c 'ls "$1"/spinclass/host-queue/gate-*.json 2>/dev/null | wc -l' sh "$XDG_RUNTIME_DIR"
  assert_output 0
}

function host_queue_is_off_without_a_section { # @test
  two_concurrent_checks
  run cat "$BATS_TEST_TMPDIR/a.rc" "$BATS_TEST_TMPDIR/b.rc"
  assert_output $'0\n0'
  run grep -l "host queue" "$BATS_TEST_TMPDIR/a.out" "$BATS_TEST_TMPDIR/b.out"
  assert_failure
  assert [ ! -e "$XDG_RUNTIME_DIR/spinclass/host-queue" ]
}

# A repo-level [host-queue] cannot turn the queue on.
function host_queue_ignores_a_repo_level_enable { # @test
  local a
  a=$(start_checked_session host_queue_repo)
  install_passthrough_direnv
  commit_repo_sweatfile "$TEST_REPO" '[hooks]
pre-merge = "true"

[host-queue]
enable = true
'
  cd "$a" || return
  run timeout --preserve-status 30s "${SPINCLASS_BIN:-spinclass}" --format plain check
  assert_success
  assert [ ! -e "$XDG_RUNTIME_DIR/spinclass/host-queue" ]
}
