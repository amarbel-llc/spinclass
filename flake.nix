{
  description = "Spinclass: shell-agnostic git worktree session manager";

  inputs = {
    nixpkgs.url = "github:amarbel-llc/nixpkgs";
    utils.url = "https://flakehub.com/f/numtide/flake-utils/0.1.102";
    bob = {
      url = "github:amarbel-llc/bob";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.nixpkgs-master.follows = "nixpkgs";
      inputs.utils.follows = "utils";
    };
};

  outputs =
    {
      self,
      nixpkgs,
      utils,
      bob,
    }:
    let
      spinclassVersion = "0.1.5";
      # shortRev for clean builds, dirtyShortRev for dirty trees so devshell
      # builds visibly read `dirty-abcdef` instead of impersonating a release.
      spinclassCommit = self.shortRev or self.dirtyShortRev or "unknown";
    in
    utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs {
          inherit system;
          overlays = [
            nixpkgs.overlays.default
          ];
        };
        inherit (pkgs) lib;

        # mkSpinclass builds spinclass with optional build-time-pinned
        # absolute /nix/store paths for `madder` and `direnv`. The
        # buildGoApplication overlay auto-injects -X main.version and
        # -X main.commit from the derivation attrs and appends any
        # ldflags supplied here.
        #
        # When `madder` is non-null the produced binary activates the
        # per-worktree blob-store flow at `sc start`. When null, the
        # feature is dormant. `direnv` falls back to PATH lookup when
        # the input is null.
        mkSpinclass = { madder ? null, direnv ? null }: pkgs.buildGoApplication {
          pname = "spinclass";
          version = spinclassVersion;
          commit = spinclassCommit;
          src = ./.;
          modules = ./gomod2nix.toml;
          subPackages = [ "cmd/spinclass" ];

          ldflags =
            (lib.optional (madder != null) "-X main.madderBin=${madder}/bin/madder")
            ++ (lib.optional (direnv != null) "-X main.direnvBin=${direnv}/bin/direnv");

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

            # clown-plugin-host resolves a relative `command` against the
            # plugin directory, with no PATH fallback (see clown
            # internal/pluginhost/config.go Desugar). Bake the absolute
            # store path so the bridge can exec the binary regardless of
            # the host's CWD or PATH.
            #
            # The same manifest is installed at both <plugin-dir>/clown.json
            # (where clown actually reads it, per LoadClownConfig) and at
            # <plugin-dir>/.clown-plugin/clown.json (kept in sync against
            # any future change in clown's discovery rules).
            install -m 0644 ${./clown.json} "$pluginShare/clown.json"
            install -m 0644 ${./clown.json} "$pluginShare/.clown-plugin/clown.json"
            substituteInPlace \
              "$pluginShare/clown.json" \
              "$pluginShare/.clown-plugin/clown.json" \
              --replace-fail '@SPINCLASS@' "$out/bin/spinclass"
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
          default = mkSpinclass {};
        };

        # mkSpinclass = { madder ? null, direnv ? null }: ...
        # Consumer flakes call this to produce a spinclass binary with
        # absolute /nix/store paths burned in.
        lib.mkSpinclass = mkSpinclass;

        devShells.default = pkgs.mkShell {
          packages = [
            pkgs.go
            pkgs.gopls
            pkgs.gotools
            pkgs.golangci-lint
            pkgs.delve
            pkgs.gofumpt
            pkgs.gomod2nix
            pkgs.just
            pkgs.bats
            bob.packages.${system}.batman
            bob.packages.${system}.tap-dancer
          ];

          # Quick fix: pkgs.bats resolves before batman in PATH, so the bats
          # binary that runs is the raw one without BATS_LIB_PATH. Export
          # the path here so common.bash's `bats_load_library` calls find
          # bats-support / bats-assert / etc. See #44 for the broader
          # untangling of bats infrastructure across repos.
          shellHook = ''
            export BATS_LIB_PATH="${bob.packages.${system}.batman}/share/bats"
          '';
        };
      }
    );
}
