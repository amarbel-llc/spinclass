#!/usr/bin/env bats
# E2E for `sc spawn` and detached `sc fork` (FDR 0006) over a stub
# multiplexer + stub harness: the [session-entry].spawn template execs the
# entry argv in the foreground (returns in ms, satisfying the
# detach-promptly contract), and the stub harness records the brief then
# pipes a SessionStart event into the REAL hook handler — so the hello the
# driver blocks on travels the production path (state spawned_by → hook →
# chat).

setup() {
  load 'common'
  setup_test_home
  setup_stubs
  write_spawn_stubs
}

# Write the stub harness scripts and export STUB_DIR. stub-harness.sh
# records its brief ($1) into the cwd (the worker worktree, where the spawn
# template runs) and fires the real SessionStart hook; stub-silent.sh
# records the brief but never fires the hook (timeout case).
write_spawn_stubs() {
  STUB_DIR="$BATS_TEST_TMPDIR/spawn-stubs"
  export STUB_DIR
  mkdir -p "$STUB_DIR"
  local bin="${SPINCLASS_BIN:-spinclass}"

  cat >"$STUB_DIR/stub-harness.sh" <<EOF
#!/bin/sh
set -e
printf '%s' "\$1" >brief.txt
printf '{"hook_event_name":"SessionStart","session_id":"bats-spawn","cwd":"%s"}' "\$PWD" | "$bin" hook
EOF
  cat >"$STUB_DIR/stub-silent.sh" <<'EOF'
#!/bin/sh
printf '%s' "$1" >brief.txt
EOF
  chmod +x "$STUB_DIR/stub-harness.sh" "$STUB_DIR/stub-silent.sh"
}

# Create the spawn target repo at $HOME/eng/repos/<name> (the dirname
# resolver's $HOME/*/repos/<leaf> search space) with an initial commit and
# a sweatfile wiring the stub templates. Usage: create_spawn_repo <name> <harness>
create_spawn_repo() {
  local name="$1" harness="$2"
  WORKER_REPO="$HOME/eng/repos/$name"
  export WORKER_REPO
  mkdir -p "$WORKER_REPO"
  git -C "$WORKER_REPO" init
  echo "initial" >"$WORKER_REPO/file.txt"
  git -C "$WORKER_REPO" add file.txt
  git -C "$WORKER_REPO" commit -m "initial commit"
  write_stub_sweatfile "$WORKER_REPO" "$harness"
}

# Write a repo-level sweatfile pointing [session-entry].spawn-entry at the stub
# harness (FDR-0017 Piece 1: spinclass execs spawn-entry directly — no
# multiplexer wrap). The stub runs in the FOREGROUND ("exec"), completing in
# milliseconds, which satisfies the detach-promptly contract while keeping the
# test deterministic; its output is redirected to spawn.log in the worktree.
# Usage: write_stub_sweatfile <repo> <harness-script>
write_stub_sweatfile() {
  local repo="$1" harness="$2"
  cat >"$repo/sweatfile" <<EOF
[session-entry]
spawn-entry = ["sh", "-c", "exec \"\$0\" \"\$@\" >spawn.log 2>&1", "$harness", "{prompt}"]
EOF
  git -C "$repo" add sweatfile
  git -C "$repo" commit -m "stub spawn-entry"
}

@test "spawn launches a hello-gated worker in a sibling repo" {
  create_spawn_repo workerrepo "$STUB_DIR/stub-harness.sh"

  SPINCLASS_SESSION_ID=driver/bats run_sc spawn workerrepo \
    --brief "do the thing" --description "bats worker"
  assert_success
  assert_output --partial "session_key: workerrepo/"
  assert_output --partial "worktree_path: $WORKER_REPO/.worktrees/"
  assert_output --partial "multiplexer_id: "
  assert_output --partial "worker will message driver/bats via chat"

  # The brief reached the stub harness verbatim, in the worker worktree.
  local wt
  wt=$(echo "$output" | grep -oP 'worktree_path: \K\S+')
  assert [ -d "$wt" ]
  run cat "$wt/brief.txt"
  assert_output "do the thing"

  # State carries the lineage + description (state.json is pretty-printed:
  # keys and values are separated by ": ").
  run cat "$wt/.spinclass/state.json"
  assert_output --partial '"spawned_by": "driver/bats"'
  assert_output --partial '"description": "bats worker"'

  # sc list surfaces the lineage annotation.
  run_sc list
  assert_output --partial "spawned-by:driver/bats"
}

@test "spawn times out without a hello and leaves the worktree for inspection" {
  create_spawn_repo silentrepo "$STUB_DIR/stub-silent.sh"

  SPINCLASS_SESSION_ID=driver/bats run_sc spawn silentrepo \
    --brief "never answered" --hello-timeout 2s
  assert_failure
  assert_output --partial "no hello from spawned session"
  assert_output --partial "2s"

  # Leave-for-inspection contract: the worktree (and its state) persist.
  local wt
  wt=$(find "$HOME/eng/repos/silentrepo/.worktrees" -mindepth 1 -maxdepth 1 -type d | head -1)
  assert [ -n "$wt" ]
  assert [ -f "$wt/brief.txt" ]
  assert [ -f "$wt/.spinclass/state.json" ]
}

@test "spawn-window fires with id and dir, in the worker worktree" {
  create_spawn_repo windowrepo "$STUB_DIR/stub-harness.sh"
  # The stub writes via temp file + mv so the existence poll below can never
  # observe the file empty between the shell's O_TRUNC open and printf's write.
  cat >>"$WORKER_REPO/sweatfile" <<'EOF'
spawn-window = ["sh", "-c", "printf '%s\n%s\n' \"$1\" \"$2\" > window.txt.tmp && mv window.txt.tmp window.txt", "sh", "{id}", "{dir}"]
EOF
  git -C "$WORKER_REPO" add sweatfile
  git -C "$WORKER_REPO" commit -m "window stub"

  SPINCLASS_SESSION_ID=driver/bats run_sc spawn windowrepo --brief "do the thing"
  assert_success
  local wt
  wt=$(echo "$output" | grep -oP 'worktree_path: \K\S+')
  # fire-and-forget: the window command runs async — poll briefly.
  local i=0
  while [ ! -f "$wt/window.txt" ] && [ "$i" -lt 50 ]; do
    sleep 0.1
    i=$((i + 1))
  done
  run cat "$wt/window.txt"
  assert_output --partial "windowrepo/"
  assert_output --partial "$wt"
}

@test "failing spawn-window does not fail the spawn" {
  create_spawn_repo windowfailrepo "$STUB_DIR/stub-harness.sh"
  cat >>"$WORKER_REPO/sweatfile" <<'EOF'
spawn-window = ["false", "{id}"]
EOF
  git -C "$WORKER_REPO" add sweatfile
  git -C "$WORKER_REPO" commit -m "failing window stub"

  SPINCLASS_SESSION_ID=driver/bats run_sc spawn windowfailrepo --brief "still works"
  assert_success
  assert_output --partial "session_key: windowfailrepo/"
}

@test "spawn rejects an unknown repo dirname" {
  SPINCLASS_SESSION_ID=driver/bats run_sc spawn no-such-repo --brief "anything"
  assert_failure
  assert_output --partial "no repo named"
}

@test "detached fork launches a hello-gated worker on a new branch" {
  create_repo
  write_stub_sweatfile "$TEST_REPO" "$STUB_DIR/stub-harness.sh"
  create_worktree feature

  cd "$WT_PATH"
  SPINCLASS_SESSION_ID=driver/bats run_sc fork forked-worker \
    --brief "branch work"
  assert_success
  assert_output --partial "session_key: repo/forked-worker"
  assert_output --partial "worktree_path: $TEST_REPO/.worktrees/forked-worker"
  assert_output --partial "worker will message driver/bats via chat"

  assert [ -d "$TEST_REPO/.worktrees/forked-worker" ]
  run cat "$TEST_REPO/.worktrees/forked-worker/brief.txt"
  assert_output "branch work"
  run cat "$TEST_REPO/.worktrees/forked-worker/.spinclass/state.json"
  assert_output --partial '"spawned_by": "driver/bats"'
}
