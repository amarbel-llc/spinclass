# spinclass's IMPURE conformist config, merged with conformist.lib.presets.eng-impure
# in flake.nix (conformistImpureEval). Carries only the eng-convention git-state
# checks (git-remotes, git-default-branch, sweatfile, agents-md) — they need a
# live .git, so they run against the working tree via `just lint-worktree`, not
# the sandboxed checks.formatting. golangci-lint USED to live here (relocated from
# the pure lane because it needs ambient `go` + a writable build cache) but now
# runs as the pure `checks.lint` (igloo's buildGoLint), which supplies both inside
# a sandbox — and, being sandboxed, cannot poison a shared golangci cache with
# dead `.merge-*` build-worktree paths whose suppressions fail open (spinclass#294).
{ lib, ... }:
{
  # Upstream: presets.eng-impure enables a gomod2nix.toml drift check that
  # regenerates gomod2nix.toml via `gomod2nix --dir . --outdir <tmp>` and
  # diffs it (nix/linters/gomod2nix.nix) — no --impure/GOFLAGS/GOPROXY
  # override, so it shells straight out to `go mod download`. spinclass
  # consumes several code.linenisgreat.com modules (tommy, crap, ringmaster,
  # purse-first/libs/dewey) via a Nix-injected `replace` (igloo's
  # goFlakeInputs bridge, gomod.nix) that only exists inside `nix
  # build`/the mkGoEnv devShell — outside that, `go mod download` tries to
  # resolve the bridged module over the network and fails, and there is no
  # check-only/vendor knob on this linter to route around it. `just build`
  # (the real nix build, where the replace is live) remains the authoritative
  # gomod2nix consistency check.
  linters.gomod2nix.enable = lib.mkForce false;
}
