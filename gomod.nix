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
# tap/go and the purse-first modules are the open candidates (#157 —
# gated on the go-mcp upgrade question).
#
# Caveat: the vestigial `require` version must stay proxy-fetchable —
# `just deps` (gomod2nix regen) reads go.mod with no knowledge of the
# eval-time bridge, so a deleted or retagged release breaks deps regen.
{
  tommy,
  crap,
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
  "github.com/amarbel-llc/crap/go-crap/v2" = {
    src = crap.packages.${system}.go-pkgs;
    subPath = "go-crap";
  };
}
