#! /usr/bin/env bats

# End-to-end coverage for the gitSync (non --local-only) merge landing:
# spinclass#315 (a merge targets origin/<default>, never the root's local
# default branch) and spinclass#295 (after a successful push the local default
# branch is fast-forwarded opportunistically — a refusal is a SKIP that says the
# merge LANDED, never a failed merge). FDR 0029 (revised).
#
# internal/landing and internal/merge's Go tests own the policy matrix; these
# own the wiring through the real `sc` binary against a real bare origin. Every
# other bats merge runs --local-only, so this file is the only end-to-end cover
# for the default merge path.

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  setup_stubs
  create_origin_checkout
}

# Start a session in the checkout and commit <file>=<content> on it. Sets WT and
# BRANCH. The sweatfile-installed untracked content is scrubbed so the merge's
# non-force `git worktree remove` succeeds (same scrub as lifecycle.bats).
start_session_with_commit() {
  local file="$1" content="$2"
  cd "$TEST_CHECKOUT" || return
  local bin="${SPINCLASS_BIN:-spinclass}" out
  out=$("$bin" --format tap start --no-attach 2>&1)
  WT=$(extract_wt_path "$out")
  [ -n "$WT" ] || fail "sc start produced no worktree: $out"
  BRANCH=$(basename "$WT")
  echo "$content" >"$WT/$file"
  git -C "$WT" add "$file"
  git -C "$WT" commit -q -m "session commit: $file"
  git -C "$WT" clean -fdq
}

origin_tip() { git -C "$TEST_UPSTREAM" rev-parse refs/heads/master; }
local_tip() { git -C "$TEST_CHECKOUT" rev-parse refs/heads/master; }

# The happy path: the merge lands on origin, then local master follows it, as
# its own ok point after the landing.
function merge_landing_advances_local_master_to_the_landing { # @test
  start_session_with_commit new.txt "session work"
  local session_tip
  session_tip=$(git -C "$WT" rev-parse HEAD)

  run_sc_crap merge "$BRANCH"
  assert_success

  assert_equal "$(origin_tip)" "$session_tip"
  assert_equal "$(local_tip)" "$session_tip"
  assert_crap 'any(.[]; .type == "test" and .description == "fetch origin/master" and .ok)'
  assert_crap 'any(.[]; .type == "test" and (.description | startswith("advance local master to ")) and .ok and .directive == null)'
  assert [ ! -d "$WT" ]
}

# The #295 rule: an operator edit in the root that overlaps the incoming change
# blocks the local fast-forward. That is a SKIP that says the merge LANDED —
# the merge exits 0, origin carries the commit, and the edit is untouched.
function merge_landing_dirty_root_skips_local_advance_but_lands { # @test
  start_session_with_commit file.txt "from the session"
  local before
  before=$(local_tip)
  echo "operator's uncommitted edit" >"$TEST_CHECKOUT/file.txt"

  run_sc_crap merge "$BRANCH"
  assert_success

  # Assert on the merge's ndjson before any later `run` replaces $output.
  local skip='.type == "test" and .description == "advance local master" and .directive.kind == "skip"'
  assert_crap "any(.[]; $skip and (.directive.reason | startswith(\"merge LANDED on origin/master at \")))"
  assert_crap "any(.[]; $skip and (.directive.reason | contains(\"uncommitted changes\")))"
  assert_crap "any(.[]; $skip and (.directive.reason | contains(\"spinclass-local-default-ref(7)\")))"
  # Nothing in the ladder failed.
  assert_crap 'all(.[]; .type != "test" or .ok)'

  run git -C "$TEST_UPSTREAM" log --format=%s master
  assert_output --partial "session commit: file.txt"
  assert_equal "$(local_tip)" "$before"
  run cat "$TEST_CHECKOUT/file.txt"
  assert_output "operator's uncommitted edit"
}

# The #315 rule: a diverged local master is irrelevant to a remote merge. Before
# #315 the pre-merge pull refused it and failed the merge. The root's
# local-only commit must NOT reach origin.
function merge_landing_diverged_root_still_lands { # @test
  start_session_with_commit new.txt "session work"
  git -C "$TEST_CHECKOUT" commit -q --allow-empty -m "local-only commit on root master"
  local diverged
  diverged=$(local_tip)

  run_sc_crap merge "$BRANCH"
  assert_success
  assert_crap 'any(.[]; .type == "test" and .description == "advance local master" and .directive.kind == "skip" and (.directive.reason | contains("diverged")))'

  run git -C "$TEST_UPSTREAM" log --format=%s master
  assert_output --partial "session commit: new.txt"
  refute_output --partial "local-only commit on root master"
  assert_equal "$(local_tip)" "$diverged"
}

# Another session landed on origin after this one started, and the root's
# local master never saw it. The merge must rebase onto origin/master (not the
# stale local ref), land with both commits on origin, and bring local master up
# to the landing.
function merge_landing_rebases_onto_origin_not_stale_local { # @test
  start_session_with_commit new.txt "session work"
  local concurrent
  concurrent=$(advance_upstream)
  local stale
  stale=$(local_tip)
  assert [ "$stale" != "$concurrent" ]

  run_sc_crap merge "$BRANCH"
  assert_success

  run git -C "$TEST_UPSTREAM" merge-base --is-ancestor "$concurrent" master
  assert_success
  run git -C "$TEST_UPSTREAM" log --format=%s master
  assert_output --partial "session commit: new.txt"
  assert_equal "$(local_tip)" "$(origin_tip)"
}

# The root checkout parked on another branch: nothing holds master, so the
# local advance moves the ref directly — and must not touch the parked branch
# or move the checkout.
function merge_landing_root_parked_off_master_advances_ref_only { # @test
  start_session_with_commit new.txt "session work"
  git -C "$TEST_CHECKOUT" checkout -q -b parked
  local parked
  parked=$(git -C "$TEST_CHECKOUT" rev-parse HEAD)

  run_sc_crap merge "$BRANCH"
  assert_success

  assert_equal "$(local_tip)" "$(origin_tip)"
  run git -C "$TEST_CHECKOUT" branch --show-current
  assert_output "parked"
  assert_equal "$(git -C "$TEST_CHECKOUT" rev-parse refs/heads/parked)" "$parked"
}

# FDR 0029's failure symmetry, kept by #295: a refused push moves NOTHING —
# origin, local master, the session branch and its worktree — and no local
# advance is attempted, so a re-merge is a plain retry.
function merge_landing_refused_push_moves_nothing { # @test
  start_session_with_commit new.txt "session work"
  local session_tip origin_before local_before
  session_tip=$(git -C "$WT" rev-parse HEAD)
  origin_before=$(origin_tip)
  local_before=$(local_tip)
  git -C "$TEST_CHECKOUT" remote set-url --push origin "$BATS_TEST_TMPDIR/nonexistent.git"

  run_sc_crap merge "$BRANCH"
  assert_failure

  assert_equal "$(origin_tip)" "$origin_before"
  assert_equal "$(local_tip)" "$local_before"
  assert_equal "$(git -C "$TEST_CHECKOUT" rev-parse "refs/heads/$BRANCH")" "$session_tip"
  assert [ -d "$WT" ]
  assert_crap 'all(.[]; .type != "test" or ((.description | startswith("advance local")) | not))'

  # Restore the push URL: the re-merge is a plain retry.
  git -C "$TEST_CHECKOUT" remote set-url --push origin "$TEST_UPSTREAM"
  run_sc_crap merge "$BRANCH"
  assert_success
  assert_equal "$(origin_tip)" "$session_tip"
}

# A local-only merge is the self target: landing IS the fast-forward of local
# master. With the root parked on another branch that fast-forward must land on
# master — never on the parked branch (the pre-#315 `git merge --ff-only` ran in
# the root and would have advanced whatever it had checked out) — and origin
# must be untouched.
function merge_landing_local_only_lands_on_master_not_parked_branch { # @test
  start_session_with_commit new.txt "session work"
  local session_tip origin_before
  session_tip=$(git -C "$WT" rev-parse HEAD)
  origin_before=$(origin_tip)
  git -C "$TEST_CHECKOUT" checkout -q -b parked
  local parked
  parked=$(git -C "$TEST_CHECKOUT" rev-parse HEAD)

  run_sc_crap merge "$BRANCH" --local-only
  assert_success

  assert_equal "$(local_tip)" "$session_tip"
  assert_equal "$(git -C "$TEST_CHECKOUT" rev-parse refs/heads/parked)" "$parked"
  assert_equal "$(origin_tip)" "$origin_before"
  assert_crap 'all(.[]; .type != "test" or ((.description | startswith("fetch ")) | not))'
}
