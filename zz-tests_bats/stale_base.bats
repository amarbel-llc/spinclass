#! /usr/bin/env bats

# End-to-end coverage for spinclass#250: a new session must be cut from the
# repo's default branch, freshened from its remote first.
#
# internal/basebranch's unit tests own the policy matrix (ahead, diverged,
# dirty, ambiguous, ...). What these tests own is the wiring: that `sc start`
# actually reaches the gate, that the refusal surfaces as a failed command, and
# that both halves of the override are honoured.

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  setup_stubs
}

# The defect in one test. The checkout is parked on an unrelated branch AND
# behind origin — the two independent halves of #250 — so a session cut from
# HEAD would miss the upstream commit twice over.
function stale_base_session_branches_from_fresh_default { # @test
  create_origin_checkout
  local tip
  tip=$(advance_upstream)

  git -C "$TEST_CHECKOUT" checkout -q -b unrelated
  git -C "$TEST_CHECKOUT" commit -q --allow-empty -m "work nobody asked for"
  local unrelated_tip
  unrelated_tip=$(git -C "$TEST_CHECKOUT" rev-parse HEAD)

  cd "$TEST_CHECKOUT" || return
  run_sc start --no-attach
  assert_success

  local wt
  wt=$(extract_wt_path "$output")
  assert [ -n "$wt" ]

  # The session contains the upstream commit...
  run git -C "$wt" merge-base --is-ancestor "$tip" HEAD
  assert_success

  # ...and is NOT sitting on the branch the checkout happened to hold.
  run git -C "$wt" merge-base --is-ancestor "$unrelated_tip" HEAD
  assert_failure

  # The local default branch really moved, rather than the session merely
  # pointing at a remote-tracking ref.
  run git -C "$TEST_CHECKOUT" rev-parse refs/heads/master
  assert_output "$tip"

  # And the checkout stayed where the operator left it.
  run git -C "$TEST_CHECKOUT" branch --show-current
  assert_output "unrelated"
}

# A repo with no remote has nothing to be stale against, so it must keep
# working exactly as before. Every other fixture in this suite is remote-less,
# so a regression here would light up the whole bats lane.
function stale_base_no_remote_still_starts { # @test
  create_repo
  cd "$TEST_REPO" || return

  run_sc start --no-attach
  assert_success
  assert_output --partial "SKIP no remote configured"
}

# An unreachable remote leaves the base unverifiable, and unverified is treated
# as stale. The failure has to name the override, since that is the operator's
# way out.
function stale_base_unreachable_remote_fails { # @test
  create_origin_checkout
  git -C "$TEST_CHECKOUT" remote set-url origin "$BATS_TEST_TMPDIR/gone.git"
  cd "$TEST_CHECKOUT" || return

  run_sc start --no-attach
  assert_failure
  assert_output --partial "allow-stale-base"
}

function stale_base_flag_overrides_unreachable_remote { # @test
  create_origin_checkout
  git -C "$TEST_CHECKOUT" remote set-url origin "$BATS_TEST_TMPDIR/gone.git"
  cd "$TEST_CHECKOUT" || return

  run_sc start --no-attach --allow-stale-base
  assert_success
}

# The sweatfile knob is the half that also covers spawn-session, which has no
# parameter for this by design.
function stale_base_sweatfile_knob_overrides_unreachable_remote { # @test
  create_origin_checkout
  git -C "$TEST_CHECKOUT" remote set-url origin "$BATS_TEST_TMPDIR/gone.git"
  mkdir -p "$HOME/.config/spinclass"
  cat >"$HOME/.config/spinclass/sweatfile" <<'EOF'
[hooks]
allow-stale-base = true
EOF
  cd "$TEST_CHECKOUT" || return

  run_sc start --no-attach
  assert_success
}

# A dirty checkout blocking the fast-forward refuses, per the operator's call
# that creation demands a verified base. The message has to say which tree.
function stale_base_dirty_checkout_fails { # @test
  create_origin_checkout
  advance_upstream >/dev/null
  echo "uncommitted local edit" >"$TEST_CHECKOUT/file.txt"
  cd "$TEST_CHECKOUT" || return

  run_sc start --no-attach
  assert_failure
  assert_output --partial "uncommitted changes"
}

# Being ahead of upstream is not staleness — it is the state of every repo
# right after a --local-only merge, so it must never block a start.
function stale_base_ahead_of_upstream_still_starts { # @test
  create_origin_checkout
  git -C "$TEST_CHECKOUT" commit -q --allow-empty -m "unpushed local work"
  cd "$TEST_CHECKOUT" || return

  run_sc start --no-attach
  assert_success
}
