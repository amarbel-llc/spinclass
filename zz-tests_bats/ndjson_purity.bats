#! /usr/bin/env bats

# #405: in ndjson mode `sc merge` and `sc check` write ndjson-crap and nothing
# else, even when a session-record warning fires mid-run. The warning used to
# go to stderr as plain text, so a caller reading the stream with 2>&1 got a
# line that is not a record. Each test forges the worktree's display copy of
# its session record so a read of it reports the disagreement, then requires
# every line of the combined output to parse (and, for merge, which reads the
# record, the warning to ride the stream as a record).

setup_file() {
  export BATS_TEST_TIMEOUT=30
}

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  setup_stubs
  create_repo
}

# Start a session with one commit and forge its display copy. Sets WT, BRANCH.
start_session_with_forged_copy() {
  cd "$TEST_REPO" || return
  local bin="${SPINCLASS_BIN:-spinclass}" out
  out=$("$bin" --format tap start --no-attach 2>&1)
  WT=$(extract_wt_path "$out")
  [ -n "$WT" ] || fail "sc start produced no worktree: $out"
  BRANCH=$(basename "$WT")
  echo "work" >"$WT/work.txt"
  git -C "$WT" add work.txt
  git -C "$WT" commit -q -m "session commit"
  git -C "$WT" clean -fdq
  local copy="$WT/.spinclass/state.json"
  [ -f "$copy" ] || fail "no display copy at $copy"
  jq '.principal = "forged-principal"' "$copy" >"$copy.new"
  mv "$copy.new" "$copy"
}

# Every line of $1 is a JSON object.
assert_pure_ndjson() {
  run jq -eR 'fromjson | type == "object"' <"$1"
  assert_success
}

# ... and one record carries the warning.
assert_pure_ndjson_with_warning() {
  local stream="$1"
  assert_pure_ndjson "$stream"
  run jq -enR '[inputs | fromjson] | any(.[]; tostring | contains("disagrees with the frontend record"))' <"$stream"
  assert_success
}

function merge_ndjson_carries_a_session_record_warning_as_a_record { # @test
  start_session_with_forged_copy
  local out="$BATS_TEST_TMPDIR/merge.ndjson"
  # shellcheck disable=SC2016  # $0/$1/$2 expand in the inner sh, not here
  run timeout --preserve-status 20s sh -c '"$0" --format ndjson merge "$1" --local-only >"$2" 2>&1' \
    "${SPINCLASS_BIN:-spinclass}" "$BRANCH" "$out"
  assert_success
  assert_pure_ndjson_with_warning "$out"
}

# sc check does not read the session record today, so the forged copy raises
# no warning here; the test pins that its stream stays records only all the
# same, so a later read that does warn cannot leak plain text into it.
function check_ndjson_stays_records_only_with_a_forged_session_copy { # @test
  start_session_with_forged_copy
  cd "$WT" || return
  local out="$BATS_TEST_TMPDIR/check.ndjson"
  # shellcheck disable=SC2016  # $0/$1 expand in the inner sh, not here
  run timeout --preserve-status 20s sh -c '"$0" --format ndjson check >"$1" 2>&1' \
    "${SPINCLASS_BIN:-spinclass}" "$out"
  assert_success
  assert_pure_ndjson "$out"
}
