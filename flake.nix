{
  description = "Spinclass: shell-agnostic git worktree session manager";

  inputs = {
    # Fork: source of the buildGoApplication / buildGoRace / godyn
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
    nixpkgs-master.url = "github:NixOS/nixpkgs/f13ff45afd1bb73e640eaa08a7066dbed07e3238";

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
        # truth that backs the go.nix bridge + the codegen binary) so the
        # graph resolves exactly one tommy rev.
        tommy.follows = "tommy";
        conformist.follows = "conformist";
        crap.follows = "crap";
      };
    };
    madder.inputs.hyphence.inputs.langlang.follows = "papi/langlang";
    madder.inputs.purse-first.follows = "purse-first";

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
    # viewport presenter), bridged via go.nix flakeInputs. Consumed
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
    # the Go module is bridged via go.nix flakeInputs and the same
    # input's binary backs checks.tommy-codegen and `just build-tommy-codegen`.
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
    ringmaster.inputs.purse-first.follows = "purse-first";
    ringmaster.inputs.conformist.follows = "conformist";

    # purse-first: source of the mesa List-Table renderer (pkgs/mesa),
    # bridged via go.nix flakeInputs and used by `sc list`'s
    # pretty/plain rendering (#185). Direct input so we get a version
    # that includes pkgs/mesa (ringmaster's transitive pin predates it,
    # hence the follows override above). Mirrors clown's flake.nix.
    purse-first = {
      url = "https://code.linenisgreat.com/purse-first/archive/master.tar.gz";
      inputs = {
        igloo.follows = "igloo";
        nixpkgs-master.follows = "nixpkgs-master";
        utils.follows = "utils";
        conformist.follows = "conformist";
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
    papi.inputs.langlang.follows = "madder/langlang";
  };

  outputs =
    inputs@{
      self,
      igloo,
      nixpkgs-master,
      utils,
      bats,
      madder,
      conformist,
      tommy,
      papi,
      ringmaster,
      ...
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

        # Where the godyn (native, per-package) backend can build: x86_64-linux
        # only. The graph is now DERIVED at eval time per-system (igloo#72 / FDR
        # 0008 — no committed graph), but godyn's per-package build itself is
        # only validated on x86_64-linux (godyn(7) LIMITATIONS, igloo#33) and the
        # cross-system eval IFD can't run from another host (igloo#75). Gates
        # `packages.default` to godyn here (bga elsewhere) and whether
        # `.#spinclass-native` is exposed.
        godynSystem = system == "x86_64-linux";

        # tommy fmt (*.toml) has no registry program and needs the `tommy`
        # flake input, so it is inlined here rather than in ./conformist.nix (a
        # standalone module file can't see flake inputs). getExe' with an
        # explicit binary name: tommy lacks meta.mainProgram. No tommy-codegen
        # repair linter: it runs `tommy generate` in the checkout, which needs
        # a go.mod. Drift is checks.tommy-codegen; regen is
        # `just build-tommy-codegen`.
        conformistTommyModule = _: {
          settings.formatter.tommy = {
            command = pkgs.lib.getExe' tommy.packages.${system}.default "tommy";
            options = [ "fmt" ];
            includes = [ "*.toml" ];
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
        # git-default-branch, sweatfile, agents-md — presets.eng-impure).
        # These need a live .git, so they run against the working tree via
        # `just lint-worktree` (crap/papi/tommy shape), not the sandboxed check.
        # Go lint runs in the pure `checks.lint` below (spinclass#294).
        conformistImpureEval = conformist.lib.evalModule pkgs {
          imports = [
            conformist.lib.presets.eng-impure
            ./conformist-impure.nix
          ];
          package = conformist.packages.${system}.default;
          projectRootFile = "flake.nix";
        };

        # The BARE spinclass binary on igloo's per-package godyn backend
        # (buildGoAuto strategy = "dev"): no artifacts or forge pins. The dev
        # inner-loop and godyn-vs-bga microbench target (`.#spinclass-native`),
        # gated to godynSystem; not in `checks` (the full godyn `default` is).
        #
        # `commit` is passed explicitly (src = ./. is a plain path, no .rev); the
        # native backend uses igloo's callPackage `pkgs.go`, so bgaArgs pins
        # pkgs-master.go_1_26 for passthru.bga parity with the default build.
        # Bare binary (no forge pins / man pages), parity with conformist-native.
        # Promoted from the parked POC once igloo#67/#68/#69 shipped — see
        # docs/plans/2026-09-10-godyn-per-package-build-poc.md.
        spinclass-native = pkgs.buildGoAuto {
          pname = "spinclass";
          src = ./.;
          # go.nix (igloo FDR 0008) replaces go.mod/gomod2nix.toml/goFlakeInputs;
          # its flakeInputs name entries of `inputs`.
          manifest = ./go.nix;
          strategy = "dev";
          inherit inputs;
          # Same test-deps graph as mkSpinclass, so both share one derivation.
          tests = true;
          nativeArgs.commit = spinclassCommit;
          bgaArgs = {
            commit = spinclassCommit;
            subPackages = [ "cmd/spinclass" ];
            go = pkgs-master.go_1_26;
            GOTOOLCHAIN = "local";
            doCheck = false;
          };
        };

        # mkSpinclass builds the FULL spinclass package (binary + generated
        # artifacts + plugin manifests + `sc` symlink) on either backend,
        # selected by `strategy` via buildGoAuto: "ci"/"bga" =
        # buildGoApplication (the default here), "dev"/"native" = godyn
        # (per-package, incremental). `strategy` defaults to bga so
        # `lib.mkSpinclass` consumers and the CI/test/bats lanes keep the proven
        # buildGoApplication backend; `packages.default` (below) selects godyn on
        # x86_64-linux. buildGoAuto forwards ldflags (forge pins), the go.nix
        # manifest (RFC 0001 bridges on both backends), and postInstall to
        # BOTH backends, so one declaration drives native and bga; version is
        # auto-read from version.env, commit is threaded per-backend below.
        #
        # Pins (all fall back to PATH when null): `madder` activates the
        # per-worktree blob-store flow; `direnv` the devshell exec; `dodder` the
        # per-worktree dodder repo (FDR 0008); `papi`/`gh` the dynamic
        # system-prompt repository line (internal/repoinfo).
        mkSpinclass =
          {
            strategy ? "ci",
            madder ? null,
            direnv ? null,
            dodder ? null,
            papi ? null,
            gh ? null,
          }:
          pkgs.buildGoAuto {
            pname = "spinclass";
            src = ./.;
            # No graphFile: from the go.nix manifest, buildGodynModule
            # DERIVES the package graph at eval time (godyn-gen in the bga
            # sandbox — igloo#72 / FDR 0008), so nothing is committed to
            # regenerate. version is auto-read from version.env by both backends
            # (an explicit `version` attr would override that); commit is
            # threaded per-backend below since src = ./. has no .rev.
            manifest = ./go.nix;
            inherit strategy inputs;

            # On PATH in every escape-hatch run (godyn-go), for go:generate.
            goRunInputs = [ tommy.packages.${system}.default ];

            # On PATH for tests on both backends: internal/clown's contract
            # tests drive the real ringmaster CLI (#253).
            nativeCheckInputs = [
              pkgs.git
              ringmaster.packages.${system}.ringmaster
            ];

            # Forge/tool pins as `-X main.*Bin` ldflags — buildGoAuto forwards
            # them to whichever backend builds. papi/gh drive the dynamic
            # system-prompt repo line (internal/repoinfo); madder/direnv/dodder
            # gate the per-worktree blob-store / dodder flows. All fall back to
            # PATH when unpinned.
            ldflags =
              (lib.optional (madder != null) "-X main.madderBin=${madder}/bin/madder")
              ++ (lib.optional (direnv != null) "-X main.direnvBin=${direnv}/bin/direnv")
              ++ (lib.optional (dodder != null) "-X main.dodderBin=${dodder}/bin/dodder")
              ++ (lib.optional (papi != null) "-X main.papiBin=${papi}/bin/papi")
              ++ (lib.optional (gh != null) "-X main.ghBin=${gh}/bin/gh");

            # godyn-only knobs (buildGodynModule).
            nativeArgs.commit = spinclassCommit;

            # Per-package go test graph derived at eval time (godyn, FDR 0008).
            tests = true;

            # buildGoApplication-only knobs: the `go test ./...` checkPhase for
            # non-godyn systems (godyn runs per-package tests as
            # checks.spinclass-tests). subPackages builds just cmd/spinclass while
            # the checkPhase tests every package (sandbox-incompatible tests
            # self-skip via NIX_BUILD_TOP, #65). GOTOOLCHAIN=local pins
            # pkgs-master.go_1_26 (no sandbox toolchain fetch).
            bgaArgs = {
              commit = spinclassCommit;
              subPackages = [ "cmd/spinclass" ];
              go = pkgs-master.go_1_26;
              GOTOOLCHAIN = "local";
              doCheck = true;
              checkPhase = ''
                runHook preCheck
                go test -p $NIX_BUILD_CORES ./...
                runHook postCheck
              '';
              meta = {
                description = "Shell-agnostic git worktree session manager";
                homepage = "https://code.linenisgreat.com/spinclass";
                license = pkgs.lib.licenses.mit;
              };
            };

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
        # binary so the forge lookup is deterministic; an unpinned build (e.g.
        # `.#spinclass-native`) falls back to PATH. gh comes from
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

        # The buildGoApplication build, named explicitly (the godyn-default
        # flip): the escape hatch, the release/CI backend, and the
        # non-x86_64-linux `default`. Full package + the `go test ./...`
        # checkPhase; off x86_64-linux it is `default` and backs `checks.spinclass`.
        spinclass-build_go_application = mkSpinclass forgePins;

        # The default `nix build`: godyn (per-package, incremental) on
        # x86_64-linux, buildGoApplication elsewhere — godyn's build is validated
        # only there (igloo#33; the graph derives per-system at eval time). Both
        # are the FULL package (binary + artifacts + plugin manifests + `sc`
        # symlink) with the papi/gh pins. See
        # docs/plans/2026-09-10-godyn-per-package-build-poc.md.
        spinclass-default =
          if godynSystem then
            mkSpinclass ({ strategy = "dev"; } // forgePins)
          else
            spinclass-build_go_application;

        batsLaneOutputs = {
          bats-default = mkBatsLane { };
          bats-race = mkBatsLane { base = spinclass-race; };
          bats-madder = mkBatsLane { base = spinclass-madder; };
        };
      in
      {
        packages = {
          # godyn on x86_64-linux, buildGoApplication elsewhere (see
          # spinclass-default above).
          default = spinclass-default;
          # The buildGoApplication build under an explicit name: the escape
          # hatch from the godyn default (`nix build .#spinclass-build_go_application`).
          inherit spinclass-build_go_application;
          inherit spinclass-race;
          # The generated impure-lane config (git-state eng-convention checks),
          # consumed by `just lint-worktree` to run `conformist check` against
          # the working tree where .git is available. See conformistImpureEval
          # above. (golangci-lint moved to the pure `checks.lint` — spinclass#294.)
          conformist-impure-config = conformistImpureEval.config.build.configFile;
        }
        // batsLaneOutputs
        # The BARE godyn binary (no artifacts) for the fast dev inner loop and
        # the backend microbench — distinct from `default`, the FULL godyn
        # package. x86_64-linux only (godynSystem; godyn builds only there, igloo#33).
        // lib.optionalAttrs godynSystem { inherit spinclass-native; };

        # `nix flake check` exercises the unit suite plus every bats lane.
        # `spinclass` is the default build: godyn on x86_64-linux, else
        # buildGoApplication, whose checkPhase runs `go test ./...`.
        checks = {
          spinclass = spinclass-default;
          # Sandboxed read-only formatting + eng-convention-linter gate
          # (conformist check against a /nix/store snapshot of the tracked
          # tree). `just lint-fmt` builds this. See conformistEval above.
          formatting = conformistEval.config.build.check self;
          # Codec drift guard (#159, igloo FDR 0008): `go generate` in the
          # vendored module tree, failing on any diff. Backend-independent.
          # `just verify-tommy-codegen` builds this.
          tommy-codegen = spinclass-default.passthru.codegenCheck {
            command = "go generate ./internal/sweatfile/";
            nativeBuildInputs = [ tommy.packages.${system}.default ];
          };
        }
        // batsLaneOutputs
        # Pure, sandboxed Go lint (`just lint-golangci` builds `lint`): it can't
        # replay stale `.merge-*` cache paths whose suppressions fail open
        # (spinclass#294).
        // (
          if godynSystem then
            {
              # godyn lanes from go.nix: godyn-lint (vet + staticcheck defaults,
              # //nolint honored), per-package tests, vet.
              lint = spinclass-default.passthru.lintAll;
              spinclass-tests = spinclass-default.passthru.checkAll;
              vet = spinclass-default.passthru.vetAll;
            }
          else
            {
              # golangci-lint (.golangci.yml) via igloo's buildGoLint.
              lint = pkgs.buildGoLint {
                base = spinclass-build_go_application;
                inherit (pkgs-master) golangci-lint;
                warmCache = true;
              };
            }
        );

        # mkSpinclass = { strategy ? "ci", madder ? null, direnv ? null, ... }: ...
        # Consumer flakes call this to produce a full spinclass package with
        # absolute /nix/store paths burned in. `strategy` defaults to
        # buildGoApplication, so existing pins-only callers are unaffected by the
        # godyn-default flip.
        lib.mkSpinclass = mkSpinclass;

        # `nix fmt` runs the module-generated conformist wrapper (see
        # conformistEval above).
        formatter = conformistEval.config.build.wrapper;

        devShells.default = pkgs-master.mkShell {
          packages = [
            # No ambient `go`: dependencies live in go.nix (igloo FDR 0008);
            # go commands run through `godyn-go -- <cmd>`, tests through godyn.
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
            # ringmaster CLI on the devshell PATH so `ringmaster version
            # --protocol` (the ProtocolVersion serve-start gate, #26) and the
            # flock probe resolve in the dev-loop and bats, matching the
            # checkPhase pin (nativeCheckInputs). Runtime resolution is still
            # PATH-based (FDR 0010); this just makes the same binary present in
            # the devshell.
            ringmaster.packages.${system}.ringmaster
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
