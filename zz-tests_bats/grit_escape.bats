#! /usr/bin/env bats

# End-to-end tests of the `sc grit <tool> [json]` escape hatch (FDR 0036
# section 7, "Implemented in step 6 (escape hatch)"; spinclass-grit(7)).
#
# The hatch refuses unless stdin AND stdout are terminals, so a bats `run`
# (piped) can only prove the refusal. The accepted paths run `sc` under
# util-linux `script`, which allocates a pty and runs the command on its slave
# side, so isatty() is true for stdin and stdout. THIS IS NOT THE OPERATOR'S
# REAL TERMINAL: it proves the isatty gate and the typed-confirmation read
# work against a pty, not anything about a real emulator, job control, or a
# human typing. Typed answers are fed through the pty input (script forwards
# its own stdin to the pty master); the kernel echoes them into the captured
# stdout, so a typed case's stdout carries that echo.
#
# stderr is redirected to a file inside the pty so the prompt/refusal text
# (stderr) and the result text (stdout) are recorded separately; only stdin
# and stdout need to be terminals for the hatch.
#
# Every case also prints a `# CASE ...` line on fd 3 (exit code, exact stdout,
# exact stderr) so `just debug-grit-escape-pty` shows the observed table.
#
# Pty tests skip cleanly when `script` is absent or cannot allocate a pty (the
# nix bats lanes do not provide util-linux today).

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  setup_stubs
  create_session_sweatfile
  create_repo
  # The outer session that runs this suite has SPINCLASS_*/CLOWN_* set; clear
  # them so each test controls the environment it exercises.
  unset SPINCLASS_SESSION_ID SPINCLASS_REPO SPINCLASS_BRANCH SPINCLASS_WORKTREE \
    SPINCLASS_DESCRIPTION CLOWN_SESSION_ID CLOWN_GROUP_ID
  ESCAPE_LOG="$XDG_STATE_HOME/spinclass/grit-escape.log"
  export ESCAPE_LOG
  # On a tty, `sc` writes a terminal background/cursor query (OSC 11 + CPR) to
  # stdout at startup and waits (~5s) for a reply; nothing answers on this
  # pty, which also leaks the query bytes into captured stdout. A dumb TERM
  # stops it. A real terminal answers the query, so this is a pty artifact.
  PTY_ENV='export TERM=dumb; '
}

require_pty() {
  command -v script >/dev/null ||
    skip "no 'script' (util-linux) on PATH: a pty cannot be allocated, so the escape hatch's terminal gate cannot be driven"
  printf '' | timeout 5 script -qefc 'test -t 0 && test -t 1' /dev/null >/dev/null 2>&1 ||
    skip "'script' is present but cannot allocate a pty with isatty(0) and isatty(1) here"
}

# Print one observed-result line on fd 3 (the bats live-output channel).
record() {
  local label="$1" err="${2-}"
  # shellcheck disable=SC2154  # $status is set by bats' run
  printf '# CASE %s | exit=%s | stdout=%q | stderr=%q\n' "$label" "$status" "$output" "$err" >&3
}

# pty_sh LABEL INPUT SNIPPET: run an sh snippet with a pty as its stdin/stdout.
# INPUT is fed through the pty (include the trailing newline). Sets $status,
# $output (stdout, CRs stripped) and $ERR (the snippet's stderr). The sleep
# keeps script's own stdin open until the child is done, so a closed pipe is
# not mistaken for an early EOF.
pty_sh() {
  local label="$1" input="$2" snippet="$3"
  local errf="$BATS_TEST_TMPDIR/pty.stderr"
  : >"$errf"
  run bash -c '{ printf "%s" "$1"; sleep 0.3; } | timeout 20 script -qefc "{ $2 ; } 2>\"$3\"" /dev/null' _ "$input" "${PTY_ENV-}$snippet" "$errf"
  output="${output//$'\r'/}"
  ERR="$(cat "$errf")"
  record "$label" "$ERR"
}

# pty_sc LABEL INPUT ARGS...: `sc ARGS...` under a pty (see pty_sh).
pty_sc() {
  local label="$1" input="$2"
  shift 2
  pty_sh "$label" "$input" "$(printf '%q ' "$SPINCLASS_BIN" "$@")"
}

# nonpty_sc LABEL ARGS...: `sc ARGS...` with piped stdout/stderr and stdin from
# /dev/null (no terminal anywhere).
nonpty_sc() {
  local label="$1"
  shift
  run --separate-stderr "$SPINCLASS_BIN" "$@" </dev/null
  record "$label" "$stderr"
}

# Start a real session through the real `sc start` flow. Sets WT and B and
# gives the repo its own git identity (distinct from the global one) so the
# commit identity can be told apart.
start_session() {
  cd "$TEST_REPO" || return
  git config user.name "Repo Ident"
  git config user.email "repo-ident@example.com"
  run_sc_session start
  assert_success
  WT="$(extract_wt_path "$output")"
  B="$(git -C "$WT" branch --show-current)"
  export WT B
  assert [ -d "$WT" ]
  assert [ -n "$B" ]
  cd "$WT" || return
}

# A local bare remote (a file path: no network). origin/main gets a
# remote-tracking ref via a plain git push.
add_local_remote() {
  BARE="$BATS_TEST_TMPDIR/remote.git"
  export BARE
  git init --bare -q "$BARE"
  git -C "$TEST_REPO" remote add origin "$BARE"
  git -C "$TEST_REPO" push -q origin main
}

bare_tip() { git -C "$BARE" rev-parse --verify -q "refs/heads/$1" || echo "(absent)"; }

# Non-terminal refusal (case 1)

REFUSAL='stdin and stdout must both be terminals; this escape hatch is for an operator at the keyboard and cannot be piped or driven by a program'

function hatch_refuses_without_a_terminal_for_read_and_write_verbs { # @test
  cd "$TEST_REPO" || return
  echo changed >"$TEST_REPO/file.txt"
  local spec tool args
  for spec in \
    'stage|{"subcommand":"status"}' \
    'stage|{"subcommand":"add","paths":["file.txt"]}' \
    'commit|{"message":"x"}' \
    'branch|{"subcommand":"create","name":"nope"}' \
    'stash|{"subcommand":"save"}' \
    'tag|{"subcommand":"create","name":"nope"}' \
    'remote|{"subcommand":"push"}'; do
    tool="${spec%%|*}"
    args="${spec#*|}"
    nonpty_sc "non-tty $tool $args" grit "$tool" "$args"
    assert_failure
    assert_equal "$output" ""
    [[ $stderr == *"$REFUSAL"* ]] || fail "refusal text missing for $tool: $stderr"
  done
  # Nothing ran and nothing was logged: refused before any attempt.
  assert [ ! -e "$ESCAPE_LOG" ]
  run git -C "$TEST_REPO" diff --cached --name-only
  assert_output ""
  run git -C "$TEST_REPO" branch --list nope
  assert_output ""
  run git -C "$TEST_REPO" tag --list nope
  assert_output ""
}

function hatch_refuses_when_only_one_of_stdin_stdout_is_a_terminal { # @test
  require_pty
  cd "$TEST_REPO" || return
  # stdin is the pty, stdout is a pipe.
  pty_sh "tty-stdin pipe-stdout" "" "'$SPINCLASS_BIN' grit stage '{\"subcommand\":\"status\"}' | cat"
  assert_output ""
  [[ $ERR == *"$REFUSAL"* ]] || fail "stdout-not-a-tty was not refused: $ERR"
  # stdout is the pty, stdin is /dev/null.
  pty_sh "tty-stdout null-stdin" "" "'$SPINCLASS_BIN' grit stage '{\"subcommand\":\"status\"}' </dev/null"
  assert_output ""
  [[ $ERR == *"$REFUSAL"* ]] || fail "stdin-not-a-tty was not refused: $ERR"
  assert [ ! -e "$ESCAPE_LOG" ]
}

# Session worktree, read verbs (case 2)

function hatch_in_a_session_runs_read_verbs_without_a_prompt { # @test
  require_pty
  start_session
  echo changed >"$WT/file.txt"
  pty_sc "session stage status" "" grit stage '{"subcommand":"status"}'
  assert_success
  assert_output --partial "file.txt"
  assert_equal "$ERR" ""
  pty_sc "session stage diff" "" grit stage '{"subcommand":"diff"}'
  assert_success
  assert_output --partial "+changed"
  assert_equal "$ERR" ""
  pty_sc "session branch log" "" grit branch '{"subcommand":"log","oneline":true}'
  assert_success
  assert_output --partial "initial commit"
  assert_equal "$ERR" ""
  # Every use is logged: three lines, all read verbs, Allow, no confirmation.
  run jq -s -r 'map(.verdict + "/" + (.confirm_required|tostring) + "/" + .outcome) | join(",")' "$ESCAPE_LOG"
  assert_output "allow/false/ok,allow/false/ok,allow/false/ok"
}

# Session worktree, owned write verbs (case 2)

function hatch_in_a_session_writes_owned_verbs_without_a_prompt { # @test
  require_pty
  start_session
  echo new >"$WT/new.txt"
  pty_sc "session stage add" "" grit stage '{"subcommand":"add","paths":["new.txt"]}'
  assert_success
  assert_equal "$ERR" ""
  pty_sc "session commit" "" grit commit '{"message":"hatch commit"}'
  assert_success
  assert_equal "$ERR" ""
  # The commit identity is the repo's git config, not the session's.
  run git -C "$WT" log -1 --format='%an <%ae> | %cn <%ce> | %s'
  assert_output "Repo Ident <repo-ident@example.com> | Repo Ident <repo-ident@example.com> | hatch commit"
  pty_sc "session branch create owned topic" "" grit branch "{\"subcommand\":\"create\",\"name\":\"$B--topic\"}"
  assert_success
  assert_equal "$ERR" ""
  assert_output --partial "created branch $B--topic"
  pty_sc "session branch checkout owned topic" "" grit branch "{\"subcommand\":\"checkout\",\"ref\":\"$B--topic\"}"
  assert_success
  assert_equal "$ERR" ""
  run git -C "$WT" branch --show-current
  assert_output "$B--topic"
  pty_sc "session branch checkout back" "" grit branch "{\"subcommand\":\"checkout\",\"ref\":\"$B\"}"
  assert_success
  run git -C "$WT" branch --show-current
  assert_output "$B"
}

function hatch_in_a_session_asks_for_an_unowned_branch_and_honours_the_answer { # @test
  require_pty
  start_session
  local args='{"subcommand":"create","name":"foreign-one"}'
  # Empty/EOF answer: declined.
  pty_sc "ask: EOF answer" "" grit branch "$args"
  assert_failure
  [[ $ERR == *"verdict: ask (not plainly allowed here)"* ]] || fail "no ask prompt: $ERR"
  [[ $ERR == *"run it? type y to confirm: "* ]] || fail "no confirm prompt: $ERR"
  [[ $ERR == *"declined by the operator; nothing ran"* ]] || fail "not declined: $ERR"
  # A non-y answer: declined, nothing ran.
  pty_sc "ask: typed n" $'n\n' grit branch "$args"
  assert_failure
  [[ $ERR == *"declined by the operator; nothing ran"* ]] || fail "n not declined: $ERR"
  run git -C "$WT" branch --list foreign-one
  assert_output ""
  # Typed y: runs.
  pty_sc "ask: typed y" $'y\n' grit branch "$args"
  assert_success
  assert_output --partial "created branch foreign-one"
  run git -C "$WT" branch --list foreign-one
  assert_output --partial "foreign-one"
  # --yes: skips the typing (still a terminal), no prompt read.
  pty_sc "ask: --yes" "" grit branch '{"subcommand":"create","name":"foreign-two"}' --yes
  assert_success
  assert_output --partial "created branch foreign-two"
  run jq -s -r 'map((.confirm_required|tostring) + "/" + (.confirmed|tostring) + "/" + .outcome) | join(",")' "$ESCAPE_LOG"
  assert_output "true/false/error,true/false/error,true/true/ok,false/false/ok"
}

function hatch_in_a_session_uses_the_session_local_stash_shelf { # @test
  require_pty
  start_session
  echo changed >"$WT/file.txt"
  pty_sc "session stash save" "" grit stash '{"subcommand":"save","message":"hatch"}'
  assert_success
  assert_equal "$ERR" ""
  run git -C "$WT" status --porcelain
  assert_output ""
  # Not git's repo-wide stack.
  run git -C "$WT" stash list
  assert_output ""
  assert [ -d "$WT/.spinclass/stash" ]
  pty_sc "session stash list" "" grit stash '{"subcommand":"list"}'
  assert_success
  assert_output --partial "hatch"
  pty_sc "session stash apply" "" grit stash '{"subcommand":"apply"}'
  assert_success
  run cat "$WT/file.txt"
  assert_output "changed"
  pty_sc "session stash drop" "" grit stash '{"subcommand":"drop","stash_ref":"stash@{0}"}'
  assert_success
  pty_sc "session stash list (empty)" "" grit stash '{"subcommand":"list"}'
  assert_success
  # A foreign repo_path is refused with the session-local text (Ask -> --yes).
  pty_sc "session stash foreign repo_path" "" grit stash "{\"subcommand\":\"list\",\"repo_path\":\"$TEST_REPO\"}" --yes
  assert_failure
  [[ $ERR == *"stashes are session-local: grit_stash only touches this session's own shelf"* ]] || fail "no session-local text: $ERR"
}

function hatch_in_a_session_creates_tags_by_namespace { # @test
  require_pty
  start_session
  pty_sc "session tag create owned" "" grit tag "{\"subcommand\":\"create\",\"name\":\"$B--v1\",\"lightweight\":true}"
  assert_success
  assert_equal "$ERR" ""
  run git -C "$WT" tag --list "$B--v1"
  assert_output "$B--v1"
  pty_sc "session tag create unowned typed y" $'y\n' grit tag '{"subcommand":"create","name":"plain-v1","lightweight":true}'
  assert_success
  [[ $ERR == *"run it? type y to confirm: "* ]] || fail "no prompt for unowned tag: $ERR"
  run git -C "$WT" tag --list plain-v1
  assert_output "plain-v1"
}

# Pushes to a LOCAL bare remote (case 2)

function hatch_pushes_an_owned_branch_and_tag_to_a_local_remote { # @test
  require_pty
  start_session
  add_local_remote
  echo new >"$WT/new.txt"
  git -C "$WT" add new.txt
  git -C "$WT" commit -q -m "work"
  pty_sc "push owned branch" "" grit remote "{\"subcommand\":\"push\",\"remote\":\"origin\",\"branch\":\"$B\"}"
  assert_success
  assert_equal "$ERR" ""
  assert_equal "$(bare_tip "$B")" "$(git -C "$WT" rev-parse HEAD)"
  git -C "$WT" tag "$B--t1"
  pty_sc "push owned tag" "" grit tag "{\"subcommand\":\"push\",\"remote\":\"origin\",\"name\":\"$B--t1\"}"
  assert_success
  assert_equal "$ERR" ""
  run git -C "$BARE" tag --list "$B--t1"
  assert_output "$B--t1"
}

function hatch_default_branch_fast_forward_push_is_an_ask_answered_with_y { # @test
  require_pty
  start_session
  add_local_remote
  echo new >"$WT/new.txt"
  git -C "$WT" add new.txt
  git -C "$WT" commit -q -m "work"
  local before args
  before="$(bare_tip main)"
  args="{\"subcommand\":\"push\",\"remote\":\"origin\",\"branch\":\"$B\",\"remote_branch\":\"main\"}"
  pty_sc "default-branch ff push: typed n" $'n\n' grit remote "$args"
  assert_failure
  [[ $ERR == *"declined by the operator; nothing ran"* ]] || fail "n not declined: $ERR"
  assert_equal "$(bare_tip main)" "$before"
  pty_sc "default-branch ff push: typed y" $'y\n' grit remote "$args"
  assert_success
  [[ $ERR == *"run it? type y to confirm: "* ]] || fail "no prompt: $ERR"
  assert_equal "$(bare_tip main)" "$(git -C "$WT" rev-parse HEAD)"
  # --yes skips the typing, not the terminal.
  echo more >"$WT/more.txt"
  git -C "$WT" add more.txt
  git -C "$WT" commit -q -m "more"
  pty_sc "default-branch ff push: --yes" "" grit remote "$args" --yes
  assert_success
  assert_equal "$ERR" ""
  assert_equal "$(bare_tip main)" "$(git -C "$WT" rev-parse HEAD)"
}

function hatch_refuses_force_delete_and_stack_pushes_of_the_default_branch_even_with_yes { # @test
  require_pty
  start_session
  add_local_remote
  echo new >"$WT/new.txt"
  git -C "$WT" add new.txt
  git -C "$WT" commit -q -m "work"
  local before spec label args
  before="$(bare_tip main)"
  for spec in \
    "force|{\"subcommand\":\"push\",\"remote\":\"origin\",\"branch\":\"$B\",\"remote_branch\":\"main\",\"force\":true}" \
    "force_with_lease|{\"subcommand\":\"push\",\"remote\":\"origin\",\"branch\":\"$B\",\"remote_branch\":\"main\",\"force_with_lease\":true}" \
    'delete|{"subcommand":"push","remote":"origin","remote_branch":"main","delete":true}'; do
    label="${spec%%|*}"
    args="${spec#*|}"
    pty_sc "default-branch push $label --yes" "" grit remote "$args" --yes
    assert_failure
    [[ $ERR == *"the escape hatch never force-pushes, deletes, or rewrites the default branch"* ]] || fail "$label not refused by the hatch rule: $ERR"
  done
  pty_sc "default-branch push-stack --yes" "" grit remote '{"subcommand":"push-stack","remote":"origin","branches":["main"]}' --yes
  assert_failure
  [[ $ERR == *"push-stack includes the default branch"* ]] || fail "push-stack not refused: $ERR"
  assert_equal "$(bare_tip main)" "$before"
  # Refusals are logged as errors.
  run jq -s -r 'map(.outcome) | unique | join(",")' "$ESCAPE_LOG"
  assert_output "error"
}

function hatch_refuses_non_fast_forward_and_unknown_tracking_default_branch_pushes { # @test
  require_pty
  start_session
  add_local_remote
  echo new >"$WT/new.txt"
  git -C "$WT" add new.txt
  git -C "$WT" commit -q -m "work"
  # Another clone advances origin/main, and this repo fetches it: B is no
  # longer a fast-forward of origin/main.
  git clone -q "$BARE" "$BATS_TEST_TMPDIR/other"
  git -C "$BATS_TEST_TMPDIR/other" -c user.name=o -c user.email=o@example.com commit -q --allow-empty -m "elsewhere"
  git -C "$BATS_TEST_TMPDIR/other" push -q origin HEAD:main
  git -C "$TEST_REPO" fetch -q origin
  local before
  before="$(bare_tip main)"
  pty_sc "default-branch non-ff push --yes" "" grit remote "{\"subcommand\":\"push\",\"remote\":\"origin\",\"branch\":\"$B\",\"remote_branch\":\"main\"}" --yes
  assert_failure
  [[ $ERR == *"would not be a fast-forward of origin/main"* ]] || fail "non-ff not refused: $ERR"
  # `master` is a default-branch name but origin/master was never fetched.
  pty_sc "default-branch push, no tracking ref --yes" "" grit remote "{\"subcommand\":\"push\",\"remote\":\"origin\",\"branch\":\"$B\",\"remote_branch\":\"master\"}" --yes
  assert_failure
  [[ $ERR == *"the remote-tracking ref origin/master is missing"* ]] || fail "missing tracking ref not refused: $ERR"
  assert_equal "$(bare_tip main)" "$before"
  assert_equal "$(bare_tip master)" "(absent)"
}

function hatch_body_refuses_branch_delete_of_the_default_branch_even_with_yes { # @test
  require_pty
  start_session
  pty_sc "branch delete main --yes" "" grit branch '{"subcommand":"delete","name":"main","force":true}' --yes
  assert_failure
  run git -C "$TEST_REPO" rev-parse --verify -q refs/heads/main
  assert_success
}

# Recovery claims (case 3)

function hatch_works_in_a_plain_checkout_with_no_session_and_ignores_session_env { # @test
  require_pty
  local plain="$BATS_TEST_TMPDIR/plain"
  git init -q "$plain"
  git -C "$plain" config user.name "Plain Ident"
  git -C "$plain" config user.email "plain@example.com"
  git -C "$plain" commit -q --allow-empty -m "first"
  cd "$plain" || return
  # No session state anywhere, and no `sc serve` of ours was ever started.
  assert [ ! -e "$XDG_STATE_HOME/spinclass/index" ]
  # Session/clown env that points elsewhere is ignored by the hatch.
  export CLOWN_SESSION_ID="not-a-real-principal" SPINCLASS_WORKTREE="/nonexistent" \
    SPINCLASS_SESSION_ID="x/y" SPINCLASS_BRANCH="zzz"
  pty_sc "plain checkout stage status" "" grit stage '{"subcommand":"status"}'
  assert_success
  echo new >"$plain/new.txt"
  # Every write outside a session is an Ask.
  pty_sc "plain checkout stage add: typed n" $'n\n' grit stage '{"subcommand":"add","paths":["new.txt"]}'
  assert_failure
  run git -C "$plain" diff --cached --name-only
  assert_output ""
  pty_sc "plain checkout stage add: typed y" $'y\n' grit stage '{"subcommand":"add","paths":["new.txt"]}'
  assert_success
  pty_sc "plain checkout commit: --yes" "" grit commit '{"message":"plain commit"}' --yes
  assert_success
  run git -C "$plain" log -1 --format='%an | %s'
  assert_output "Plain Ident | plain commit"
  run jq -s -r 'map(.verdict + "/" + (.confirm_required|tostring)) | join(",")' "$ESCAPE_LOG"
  assert_output "allow/false,ask/true,ask/true,ask/false"
}

# The use log (case 3)

function hatch_log_is_0600_in_a_0700_directory_and_records_each_attempt { # @test
  require_pty
  local plain="$BATS_TEST_TMPDIR/plain"
  git init -q "$plain"
  git -C "$plain" commit -q --allow-empty -m "first"
  cd "$plain" || return
  pty_sc "log: read" "" grit stage '{"subcommand":"status"}'
  assert_success
  assert_equal "$(stat -c %a "$ESCAPE_LOG")" "600"
  assert_equal "$(stat -c %a "$(dirname "$ESCAPE_LOG")")" "700"
  # Arguments refused before any attempt are not logged.
  pty_sc "log: malformed args" "" grit stage '{not json'
  assert_failure
  pty_sc "log: unknown tool" "" grit nosuchtool '{}'
  assert_failure
  assert_equal "$(wc -l <"$ESCAPE_LOG")" "1"
  run jq -r '[.time, .cwd, .tool, .subcommand, (.args|tojson), .verdict, (.confirm_required|tostring), (.confirmed|tostring), .outcome] | join(" ")' "$ESCAPE_LOG"
  assert_output --regexp "^[0-9T:Z-]+ $plain stage status \{\"subcommand\":\"status\"\} allow false false ok$"
}

function hatch_refuses_the_operation_when_the_log_is_unwritable { # @test
  require_pty
  [ "$(id -u)" -ne 0 ] || skip "running as root: file modes do not make the log unwritable"
  local plain="$BATS_TEST_TMPDIR/plain"
  git init -q "$plain"
  git -C "$plain" commit -q --allow-empty -m "first"
  cd "$plain" || return
  echo new >"$plain/new.txt"
  pty_sc "log: first use creates it" "" grit stage '{"subcommand":"status"}'
  assert_success
  chmod 0400 "$ESCAPE_LOG"
  pty_sc "log: read-only log file" "" grit stage '{"subcommand":"add","paths":["new.txt"]}' --yes
  assert_failure
  [[ $ERR == *"grit: refusing to run, the escape log cannot be written:"* ]] || fail "no refusal: $ERR"
  run git -C "$plain" diff --cached --name-only
  assert_output ""
  chmod 0600 "$ESCAPE_LOG"
  # A state directory that cannot be created also refuses.
  rm -rf "$XDG_STATE_HOME/spinclass"
  chmod 0500 "$XDG_STATE_HOME"
  pty_sc "log: state dir not creatable" "" grit stage '{"subcommand":"add","paths":["new.txt"]}' --yes
  chmod 0700 "$XDG_STATE_HOME"
  assert_failure
  [[ $ERR == *"grit: refusing to run, the escape log cannot be written:"* ]] || fail "no refusal: $ERR"
  run git -C "$plain" diff --cached --name-only
  assert_output ""
}
