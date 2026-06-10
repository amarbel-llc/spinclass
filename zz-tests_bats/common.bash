bats_load_library bats-support
bats_load_library bats-assert
bats_load_library bats-assert-additions
bats_load_library bats-emo

require_bin SPINCLASS_BIN spinclass

set_xdg() {
  loc="$(realpath "$1" 2>/dev/null)"
  export XDG_DATA_HOME="$loc/.xdg/data"
  export XDG_CONFIG_HOME="$loc/.xdg/config"
  export XDG_STATE_HOME="$loc/.xdg/state"
  export XDG_CACHE_HOME="$loc/.xdg/cache"
  export XDG_RUNTIME_HOME="$loc/.xdg/runtime"
  mkdir -p "$XDG_DATA_HOME" "$XDG_CONFIG_HOME" "$XDG_STATE_HOME" \
    "$XDG_CACHE_HOME" "$XDG_RUNTIME_HOME"
}

setup_test_home() {
  export REAL_HOME="$HOME"
  export HOME="$BATS_TEST_TMPDIR/home"
  mkdir -p "$HOME"
  set_xdg "$BATS_TEST_TMPDIR"
  mkdir -p "$XDG_CONFIG_HOME/git"
  export GIT_CONFIG_GLOBAL="$XDG_CONFIG_HOME/git/config"
  git config --global user.name "Test User"
  git config --global user.email "test@example.com"
  git config --global init.defaultBranch main
  export GIT_EDITOR=true
  export GIT_CEILING_DIRECTORIES="$BATS_TEST_TMPDIR"
}

setup_stubs() {
  local stub_dir="$BATS_TEST_TMPDIR/stubs"
  mkdir -p "$stub_dir"

  for cmd in claude direnv; do
    cat >"$stub_dir/$cmd" <<'STUB'
#!/bin/sh
printf '%s' "$@" >> "$BATS_TEST_TMPDIR/stubs/CMDNAME.log"
printf '\n' >> "$BATS_TEST_TMPDIR/stubs/CMDNAME.log"
exit 0
STUB
    sed -i "s/CMDNAME/$cmd/g" "$stub_dir/$cmd"
    chmod +x "$stub_dir/$cmd"
  done

  export PATH="$stub_dir:$PATH"
}

# Create a git repo with an initial commit.
# Sets TEST_REPO to the repo path.
create_repo() {
  export TEST_REPO="$BATS_TEST_TMPDIR/repo"
  mkdir -p "$TEST_REPO"
  git -C "$TEST_REPO" init
  echo "initial" >"$TEST_REPO/file.txt"
  git -C "$TEST_REPO" add file.txt
  git -C "$TEST_REPO" commit -m "initial commit"
}

# Create a worktree in the standard .worktrees/ location.
# Usage: create_worktree <branch-name>
# Sets WT_PATH to the worktree path.
create_worktree() {
  local branch="$1"
  local wt_dir="$TEST_REPO/.worktrees"
  mkdir -p "$wt_dir"
  export WT_PATH="$wt_dir/$branch"
  git -C "$TEST_REPO" worktree add -b "$branch" "$WT_PATH"
}

# Run spinclass with timeout.
# Usage: run_sc <subcommand> [args...]
#
# 10s (not 5s): under the bats-madder lane, `sc start` forks an extra
# synchronous `madder init` subprocess inside applyWorktreeConfig (gated on the
# madder build-pin; bats-default skips it). With bats running --jobs
# $NIX_BUILD_CORES, the ~dozen parallel `sc start` tests each fork
# `git worktree add` + `madder init`, and the contention occasionally pushed a
# single start past a 5s bound — SIGTERM'd to status 143 (issue #133). 10s
# matches run_sc_session and BATS_TEST_TIMEOUT. A real hang can't occur on the
# --no-attach path (no entrypoint is exec'd), so the looser bound costs nothing.
run_sc() {
  local bin="${SPINCLASS_BIN:-spinclass}"
  run timeout --preserve-status 10s "$bin" --format tap "$@"
}

# Run spinclass merge/check on the ndjson-crap wire. TAP is retired for
# merge/check (present.ResolveFormat rejects --format tap), so those call
# sites cannot go through run_sc. Piped bats output means --format auto
# would also resolve to ndjson; the explicit flag keeps the intent visible.
# Usage: run_sc_crap <merge|check> [args...]
run_sc_crap() {
  local bin="${SPINCLASS_BIN:-spinclass}"
  run timeout --preserve-status 10s "$bin" --format ndjson "$@"
}

# Assert a jq filter holds over the array of ndjson-crap records parsed
# from $output. Non-JSON lines (interleaved stderr) are dropped rather
# than breaking the parse; the filter sees one array of record objects.
# Prefer this over substring assertions on raw records — JSON key order
# comes from Go struct field order and is not contractual.
# Usage: assert_crap '<jq filter over the records array>'
assert_crap() {
  # shellcheck disable=SC2154  # $output is set by bats' run
  echo "$output" | jq -enR "[inputs | fromjson?] | $1" >/dev/null ||
    fail "ndjson-crap assertion failed: $1 -- output: $output"
}

# Extract the worktree absolute path from TAP output of a start command.
# Looks for "ok N - create <branch> <path>" and returns <path>.
# Usage: extract_wt_path "$output"
extract_wt_path() {
  echo "$1" | grep -oP 'ok \d+ - create \S+ \K\S+'
}

# Check that at least one session is tracked in the central index.
# Counts both live entries (resolving symlinks) and tombstones (regular
# files); does NOT count dangling symlinks (externally-closed sessions).
# Usage: assert_session_state
assert_session_state() {
  local index_dir="$XDG_STATE_HOME/spinclass/index"
  assert [ -d "$index_dir" ]
  local count=0
  for entry in "$index_dir"/*.json; do
    [ -e "$entry" ] || continue
    count=$((count + 1))
  done
  assert [ "$count" -gt 0 ]
}

# Echo the path of the worktree-local state.json (or tombstone fallback)
# for the first index entry found. Used by tests that need to grep the
# state JSON for a description or other field.
# Usage: first_session_state_path
first_session_state_path() {
  local index_dir="$XDG_STATE_HOME/spinclass/index"
  for entry in "$index_dir"/*.json; do
    [ -e "$entry" ] || continue
    if [ -L "$entry" ]; then
      readlink -f "$entry"
    else
      echo "$entry"
    fi
    return 0
  done
  return 1
}

# Assert there is no tracked session — index dir is missing or empty of
# resolvable entries (live or tombstone). Counterpart to assert_session_state.
# Usage: assert_no_session_state
assert_no_session_state() {
  local index_dir="$XDG_STATE_HOME/spinclass/index"
  if [ ! -d "$index_dir" ]; then
    return 0
  fi
  for entry in "$index_dir"/*.json; do
    [ -e "$entry" ] || continue
    fail "unexpected session index entry: $entry"
  done
}

# Write a global sweatfile with fast-exiting entrypoints for session tests.
# Both start and resume use "true" so the session writes state and exits
# immediately.
create_session_sweatfile() {
  local sweatfile_dir="$HOME/.config/spinclass"
  mkdir -p "$sweatfile_dir"
  cat >"$sweatfile_dir/sweatfile" <<'EOF'
[session-entry]
start = ["true"]
resume = ["true"]
EOF
}

# Variant of `create_session_sweatfile` that appends every
# sweatfile-installed-but-not-default-excluded path to `[git].excludes`.
# Needed by tests that drive the closeShop auto-close branch:
#   - `.envrc` from sweatfile.Apply.prepareDirenv (the bats `direnv`
#     stub is on PATH, so prepareDirenv runs).
#   - `.claude/` from sweatfile.ApplyClaudeSettings (which writes
#     `.claude/settings.local.json`; the dir itself is untracked).
# Without these the porcelain check inside closeShop is non-empty and
# the auto-close gate (commitsAhead == 0 && worktreeStatus == "")
# never fires. Excludes merge by append against GetDefault()'s
# `.worktrees/`, `.spinclass/`, `.mcp.json` set.
create_session_sweatfile_with_envrc_exclude() {
  local sweatfile_dir="$HOME/.config/spinclass"
  mkdir -p "$sweatfile_dir"
  cat >"$sweatfile_dir/sweatfile" <<'EOF'
[session-entry]
start = ["true"]
resume = ["true"]

[git]
excludes = [".envrc", ".claude/"]
EOF
}

# Run spinclass for session-attach tests (subprocess spawn + closeShop
# workflow). Same 10s bound as run_sc; kept as a distinct named helper so
# attach-path call sites read clearly. (Both were 10s after #133 raised
# run_sc from 5s — see run_sc above.)
# Usage: run_sc_session <subcommand> [args...]
run_sc_session() {
  local bin="${SPINCLASS_BIN:-spinclass}"
  run timeout --preserve-status 10s "$bin" --format tap "$@"
}
