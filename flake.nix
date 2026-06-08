{
  description = "Spinclass: shell-agnostic git worktree session manager";

  inputs = {
    # Fork: source of the buildGoApplication / buildGoRace / mkGoEnv
    # overlay. The fork's underlying nixpkgs follows our pinned
    # `nixpkgs-master` so the overlay sits on the same base that
    # `pkgs-master` consumes, instead of pulling a second master-tracking
    # copy.
    igloo.url = "github:amarbel-llc/igloo";

    # Upstream pin: source of the Go toolchain we pin via
    # GOTOOLCHAIN=local + go_1_26, plus general dev tools that don't
    # depend on the fork's overlay. Bumped deliberately, not on every
    # `nix flake update` of the fork.
    nixpkgs-master.url = "github:NixOS/nixpkgs/d233902339c02a9c334e7e593de68855ad26c4cb";

    utils.url = "https://flakehub.com/f/numtide/flake-utils/0.1.102";

    # Source of `batsLane`, `batman`, and the bats helper libraries
    # (`bats-libs`, `bats-support`, `bats-assert`, …). Previously
    # consumed indirectly via the amarbel-llc/nixpkgs overlay
    # (`pkgs.testers.batsLane`); the builder has since moved into this
    # flake and is reached as `bats.lib.${system}.batsLane`.
    bats = {
      url = "github:amarbel-llc/bats";
      inputs.igloo.follows = "igloo";
      inputs.nixpkgs-master.follows = "nixpkgs-master";
      inputs.utils.follows = "utils";
    };

    # Source of the madder binary the `bats-madder` lane pins into
    # spinclass via `mkSpinclass { madder = ...; }`. The pin flips
    # `embeds.MadderBin()` from "" to an absolute /nix/store path, which
    # activates internal/check/check.go:runHookCompact's compact path so
    # the format-aware tap-ndjson tests in zz-tests_bats/hooks.bats run
    # in CI instead of skipping via require_madder_pinned (see #85, FDR
    # 0003/0005). The default `mkSpinclass {}` build is unaffected.
    madder = {
      url = "github:amarbel-llc/madder";
      inputs.igloo.follows = "igloo";
      inputs.nixpkgs-master.follows = "nixpkgs-master";
      inputs.utils.follows = "utils";
      inputs.bats.follows = "bats";
      # Dedupe tommy onto our top-level input (the single source of
      # truth that backs goFlakeInputs + the codegen binary) so the
      # graph resolves exactly one tommy rev.
      inputs.tommy.follows = "tommy";
    };

    # conformist: the linter + formatter multiplexer (treefmt successor).
    # Drives the goimports → gofumpt → nixfmt → shfmt chain plus
    # shellcheck; config lives in ./conformist.toml. Exposed as the
    # flake `formatter` and gated by `just lint-fmt` (conformist check).
    conformist = {
      url = "github:amarbel-llc/conformist";
      inputs.igloo.follows = "igloo";
      inputs.nixpkgs-master.follows = "nixpkgs-master";
      inputs.utils.follows = "utils";
    };

    # Single source of truth for tommy (TOML library + codegen tool). The
    # `tommy` Go module is bridged into go.mod via gomod2nix(7) goFlakeInputs
    # and the same input's binary (tommy.packages.<system>.default) is what
    # `go generate ./internal/sweatfile` (`//go:generate tommy generate`)
    # runs — so the codegen tool and the library it targets are one rev,
    # avoiding the cst-API skew that an ambient out-of-flake tommy causes.
    # Pinned to a release tag (not master) for reproducibility; bump the tag
    # deliberately + regen the codec when adopting a new tommy.
    tommy = {
      url = "github:amarbel-llc/tommy/v0.4.0";
      inputs.igloo.follows = "igloo";
      inputs.nixpkgs-master.follows = "nixpkgs-master";
      inputs.utils.follows = "utils";
    };
  };

  outputs =
    {
      self,
      igloo,
      nixpkgs-master,
      utils,
      bats,
      madder,
      conformist,
      tommy,
    }:
    let
      spinclassVersion = "0.1.22";
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
        pkgs = import igloo { inherit system; };
        pkgs-master = import nixpkgs-master { inherit system; };
        inherit (pkgs) lib;

        # `nix fmt` entry point: conformist (the treefmt successor) wrapped
        # with the formatter binaries its ./conformist.toml drives on PATH.
        # Formatting drift is gated by `just lint-fmt` (conformist check).
        # Mirrors madder's conformistFmt.
        conformistFmt = pkgs.writeShellApplication {
          name = "conformist-fmt";
          runtimeInputs = [
            conformist.packages.${system}.default
            pkgs-master.gofumpt
            pkgs-master.gotools
            pkgs.nixfmt
            pkgs.shfmt
            pkgs.shellcheck
          ];
          text = ''exec conformist "$@"'';
        };

        # Bridge the tommy Go module from its flake input (gomod2nix(7)
        # § GOFLAKEINPUTS) so buildGoApplication / mkGoEnv resolve it from
        # the same rev whose binary regenerates sweatfile_tommy.go. Module
        # is at tommy's repo root, so the shorthand form applies. (Hardcoded
        # path pending tommy#112, which would expose this mapping directly.)
        goFlakeInputs = {
          "github.com/amarbel-llc/tommy" = tommy;
        };

        # mkSpinclass builds spinclass with optional build-time-pinned
        # absolute /nix/store paths for `madder` and `direnv`. The
        # buildGoApplication overlay auto-injects -X main.version and
        # -X main.commit from the derivation attrs and appends any
        # ldflags supplied here.
        #
        # When `madder` is non-null the produced binary activates the
        # per-worktree blob-store flow at `sc start`. When null, the
        # feature is dormant. `direnv` falls back to PATH lookup when
        # the input is null. When `dodder` is non-null the binary also
        # inits a per-worktree dodder repository over the madder store
        # (FDR 0008); dormant when null.
        mkSpinclass =
          {
            madder ? null,
            direnv ? null,
            dodder ? null,
          }:
          pkgs.buildGoApplication {
            pname = "spinclass";
            version = spinclassVersion;
            commit = spinclassCommit;
            src = ./.;
            modules = ./gomod2nix.toml;
            inherit goFlakeInputs;
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
              ++ (lib.optional (direnv != null) "-X main.direnvBin=${direnv}/bin/direnv")
              ++ (lib.optional (dodder != null) "-X main.dodderBin=${dodder}/bin/dodder");

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
              install -m 0644 ${./.clown-plugin/system-prompt-append.d/10-session-jobs.md} \
                "$pluginShare/.clown-plugin/system-prompt-append.d/10-session-jobs.md"

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
        mkBatsLane =
          {
            filter ? null,
            base ? mkSpinclass { },
          }:
          bats.lib.${system}.batsLane (
            {
              inherit base;
              batsSrc = ./zz-tests_bats;
              binaries = {
                SPINCLASS_BIN = {
                  inherit base;
                  name = "spinclass";
                };
              };
              batsLibPath = [ bats.packages.${system}.bats-libs.batsLibPath ];
              extraEnv = {
                BATS_TEST_TIMEOUT = "10";
              };
              nativeBuildInputs = [
                pkgs.git
                pkgs.jq
              ];
            }
            // lib.optionalAttrs (filter != null) { inherit filter; }
          );

        spinclass-race = pkgs.buildGoRace { base = mkSpinclass { }; };

        # Madder-pinned spinclass: the base for the `bats-madder` lane.
        # The pin sets `-X main.madderBin` to an absolute /nix/store path,
        # activating runHookCompact's compact path so the tap-ndjson tests
        # in hooks.bats no longer skip (see #85, FDR 0003/0005).
        spinclass-madder = mkSpinclass {
          madder = madder.packages.${system}.default;
        };

        batsLaneOutputs = {
          bats-default = mkBatsLane { };
          bats-race = mkBatsLane { base = spinclass-race; };
          bats-madder = mkBatsLane { base = spinclass-madder; };
        };
      in
      {
        packages = {
          default = mkSpinclass { };
          spinclass-race = spinclass-race;
        }
        // batsLaneOutputs;

        # `nix flake check` exercises the unit suite (via the
        # spinclass derivation's checkPhase) plus every bats lane.
        checks = {
          spinclass = mkSpinclass { };
        }
        // batsLaneOutputs;

        # mkSpinclass = { madder ? null, direnv ? null }: ...
        # Consumer flakes call this to produce a spinclass binary with
        # absolute /nix/store paths burned in.
        lib.mkSpinclass = mkSpinclass;

        # `nix fmt` runs conformist (see conformistFmt above).
        formatter = conformistFmt;

        devShells.default = pkgs-master.mkShell {
          packages = [
            # gomod2nix-aware Go env; reads gomod2nix.toml for module
            # resolution. Drop-in for `pkgs.go` once gomod2nix is in
            # use. Madder pattern. goFlakeInputs bridges tommy so devshell
            # `go build`/gopls resolve it identically to the nix build.
            (pkgs.mkGoEnv {
              pwd = ./.;
              inherit goFlakeInputs;
            })
            # gomod2nix CLI lives in the fork's overlay alongside
            # buildGoApplication / mkGoEnv — not in upstream nixpkgs.
            pkgs.gomod2nix
            pkgs.bats
            # conformist (treefmt successor) + the formatters/linters its
            # ./conformist.toml drives, so `just fmt` / `just lint-fmt`
            # (conformist / conformist check) work in the devshell.
            conformist.packages.${system}.default
            pkgs.nixfmt
            pkgs.shfmt
            pkgs.shellcheck
            # tommy codegen tool, from the same flake input that backs the
            # bridged tommy library — so `go generate ./internal/sweatfile`
            # (//go:generate tommy generate) targets a matching cst API.
            tommy.packages.${system}.default
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

          # pkgs.bats is the test runner; bats-libs supplies bats-support,
          # bats-assert, etc. Tests run inside nix lanes (see mkBatsLane);
          # BATS_LIB_PATH is exported here only for ad-hoc
          # `bats some_test.bats` debugging in the devshell.
          shellHook = ''
            export BATS_LIB_PATH="${bats.packages.${system}.bats-libs.batsLibPath}"
          '';
        };
      }
    );
}
