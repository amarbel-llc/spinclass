#! /usr/bin/env bats

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  setup_stubs
  create_repo
}

# Create a worktree, drop a sweatfile with the given body at its root,
# and cd into it. Mirrors sc_check_setup_worktree in sweatfile.bats so
# the pre-merge-output-format tests below match the existing sc_check_*
# pattern.
pre_merge_setup_worktree() {
  local sweatfile_body="$1"
  local wt_path

  cd "$TEST_REPO" || return
  run_sc start --no-attach pre_merge_test
  assert_success

  wt_path=$(extract_wt_path "$output")
  assert [ -d "$wt_path" ]

  printf '%s' "$sweatfile_body" >"$wt_path/sweatfile"
  cd "$wt_path" || return
}

# The pre-merge-output-format tests below assert blob behavior (the
# `resource_link: madder://blobs/...` output record emitted by
# internal/check/check.go runHookPhase), which is gated on
# `embeds.MadderBin() != ""` — i.e. spinclass was built via
# `lib.mkSpinclass { madder = ...; }`. The default `just test-bats` lane
# builds `mkSpinclass {}` with no madder pin, so these tests skip there
# and only assert against madder-pinned binaries (the
# `merge-this-session` / `~/.nix-profile/bin/spinclass` environment).
# See FDR 0003 and CLAUDE.md "External tool deps".
# TODO(#85): once a madder-pinned bats lane lands, this guard becomes a no-op for that lane.
require_madder_pinned() {
  local bin="${SPINCLASS_BIN:-spinclass}"
  # `sc version` uses text/tabwriter padded with spaces (not tabs), so we
  # anchor the row at line-start and require space-separated `- ... dormant`
  # in the trailing columns. Tighter than the original `^madder.*dormant`,
  # which would match any line beginning with `madder` and containing
  # `dormant` anywhere.
  if "$bin" version 2>/dev/null | grep -qE '^madder +- +dormant *$'; then
    skip "format-aware pre-merge hook output requires a madder-pinned spinclass build (lib.mkSpinclass with madder input); current binary reports madder as dormant"
  fi
}

function tool_use_log_writes_to_xdg_log_home { # @test
  skip "pre-existing failure — see #45"
  local bin="${SPINCLASS_BIN:-spinclass}"

  # Create a worktree so hooks can detect worktree context
  cd "$TEST_REPO" || return
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
  cd "$wt" || return
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

  cd "$TEST_REPO" || return
  local attach_output
  attach_output=$("$bin" --format tap start --no-attach 2>&1)
  local wt
  wt=$(extract_wt_path "$attach_output")
  local branch
  branch=$(basename "$wt")
  local repo_name
  repo_name=$(basename "$TEST_REPO")
  export SPINCLASS_SESSION_ID="$repo_name/$branch"

  cd "$wt" || return
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

  cd "$TEST_REPO" || return
  run bash -c 'echo '"'"'{"hook_event_name":"PostToolUse","session_id":"test","tool_name":"Read","tool_input":{},"cwd":"'"$TEST_REPO"'"}'"'"' | '"$bin"' hooks'
  assert_success

  # No log dir should be created
  local log_dir="$HOME/.local/log/spinclass/tool-uses"
  assert [ ! -d "$log_dir" ]
}

function pre_merge_output_format_tap_ndjson_success_omits_tail { # @test
  require_madder_pinned
  # Build the TAP-14 stream with one echo per line so the test sweatfile
  # does not have to thread newline escapes through bash → TOML → shell →
  # printf. Each echo emits its argument plus a newline.
  pre_merge_setup_worktree '[hooks]
pre-merge = "echo '"'"'TAP version 14'"'"'; echo '"'"'1..1'"'"'; echo '"'"'ok 1 - synthetic'"'"'"
pre-merge-output-format = "tap-ndjson"
'

  run_sc_crap check
  assert_success
  # Success → exactly one ok test record carrying NO diagnostic (no tail,
  # no failure summary; the ok verdict is itself the liveness signal).
  assert_crap '[.[] | select(.type == "test")] | length == 1 and all(.ok) and all(.diagnostic == null)'
  # Madder-pinned: the parsed tap-ndjson stream is stored as a blob and
  # linked from an output record.
  assert_output --partial "resource_link: madder://blobs/"
}

function pre_merge_output_format_tap_ndjson_failure_emits_failure_field { # @test
  require_madder_pinned
  pre_merge_setup_worktree '[hooks]
pre-merge = "echo '"'"'TAP version 14'"'"'; echo '"'"'1..1'"'"'; echo '"'"'not ok 1 - synthetic'"'"'; echo '"'"'  ---'"'"'; echo '"'"'  message: expected 7 got 9'"'"'; echo '"'"'  ...'"'"'; exit 1"
pre-merge-output-format = "tap-ndjson"
'

  run_sc_crap check
  assert_failure
  # Failure → the failing test record's diagnostic carries the resolved
  # format plus a failure SUMMARY built from the parsed TAP records
  # ("#<N> <desc>: <message>" lines), not the raw output ring tail — the
  # raw "TAP version 14" line would only appear via the tail fallback.
  # shellcheck disable=SC2016  # $f is a jq binding, not a shell variable
  assert_crap '[.[] | select(.type == "test" and .ok == false)] as $f
    | ($f | length) == 1
    and ($f[0].diagnostic.format == "tap-ndjson")
    and ($f[0].diagnostic.output | startswith("#1 synthetic"))
    and ($f[0].diagnostic.output | contains("expected 7 got 9"))
    and ($f[0].diagnostic.output | contains("TAP version 14") | not)'
}

function pre_merge_output_format_tap_ndjson_degenerate_falls_back_to_tail { # @test
  require_madder_pinned
  pre_merge_setup_worktree '[hooks]
pre-merge = "echo '"'"'this is not tap'"'"'; exit 2"
pre-merge-output-format = "tap-ndjson"
'

  run_sc_crap check
  assert_failure
  # Degenerate stream (zero parsed TAP records) → the diagnostic's output
  # falls back to the raw ring tail (the literal hook lines), not a parsed
  # "#<N> ..." failure summary.
  # shellcheck disable=SC2016  # $f is a jq binding, not a shell variable
  assert_crap '[.[] | select(.type == "test" and .ok == false)] as $f
    | ($f | length) == 1
    and ($f[0].diagnostic.format == "tap-ndjson")
    and ($f[0].diagnostic.output | contains("this is not tap"))
    and ($f[0].diagnostic.output | startswith("#") | not)'
}

# By default the pre-merge hook runs in an isolated detached build worktree
# (a .merge-* sibling under .worktrees/), not in the session worktree. The hook
# records its working directory to a file outside the (transient) worktree so the
# assertion works on both madder-pinned (hook stdout → blob) and plain builds.
function pre_merge_hook_runs_in_isolated_build_worktree { # @test
  # SC2016: $BATS_TEST_TMPDIR is deliberately left unexpanded here — it is
  # written literally into the sweatfile and expanded by the hook's `sh -c`.
  # shellcheck disable=SC2016
  pre_merge_setup_worktree '[hooks]
pre-merge = "pwd > \"$BATS_TEST_TMPDIR/hookpwd.txt\""
'

  run_sc_crap check
  assert_success
  run cat "$BATS_TEST_TMPDIR/hookpwd.txt"
  assert_output --partial "/.worktrees/.merge-"
}

# With [hooks].disable-merge-build-worktree the hook runs in place in the session
# worktree (legacy behavior) — its cwd is the session worktree, not a .merge-*.
function pre_merge_hook_runs_in_place_when_build_worktree_disabled { # @test
  # SC2016: see the note in pre_merge_hook_runs_in_isolated_build_worktree.
  # shellcheck disable=SC2016
  pre_merge_setup_worktree '[hooks]
pre-merge = "pwd > \"$BATS_TEST_TMPDIR/hookpwd.txt\""
disable-merge-build-worktree = true
'

  run_sc_crap check
  assert_success
  run cat "$BATS_TEST_TMPDIR/hookpwd.txt"
  assert_output --partial "/.worktrees/"
  refute_output --partial "/.worktrees/.merge-"
}
