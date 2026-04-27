{
  description = "Spinclass: shell-agnostic git worktree session manager";

  inputs = {
    nixpkgs.url = "github:amarbel-llc/nixpkgs";
    nixpkgs-master.url = "github:NixOS/nixpkgs/e2dde111aea2c0699531dc616112a96cd55ab8b5";
    utils.url = "https://flakehub.com/f/numtide/flake-utils/0.1.102";
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
      bob,
    }:
    let
      spinclassVersion = "0.1.0";
      # shortRev for clean builds, dirtyShortRev for dirty trees so devshell
      # builds visibly read `dirty-abcdef` instead of impersonating a release.
      spinclassCommit = self.shortRev or self.dirtyShortRev or "unknown";
    in
    utils.lib.eachDefaultSystem (
      system:
      let
        pkgs-master = import nixpkgs-master { inherit system; };
        pkgs = import nixpkgs {
          inherit system;
          overlays = [
            nixpkgs.overlays.default
            (_: _: { go = pkgs-master.go; })
          ];
        };

        spinclass = pkgs.buildGoApplication {
          pname = "spinclass";
          version = spinclassVersion;
          commit = spinclassCommit;
          src = ./.;
          modules = ./gomod2nix.toml;
          subPackages = [ "cmd/spinclass" ];

          # Generate manpages, mappings, hooks, and shell completions from
          # the command.App definitions. The plugin manifest (and clown
          # plugin metadata) is owned by spinclass directly, not the
          # command.App framework, so we copy and substitute the source
          # templates here.
          postInstall = ''
            $out/bin/spinclass generate-artifacts $out
            ln -s spinclass $out/bin/sc

            pluginShare="$out/share/purse-first/spinclass"
            mkdir -p "$pluginShare/.claude-plugin" \
                     "$pluginShare/.clown-plugin/system-prompt-append.d"

            install -m 0644 ${./.claude-plugin/plugin.json} \
              "$pluginShare/.claude-plugin/plugin.json"
            substituteInPlace "$pluginShare/.claude-plugin/plugin.json" \
              --replace-fail '@VERSION@' '${spinclassVersion}+${spinclassCommit}'

            install -m 0644 ${./clown.json} "$pluginShare/clown.json"
            install -m 0644 ${./.clown-plugin/clown.json} \
              "$pluginShare/.clown-plugin/clown.json"
            install -m 0644 ${./.clown-plugin/system-prompt-append.d/00-worktree.md} \
              "$pluginShare/.clown-plugin/system-prompt-append.d/00-worktree.md"
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
            pkgs.gomod2nix
            pkgs.just
            pkgs.bats
            bob.packages.${system}.batman
            bob.packages.${system}.tap-dancer
          ];
        };
      }
    );
}
