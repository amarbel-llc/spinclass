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
  # Success → exactly one passing node_end carrying NO diagnostic (no tail,
  # no failure summary), and no paired test record — the hook stage is a
  # self-sufficient execution Phase since go-crap v2.2.1 (crap#22).
  assert_crap '([.[] | select(.type == "node_end")] | length == 1 and all(.exit_code == 0 and .diagnostic == null))
    and ([.[] | select(.type == "test")] | length == 0)'
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
  # Failure → the failing node_end's diagnostic (Phase.FailDiag, crap#22)
  # carries the resolved format plus a failure SUMMARY built from the
  # parsed TAP records ("#<N> <desc>: <message>" lines), not the raw output
  # ring tail — the raw "TAP version 14" line would only appear via the
  # tail fallback.
  # shellcheck disable=SC2016  # $f is a jq binding, not a shell variable
  assert_crap '[.[] | select(.type == "node_end")] as $f
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
  # Degenerate stream (zero parsed TAP records) → the failing node_end's
  # diagnostic output falls back to the raw ring tail (the literal hook
  # lines), not a parsed "#<N> ..." failure summary.
  # shellcheck disable=SC2016  # $f is a jq binding, not a shell variable
  assert_crap '[.[] | select(.type == "node_end")] as $f
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

# sc merge runs the REPAIR phase (before the absent verify hook) when
# [hooks].repair is set. A no-op repair command (no tree change) emits an
# "already conformant" verdict and the merge proceeds. Exercises the
# cmd/spinclass merge wiring; the Go tests cover the amend→default-branch path
# directly (TestPrepareMergeRepairAmend). FDR 0018.
function repair_phase_runs_on_merge { # @test
  cd "$TEST_REPO" || return
  local bin="${SPINCLASS_BIN:-spinclass}"

  # Repair sweatfile at the repo root (the repo layer of the hierarchy) so it
  # survives the worktree `git clean -fd` below and is not removed at merge.
  # pre-merge = "true" overrides the ancestor sweatfile's slow CI hook
  # (the hierarchy climbs past the sandbox to the real repo) so the merge
  # exercises only the REPAIR phase, not a full `just` build.
  printf '%s\n' '[hooks]
repair = "true"
pre-merge = "true"' >"$TEST_REPO/sweatfile"

  local attach_output wt branch
  attach_output=$("$bin" --format tap start --no-attach 2>&1)
  wt=$(extract_wt_path "$attach_output")
  branch=$(basename "$wt")

  echo "new content" >"$wt/new-file.txt"
  git -C "$wt" add new-file.txt
  git -C "$wt" commit -m "add new file"

  # Drop untracked sweatfile-apply artifacts so worktree removal succeeds.
  git -C "$wt" clean -fd

  run_sc_crap merge "$branch" --local-only
  assert_success
  assert_output --partial "repair $branch"
  assert_output --partial "already conformant"

  # The merge still lands the commit on the default branch.
  run git -C "$TEST_REPO" log --oneline main
  assert_output --partial "add new file"
}

# install_fakefmt_stub drops a stand-in formatter on PATH that proves the
# pre-commit hook fired: it logs to fakefmt.log AND appends a FORMATTED marker
# to each staged file and restages it (mimicking `conformist --staged`), so the
# committed content reflects the hook's restaged output. See
# docs/plans/2026-06-16-per-commit-repair-hook-design.md.
install_fakefmt_stub() {
  local stub="$BATS_TEST_TMPDIR/stubs/fakefmt"
  cat >"$stub" <<'STUB'
#!/bin/sh
echo fired >> "$BATS_TEST_TMPDIR/fakefmt.log"
for f in $(git diff --cached --name-only); do
  printf 'FORMATTED\n' >> "$f"
  git add "$f"
done
exit 0
STUB
  chmod +x "$stub"
}

# A repo-root sweatfile with [hooks].pre-commit is installed as a per-worktree
# git pre-commit hook at `sc start` time (worktree.Create → Apply →
# installPreCommitHook). The sweatfile must exist BEFORE start so the hierarchy
# carries it into the worktree (mirrors repair_phase_runs_on_merge). The hook
# repairs staged content at authoring time, so the committed blob carries the
# formatter's restaged output.
function pre_commit_hook_formats_staged_content { # @test
  cd "$TEST_REPO" || return
  install_fakefmt_stub
  printf '%s\n' '[hooks]
pre-commit = "fakefmt"' >"$TEST_REPO/sweatfile"

  run_sc start --no-attach pc_fmt
  assert_success
  local wt
  wt=$(extract_wt_path "$output")
  assert [ -d "$wt" ]

  echo "hello" >"$wt/note.txt"
  git -C "$wt" add note.txt
  git -C "$wt" commit -m "add note"

  assert [ -f "$BATS_TEST_TMPDIR/fakefmt.log" ]
  run git -C "$wt" show HEAD:note.txt
  assert_output --partial "FORMATTED"
}

# The hook is scoped to the worktree via a per-worktree core.hooksPath
# (extensions.worktreeConfig); a commit in the parent checkout must NOT fire it.
function pre_commit_hook_does_not_touch_main_checkout { # @test
  cd "$TEST_REPO" || return
  install_fakefmt_stub
  printf '%s\n' '[hooks]
pre-commit = "fakefmt"' >"$TEST_REPO/sweatfile"

  run_sc start --no-attach pc_iso
  assert_success

  rm -f "$BATS_TEST_TMPDIR/fakefmt.log"
  echo "main change" >"$TEST_REPO/mainfile.txt"
  git -C "$TEST_REPO" add mainfile.txt
  git -C "$TEST_REPO" commit -m "main commit"

  refute [ -f "$BATS_TEST_TMPDIR/fakefmt.log" ]
  run git -C "$TEST_REPO" show HEAD:mainfile.txt
  refute_output --partial "FORMATTED"
}

# `git commit --no-verify` is git's deliberate hook escape hatch: the pre-commit
# repair hook must not fire.
function pre_commit_hook_no_verify_bypasses { # @test
  cd "$TEST_REPO" || return
  install_fakefmt_stub
  printf '%s\n' '[hooks]
pre-commit = "fakefmt"' >"$TEST_REPO/sweatfile"

  run_sc start --no-attach pc_nv
  assert_success
  local wt
  wt=$(extract_wt_path "$output")

  echo "x" >"$wt/nv.txt"
  git -C "$wt" add nv.txt
  git -C "$wt" commit --no-verify -m "skip hook"

  refute [ -f "$BATS_TEST_TMPDIR/fakefmt.log" ]
}

# When the configured formatter is not on PATH, the hook's `command -v` guard
# makes it a no-op so commits are never blocked by a missing tool.
function pre_commit_hook_missing_tool_is_noop { # @test
  cd "$TEST_REPO" || return
  printf '%s\n' '[hooks]
pre-commit = "definitely-not-a-real-binary-xyz"' >"$TEST_REPO/sweatfile"

  run_sc start --no-attach pc_missing
  assert_success
  local wt
  wt=$(extract_wt_path "$output")

  echo "y" >"$wt/m.txt"
  git -C "$wt" add m.txt
  run git -C "$wt" commit -m "commit with missing formatter"
  assert_success
}

# Composition: our hook does not shadow a repo's native pre-commit. The
# dispatcher runs conformist first, then delegates to the native hook in the
# shared $GIT_COMMON_DIR/hooks, so BOTH fire. See
# docs/plans/2026-06-17-precommit-hook-composition-design.md.
function pre_commit_hook_composes_with_native_pre_commit { # @test
  cd "$TEST_REPO" || return
  install_fakefmt_stub
  printf '%s\n' '[hooks]
pre-commit = "fakefmt"' >"$TEST_REPO/sweatfile"

  # Native pre-commit in the shared hooks dir (common to all worktrees).
  mkdir -p "$TEST_REPO/.git/hooks"
  cat >"$TEST_REPO/.git/hooks/pre-commit" <<EOF
#!/bin/sh
echo native >> "$BATS_TEST_TMPDIR/native.log"
exit 0
EOF
  chmod +x "$TEST_REPO/.git/hooks/pre-commit"

  run_sc start --no-attach pc_compose
  assert_success
  local wt
  wt=$(extract_wt_path "$output")

  echo "hello" >"$wt/note.txt"
  git -C "$wt" add note.txt
  git -C "$wt" commit -m "add note"

  # Both our formatter and the repo's native pre-commit ran.
  assert [ -f "$BATS_TEST_TMPDIR/fakefmt.log" ]
  assert [ -f "$BATS_TEST_TMPDIR/native.log" ]
}
