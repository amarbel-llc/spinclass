# Nix interface to go.mod for spinclass. Pure-consumer half of the
# flake-input-go_mod protocol (amarbel-llc/igloo RFC 0001).
#
# Routes bridged go.mod `require` lines onto their producer flakes'
# go-pkgs outputs, collapsing the three-place lockstep (go.mod
# pseudo-version + gomod2nix.toml NAR hash + flake.lock rev) into a
# single source of truth: each producer's flake.lock entry. For modules
# listed here the organic go.mod `require` version is vestigial — the
# eval-time synthetic `replace` binds to it and wins in both nix build
# and the mkGoEnv devshell (gomod2nix(7) § GOFLAKEINPUTS); out-of-Nix
# `go build` is unsupported by RFC 0001 design.
#
# Modules NOT listed here continue to resolve organically through
# gomod2nix.toml. Add an entry when its producer flake exposes go-pkgs;
# tap/go and the other purse-first modules (go-mcp, go-mcp/command/huh)
# are the remaining open candidates (#157 — gated on the go-mcp upgrade
# question). purse-first/libs/dewey was promoted below (#185).
#
# Caveat: the vestigial `require` version must stay proxy-fetchable —
# `just deps` (gomod2nix regen) reads go.mod with no knowledge of the
# eval-time bridge, so a deleted or retagged release breaks deps regen.
{
  tommy,
  crap,
  ringmaster,
  purse-first,
  system,
}:
{
  # tommy's go-pkgs is the whole repo-root module (its module path is
  # the repo root), so no subPath. Bridged so the devshell `tommy`
  # codegen binary (also from this input — see the devShell packages)
  # and the Go library compile at one flake.lock rev. (Hardcoded module
  # path pending tommy#112, which would expose this mapping directly.)
  "code.linenisgreat.com/tommy" = {
    src = tommy.packages.${system}.go-pkgs;
  };
  # crap's go-pkgs is full-repo-filtered (polyglot), so slice into
  # go-crap. The module is at major version 2, so the key carries the
  # /v2 suffix while the on-disk subPath stays go-crap.
  "code.linenisgreat.com/crap/go-crap/v2" = {
    src = crap.packages.${system}.go-pkgs;
    subPath = "go-crap";
  };
  # ringmaster's go-pkgs is the whole repo-root module (module path is the
  # repo root, single root go.mod), so no subPath — the tommy shape. Bridged
  # so the linked jobwake library (internal/clown imports
  # code.linenisgreat.com/ringmaster/pkgs/jobwake for the flock +
  # ProtocolVersion, #26) compiles at the same flake.lock rev the ringmaster
  # CLI is pinned at for the checkPhase. The runtime CLI still resolves from
  # PATH (FDR 0010) — this bridge is the compile-time library half only.
  "code.linenisgreat.com/ringmaster" = {
    src = ringmaster.packages.${system}.go-pkgs;
  };
  # mesa (List-Table NDJSON renderer, RFC 0003) lives in purse-first's
  # dewey package. Bridged so `sc list`'s pretty/plain rendering (#185)
  # compiles against a purse-first rev that actually has pkgs/mesa (the
  # transitive pin ringmaster brings in predates it — see the
  # ringmaster.inputs.purse-first.follows override in flake.nix). Same
  # shape as clown's gomod.nix.
  "code.linenisgreat.com/purse-first/libs/dewey" = {
    src = purse-first.packages.${system}.go-pkgs;
    subPath = "libs/dewey";
  };
}
