{
  description = "Spinclass: shell-agnostic git worktree session manager";

  inputs = {
    # Fork: source of the buildGoApplication / buildGoRace / mkGoEnv
    # overlay. Tracks master.
    nixpkgs.url = "github:amarbel-llc/nixpkgs";

    # Upstream pin: source of the Go toolchain we pin via
    # GOTOOLCHAIN=local + go_1_26, plus general dev tools that don't
    # depend on the fork's overlay. Bumped deliberately, not on every
    # `nix flake update` of the fork.
    nixpkgs-master.url = "github:NixOS/nixpkgs/ae921939fcbd44874664477bd1d22543c10a8306";

    utils.url = "https://flakehub.com/f/numtide/flake-utils/0.1.102";

    bob = {
      url = "github:amarbel-llc/bob";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.nixpkgs-master.follows = "nixpkgs-master";
      inputs.utils.follows = "utils";
    };

    # Source of `batsLane` and `batman`. Previously consumed indirectly
    # via the amarbel-llc/nixpkgs overlay (`pkgs.testers.batsLane`); the
    # builder has since moved into this flake and is reached as
    # `bats.lib.${system}.batsLane`.
    bats = {
      url = "github:amarbel-llc/bats";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.nixpkgs-master.follows = "nixpkgs-master";
      inputs.utils.follows = "utils";
      inputs.tap.follows = "bob/tap";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      nixpkgs-master,
      utils,
      bob,
      bats,
    }:
    let
      spinclassVersion = "0.1.10";
      # shortRev for clean builds, dirtyShortRev for dirty trees so devshell
      # builds visibly read `dirty-abcdef` instead of impersonating a release.
      spinclassCommit = self.shortRev or self.dirtyShortRev or "unknown";
    in
    utils.lib.eachDefaultSystem (
      system:
      let
        # The fork's default.nix shim auto-applies overlays.default, so
        # an explicit `overlays = [ nixpkgs.overlays.default ]` would
        # just compose the overlay twice. Mirror madder's pattern.
        pkgs = import nixpkgs { inherit system; };
        pkgs-master = import nixpkgs-master { inherit system; };
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

          # Pin Go through upstream nixpkgs and disable toolchain
          # auto-download. Without these, `GOTOOLCHAIN=auto` can try to
          # fetch a toolchain when go.mod's `go 1.26` requirement isn't
          # satisfied by `pkgs.go`, which fails in the sandbox. Madder
          # pattern.
          go = pkgs-master.go_1_26;
          GOTOOLCHAIN = "local";

          ldflags =
            (lib.optional (madder != null) "-X main.madderBin=${madder}/bin/madder")
            ++ (lib.optional (direnv != null) "-X main.direnvBin=${direnv}/bin/direnv");

          # buildGoApplication's stock checkPhase runs only the
          # subPackages (cmd/spinclass). Override to test every package
          # so internal/* coverage isn't silently skipped. Tests that
          # hit pre-existing sandbox-incompatibilities (currently
          # tracked in #65) detect the sandbox via NIX_BUILD_TOP and
          # t.Skip themselves.
          doCheck = true;
          nativeCheckInputs = [ pkgs.git ];
          checkPhase = ''
            runHook preCheck
            go test -p $NIX_BUILD_CORES ./...
            runHook postCheck
          '';

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

            # Plugin-level hook registration. Clown auto-discovers
            # ${"\${CLAUDE_PLUGIN_ROOT}"}/hooks/hooks.json and wires the listed
            # PreToolUse/Stop/PostToolUse events for every Claude Code
            # session, with no per-worktree settings.local.json plumbing
            # required. The handler script execs the spinclass binary at
            # the absolute store path baked in here.
            mkdir -p "$pluginShare/hooks"
            install -m 0644 ${./hooks/hooks.json} "$pluginShare/hooks/hooks.json"
            install -m 0755 ${./hooks/handler}    "$pluginShare/hooks/handler"
            substituteInPlace "$pluginShare/hooks/handler" \
              --replace-fail '@SPINCLASS@' "$out/bin/spinclass"
          '';

          meta = {
            description = "Shell-agnostic git worktree session manager";
            homepage = "https://github.com/amarbel-llc/spinclass";
            license = pkgs.lib.licenses.mit;
          };
        };

        # mkBatsLane wraps bats.lib.${system}.batsLane (from the
        # amarbel-llc/bats flake) to run zz-tests_bats/ against a chosen
        # spinclass build. Exports SPINCLASS_BIN to the binary inside
        # `base`, stages the bats suite, and exits non-zero on any
        # failure.
        mkBatsLane = { filter ? null, base ? mkSpinclass {} }:
          bats.lib.${system}.batsLane ({
            inherit base;
            batsSrc           = ./zz-tests_bats;
            binaries          = { SPINCLASS_BIN = { inherit base; name = "spinclass"; }; };
            batsLibPath       = [ "${bob.packages.${system}.batman}/share/bats" ];
            extraEnv          = { BATS_TEST_TIMEOUT = "10"; };
            nativeBuildInputs = [ pkgs.git pkgs.jq ];
          } // lib.optionalAttrs (filter != null) { inherit filter; });

        spinclass-race = pkgs.buildGoRace { base = mkSpinclass {}; };

        batsLaneOutputs = {
          bats-default = mkBatsLane {};
          bats-race    = mkBatsLane { base = spinclass-race; };
        };
      in
      {
        packages = {
          default        = mkSpinclass {};
          spinclass-race = spinclass-race;
        } // batsLaneOutputs;

        # `nix flake check` exercises the unit suite (via the
        # spinclass derivation's checkPhase) plus every bats lane.
        checks = {
          spinclass = mkSpinclass {};
        } // batsLaneOutputs;

        # mkSpinclass = { madder ? null, direnv ? null }: ...
        # Consumer flakes call this to produce a spinclass binary with
        # absolute /nix/store paths burned in.
        lib.mkSpinclass = mkSpinclass;

        devShells.default = pkgs-master.mkShell {
          packages = [
            # gomod2nix-aware Go env; reads gomod2nix.toml for module
            # resolution. Drop-in for `pkgs.go` once gomod2nix is in
            # use. Madder pattern.
            (pkgs.mkGoEnv { pwd = ./.; })
            # gomod2nix CLI lives in the fork's overlay alongside
            # buildGoApplication / mkGoEnv — not in upstream nixpkgs.
            pkgs.gomod2nix
            bob.packages.${system}.batman
          ]
          ++ (with pkgs-master; [
            delve
            gofumpt
            golangci-lint
            gopls
            gotools
            just
          ]);

          GOTOOLCHAIN = "local";

          # batman provides bats + helper libraries (bats-support, etc.).
          # Tests run inside nix lanes (see mkBatsLane); BATS_LIB_PATH is
          # exported here only for ad-hoc `bats some_test.bats` debugging
          # in the devshell.
          shellHook = ''
            export BATS_LIB_PATH="${bob.packages.${system}.batman}/share/bats"
          '';
        };
      }
    );
}
