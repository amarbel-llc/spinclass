{
  description = "Spinclass: shell-agnostic git worktree session manager";

  inputs = {
    # Fork: source of the buildGoApplication / buildGoRace / mkGoEnv
    # overlay. The fork's underlying nixpkgs follows our pinned
    # `nixpkgs-master` so the overlay sits on the same base that
    # `pkgs-master` consumes, instead of pulling a second master-tracking
    # copy.
    igloo = {
      url = "https://code.linenisgreat.com/igloo/archive/master.tar.gz";
      inputs.nixpkgs-master.follows = "nixpkgs-master";
    };

    # Upstream pin: source of the Go toolchain we pin via
    # GOTOOLCHAIN=local + go_1_26, plus general dev tools that don't
    # depend on the fork's overlay. Bumped deliberately, not on every
    # `nix flake update` of the fork.
    nixpkgs-master.url = "github:NixOS/nixpkgs/567a49d1913ce81ac6e9582e3553dd90a955875f";

    utils = {
      url = "https://flakehub.com/f/numtide/flake-utils/0.1.102";
      inputs.systems.follows = "igloo/systems";
    };

    # Source of `batsLane`, `batman`, and the bats helper libraries
    # (`bats-libs`, `bats-support`, `bats-assert`, …). Previously
    # consumed indirectly via the amarbel-llc/nixpkgs overlay
    # (`pkgs.testers.batsLane`); the builder has since moved into this
    # flake and is reached as `bats.lib.${system}.batsLane`.
    bats = {
      url = "https://code.linenisgreat.com/bats/archive/master.tar.gz";
      inputs = {
        igloo.follows = "igloo";
        nixpkgs-master.follows = "nixpkgs-master";
        utils.follows = "utils";
        conformist.follows = "conformist";
      };
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
      url = "https://code.linenisgreat.com/madder/archive/master.tar.gz";
      inputs = {
        igloo.follows = "igloo";
        nixpkgs-master.follows = "nixpkgs-master";
        utils.follows = "utils";
        bats.follows = "bats";
        # Dedupe tommy onto our top-level input (the single source of
        # truth that backs goFlakeInputs + the codegen binary) so the
        # graph resolves exactly one tommy rev.
        tommy.follows = "tommy";
        conformist.follows = "conformist";
        crap.follows = "crap";
      };
    };
    madder.inputs.hyphence.inputs.langlang.follows = "papi/langlang";

    # conformist: the linter + formatter multiplexer (treefmt successor).
    # Consumed as a nix module (conformist.lib.evalModule): config is defined
    # in ./conformist.nix + presets.{eng,eng-go} and GENERATED (no
    # hand-written conformist.toml). Exposed as the flake `formatter` and
    # gated by `just lint-fmt` (the sandboxed checks.formatting).
    conformist = {
      url = "https://code.linenisgreat.com/conformist/archive/master.tar.gz";
      inputs = {
        igloo.follows = "igloo";
        nixpkgs-master.follows = "nixpkgs-master";
        utils.follows = "utils";
      };
    };

    # crap: source of the go-crap Go module (ndjson-crap wire format +
    # viewport presenter), bridged into go.mod via gomod.nix. Consumed
    # by the `ndjson-crap` pre-merge-output-format in internal/check.
    crap = {
      url = "https://code.linenisgreat.com/crap/archive/master.tar.gz";
      inputs = {
        igloo.follows = "igloo";
        nixpkgs-master.follows = "nixpkgs-master";
        utils.follows = "utils";
        bats.follows = "bats";
        conformist.follows = "conformist";
      };
    };

    # Single source of truth for tommy (TOML library + codegen tool):
    # the Go module is bridged into go.mod via gomod.nix and the same
    # input's binary backs `just gen-tommy` (see the devShell packages).
    # Pinned to a release tag (not master) for reproducibility; bump the
    # tag deliberately + regen the codec when adopting a new tommy.
    tommy = {
      url = "https://code.linenisgreat.com/tommy/archive/master.tar.gz";
      inputs = {
        igloo.follows = "igloo";
        nixpkgs-master.follows = "nixpkgs-master";
        utils.follows = "utils";
        bats.follows = "bats";
        conformist.follows = "conformist";
        tap = {
          follows = "madder/tap";
          inputs = {
            treefmt-nix.follows = "igloo/treefmt-nix";
            gomod2nix.inputs.flake-utils.follows = "utils";
          };
        };
      };
    };

    # ringmaster: clown's job platform (durable job journal +
    # wake-on-completion channel), extracted standalone from clown and now
    # upstream of both troupe and clown. internal/clown shells out to its CLI
    # to emit async-job lifecycle events (FDR 0010).
    #
    # Consumed ONLY as a checkPhase input: it puts a real `ringmaster` on PATH
    # inside the sandbox so internal/clown's contract test exercises the actual
    # argv + journal behaviour instead of the stub it had to settle for before
    # (#253). This is deliberately NOT a runtime pin — the binary still
    # resolves from PATH at run time, gated on CLOWN_BIN, so the shipped
    # closure is unchanged.
    #
    # FDR 0010 originally declined to pin on the grounds that "pinning clown
    # would drag its whole input closure in". That reasoning applied to clown,
    # not to the extracted platform: ringmaster's four inputs (igloo,
    # nixpkgs-master, utils, bats) are a strict subset of spinclass's own, so
    # every one `follows` an existing pin and no closure grows. Tracks master
    # like every sibling input — deliberately not rev-pinned.
    ringmaster = {
      url = "https://code.linenisgreat.com/ringmaster/archive/master.tar.gz";
      inputs = {
        igloo.follows = "igloo";
        nixpkgs-master.follows = "nixpkgs-master";
        utils.follows = "utils";
        bats.follows = "bats";
      };
    };

    # papi: the Personal API CLI. Pinned into spinclass via
    # `mkSpinclass { papi = ...; }` and burned into the default build's
    # binary (-X main.papiBin). The dynamic system-prompt fragment
    # (internal/repoinfo) shells out to it to resolve the forge kind of a
    # non-github.com remote against the operator's published PAPI forges.
    # gh (the other repo-line dependency) is not a standalone flake — it
    # is pinned from nixpkgs-master (`pkgs-master.gh`).
    papi = {
      url = "https://code.linenisgreat.com/papi/archive/master.tar.gz";
      inputs = {
        igloo.follows = "igloo";
        nixpkgs-master.follows = "nixpkgs-master";
        utils.follows = "utils";
        conformist.follows = "conformist";
        piggy.follows = "madder/piggy";
        purse-first.follows = "madder/purse-first";
      };
    };
    papi.inputs.hyphence.follows = "madder/hyphence";
    papi.inputs.langlang.inputs.tap.inputs.crane.follows = "madder/tap/crane";
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
      ringmaster,
    }:
    let
      # version.env at repo root is the single source of truth for the release
      # version (eng-versioning(7)). The fork's buildGoApplication auto-reads
      # it (see mkSpinclass below — no explicit `version` attr is passed), so
      # this let-binding exists only for the genuine eval-time consumer that
      # isn't Go: the plugin.json `@VERSION@` substitution in postInstall.
      spinclassVersion = builtins.head (
        builtins.match ".*SPINCLASS_VERSION=([^\n]+).*" (builtins.readFile ./version.env)
      );
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

        # tommy fmt (*.toml) and the tommy-codegen repair linter have no
        # registry program (repo-specific) and need the `tommy` flake input,
        # so they are inlined here rather than in ./conformist.nix (a
        # standalone module file can't see flake inputs — same shape as
        # cutting-garden's/dodder's conformistTommyModule). getExe' with an
        # explicit binary name: tommy lacks meta.mainProgram.
        #
        # command="true" / repair-command=conformist-tommy-codegen matches the
        # old [linter.tommy-codegen] stanza exactly: run-once whole-tree
        # (passes-files=false), check is a deliberate no-op (tommy's own
        # --check diverges from the gofumpt'd committed codec and needs `go`
        # on PATH), repair regenerates via the store-pinned driver. `just
        # gen-tommy-check` (not conformist) is the separate drift-enforcing
        # guard (#159) — no restage-repair-outputs/stage-* flags here.
        conformistTommyModule = _: {
          settings.formatter.tommy = {
            command = pkgs.lib.getExe' tommy.packages.${system}.default "tommy";
            options = [ "fmt" ];
            includes = [ "*.toml" ];
          };
          settings.linter.tommy-codegen = {
            command = "true";
            "repair-command" =
              pkgs.lib.getExe' tommy.packages.${system}.conformist-tommy-codegen
                "conformist-tommy-codegen";
            includes = [ "*.go" ];
            "passes-files" = false;
          };
        };

        # conformist config via its nix module (conformist#51/#114): the eng
        # preset (eng-convention linters) + the canonical Go formatter chain
        # (eng-go) + this repo's formatters/excludes (./conformist.nix) + the
        # tommy blocks above. Drives `nix fmt` (build.wrapper), the sandboxed
        # `checks.formatting` (build.check), and the store-pinned
        # `conformist-pre-commit` / `conformist-repair` hook commands the
        # sweatfile names (FDR 0019). Supersedes the hand-rolled
        # mkToolchainHooks + hand-written ./conformist.toml.
        conformistEval = conformist.lib.evalModule pkgs {
          imports = [
            conformist.lib.presets.eng
            conformist.lib.presets.eng-go
            ./conformist.nix
            conformistTommyModule
          ];
          package = conformist.packages.${system}.default;
        };

        # Impure lane: the eng-convention git-state checks (git-remotes,
        # git-default-branch, sweatfile, agents-md, gomod2nix — presets.eng-impure)
        # PLUS golangci-lint, relocated here from the pure eval above. golangci-lint
        # is a package-loading linter: it needs ambient `go` (to resolve imports)
        # and a writable $HOME for its build cache, neither available in the
        # sandboxed `checks.formatting` derivation (confirmed: `exec: "go":
        # executable file not found in $PATH` + `mkdir /homeless-shelter:
        # permission denied` when it was wired into the pure lane). This is why
        # the OLD spinclass ran it via `nix develop --command conformist check`
        # (an impure devShell invocation) rather than a sandboxed build — no
        # fleet repo runs a real `golangci-lint run` inside checks.formatting
        # (golangci-dewey, the only related preset linter, only greps for
        # .custom-gcl.yml wiring). These checks need a live .git / a real Go
        # toolchain, so they run against the working tree via `just lint-worktree`
        # (crap/papi/tommy shape), not the sandboxed check.
        conformistImpureEval = conformist.lib.evalModule pkgs {
          imports = [
            conformist.lib.presets.eng-impure
            ./conformist-impure.nix
          ];
          package = conformist.packages.${system}.default;
          projectRootFile = "flake.nix";
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
            # No explicit `version` here: buildGoApplication auto-reads
            # version.env (eng-versioning(7) VERSION EMBEDDING) from `src`
            # (pwd defaults to src when unset) and feeds both the derivation
            # `version` attr and the `-X main.version` ldflag. An explicit
            # `version` attr would silently override that auto-read.
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
            # ringmaster is a check-only input: it puts the real job-platform
            # CLI on PATH so internal/clown's contract test can drive actual
            # start/spool-path/done calls against a scratch XDG journal
            # instead of asserting argv against a stub (#253). It stays out of
            # the runtime closure — at run time the binary is resolved from
            # PATH and gated on CLOWN_BIN. Kept unconditional so
            # `packages.default` and `checks.spinclass` remain one derivation.
            doCheck = true;
            nativeCheckInputs = [
              pkgs.git
              ringmaster.packages.${system}.ringmaster
            ];
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
              homepage = "https://code.linenisgreat.com/spinclass";
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
          # The generated impure-lane config (git-state eng-convention checks
          # + golangci-lint), consumed by `just lint-worktree` to run
          # `conformist check` against the working tree where .git/go are
          # available. See conformistImpureEval above.
          conformist-impure-config = conformistImpureEval.config.build.configFile;
        }
        // batsLaneOutputs;

        # `nix flake check` exercises the unit suite (via the
        # spinclass derivation's checkPhase) plus every bats lane. The
        # `spinclass` check builds the same forge-pinned variant as the
        # default package, so `nix flake check` also verifies the papi/gh
        # burn-in.
        checks = {
          spinclass = mkSpinclass forgePins;
          # Sandboxed read-only formatting + eng-convention-linter gate
          # (conformist check against a /nix/store snapshot of the tracked
          # tree). `just lint-fmt` builds this. See conformistEval above.
          formatting = conformistEval.config.build.check self;
        }
        // batsLaneOutputs;

        # mkSpinclass = { madder ? null, direnv ? null }: ...
        # Consumer flakes call this to produce a spinclass binary with
        # absolute /nix/store paths burned in.
        lib.mkSpinclass = mkSpinclass;

        # `nix fmt` runs the module-generated conformist wrapper (see
        # conformistEval above).
        formatter = conformistEval.config.build.wrapper;

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
            # The RAW conformist binary on PATH — NOT conformistEval.config.build.wrapper.
            # The wrapper hardcodes `--tree-root-file=flake.nix` (repair mode,
            # `nix fmt`'s job); `just lint-worktree` invokes the bare `conformist`
            # with an explicit `--config-file <impure-config> --tree-root .`,
            # which collides with the wrapper's baked-in flags (mutually
            # exclusive). Mirrors crap/tommy's devShell (cutting-garden's flake.nix
            # documents the same collision explicitly). `nix fmt` still runs the
            # wrapper via the `formatter` flake output below.
            conformist.packages.${system}.default
            # conformist-pre-commit / conformist-repair: the config-specific,
            # toolchain-hermetic hook commands (FDR 0019) the sweatfile names.
            conformistEval.config.build.preCommit
            conformistEval.config.build.repair
            pkgs.nixfmt
            pkgs.shfmt
            pkgs.shellcheck
            pkgs.statix
            pkgs.deadnix
            # tommy codegen tool, from the same flake input that backs the
            # bridged tommy library — so `go generate ./internal/sweatfile`
            # (//go:generate tommy generate) targets a matching cst API.
            tommy.packages.${system}.default
            # conformist tommy-codegen repair driver ([linter.tommy-codegen]
            # in conformistTommyModule above); so `just fmt` regenerates the
            # codec.
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
