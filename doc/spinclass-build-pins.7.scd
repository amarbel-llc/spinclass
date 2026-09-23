spinclass-build-pins(7) "spinclass" "Spinclass Reference"

# NAME

spinclass-build-pins - build-time-pinned binaries (madder, direnv, dodder)

# DESCRIPTION

Spinclass exposes a Nix flake function *lib.mkSpinclass* that produces a
spinclass binary with absolute _/nix/store_ paths burned in for selected
runtime tools. At link time these paths are written into package-level Go
variables via *-X* _ldflags_, and the binary picks them up at runtime without
consulting *PATH*.

The default *packages.default* output of the spinclass flake is
*mkSpinclass {}* — no inputs supplied, all pinned paths empty, every
integration dormant. A consumer flake that wants the integrations active calls
*mkSpinclass* with the desired derivations:

```
spinclass.lib.${system}.mkSpinclass {
  madder = madder.packages.${system}.default;
  direnv = pkgs.direnv;
  dodder = dodder.packages.${system}.default;
}
```

# PINS

## madder

When the *madder* input is supplied, *sc start* (and any other path that
creates a worktree) initialises a per-worktree blob store at
_<worktree>/.madder/local/share/blob\_stores/default/_ on first session
creation. The init command run is

	*madder init -encryption none .default*

with *MADDER\_CEILING\_DIRECTORIES=<worktree>* set on the invocation only (not
exported into the broader session) so madder will not walk up into a parent
repo's _.madder/_ directory during store discovery.

The init step is guarded by an existence check on _blob\_store-config_ —
rerunning *sc start* in a worktree that already has a store is a no-op.

When the integration is active, two additional rules are applied at worktree
creation:

- _.madder/_ is appended to the worktree's _.git/info/exclude_.
- *Bash(madder:\*)* is appended to the worktree's Claude Code allow list in
  _.claude/settings.local.json_.

When the *madder* input is null (the default *packages.default*), none of the
above happens. The feature is fully dormant; no _.madder/_ directory is created
and no madder-related allow rule is added.

When madder is pinned, the *sc merge* / *merge-this-session* and *sc check* /
*check-this-session* flows emit a *compact response shape:* the pre-merge hook
stage is a single verdict line in the plain rendering of the ndjson-crap
stream; on failure its diagnostic carries *command*, *exit\_code*, *elapsed*, a
15-line *tail* of stdout+stderr, and a *resource\_link* URI of the form
*madder://blobs/<digest>* that points at the full hook output stored in the
per-worktree blob store (surfaced to MCP clients as a resource\_link content
block; it only needs to be followed on failure). Without madder pinned, no blob
is stored and no resource\_link is emitted; the hook's lines still stream as
live output records (the viewport's rolling tail), and a failure falls back to
the inline tail.

## direnv

When the *direnv* input is supplied, spinclass invokes the pinned binary
directly instead of consulting *PATH*. When the input is null, spinclass falls
back to *exec.LookPath*; if no *direnv* is found, the _.envrc_ generation step
is skipped (this matches the pre-pin behaviour).

## dodder

When the *dodder* input is supplied, *sc start* (and any other path that
creates a worktree) additionally initialises a per-worktree dodder repository
at _<worktree>/.dodder/_ on first session creation, layered over the
per-worktree madder blob store: when the madder *.default* store already exists
it is adopted via *-blob\_store-id .default*; otherwise dodder's embedded madder
creates a default store. The init command run is

	*dodder init -encryption none -repo\_id . -private\_key <key> [-blob\_store-id .default] <repo-id>*

with both *DODDER\_CEILING\_DIRECTORIES=<worktree>* and
*MADDER\_CEILING\_DIRECTORIES=<worktree>* set on the invocation only (not
exported into the broader session) so discovery will not walk up into a parent
repo's _.dodder/_ or _.madder/_ directories. The signing key is resolved from
the user's ssh agent via *dodder info-ssh\_agent* (first key listed); a locked
or empty agent is a hard error so no divergent per-worktree signing identity is
silently generated.

The init step is guarded by an existence check on
_.dodder/local/share/config-seed_ — rerunning *sc start* in a worktree that
already has a repository is a no-op.

When the integration is active, two additional rules are applied at worktree
creation:

- _.dodder/_ is appended to the worktree's _.git/info/exclude_.
- *Bash(dodder:\*)* is appended to the worktree's Claude Code allow list in
  _.claude/settings.local.json_.

When the *dodder* input is null (the default *packages.default*), none of the
above happens. The feature is fully dormant; no _.dodder/_ directory is created
and no dodder-related allow rule is added.

# SEE ALSO

*spinclass*(1), *spinclass-sweatfile*(5), *madder*(1), *direnv*(1), *dodder*(1)
