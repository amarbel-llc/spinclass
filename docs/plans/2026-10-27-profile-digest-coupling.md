# Confined spawns after a clown bump: the profile digest coupling

Date: 2026-10-27. Status: diagnosis from code plus one observed failure; the
bump steps are a draft for the operator (lock bumps are the operator's). Labels:
R read in code, V observed (by the coordinator, in a spawn log), I inferred.

## Observation (V, coordinator)

circus/fond-plum, 2026-10-26 22:10Z, spinclass fae0c6d and clown ffceefcb6403
installed: `clown: profile "canary-spinclass" resolves to digest 0758ba88..., not
the CLOWN_PROFILE_DIGEST d4c9d193... the launcher pinned; refusing`, then
spinclass: "confined worker ... was launched but its principal is not bound: no
agent unit record".

## Cause (R)

- The pin comes from the clown profile LIBRARY linked into the spinclass binary,
  not from the installed clown: `clownResolver.Resolve` calls
  `profilelookup.Resolve` (internal/foxtrot/spawn/clownresolver.go:56) and stores
  `res.DigestSHA256` as `ResolvedProfile.DigestHex` (:104); the spawn hands it to the
  worker as `CLOWN_PROFILE_DIGEST` via `ProfileLaunchEnv(profile.DigestHex, ...)`
  (spawn.go:619, clownresolver.go:36).
- The launched (installed) clown resolves the profile itself and refuses when its
  digest differs from the pin (cmd/clown/profilegate.go:195).
- The linked library is whatever clown rev spinclass's `flake.lock` holds
  (`clown` input, bridged through go.nix; `LinkedClownRev`). It is older than the
  installed clown, whose `canary-spinclass` changed (the `Agent` disallow,
  clown#331), so the two digests differ. The confined-profile rules
  (`requireConfinedProfile`) run on the OLD definition and pass.
- I found no other cause in code. The nix.conf seed and the frontend moxin plugin
  live in the clown frontend after the pin check; the observed refusal is the pin
  check itself. (I did not reproduce it.)

### Confirmed (V, 2026-10-27, in a spinclass worktree)

`go test` of the linked library (`TestLinkedClownHasTheCanaryBuiltins -test.v`,
which now logs digests) prints `canary-spinclass digest d4c9d193ce15...`, exactly
the pin in the refusal. spinclass master's `flake.lock` holds clown
`d3645fac9ff1` (the `worker` builtin resolves there and passes the confined rules);
the installed clown is `ffceefc`, which adds `3605b87` (clown#331, no Agent tool
where moxy is dropped) after it. So the library is one clown change behind, and
that change altered the profile digests. (flic's installed spinclass fae0c6d is
older still: its lock predates even `worker`.)

## Which profiles are affected

Any builtin profile whose canonical definition (and so digest) differs between the
linked library and the installed clown. Known: `canary-spinclass`. `worker` is
newer still: the linked library does not have it at all, so spinclass refuses
earlier with "profile not found" (the new skip test
`TestLinkedClownWorkerPassesTheConfinedProfileRules` shows it). `canary` and
`canary-grit` are affected only if their definition changed; unknown (I). The
spawn-time error prints the installed digest; compare it with
`just debug-go-test internal/foxtrot/spawn TestLinkedClownHasTheCanaryBuiltins -test.v`,
which now logs each linked builtin's digest.

## Related hazard: restarts of already-running confined sessions

A running confined session carries the digest it launched with. After a clown
bump changes its profile, clown's restart relaunch re-checks the profile
(`restart_refusal`, kind `profile-gate`) and cancels the restart; the old session
keeps running. So the weekly restart sweep (weekly-restart-runbook.md) will see
"restart CANCELLED by clown (profile-gate)" on such sessions until an operator
re-pins; the holder wake now says so (5425ebf).

## Landing order (draft; operator owns lock bumps)

1. Operator, in a spinclass worktree: `nix flake update clown` (the input is
   `https://code.linenisgreat.com/clown/archive/master.tar.gz`, so this moves the
   locked rev to clown master). Nothing else in the lock should move; check
   `git diff flake.lock`.
2. Gate: the merge's pre-merge hook (`just`). It builds against the new library;
   `TestLinkedClownWorkerPassesTheConfinedProfileRules` stops skipping and checks
   `worker`; a clown API change in `pkg/profilelookup` would fail the build here,
   not on a host.
3. Merge spinclass with `targets=[]`.
4. Operator: bump spinclass in circus (the paved relock recipe) and deploy flic.
5. Restart the sessions that SPAWN confined workers (the coordinator): a spawn runs
   in the caller's own `spinclass serve`, which keeps the old linked library until
   that session restarts (`session-health`/`version` shows STALE). The `sc spawn`
   CLI uses the PATH binary, which is new right after step 4.
6. Existing confined sessions pinned to the old digest need an operator re-pin
   before their restart succeeds (see above).

## Guard against a repeat

The coupling is: any clown change to a builtin profile's definition needs the
spinclass library bump before confined spawns with it work. A cheap check after
any clown deploy: spawn nothing, run the test above with `-test.v` and compare its
logged digests with the installed clown's (`clown profile` output, or the digest in
the refusal text).
