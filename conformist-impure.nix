# spinclass's IMPURE conformist config, merged with conformist.lib.presets.eng-impure
# in flake.nix (conformistImpureEval). Carries golangci-lint — relocated here from
# the pure ./conformist.nix — because it's a package-loading linter that needs
# ambient `go` (import resolution) and a writable $HOME for its build cache,
# neither available in the sandboxed checks.formatting derivation (a real
# `golangci-lint run` there fails with `exec: "go": executable file not found in
# $PATH` and `mkdir /homeless-shelter: permission denied`). The OLD spinclass ran
# it via `nix develop --command conformist check` for exactly this reason; this
# is the nix-module-shaped equivalent, consumed via `just lint-worktree`.
#
# v2 `standard` linter set (config in .golangci.yml): errcheck/govet/staticcheck/
# ineffassign/unused. CHECK-ONLY (no --fix) to match the read-only posture of the
# other Go linters. Run-once over the whole module: passes-files=false ->
# `golangci-lint run ./...`; includes only GATES whether it fires. Cache isolation
# (GOLANGCI_LINT_CACHE) is pinned per-worktree via the sweatfile [direnv.dotenv]
# (conformist#34).
{ pkgs, ... }:
{
  settings.linter.golangci-lint = {
    command = "${pkgs.golangci-lint}/bin/golangci-lint";
    options = [
      "run"
      "./..."
    ];
    includes = [ "*.go" ];
    "passes-files" = false;
  };
}
