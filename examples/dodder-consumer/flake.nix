{
  description = "Example consumer: a dodder-pinned spinclass (FDR 0008), plus a clown circus that loads it as a plugin";

  inputs = {
    # spinclass master already carries the FDR 0008 dodder integration.
    # To test LOCAL spinclass changes instead, swap this for:
    #   spinclass.url = "path:../..";
    spinclass.url = "github:amarbel-llc/spinclass";

    # dodder also re-exports the exact madder it embeds as `madder-bin`,
    # which we use for the binary pin (see `spinclassPinned`) so the
    # madder that creates the .default store and the dodder that reuses
    # it are a version-matched pair — sidestepping the FDR 0008 caveat
    # that store reuse was only verified for one binary pair.
    dodder.url = "github:amarbel-llc/dodder";

    # madder is pulled in ONLY for its clown plugin (`madder-clown-plugin`);
    # the spinclass binary pin uses dodder's matched `madder-bin` instead.
    madder.url = "github:amarbel-llc/madder";

    # clown's mkCircus bundles the plugins into a launchable clown binary.
    clown.url = "github:amarbel-llc/clown";

    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    utils.url = "github:numtide/flake-utils";
  };

  outputs =
    { spinclass, dodder, madder, clown, nixpkgs, utils, ... }:
    utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs { inherit system; };

        # The dodder-pinned spinclass. mkSpinclass burns the three binary
        # paths in at link time; `sc start` then inits a per-worktree
        # madder store + a dodder repo over it, signing with the pivy key.
        spinclassPinned = spinclass.lib.${system}.mkSpinclass {
          dodder = dodder.packages.${system}.default;
          madder = dodder.packages.${system}.madder-bin;
          direnv = pkgs.direnv;
        };

        # Plugin records mirror ~/eng/lib/circus.nix: each is a flake-shaped
        # attrset exposing packages.${system}.default + rev, plus the
        # plugin dir(s) inside that output. Focused dodder set; moxy/eng/
        # caldav (as in eng) are added the same way.
        plugins = [
          {
            flake = {
              packages.${system}.default = spinclassPinned;
              rev = spinclass.rev or spinclass.dirtyRev or "dirty";
            };
            dirs = [ "share/purse-first/spinclass" ];
          }
          {
            flake = {
              packages.${system}.default = dodder.packages.${system}.dodder-clown-plugin;
              rev = dodder.rev or dodder.dirtyRev or "dirty";
            };
            dirs = [ "share/purse-first/dodder" ];
          }
          {
            flake = {
              packages.${system}.default = madder.packages.${system}.madder-clown-plugin;
              rev = madder.rev or madder.dirtyRev or "dirty";
            };
            dirs = [ "share/purse-first/madder" ];
          }
        ];
      in
      {
        # Bare dodder-pinned spinclass — what e2e.sh builds and drives.
        packages.default = spinclassPinned;

        # A clown circus bundling the dodder-pinned spinclass + the dodder
        # and madder clown plugins. mkCircus returns a set of flake outputs
        # ({ packages; devShells; checks; }); the launchable clown binary is
        # its packages.default. enableTentClaude = false keeps the example
        # off the aarch64-linux tent closure (eng defaults to isLinux).
        packages.circus =
          (clown.lib.${system}.mkCircus {
            inherit plugins;
            enableTentClaude = false;
          }).packages.default;
      }
    );
}
