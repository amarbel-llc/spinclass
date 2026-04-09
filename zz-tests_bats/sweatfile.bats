#! /usr/bin/env bats

setup() {
  load "$(dirname "$BATS_TEST_FILE")/common.bash"
  export output
  setup_test_home
  setup_stubs
  create_repo
}

function apply_writes_claude_settings { # @test
  # Create a sweatfile with claude-allow rules
  mkdir -p "$TEST_REPO"
  cat >"$TEST_REPO/sweatfile" <<'EOF'
[claude]
allow = ["Bash(git *)"]
EOF

  cd "$TEST_REPO"
  run_sc start --no-attach test_settings
  assert_success

  # Extract the worktree path from TAP output (ok N - create <branch> <path>)
  local wt_path
  wt_path=$(extract_wt_path "$output")
  local settings="$wt_path/.claude/settings.local.json"
  assert [ -f "$settings" ]

  # Check that claude-allow rules appear in the settings
  run cat "$settings"
  assert_output --partial '"Bash(git *)"'
  # Should also have the default Read rule
  assert_output --partial '"defaultMode"'
}

function apply_merges_hierarchy { # @test
  # Global sweatfile (hierarchy loads from $HOME/.config, not XDG_CONFIG_HOME)
  mkdir -p "$HOME/.config/spinclass"
  cat >"$HOME/.config/spinclass/sweatfile" <<'EOF'
[claude]
allow = ["Bash(git *)"]
EOF

  # Repo sweatfile
  cat >"$TEST_REPO/sweatfile" <<'EOF'
[claude]
allow = ["Bash(nix *)"]
EOF

  cd "$TEST_REPO"
  run_sc start --no-attach test_hierarchy
  assert_success

  # Extract the worktree path from TAP output
  local wt_path
  wt_path=$(extract_wt_path "$output")
  local settings="$wt_path/.claude/settings.local.json"
  assert [ -f "$settings" ]

  # Both rules should appear (global + repo merged)
  run cat "$settings"
  assert_output --partial '"Bash(git *)"'
  assert_output --partial '"Bash(nix *)"'
}

function apply_writes_envrc_when_flake_exists { # @test
  # Create a flake.nix in the repo
  cat >"$TEST_REPO/flake.nix" <<'EOF'
{ }
EOF
  git -C "$TEST_REPO" add flake.nix
  git -C "$TEST_REPO" commit -m "add flake.nix"

  cd "$TEST_REPO"
  run_sc start --no-attach test_envrc_flake
  assert_success

  # Extract the worktree path from TAP output
  local wt_path
  wt_path=$(extract_wt_path "$output")
  local envrc="$wt_path/.envrc"
  assert [ -f "$envrc" ]
  run cat "$envrc"
  assert_output --partial "source_up"
  assert_output --partial "use flake"
}

function apply_skips_use_flake_without_flake_nix { # @test
  cd "$TEST_REPO"
  run_sc start --no-attach test_envrc_no_flake
  assert_success

  # Extract the worktree path from TAP output
  local wt_path
  wt_path=$(extract_wt_path "$output")
  local envrc="$wt_path/.envrc"
  assert [ -f "$envrc" ]
  run cat "$envrc"
  assert_output --partial "source_up"
  assert_output --partial "PATH_add"
  refute_output --partial "use flake"
}

function apply_writes_mcps_to_mcp_json { # @test
  cat >"$TEST_REPO/sweatfile" <<'EOF'
[[mcps]]
name = "my-linter"
command = "my-linter"
args = ["serve", "--format=json"]

[mcps.env]
DEBUG = "1"
EOF

  cd "$TEST_REPO"
  run_sc start --no-attach test_mcps
  assert_success

  local wt_path
  wt_path=$(extract_wt_path "$output")
  local mcp_json="$wt_path/.mcp.json"
  assert [ -f "$mcp_json" ]

  # spinclass server always present
  run jq -r '.mcpServers.spinclass.command' "$mcp_json"
  assert_output "spinclass"

  # my-linter server registered from sweatfile
  run jq -r '.mcpServers."my-linter".command' "$mcp_json"
  assert_output "my-linter"

  run jq -r '.mcpServers."my-linter".args[0]' "$mcp_json"
  assert_output "serve"

  run jq -r '.mcpServers."my-linter".env.DEBUG' "$mcp_json"
  assert_output "1"
}

function apply_mcps_adds_to_enabled_mcp_servers { # @test
  cat >"$TEST_REPO/sweatfile" <<'EOF'
allowed-mcps = ["external-server"]

[[mcps]]
name = "my-linter"
command = "my-linter"
args = ["serve"]
EOF

  cd "$TEST_REPO"
  run_sc start --no-attach test_mcps_enabled
  assert_success

  local wt_path
  wt_path=$(extract_wt_path "$output")
  local settings="$wt_path/.claude/settings.local.json"
  assert [ -f "$settings" ]

  # enabledMcpjsonServers should include all three
  run jq -r '.enabledMcpjsonServers[]' "$settings"
  assert_output --partial "spinclass"
  assert_output --partial "external-server"
  assert_output --partial "my-linter"
}

function apply_mcps_hierarchy_override { # @test
  # Global: register linter v1
  mkdir -p "$HOME/.config/spinclass"
  cat >"$HOME/.config/spinclass/sweatfile" <<'EOF'
[[mcps]]
name = "linter"
command = "lint-v1"
args = ["serve"]
EOF

  # Repo: override with linter v2
  cat >"$TEST_REPO/sweatfile" <<'EOF'
[[mcps]]
name = "linter"
command = "lint-v2"
args = ["serve", "--new"]
EOF

  cd "$TEST_REPO"
  run_sc start --no-attach test_mcps_override
  assert_success

  local wt_path
  wt_path=$(extract_wt_path "$output")
  local mcp_json="$wt_path/.mcp.json"

  # Should have v2, not v1
  run jq -r '.mcpServers.linter.command' "$mcp_json"
  assert_output "lint-v2"
}

function apply_mcps_removal_sentinel { # @test
  # Global: register linter
  mkdir -p "$HOME/.config/spinclass"
  cat >"$HOME/.config/spinclass/sweatfile" <<'EOF'
[[mcps]]
name = "linter"
command = "lint"
args = ["serve"]
EOF

  # Repo: remove linter with name-only sentinel
  cat >"$TEST_REPO/sweatfile" <<'EOF'
[[mcps]]
name = "linter"
EOF

  cd "$TEST_REPO"
  run_sc start --no-attach test_mcps_removal
  assert_success

  local wt_path
  wt_path=$(extract_wt_path "$output")
  local mcp_json="$wt_path/.mcp.json"

  # linter should NOT be in .mcp.json (only spinclass)
  run jq -r '.mcpServers | keys[]' "$mcp_json"
  assert_output "spinclass"
  refute_output --partial "linter"
}

function session_entrypoint_expands_env_vars { # @test
  # Create a sweatfile with session.start referencing $SPINCLASS_SESSION_ID
  cat >"$TEST_REPO/sweatfile" <<'EOF'
[session-entry]
start = ["echo", "$SPINCLASS_SESSION_ID", "$SPINCLASS_BRANCH"]
EOF

  cd "$TEST_REPO"
  run_sc start --no-attach env_expand_test
  assert_success

  # The TAP output should show expanded env vars (repo/<random-branch>), not literals
  assert_output --partial "repo/"
  refute_output --partial '$SPINCLASS_SESSION_ID'
  refute_output --partial '$SPINCLASS_BRANCH'
}
