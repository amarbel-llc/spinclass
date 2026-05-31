# dodder-consumer example

A standalone consumer flake that builds a **dodder-pinned spinclass** and an
end-to-end harness that exercises the live flow from FDR 0008
(`docs/features/0008-per-worktree-dodder-repository.md`).

It is intentionally separate from the parent repo: its own inputs and
`flake.lock`, not wired into spinclass's `checks`/`packages`.

## What it demonstrates

```nix
packages.default = spinclass.lib.${system}.mkSpinclass {
  dodder = dodder.packages.${system}.default;
  madder = dodder.packages.${system}.madder-bin;   # see note below
  direnv = pkgs.direnv;
};
```

`mkSpinclass` burns the three binary paths into the spinclass binary at link
time. With dodder pinned, `sc start` inits a per-worktree madder `.default`
store and a dodder repository layered over it, signs the repo with your
pivy-agent key, and auto-registers dodder's MCP server in the worktree's
`.mcp.json`.

**Why `madder` comes from `dodder...madder-bin`:** dodder re-exports the
exact madder it embeds. Pinning madder from there guarantees the madder that
*creates* the `.default` store and the dodder that *reuses* it are a
version-matched pair — sidestepping the FDR 0008 caveat that store reuse was
only verified for one binary pair. To pin an independent madder instead, add
a `madder.url = "github:amarbel-llc/madder"` input and use
`madder.packages.${system}.default`.

## Running the e2e

```sh
nix build              # builds the dodder-pinned spinclass
./e2e.sh               # builds (if needed) + drives sc start + asserts
# or, from the spinclass repo root:
just explore-dodder-e2e
```

**pivy-agent must be unlocked.** dodder signs the new repo with your agent
key; a locked/empty agent makes `dodder init` hard-fail (by design). The
harness runs outside the nix sandbox precisely because the sandbox has no
agent — it cannot be a `nix flake check`.

The harness creates a throwaway git repo under an isolated `HOME`, runs
`sc start` with a non-interactive `[session-entry].start = ["true"]`
entrypoint, and asserts: the `.dodder` repo (`config-seed`), the reused
`.madder` `.default` store, `.dodder/` + `.madder/` git-excludes,
`Bash(dodder:*)` + `Bash(madder:*)` claude-allow rules, the `dodder`/`madder`
shim symlinks, and the `dodder` MCP server in `.mcp.json`.

## The clown circus (`packages.circus`)

`packages.circus` builds a launchable **clown** binary that bundles the
dodder-pinned spinclass as a plugin, modeled after `~/eng/lib/circus.nix`:

```nix
packages.circus = clown.lib.${system}.mkCircus {
  plugins = [
    { flake = { packages.${system}.default = spinclassPinned; rev = …; };
      dirs = [ "share/purse-first/spinclass" ]; }
    { flake = { packages.${system}.default = dodder…dodder-clown-plugin; rev = …; };
      dirs = [ "share/purse-first/dodder" ]; }
    { flake = { packages.${system}.default = madder…madder-clown-plugin; rev = …; };
      dirs = [ "share/purse-first/madder" ]; }
  ];
  enableTentClaude = false;
};
```

```sh
nix build .#circus            # build the circus (proves composition)
# or, from the spinclass repo root:
just explore-dodder-circus
```

Notes on the circus:

- **Focused plugin set.** Just spinclass + dodder + madder. eng's real
  circus also bundles `moxy`, `eng`, and `caldav`; add them as more
  `plugins` records the same way.
- **`madder` appears twice, deliberately.** The spinclass *binary* pin uses
  dodder's matched `madder-bin` (for store reuse); the *clown plugin* comes
  from the `madder` input's `madder-clown-plugin`. Different roles.
- **`enableTentClaude = false`** keeps the example off the aarch64-linux
  tent closure. eng defaults to `isLinux`.
- Building the circus proves composition (plugin dirs resolve, the clown
  wrapper links). Actually *launching* clown (`nix run .#circus`) needs a
  Claude auth/session and is out of scope for this example.

## Notes

- `spinclass.url` points at `github:amarbel-llc/spinclass` (master carries
  the FDR 0008 integration). To test **local** spinclass changes, swap it
  for `spinclass.url = "path:../..";`.
- If the store-reuse assertion ever fails, that is the FDR 0008 version-pair
  signal for whatever `flake.lock` resolved — investigate the
  madder/dodder pair, don't ignore it.
