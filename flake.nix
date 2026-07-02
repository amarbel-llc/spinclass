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
    nixpkgs-master.url = "github:NixOS/nixpkgs/567a49d1913ce81ac6e9582e3553dd90a955875f";

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
    # activates internal/check/check.go:runHookPhase's blob-storage /
    # resource_link path so the format-aware tap-ndjson tests in
    # zz-tests_bats/hooks.bats run in CI instead of skipping via
    # require_madder_pinned (see #85, FDR 0003/0015). The default
    # `mkSpinclass {}` build is unaffected.
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

    # crap: source of the go-crap Go module (ndjson-crap wire format +
    # viewport presenter), bridged into go.mod via gomod.nix. Consumed
    # by the `ndjson-crap` pre-merge-output-format in internal/check.
    crap = {
      url = "github:amarbel-llc/crap";
      inputs.igloo.follows = "igloo";
      inputs.nixpkgs-master.follows = "nixpkgs-master";
      inputs.utils.follows = "utils";
      inputs.bats.follows = "bats";
    };

    # Single source of truth for tommy (TOML library + codegen tool):
    # the Go module is bridged into go.mod via gomod.nix and the same
    # input's binary backs `just gen-tommy` (see the devShell packages).
    # Pinned to a release tag (not master) for reproducibility; bump the
    # tag deliberately + regen the codec when adopting a new tommy.
    tommy = {
      url = "github:amarbel-llc/tommy/v0.4.6";
      inputs.igloo.follows = "igloo";
      inputs.nixpkgs-master.follows = "nixpkgs-master";
      inputs.utils.follows = "utils";
    };

    # papi: the Personal API CLI. Pinned into spinclass via
    # `mkSpinclass { papi = ...; }` and burned into the default build's
    # binary (-X main.papiBin). The dynamic system-prompt fragment
    # (internal/repoinfo) shells out to it to resolve the forge kind of a
    # non-github.com remote against the operator's published PAPI forges.
    # gh (the other repo-line dependency) is not a standalone flake — it
    # is pinned from nixpkgs-master (`pkgs-master.gh`).
    papi = {
      url = "github:amarbel-llc/papi";
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
      crap,
      tommy,
      papi,
    }:
    let
      spinclassVersion = "0.1.33";
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

        # Toolchain-hermetic conformist hooks (conformist#59): the TOML-consumer
        # mirror of the module's build.{wrapper,preCommit,repair}. Returns three
        # store-pinned wrappers — `conformist` (nix fmt / check / repair entry,
        # the flake `formatter`), `conformist-pre-commit`
        # (--staged --exit-zero-on-fix), and `conformist-repair`
        # (--commit --amend …) — each exec'ing conformist with the toolchain below
        # baked on PATH, so a hook never silently skips a filetype whose formatter
        # is absent from the ambient PATH (the conformist#51 trap), and with
        # --config-file pinned to ./conformist.toml. Supersedes the hand-rolled
        # conformistFmt; the sweatfile names `conformist-pre-commit` as the
        # per-commit hook. `go` stays ambient (devShell mkGoEnv) for the
        # golangci-lint / tommy-codegen linters, per the helper's documented caveat.
        conformistHooks = conformist.lib.mkToolchainHooks pkgs {
          conformist = conformist.packages.${system}.default;
          configFile = ./conformist.toml;
          tools = [
            pkgs-master.gofumpt
            pkgs-master.gotools # provides goimports
            pkgs.nixfmt
            pkgs.shfmt
            pkgs.shellcheck
            # Nix linters (conformist.toml [linter.statix] / [linter.deadnix]).
            pkgs.statix
            pkgs.deadnix
            # Go linter (conformist.toml [linter.golangci-lint], v2 standard set).
            pkgs-master.golangci-lint
            # tommy fmt owns *.toml (conformist.toml [formatter.tommy]); same
            # input that backs the bridged library + codegen tool.
            tommy.packages.${system}.default
            # conformist.toml [linter.tommy-codegen] repair driver.
            tommy.packages.${system}.conformist-tommy-codegen
          ];
        };

        # Consumer half of the flake-input-go_mod protocol (RFC 0001):
        # which sibling Go modules resolve from producer go-pkgs outputs
        # instead of the proxy. See gomod.nix for the entries and the
        # lockstep rationale.
        goFlakeInputs = import ./gomod.nix { inherit tommy crap system; };

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
        # (FDR 0008); dormant when null. `papi` and `gh` are pinned for
        # the dynamic system-prompt repository line (internal/repoinfo);
        # both fall back to PATH lookup when null.
        mkSpinclass =
          {
            madder ? null,
            direnv ? null,
            dodder ? null,
            papi ? null,
            gh ? null,
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
              ++ (lib.optional (dodder != null) "-X main.dodderBin=${dodder}/bin/dodder")
              ++ (lib.optional (papi != null) "-X main.papiBin=${papi}/bin/papi")
              ++ (lib.optional (gh != null) "-X main.ghBin=${gh}/bin/gh");

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
                       "$pluginShare/.clown-plugin"

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

              # No static system-prompt-append.d fragments are installed: the
              # session orientation is contributed dynamically at launch by
              # `spinclass serve` (clown plugin protocol RFC-0002 §5; the
              # `systemPrompt: true` opt-in in clown.json above). See
              # internal/sysprompt and spinclass#187.

              # Plugin-level hook registration. Clown auto-discovers
              # ${"\${CLAUDE_PLUGIN_ROOT}"}/hooks/hooks.json and wires the listed
              # PreToolUse/Stop/PostToolUse/SessionStart/SessionEnd events for every Claude Code
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

        # papi/gh pins for the default build's dynamic system-prompt
        # repository line (internal/repoinfo). Burned into the shipped
        # binary so the forge lookup is deterministic; a devshell `go build`
        # (which sets no ldflags) falls back to PATH. gh comes from
        # nixpkgs-master, papi from its flake input.
        forgePins = {
          papi = papi.packages.${system}.default;
          inherit (pkgs-master) gh;
        };

        spinclass-race = pkgs.buildGoRace { base = mkSpinclass { }; };

        # Madder-pinned spinclass: the base for the `bats-madder` lane.
        # The pin sets `-X main.madderBin` to an absolute /nix/store path,
        # activating runHookPhase's blob-storage/resource_link path so the
        # tap-ndjson tests in hooks.bats no longer skip (see #85, FDR
        # 0003/0015).
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
          default = mkSpinclass forgePins;
          inherit spinclass-race;
        }
        // batsLaneOutputs;

        # `nix flake check` exercises the unit suite (via the
        # spinclass derivation's checkPhase) plus every bats lane. The
        # `spinclass` check builds the same forge-pinned variant as the
        # default package, so `nix flake check` also verifies the papi/gh
        # burn-in.
        checks = {
          spinclass = mkSpinclass forgePins;
        }
        // batsLaneOutputs;

        # mkSpinclass = { madder ? null, direnv ? null }: ...
        # Consumer flakes call this to produce a spinclass binary with
        # absolute /nix/store paths burned in.
        lib.mkSpinclass = mkSpinclass;

        # `nix fmt` runs conformist (the toolchain-hermetic wrapper).
        inherit (conformistHooks) formatter;

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
            # conformist hooks (toolchain-hermetic wrappers, conformist#59):
            # `conformist` (= conformistHooks.formatter; what `just fmt` /
            # `just lint-fmt` invoke) REPLACES the bare conformist binary on PATH,
            # so a bare `conformist` resolves to the toolchain-carrying wrapper —
            # plus the `conformist-pre-commit` / `conformist-repair` hook commands
            # the sweatfile names. The formatter tools below stay for ad-hoc use.
            conformistHooks.formatter
            conformistHooks.preCommit
            conformistHooks.repair
            pkgs.nixfmt
            pkgs.shfmt
            pkgs.shellcheck
            pkgs.statix
            pkgs.deadnix
            # tommy codegen tool, from the same flake input that backs the
            # bridged tommy library — so `go generate ./internal/sweatfile`
            # (//go:generate tommy generate) targets a matching cst API.
            tommy.packages.${system}.default
            # conformist tommy-codegen repair driver (conformist.toml
            # [linter.tommy-codegen]); so `just fmt` regenerates the codec.
            tommy.packages.${system}.conformist-tommy-codegen
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
