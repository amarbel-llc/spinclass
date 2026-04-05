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
#!/usr/bin/env bash
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
run_sc() {
  local bin="${SPINCLASS_BIN:-spinclass}"
  run timeout --preserve-status 5s "$bin" --format tap "$@"
}

# Extract the worktree absolute path from TAP output of a start command.
# Looks for "ok N - create <branch> <path>" and returns <path>.
# Usage: extract_wt_path "$output"
extract_wt_path() {
  echo "$1" | grep -oP 'ok \d+ - create \S+ \K\S+'
}

# Check if a session state file exists for a given repo+branch.
# Usage: assert_session_state <repo-path> <branch>
assert_session_state() {
  local state_dir="$XDG_STATE_HOME/spinclass/sessions"
  assert [ -d "$state_dir" ]
  local count
  count="$(find "$state_dir" -name '*-state.json' | wc -l)"
  assert [ "$count" -gt 0 ]
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

# Run spinclass with a longer timeout for session attach tests.
# The subprocess spawn + closeShop workflow needs more headroom than
# the 5s used by run_sc.
# Usage: run_sc_session <subcommand> [args...]
run_sc_session() {
  local bin="${SPINCLASS_BIN:-spinclass}"
  run timeout --preserve-status 10s "$bin" --format tap "$@"
}
