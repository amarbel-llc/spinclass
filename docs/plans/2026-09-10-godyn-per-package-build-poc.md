# godyn per-package build — POC results & promotion plan

**Status:** promoted. All three blocking igloo fixes shipped to igloo master
(`11b98425`), the igloo input is bumped to it, and `.#spinclass-native` is a
supported **opt-in** build (not the default, not in `checks`, does not gate the
merge). Re-verified against igloo master: builds green, runs on the REAL
`templates/*.md.tmpl` embed pattern (no probe), incremental leaf edit ~3.7 s
(godyn) vs ~34 s (buildGoApplication). Remains on `fast-aspen`; whether to merge
to `master` is the operator's call.

**Owner split:** the spinclass consumer side is this repo's lane
(`spinclass/fast-aspen`); the godyn backend and the three (now-shipped) fixes are
igloo's (`igloo/vivid-fir`). Cross-session coordination happened over chat.

**Promotion applied (2026-09-10):** igloo bumped `acd1c26 → 11b98425`; the
`debug-godyn-graph` / `-drift` recipes now use `godyn-gen -gomod <mergedGoMod>`
(igloo#67, replacing the manual go.mod swap); `flake.nix` collapsed to a single
`buildGoAuto { … goFlakeInputs = goFlakeInputs; }` (igloo#69, dropping the
hand-built `godynBridges` and the `bgaArgs` goFlakeInputs); the graph was
regenerated and now carries the per-pattern `embedPatternFiles` mapping (igloo#68)
so the real embed pattern builds without a source change. The three "when each
lands" sub-sections below are retained as the record of what changed.

## What this POC answers

Can spinclass build under igloo's per-package `godyn` backend
(`buildGoAuto`, godyn(7))? **Yes — it builds and runs**, once the two build-side
gaps below are worked around, with a large incremental-edit win.

Measured on x86_64-linux:

- **Builds green:** 153 per-package content-addressed derivations compile and
  link `$out/bin/spinclass`.
- **Runs:** `spinclass version` prints the correct version/commit burn-in
  (`0.1.42+<commit>`) — after clearing the embed gap (#68 below).
- **Incremental edit (leaf package, `internal/run`):** godyn **~3.6 s** vs
  `buildGoApplication` **~38.6 s** — roughly **10.6×**. godyn recompiles only
  the changed cone (the edited package + `cmd/spinclass` relink); bga rebuilds
  the whole module. Confirms godyn(7)'s merkle-delta claim.

## What is committed now (the parked wiring)

- **`flake.nix`**
  - `godynSystem = system == "x86_64-linux"` — the only platform the committed
    single-platform graph is valid for (godyn(7) LIMITATIONS, igloo#33).
  - `godynBridges = lib.mapAttrs (_: v: v.src + lib.optionalString (v ? subPath) "/${v.subPath}") goFlakeInputs`
    — maps spinclass's four flake-input-go_mod bridges (tommy, crap/go-crap,
    ringmaster, purse-first/libs/dewey) onto godyn's `bridges` (approach 2,
    source composition). The `subPath` entries deep-reference into the producer
    go-pkgs tree because godyn's `bridges` assumes bridge-root == module-root and
    has no `subPath` knob; the deep-reference composes correctly with godyn's
    `${bridge}/<importPath − modpath>` concatenation. Derived straight from
    `gomod.nix` so the two stay in lockstep.
  - `spinclass-native = pkgs.buildGoAuto { strategy = "dev"; … }`, exposed as
    `.#spinclass-native` only under `godynSystem`. Bare binary (no forge pins /
    man pages), parity with conformist-native. NOT in `checks` — it does not gate
    the merge.
  - `bgaArgs = { inherit goFlakeInputs; … }` — see #69 below.
- **`godyn-graph.json`** — the committed graph from `godyn-gen`, 154 packages,
  portable (module-root-relative dirs + file basenames; zero `/nix/store`
  references). Carries spinclass's REAL embed pattern `templates/*.md.tmpl`, so
  it documents gap #68 rather than hiding it. Excluded from conformist
  formatting (`conformist.nix`).
- **`justfile`** (`debug-godyn-*` group): `debug-godyn-graph` (+ `-drift`)
  regenerate the graph under the merged-go.mod interim (#67); `debug-godyn-build`
  builds `.#spinclass-native`; `debug-godyn-bench` runs the godyn-vs-bga
  incremental timing. `godyn-gen` is on the devShell.

## The three blocking igloo issues, and what changes when each lands

### igloo#67 — gen-under-bridge (`-gomod` flag)

`godyn-gen` runs `go list`, which cannot resolve spinclass's flake-input-go_mod
bridges ambiently: the go.mod `require`s are vestigial (e.g. `dewey v0.5.0`
predates `pkgs/mesa`) and the real versions are bridged only inside the nix
sandbox. **Interim (working):** `debug-godyn-graph` swaps in
`buildGoApplication`'s merged go.mod (`passthru.mergedGoMod`, whose `replace`s
point at the exact go-pkgs store paths `godynBridges` uses) for the duration of
the `go list`, then restores go.mod.

**When #67 ships** a `godyn-gen -gomod <merged-go.mod>` flag: replace the manual
go.mod swap in `debug-godyn-graph` / `debug-godyn-graph-drift` with the flag.
**Acceptance test (igloo's):** the recipe output must be byte-identical with and
without the manual swap.

### igloo#68 — embed mid-glob matcher (build-green / runtime-panic)

`build-godyn-module.nix`'s embedcfg `matchPat` only handles literal patterns and
*trailing*-`*` suffix globs. `internal/sysprompt`'s `//go:embed
templates/*.md.tmpl` (a mid-glob with a compound suffix) resolves to an empty
`Patterns` set → the runtime `embed.FS` is empty → `template.Must(ParseFS(…))`
panics at init. `godyn-gen` records the embed files correctly; the gap is purely
the build-side glob→files translation. (`cmd/spinclass`'s `//go:embed doc/*` is a
trailing-`*` glob and already works.) igloo#68 proposes resolving pattern→files
at gen time via Go's `path.Match`, plus a build-time error on any zero-match
pattern (so it can never again ship a binary that panics at init).

**When #68 ships:** drop any embed probe; re-verify the REAL `templates/*.md.tmpl`
pattern builds and runs (`just debug-godyn-build` then `spinclass version`). No
spinclass source change is wanted — the fix is in godyn.

### igloo#69 — first-class `goFlakeInputs` on `buildGoAuto`

`buildGoAuto`'s `bgaArgs` path does not thread `goFlakeInputs`, so a bridged
repo's `passthru.bga` fails (`cannot find .../dewey/pkgs/mesa: -mod=vendor`).
Worked around here by `bgaArgs = { inherit goFlakeInputs; }`.

**When #69 ships** a first-class `goFlakeInputs` arg that routes to both backends
(bga's `goFlakeInputs` and the `bridges` derivation): drop the `bgaArgs`
`goFlakeInputs` line and pass the first-class arg instead.

## Promotion checklist (once #67/#68/#69 land)

1. Re-run `just debug-godyn-graph` (under the shipped `-gomod` flag) and commit
   the regenerated graph; confirm `debug-godyn-graph-drift` is clean.
2. `just debug-godyn-build` && `spinclass version` — confirm the real embed
   pattern builds and runs.
3. Apply the #67/#69 wiring simplifications above; keep the #68 verification.
4. Keep `.#spinclass-native` an **opt-in** package (not the default), gated to
   `godynSystem`, mirroring conformist's `.#conformist-native`. Do NOT add it to
   `checks` / the merge gate: it is single-platform and content-addressed
   (needs the `ca-derivations` experimental feature), and the default
   `buildGoApplication` build stays the release/CI backend (cold builds favor
   bga; godyn wins the incremental dev loop).
5. Decide the graph-regen cadence guard. The graph must be regenerated whenever
   imports/deps/embeds change OR a bridged producer's flake.lock rev moves.
   `debug-godyn-graph-drift` is the manual guard; it needs a network go.mod
   materialization, so it stays a debug-group check, not a merge-gate lane
   (same posture as conformist's opt-in drift check).
6. Optional (deferred POC deliverable #3): per-package `go test` via
   `testGraphFile` — add a `debug-godyn-test-graph` recipe
   (`godyn-gen -tests . godyn-test-graph.json`, under the same merged-go.mod
   materialization) and wire `spinclass-native.passthru.checkAll` into a
   debug-group check. Note the godyn test limitations (igloo#32): cgo/asm tests,
   test-only third-party deps, `-race`, and `_test.go`-only embeds are
   unsupported; the checkPhase needs `ringmaster` on PATH.

## References

- godyn(7) (igloo `pkgs/build-support/godyn/godyn.7.scd`); the
  `conformist-native` template in conformist's `flake.nix`.
- igloo#67 (gen-under-bridge), igloo#68 (embed mid-glob), igloo#69
  (buildGoAuto goFlakeInputs); igloo#33 (per-system graphs), igloo#32 (test
  limitations).
- The flake-input-go_mod bridge protocol: `gomod.nix` + igloo RFC 0001.
