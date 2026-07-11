# Spawn/Fork Model Selection Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** Let `spawn-session`/`sc spawn` and `fork-session`/`sc fork --brief` accept an optional `model` param (short alias: `sonnet`/`opus`/`haiku`/`fable`) that gets spliced into the launched worker's provider-args, per a configurable `[session-entry.model-flags]` provider→flag sweatfile map.

**Architecture:** A pure `internal/spawn/model.go` owns alias validation, provider detection (scanning `spawn-entry` for `--provider`), and argv splicing (insert `[flag, alias]` right after the entry's literal `"--"`). `internal/spawn.renderSpawn` calls it when a worker's sweatfile hierarchy is loaded, so provider/flag resolution has the real merged config. Cheap alias-format validation additionally runs eagerly in the `cmd/spinclass` layer (mirroring the existing `hello-timeout` pattern) so a typo'd alias fails before any worktree work on the `spawn` path. `model` threads through `Launch`/`LaunchExisting` as a new positional param, and through `spawnParams`/`forkDetachedParams` as a new JSON field shared by both the MCP tools and their CLI twins.

**Tech Stack:** Go; tommy-generated TOML codec (regen via `just gen-tommy` after the `SessionEntry` struct change); existing stub-harness sweatfile-fixture test pattern in `internal/spawn/spawn_test.go`.

**Rollback:** N/A — purely additive. Omitting `model` reproduces exactly today's behavior on every surface. See `docs/plans/2026-07-11-spawn-model-selection-design.md` for the full design rationale and out-of-scope items (no `--profile` provider detection, no per-provider alias-value translation, no `sc validate` integration).

**Build/test commands:** scoped tests via `hamster` MCP (`go-test` with `packages`/`run`); compile via `hamster go-build`. After the `internal/sweatfile` struct change you MUST run `just gen-tommy` (regenerates `internal/sweatfile/sweatfile_tommy.go` — see justfile `gen-tommy` recipe; if it needs manual patching, STOP and report per issue #50). Do NOT run full `just` mid-series — the merge gate covers it.

---

### Task 1: sweatfile — `[session-entry.model-flags]` knob

**Promotion criteria:** N/A — no old approach to retire.

**Files:**
- Modify: `internal/sweatfile/sweatfile.go` (`SessionEntry` struct ~line 23; accessors block ~line 439-460; `GetDefault` unaffected)
- Modify: `internal/sweatfile/hierarchy.go` (`MergeWith`, `[session-entry]` arm ~line 154-188)
- Regenerate: `internal/sweatfile/sweatfile_tommy.go` via `just gen-tommy`
- Test: `internal/sweatfile/sweatfile_test.go`

**Step 1: Write failing tests.** Add to `internal/sweatfile/sweatfile_test.go`, near the existing `SessionSpawnEntry`/`SessionEnv` tests (~line 1330-1500):

```go
func TestSessionModelFlagsAccessorDefault(t *testing.T) {
	for _, sf := range []Sweatfile{
		{},
		{SessionEntry: &SessionEntry{}},
	} {
		got := sf.SessionModelFlags()
		want := map[string]string{"claude": "--model"}
		if len(got) != len(want) || got["claude"] != want["claude"] {
			t.Errorf("SessionModelFlags() = %v, want built-in default %v", got, want)
		}
	}
}

func TestSessionModelFlagsAccessorConfigured(t *testing.T) {
	sf := Sweatfile{
		SessionEntry: &SessionEntry{
			ModelFlags: map[string]string{"codex": "--model-name"},
		},
	}
	got := sf.SessionModelFlags()
	if len(got) != 1 || got["codex"] != "--model-name" {
		t.Errorf("SessionModelFlags() = %v, want configured map verbatim (no claude default folded in)", got)
	}
}

func TestMergeSessionModelFlagsPerKey(t *testing.T) {
	// Mirrors TestMergeSessionEnvPerKeyOverride: child adds a key without
	// dropping the parent's, and overrides a colliding key.
	base := Sweatfile{
		SessionEntry: &SessionEntry{
			ModelFlags: map[string]string{"claude": "--model", "circus": "--old-flag"},
		},
	}
	override := Sweatfile{
		SessionEntry: &SessionEntry{
			ModelFlags: map[string]string{"circus": "--new-flag", "codex": "--model-name"},
		},
	}
	merged := base.MergeWith(override)
	want := map[string]string{"claude": "--model", "circus": "--new-flag", "codex": "--model-name"}
	got := merged.SessionEntry.ModelFlags
	if len(got) != len(want) {
		t.Fatalf("merged ModelFlags = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("merged ModelFlags[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestMergeSessionModelFlagsInherit(t *testing.T) {
	base := Sweatfile{
		SessionEntry: &SessionEntry{ModelFlags: map[string]string{"claude": "--model"}},
	}
	override := Sweatfile{SessionEntry: &SessionEntry{Start: []string{"zellij"}}}
	merged := base.MergeWith(override)
	if got := merged.SessionEntry.ModelFlags; len(got) != 1 || got["claude"] != "--model" {
		t.Errorf("expected inherited ModelFlags, got %v", got)
	}
}
```

**Step 2: Run tests to verify they fail.**

Run: `hamster go-test packages=./internal/sweatfile run=ModelFlags`
Expected: compile failure — `ModelFlags` field and `SessionModelFlags` method do not exist yet.

**Step 3: Add the struct field.** In `internal/sweatfile/sweatfile.go`, add to `SessionEntry` (after `SpawnWindow`, ~line 41):

```go
	// ModelFlags maps a clown provider name (as selected by spawn-entry's
	// --provider/--provider=) to the CLI flag that provider's binary uses to
	// select a model, e.g. {"claude": "--model"}. Consulted by
	// spawn.SpliceModelFlag when the spawn-session/fork-session `model` param
	// is set. Per-key merge like Env. Defaults to {"claude": "--model"} — the
	// only mapping verified against an actual provider CLI (forwarded through
	// clown's `--` provider-args boundary). See
	// docs/plans/2026-07-11-spawn-model-selection-design.md.
	ModelFlags map[string]string `toml:"model-flags"`
```

**Step 4: Add the accessor.** In `internal/sweatfile/sweatfile.go`, after `SessionSpawnWindow` (~line 460):

```go
// SessionModelFlags returns the configured [session-entry.model-flags]
// provider->flag map, falling back to the built-in default
// {"claude": "--model"} when unset or empty. See the spawn model-selection
// design doc.
func (sf Sweatfile) SessionModelFlags() map[string]string {
	if sf.SessionEntry != nil && len(sf.SessionEntry.ModelFlags) > 0 {
		return sf.SessionEntry.ModelFlags
	}
	return map[string]string{"claude": "--model"}
}
```

**Step 5: Wire the merge arm.** In `internal/sweatfile/hierarchy.go`, inside the `if other.SessionEntry != nil {` block (~line 155-188), add after the existing `Env` per-key-merge block (~line 175), mirroring it exactly:

```go
		// ModelFlags: per-key merge, same rationale as Env.
		if len(other.SessionEntry.ModelFlags) > 0 {
			if merged.SessionEntry.ModelFlags == nil {
				merged.SessionEntry.ModelFlags = make(map[string]string, len(other.SessionEntry.ModelFlags))
			}
			for k, v := range other.SessionEntry.ModelFlags {
				merged.SessionEntry.ModelFlags[k] = v
			}
		}
```

**Step 6: Regenerate the tommy codec.**

Run: `just gen-tommy` (or `mcp__plugin_moxy_moxy__just-us-agents run-recipe recipe=gen-tommy`)
Expected: `internal/sweatfile/sweatfile_tommy.go` regenerated with `model-flags` TOML decode support. If it needs manual patching, STOP and report (issue #50) rather than hand-editing the generated file.

**Step 7: Run tests to verify they pass.**

Run: `hamster go-test packages=./internal/sweatfile`
Expected: PASS, including the new tests and all pre-existing ones (`TestMergeSessionEnvInherit` etc. must be untouched).

**Step 8: Commit.**

```
feat(sweatfile): [session-entry.model-flags] provider->flag map

Per-key-merge map field (mirrors [session-entry].env), defaulting to
{"claude": "--model"} — the only provider->flag mapping verified
against an actual CLI. Part of the spawn/fork model-selection design
(docs/plans/2026-07-11-spawn-model-selection-design.md).

🤡 Generated with clown 0.3.18+5a832ac
https://github.com/amarbel-llc/clown/commit/5a832acfbea010956981409c4725e63b9bdd986c
```

---

### Task 2: `internal/spawn` — alias validation, provider detection, argv splice (pure functions)

**Promotion criteria:** N/A.

**Files:**
- Create: `internal/spawn/model.go`
- Test: `internal/spawn/model_test.go`

**Step 1: Write failing tests.** Create `internal/spawn/model_test.go`:

```go
package spawn

import "testing"

func TestValidateModelAliasKnown(t *testing.T) {
	for _, alias := range []string{"sonnet", "opus", "haiku", "fable"} {
		if err := ValidateModelAlias(alias); err != nil {
			t.Errorf("ValidateModelAlias(%q) = %v, want nil", alias, err)
		}
	}
}

func TestValidateModelAliasUnknown(t *testing.T) {
	if err := ValidateModelAlias("gpt5"); err == nil {
		t.Error("ValidateModelAlias(\"gpt5\") = nil, want error")
	}
}

func TestResolveProviderDefault(t *testing.T) {
	got := resolveProvider([]string{"clown", "--clown-attach=spawn", "--", "{prompt}"})
	if got != "claude" {
		t.Errorf("resolveProvider() = %q, want \"claude\" (default)", got)
	}
}

func TestResolveProviderExplicitSpaceForm(t *testing.T) {
	got := resolveProvider([]string{"clown", "--provider", "codex", "--", "{prompt}"})
	if got != "codex" {
		t.Errorf("resolveProvider() = %q, want \"codex\"", got)
	}
}

func TestResolveProviderExplicitEqualsForm(t *testing.T) {
	got := resolveProvider([]string{"clown", "--provider=codex", "--", "{prompt}"})
	if got != "codex" {
		t.Errorf("resolveProvider() = %q, want \"codex\"", got)
	}
}

func TestResolveProviderStopsAtSeparator(t *testing.T) {
	// --provider appearing AFTER "--" is a provider-arg, not a clown flag —
	// must not be mistaken for the clown-level provider selector.
	got := resolveProvider([]string{"clown", "--", "--provider", "not-a-clown-flag"})
	if got != "claude" {
		t.Errorf("resolveProvider() = %q, want \"claude\" (post-separator --provider ignored)", got)
	}
}

func TestSpliceModelFlagInsertsAfterSeparator(t *testing.T) {
	entry := []string{"clown", "--clown-attach=spawn", "--", "{prompt}"}
	flags := map[string]string{"claude": "--model"}
	got, err := SpliceModelFlag(entry, "opus", flags)
	if err != nil {
		t.Fatalf("SpliceModelFlag: %v", err)
	}
	want := []string{"clown", "--clown-attach=spawn", "--", "--model", "opus", "{prompt}"}
	if len(got) != len(want) {
		t.Fatalf("SpliceModelFlag() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SpliceModelFlag()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSpliceModelFlagCustomProvider(t *testing.T) {
	entry := []string{"clown", "--provider=codex", "--", "{prompt}"}
	flags := map[string]string{"claude": "--model", "codex": "--model-name"}
	got, err := SpliceModelFlag(entry, "opus", flags)
	if err != nil {
		t.Fatalf("SpliceModelFlag: %v", err)
	}
	want := []string{"clown", "--provider=codex", "--", "--model-name", "opus", "{prompt}"}
	if len(got) != len(want) || got[3] != want[3] || got[4] != want[4] {
		t.Errorf("SpliceModelFlag() = %v, want %v", got, want)
	}
}

func TestSpliceModelFlagNoSeparatorErrors(t *testing.T) {
	entry := []string{"my-harness", "{prompt}"}
	_, err := SpliceModelFlag(entry, "opus", map[string]string{"claude": "--model"})
	if err == nil {
		t.Fatal("SpliceModelFlag() = nil error, want error (no \"--\" in entry)")
	}
}

func TestSpliceModelFlagUnmappedProviderErrors(t *testing.T) {
	entry := []string{"clown", "--provider=circus", "--", "{prompt}"}
	_, err := SpliceModelFlag(entry, "opus", map[string]string{"claude": "--model"})
	if err == nil {
		t.Fatal("SpliceModelFlag() = nil error, want error (circus not in flags map)")
	}
}

func TestSpliceModelFlagDoesNotMutateInput(t *testing.T) {
	entry := []string{"clown", "--", "{prompt}"}
	orig := append([]string(nil), entry...)
	if _, err := SpliceModelFlag(entry, "opus", map[string]string{"claude": "--model"}); err != nil {
		t.Fatal(err)
	}
	for i := range orig {
		if entry[i] != orig[i] {
			t.Errorf("input entry mutated: entry[%d] = %q, want %q", i, entry[i], orig[i])
		}
	}
}
```

**Step 2: Run tests to verify they fail.**

Run: `hamster go-test packages=./internal/spawn run=Model`
Expected: compile failure — `ValidateModelAlias`, `resolveProvider`, `SpliceModelFlag` undefined.

**Step 3: Implement.** Create `internal/spawn/model.go`:

```go
package spawn

import (
	"fmt"
	"strings"
)

// KnownModelAliases is the fixed set of short model aliases accepted by the
// `model` param on spawn-session/fork-session (and sc spawn/sc fork
// --brief). Update as models are renamed or added — see the design doc's
// Tuning Levers (docs/plans/2026-07-11-spawn-model-selection-design.md).
var KnownModelAliases = []string{"sonnet", "opus", "haiku", "fable"}

// ValidateModelAlias returns an error unless alias is one of
// KnownModelAliases. Callers only invoke this when a model was actually
// requested (empty string means "no model requested" and is handled by the
// caller, not this function).
func ValidateModelAlias(alias string) error {
	for _, a := range KnownModelAliases {
		if alias == a {
			return nil
		}
	}
	return fmt.Errorf("unrecognized model %q (want one of: %s)", alias, strings.Join(KnownModelAliases, ", "))
}

// resolveProvider scans entry elements BEFORE the literal "--" separator for
// --provider <name> or --provider=<name> (clown's own flag grammar).
// Defaults to "claude" — clown's own default — when absent. A --provider
// occurring AFTER "--" is a provider-arg, not a clown flag, and is ignored.
func resolveProvider(entry []string) string {
	for i, e := range entry {
		if e == "--" {
			break
		}
		if e == "--provider" && i+1 < len(entry) {
			return entry[i+1]
		}
		if v, ok := strings.CutPrefix(e, "--provider="); ok {
			return v
		}
	}
	return "claude"
}

// SpliceModelFlag inserts the resolved provider's model flag and alias into
// entry immediately after the literal "--" provider-args separator (before
// {prompt}/{dir} substitution — SubstituteEntry runs on the result). Does
// not mutate entry. Hard errors — no silent fallback — when entry has no
// "--" element, or the resolved provider has no entry in flags.
func SpliceModelFlag(entry []string, alias string, flags map[string]string) ([]string, error) {
	idx := -1
	for i, e := range entry {
		if e == "--" {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil, fmt.Errorf(
			"model %q requested but this spawn-entry has no \"--\" provider-args separator to splice into: %v",
			alias, entry,
		)
	}
	provider := resolveProvider(entry)
	flag, ok := flags[provider]
	if !ok {
		return nil, fmt.Errorf(
			"model %q requested but provider %q has no [session-entry.model-flags] entry "+
				"(add e.g. model-flags.%s = \"--model\" to the sweatfile once the flag is verified)",
			alias, provider, provider,
		)
	}
	out := make([]string, 0, len(entry)+2)
	out = append(out, entry[:idx+1]...)
	out = append(out, flag, alias)
	out = append(out, entry[idx+1:]...)
	return out, nil
}
```

**Step 4: Run tests to verify they pass.**

Run: `hamster go-test packages=./internal/spawn run=Model`
Expected: PASS.

**Step 5: Commit.**

```
feat(spawn): model alias validation, provider detection, argv splice

Pure functions in internal/spawn/model.go: ValidateModelAlias (fixed
alias set), resolveProvider (scans spawn-entry for --provider before
the "--" separator, defaults "claude"), SpliceModelFlag (inserts
[flag, alias] right after "--", hard errors on no separator or an
unmapped provider). Not yet wired into Launch/LaunchExisting.

🤡 Generated with clown 0.3.18+5a832ac
https://github.com/amarbel-llc/clown/commit/5a832acfbea010956981409c4725e63b9bdd986c
```

---

### Task 3: wire `model` into `renderSpawn`/`Launch`/`LaunchExisting`

**Promotion criteria:** N/A.

**Files:**
- Modify: `internal/spawn/spawn.go` (`Launch` ~line 44, `LaunchExisting` ~line 78, `renderSpawn` ~line 91)
- Test: `internal/spawn/spawn_test.go` (6 existing `Launch(` call sites need a new positional arg; see Step 1)

**Step 1: Update existing call sites first (mechanical, not a behavior change).** In `internal/spawn/spawn_test.go`, every `Launch(home, repoPath, driverKey, brief, desc, deadline)` call (lines ~111, 145, 163, 268, 339, 390) becomes `Launch(home, repoPath, driverKey, brief, desc, "", deadline)` — insert an empty-string `model` arg between `desc` and `deadline`. Do this now so Step 2's build failure is ONLY about the new tests, not these pre-existing call sites.

**Step 2: Write the new failing tests.** Add to `internal/spawn/spawn_test.go`:

```go
// modelSpawnSweatfile's spawn-entry mirrors the real clown default shape
// (has a literal "--" provider-args separator) so a model param has
// somewhere to splice into, and records its full argv like
// happySweatfile.
const modelSpawnSweatfile = `[session-entry]
spawn-entry = ["sh", "-c", 'printf "%s\n" "$@" > "$PWD/argv.txt"; touch "$PWD/launched"', "sh", "--", "{prompt}"]
`

func TestLaunchSplicesModelFlag(t *testing.T) {
	home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
	driverKey := "driver/test-session"
	stop := helloAfterLaunch(t, home, driverKey, repoPath, "worker", testPoshID)

	res, err := Launch(home, repoPath, driverKey, "brief", "", "opus", 15*time.Second)
	stop()
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if res.SessionKey == "" {
		t.Fatal("expected a session key")
	}

	argvBytes, err := os.ReadFile(filepath.Join(repoPath, ".worktrees", "worker", "argv.txt"))
	if err != nil {
		t.Fatalf("reading argv.txt: %v", err)
	}
	argv := strings.Split(strings.TrimRight(string(argvBytes), "\n"), "\n")
	// "sh" "--" "{prompt}" is the entry's provider-args tail; the default
	// model-flags map ({"claude": "--model"}) applies since the entry
	// selects no --provider (defaults to "claude").
	want := []string{"sh", "--", "--model", "opus", "brief"}
	if len(argv) != len(want) {
		t.Fatalf("argv = %v, want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, argv[i], want[i])
		}
	}
}

func TestLaunchModelWithoutSeparatorErrors(t *testing.T) {
	// happySweatfile's spawn-entry has no "--" — model has nowhere to splice.
	home, repoPath := newWorkerFixture(t, happySweatfile)
	driverKey := "driver/test-session"

	_, err := Launch(home, repoPath, driverKey, "brief", "", "opus", 15*time.Second)
	if err == nil {
		t.Fatal("Launch() = nil error, want error (no \"--\" separator)")
	}
	if !strings.Contains(err.Error(), "\"--\"") {
		t.Errorf("error = %q, want it to mention the missing separator", err.Error())
	}
	// Must fail BEFORE worktree creation (matches the existing bad-template
	// contract for spawn-entry validation).
	if _, statErr := os.Stat(filepath.Join(repoPath, ".worktrees", "worker")); statErr == nil {
		t.Error("worktree was created despite the model-splice error")
	}
}
```

Check `helloAfterLaunch`'s exact signature/behavior first (`internal/spawn/spawn_test.go` ~line 76-104) and adjust the call above to match — it may take different args than shown; read it before pasting this test in.

**Step 3: Update `internal/spawn/spawn.go` signatures.**

```go
// Launch(home, repoPath, driverKey, brief, desc, model string, deadline time.Duration) (Result, error)
func Launch(home, repoPath, driverKey, brief, desc, model string, deadline time.Duration) (Result, error) {
	...
	argv, window, sessionEnv, err := renderSpawn(home, rp, brief, model)
	...
}

func LaunchExisting(home string, rp worktree.ResolvedPath, driverKey, brief, desc, model string, deadline time.Duration) (Result, error) {
	argv, window, sessionEnv, err := renderSpawn(home, rp, brief, model)
	...
}

func renderSpawn(home string, rp worktree.ResolvedPath, brief, model string) (argv, window []string, sessionEnv map[string]string, err error) {
	hierarchy, err := sweatfileio.LoadWorktreeHierarchy(home, rp.RepoPath, rp.AbsPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("loading worker sweatfile hierarchy: %w", err)
	}
	merged := hierarchy.Merged

	entry := merged.SessionSpawnEntry()
	if model != "" {
		entry, err = SpliceModelFlag(entry, model, merged.SessionModelFlags())
		if err != nil {
			return nil, nil, nil, err
		}
	}

	argv = SubstituteEntry(entry, brief, rp.AbsPath)
	window = SubstituteWindow(merged.SessionSpawnWindow(), rp.SessionKey, rp.AbsPath)
	return argv, window, merged.SessionEnv(), nil
}
```

Update the doc comments on `Launch`/`LaunchExisting`/`renderSpawn` to mention the new `model` param one sentence each (what it does, that "" means no model requested) — do not leave stale comments referencing the old signature.

**Step 4: Run tests to verify they pass.**

Run: `hamster go-test packages=./internal/spawn`
Expected: PASS, including all pre-existing tests (now compiling with the extra `""` arg) and the two new ones.

Run: `hamster go-build` — confirm the whole module still compiles (production call sites in `cmd/spinclass` are NOT updated yet — this build is expected to FAIL at this point because `spawn_cmd.go`/`fork_cmd.go` call the old 6-arg `Launch`/6-arg `LaunchExisting`. That's fine; Task 4 fixes it. Just confirm the failure is ONLY in `cmd/spinclass`, not in `internal/spawn` itself.)

**Step 5: Commit.**

```
feat(spawn): thread model param through renderSpawn/Launch/LaunchExisting

renderSpawn splices the model flag (via SpliceModelFlag, Task 2) into
the resolved spawn-entry template before {prompt}/{dir} substitution,
using the worker's [session-entry.model-flags] map (Task 1). Launch
validates spawn templates — now including the model splice — BEFORE
creating the worktree, so a bad model+provider combination on `sc
spawn` never litters a worktree; LaunchExisting (detached fork) still
runs its render before writing session state, matching the existing
bad-spawn-entry-config contract.

cmd/spinclass callers are NOT updated in this commit — production
build is expected to fail until Task 4.

🤡 Generated with clown 0.3.18+5a832ac
https://github.com/amarbel-llc/clown/commit/5a832acfbea010956981409c4725e63b9bdd986c
```

(This is an intentionally red commit for a tight two-commit sequence; if your repo convention frowns on landing a non-building intermediate state — check `git log --oneline -20` for precedent of split Launch signature changes before committing this separately. If in doubt, SQUASH Task 3 and Task 4 into one commit instead of committing here.)

---

### Task 4: CLI + MCP surfaces — `spawn-session`, `fork-session`, `sc spawn`, `sc fork --brief`

**Promotion criteria:** N/A.

**Files:**
- Modify: `cmd/spinclass/spawn_cmd.go` (`spawnParams` ~line 25, `runSpawn` ~line 39, `spawnParamList` ~line 259)
- Modify: `cmd/spinclass/fork_cmd.go` (`forkDetachedParams` ~line 20, `runForkDetached` ~line 70, `handleForkSession` ~line 129, `forkSessionParamList` ~line 168)
- Modify: `cmd/spinclass/commands_query.go` (classic `fork` command's inline `Params` list ~line 205-211 — this is a SEPARATE param declaration from `forkSessionParamList`, used by the `sc fork --brief` CLI path; both must get the new param)
- Test: `cmd/spinclass/spawn_cmd_test.go`, `cmd/spinclass/fork_cmd_test.go`

**Step 1: Write failing tests — cheap validation cases.** In `cmd/spinclass/spawn_cmd_test.go`, add a case to the existing `TestHandleSpawnSessionValidation` table (~line 27-38):

```go
		{"bad model", `{"repo":"somewhere","brief":"do","model":"gpt5"}`, "unrecognized model"},
```

In `cmd/spinclass/fork_cmd_test.go`, add a case to `TestHandleForkSessionValidation`'s table (~line 28-35):

```go
		{"bad model", `{"brief":"do","model":"gpt5"}`, "unrecognized model"},
```

**Step 2: Run tests to verify they fail.**

Run: `hamster go-test packages=./cmd/spinclass run=Validation`
Expected: compile failure or test failure — `model` param not yet threaded, so the args JSON is silently ignored by `json.Unmarshal` (unknown field) and the case falls through past validation into a real spawn attempt against a nonexistent repo, producing the WRONG error message ("no repo named", not "unrecognized model"). Confirms the gap before fixing it.

**Step 3: Add `Model` fields.**

`spawn_cmd.go`, `spawnParams` struct:
```go
type spawnParams struct {
	Repo         string `json:"repo"`
	Brief        string `json:"brief"`
	Issue        string `json:"issue"`
	Description  string `json:"description"`
	HelloTimeout string `json:"hello-timeout"`
	Model        string `json:"model"`
}
```

`fork_cmd.go`, `forkDetachedParams` struct:
```go
type forkDetachedParams struct {
	NewBranch    string `json:"new-branch"`
	Brief        string `json:"brief"`
	Description  string `json:"description"`
	HelloTimeout string `json:"hello-timeout"`
	Model        string `json:"model"`
}
```

**Step 4: Wire eager validation + threading in `runSpawn` (`spawn_cmd.go`).** After the existing `deadline, err := parseHelloTimeout(p.HelloTimeout)` block (~line 46-49):

```go
	if p.Model != "" {
		if err := spawn.ValidateModelAlias(p.Model); err != nil {
			return spawn.Result{}, "", err
		}
	}
```

Update the `spawn.Launch` call (~line 81):
```go
	res, err := spawn.Launch(home, repoPath, driverKey, brief, p.Description, p.Model, deadline)
```

**Step 5: Wire eager validation + threading in `runForkDetached` (`fork_cmd.go`).** After the existing `deadline, err := parseHelloTimeout(p.HelloTimeout)` block (~line 74-77):

```go
	if p.Model != "" {
		if err := spawn.ValidateModelAlias(p.Model); err != nil {
			return spawn.Result{}, "", err
		}
	}
```

Update the `spawn.LaunchExisting` call (~line 116):
```go
	res, err := spawn.LaunchExisting(home, rp, driverKey, p.Brief, p.Description, p.Model, deadline)
```

Also add the SAME early check to `handleForkSession` (~line 134-139, alongside its existing `Brief`/`HelloTimeout` pre-checks — this duplicates `runForkDetached`'s check exactly as `HelloTimeout`'s validation is already duplicated between the two):

```go
	if params.Model != "" {
		if err := spawn.ValidateModelAlias(params.Model); err != nil {
			return command.TextErrorResult(err.Error()), nil
		}
	}
```

**Step 6: Add the `model` param to all three `command.Param` declarations.**

`spawn_cmd.go`, `spawnParamList()` — add after the `hello-timeout` entry:
```go
		{
			Name:        "model",
			Type:        command.String,
			Description: "Model alias for the worker (sonnet, opus, haiku, fable). Spliced into the resolved spawn-entry's provider-args per [session-entry.model-flags] (default: {\"claude\": \"--model\"}). Omit to use the harness's own default.",
			Completer:   completeModelAliases,
		},
```

`fork_cmd.go`, `forkSessionParamList()` — same entry, added after `hello-timeout`.

`commands_query.go`, the classic `fork` command's inline `Params` list (~line 205-211) — same entry, added after `hello-timeout`. This is the ONLY reason `sc fork --brief --model opus` will work from the CLI; `forkSessionParamList()` alone only covers the `fork-session` MCP tool.

Add the shared completer near `completeSpawnRepos` in `spawn_cmd.go`:
```go
// completeModelAliases offers the fixed set of known model aliases for
// tab completion / MCP client hinting.
func completeModelAliases() map[string]string {
	return map[string]string{
		"sonnet": "Claude Sonnet 5",
		"opus":   "Claude Opus 4.8",
		"haiku":  "Claude Haiku 4.5",
		"fable":  "Claude Fable 5",
	}
}
```

**Step 7: Run tests to verify they pass.**

Run: `hamster go-test packages=./cmd/spinclass run=Validation`
Expected: PASS.

Run: `hamster go-build`
Expected: whole module builds clean now (Task 3's intentionally-broken build is fixed).

Run: `hamster go-test packages=./...`
Expected: full PASS — this is the first point since Task 3 where every package builds AND tests pass together; treat any failure as a real regression, not an artifact of the split.

**Step 8: Commit.**

```
feat(cli): model param on spawn-session, fork-session, sc spawn, sc fork --brief

Mirrors the existing hello-timeout per-call-override pattern: `model`
threads through spawnParams/forkDetachedParams, validated eagerly
(spawn.ValidateModelAlias) before any worktree/state work, then passed
to spawn.Launch/LaunchExisting for provider-aware splicing (Tasks 2-3).
Added to all three command.Param declarations that need it —
spawnParamList, forkSessionParamList, AND the classic `fork` command's
own inline Params list in commands_query.go (sc fork --brief's actual
CLI registration, distinct from forkSessionParamList which only covers
the fork-session MCP tool).

🤡 Generated with clown 0.3.18+5a832ac
https://github.com/amarbel-llc/clown/commit/5a832acfbea010956981409c4725e63b9bdd986c
```

---

### Task 5: docs — manpage, CLAUDE.md, design doc status

**Promotion criteria:** N/A.

**Files:**
- Modify: `cmd/spinclass/doc/spinclass-sweatfile.5` (`[session-entry]` section, after the `spawn-window` `.TP` block ~line 700-730)
- Modify: `CLAUDE.md` (Sweatfile config quick reference section)
- Modify: `docs/plans/2026-07-11-spawn-model-selection-design.md` (status line)

**Step 1: Add the manpage `.TP` entry.** In `cmd/spinclass/doc/spinclass-sweatfile.5`, insert a new `.TP` block after the `spawn-window` entry ends (find the line with `.B Merge:` immediately following the `spawn-window` prose block, ~line 729 area — read the surrounding 40 lines first to match exact groff conventions: `.BR`, `.I`, `\(dq` for quotes, `\(em` for em-dash):

```groff
.TP
.B model\-flags
Map from a clown provider name (as selected by
.B spawn\-entry \(cq s
.BR \-\-provider / \-\-provider= )
to the CLI flag that provider's binary uses to select a model.
Consulted when the
.BR spawn\-session / fork\-session
.B model
param (or
.B sc spawn \-\-model / sc fork \-\-brief \-\-model )
is set: the flag and the requested model alias
.RB ( sonnet ", " opus ", " haiku ", or " fable )
are spliced into
.B spawn\-entry
immediately after its literal
.B \(dq\-\-\(dq
provider-args separator.
Defaults to
.BR "{\(dqclaude\(dq: \(dq\-\-model\(dq}" ,
the only mapping verified against an actual provider CLI; a model
requested for a provider absent from this map, or a
.B spawn\-entry
with no
.BR \(dq\-\-\(dq ,
is a hard spawn error.
.B Merge:
per-key (like
.BR env ).
```

**Step 2: Render-check the manpage.**

Run: `mandoc -T utf8 cmd/spinclass/doc/spinclass-sweatfile.5 | grep -A20 model-flags` (or via a Bash-equivalent tool available in this environment)
Expected: clean render, no mandoc warnings on stderr, output reads sensibly next to the `spawn-window` entry above it.

**Step 3: Update CLAUDE.md.** In the "Sweatfile config quick reference" section, find the bullet listing map-merge fields (mentions `[env]` map merge). Add `model-flags` to that same bullet or as an adjacent one — read the current exact wording first (it may already be phrased as a single sentence listing `[env]` — extend it rather than adding a whole new paragraph, keeping the doc DRY).

**Step 4: Update the design doc status.** In `docs/plans/2026-07-11-spawn-model-selection-design.md`, change the header:

```markdown
**Status:** implemented 2026-07-11
```

(Only mark this once Tasks 1-4 are merged and tests pass — if executing this plan across multiple sessions, leave the status as "approved... pending implementation plan" until the whole series lands, then update it as the LAST commit.)

**Step 5: Commit.**

```
docs: [session-entry.model-flags] manpage entry + design doc status

🤡 Generated with clown 0.3.18+5a832ac
https://github.com/amarbel-llc/clown/commit/5a832acfbea010956981409c4725e63b9bdd986c
```

---

## Notes for the implementing agent

- Task 3's commit intentionally leaves `cmd/spinclass` non-building for one commit (documented in that task). If your workflow prefers every commit to build, squash Tasks 3 and 4.
- The design doc (`docs/plans/2026-07-11-spawn-model-selection-design.md`) is the source of truth for WHY each behavior was chosen (hard-error semantics, claude-only default, no `--profile` detection, no alias-value translation). Re-read it if a step here seems arbitrary.
- Do not add `sc validate` integration, `--profile` provider detection, or per-provider alias-value translation — all explicitly out of scope per the design doc.
