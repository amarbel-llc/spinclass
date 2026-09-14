# spinclass's IMPURE conformist config, merged with conformist.lib.presets.eng-impure
# in flake.nix (conformistImpureEval). Carries only the eng-convention git-state
# checks (git-remotes, git-default-branch, sweatfile, agents-md) — they need a
# live .git, so they run against the working tree via `just lint-worktree`, not
# the sandboxed checks.formatting. Go lint runs as the pure `checks.lint`
# (spinclass#294).
{ lib, ... }:
{
  # presets.eng-impure's gomod2nix.toml drift check has nothing to check:
  # dependencies live in go.nix (igloo FDR 0008), with no go.mod or
  # gomod2nix.toml in the checkout.
  linters.gomod2nix.enable = lib.mkForce false;
}
