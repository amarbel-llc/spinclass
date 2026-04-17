{
  description = "Spinclass: shell-agnostic git worktree session manager";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/4590696c8693fea477850fe379a01544293ca4e2";
    nixpkgs-master.url = "github:NixOS/nixpkgs/e2dde111aea2c0699531dc616112a96cd55ab8b5";
    utils.url = "https://flakehub.com/f/numtide/flake-utils/0.1.102";
    gomod2nix = {
      url = "github:amarbel-llc/gomod2nix";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.flake-utils.follows = "utils";
    };
    bob = {
      url = "github:amarbel-llc/bob";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.nixpkgs-master.follows = "nixpkgs-master";
      inputs.utils.follows = "utils";
    };
};

  outputs =
    {
      self,
      nixpkgs,
      nixpkgs-master,
      utils,
      gomod2nix,
      bob,
    }:
    utils.lib.eachDefaultSystem (
      system:
      let
        pkgs-master = import nixpkgs-master { inherit system; };
        pkgs = import nixpkgs {
          inherit system;
          overlays = [
            gomod2nix.overlays.default
            (_: _: { go = pkgs-master.go; })
          ];
        };

        spinclass = pkgs.buildGoApplication {
          pname = "spinclass";
          version = "0.1.0";
          src = ./.;
          modules = ./gomod2nix.toml;
          subPackages = [ "cmd/spinclass" ];

          # Generate manpages, shell completions, and the purse-first
          # plugin manifest from the command.App definitions.
          postInstall = ''
            $out/bin/spinclass generate-artifacts $out
            ln -s spinclass $out/bin/sc
          '';

          meta = {
            description = "Shell-agnostic git worktree session manager";
            homepage = "https://github.com/amarbel-llc/spinclass";
            license = pkgs.lib.licenses.mit;
          };
        };
      in
      {
        packages = {
          default = spinclass;
        };

        devShells.default = pkgs.mkShell {
          packages = [
            pkgs-master.go
            pkgs-master.gopls
            pkgs-master.gotools
            pkgs-master.golangci-lint
            pkgs-master.delve
            pkgs-master.gofumpt
            gomod2nix.packages.${system}.default
            pkgs.just
            pkgs.bats
            bob.packages.${system}.batman
            bob.packages.${system}.tap-dancer
          ];
        };
      }
    );
}
