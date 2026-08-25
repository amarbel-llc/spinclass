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
{ pkgs, lib, ... }:
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

  # Upstream: presets.eng-impure enables a gomod2nix.toml drift check that
  # regenerates gomod2nix.toml via `gomod2nix --dir . --outdir <tmp>` and
  # diffs it (nix/linters/gomod2nix.nix) — no --impure/GOFLAGS/GOPROXY
  # override, so it shells straight out to `go mod download`. spinclass
  # consumes several code.linenisgreat.com modules (tommy, crap, ringmaster,
  # purse-first/libs/dewey) via a Nix-injected `replace` (igloo's
  # goFlakeInputs bridge, gomod.nix) that only exists inside `nix
  # build`/the mkGoEnv devShell — outside that, `go mod download` tries to
  # resolve the bridged module over the network and fails. `just
  # lint-worktree`'s recipe additionally materializes the merged go.mod
  # (with the bridge's replace lines) around the golangci-lint run so IT
  # can resolve a bridged module too (igloo#62 — mkGoEnv's own ambient `go`
  # doesn't apply the bridge, contrary to gomod2nix(7)'s prior "mkGoEnv
  # parity" claim, fixed on igloo master); that materialized go.mod would
  # make THIS linter's own `gomod2nix generate` diff-fail (generate
  # correctly strips the bridged keys, but the committed gomod2nix.toml
  # still carries their vestigial pre-bridge entries). Same clown#174 shape;
  # no check-only/vendor knob exists on this linter to route around either
  # failure mode. `just build` (the real nix build, where the replace is
  # live) remains the authoritative gomod2nix consistency check.
  linters.gomod2nix.enable = lib.mkForce false;
}
