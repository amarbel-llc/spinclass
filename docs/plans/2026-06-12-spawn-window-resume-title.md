# spawn-window (#149) + resume-title (#154) Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use eng:subagent-driven-development to implement this plan task-by-task.

**Goal:** Spawning a worker optionally opens a terminal window already attached to it, and `sc resume` emits a terminal title so spawned sessions stop showing the stale outer title.

**Architecture:** Two new `[session-entry]` sweatfile knobs. `spawn-window` is an argv template (`{id}`/`{dir}`) exec'd fire-and-forget in `internal/spawn.launchRendered` right after the spawn template returns and before the hello wait; failures are warnings, never spawn failures. `resume-title` is a title template (`{id}`, default `"{id}"`, empty disables) emitted as one OSC-2 escape in `runResume` before `shop.Attach`, gated on stdout-TTY. Platform dispatch (kitty/sway vs kitty/PaperWM-macOS) lives in a user-side `sc-spawn-window` script in ~/eng, NOT in spinclass.

**Design doc:** docs/plans/2026-06-12-spawn-window-resume-title-design.md (approved 2026-06-12).

**Tech Stack:** Go, tommy codegen (sweatfile fields), bats e2e, home-manager (eng side).

**Rollback:** Purely additive and config-gated — both knobs unset reproduce today's behavior exactly; rollback = remove the sweatfile lines.

**Repo conventions that bite here:**
- Do NOT run full `just` before merging — `merge-this-session`'s pre-merge hook runs it. Cheap `go test ./internal/<pkg>/` runs are fine.
- nix lanes only see git-tracked files: `git add` new files before any nix-backed run.
- Commit messages sign off as Clown with the build identifier, per harness conventions.
- Two workers may merge concurrently (#140 justfile area, #150 sweatfile.5). Rebase normally; Task 7 EXPECTS #150's new manpage section to exist after rebase.

---

### Task 1: Sweatfile fields + accessors

**Files:**
- Modify: `internal/sweatfile/sweatfile.go` (SessionEntry struct ~line 25; accessors near `SessionSpawn` ~line 320; the SessionEntry arm of `MergeWith`)
- Regenerate: `internal/sweatfile/sweatfile_tommy.go` (tommy codegen)
- Test: `internal/sweatfile/sweatfile_test.go` (beside the SessionSpawn accessor/merge tests ~line 1329)

**Step 1: Write failing tests** (mirror the existing `TestSessionSpawnAccessor*` / `TestMergeSessionSpawn*` shapes exactly):

```go
func TestSessionSpawnWindowAccessor(t *testing.T) {
	for _, sf := range []Sweatfile{{}, {SessionEntry: &SessionEntry{}}} {
		if got := sf.SessionSpawnWindow(); got != nil {
			t.Errorf("SessionSpawnWindow() = %v, want nil (no default)", got)
		}
	}
	sf := Sweatfile{SessionEntry: &SessionEntry{
		SpawnWindow: []string{"sc-spawn-window", "{id}", "{dir}"},
	}}
	if got := sf.SessionSpawnWindow(); len(got) != 3 || got[0] != "sc-spawn-window" {
		t.Errorf("SessionSpawnWindow() = %v, want configured argv", got)
	}
}

func TestSessionResumeTitleAccessor(t *testing.T) {
	for _, sf := range []Sweatfile{{}, {SessionEntry: &SessionEntry{}}} {
		if got := sf.SessionResumeTitle(); got != "{id}" {
			t.Errorf("SessionResumeTitle() = %q, want default {id}", got)
		}
	}
	custom := "sc/{id}"
	sf := Sweatfile{SessionEntry: &SessionEntry{ResumeTitle: &custom}}
	if got := sf.SessionResumeTitle(); got != "sc/{id}" {
		t.Errorf("SessionResumeTitle() = %q, want sc/{id}", got)
	}
	empty := ""
	sf = Sweatfile{SessionEntry: &SessionEntry{ResumeTitle: &empty}}
	if got := sf.SessionResumeTitle(); got != "" {
		t.Errorf("SessionResumeTitle() = %q, want empty (disabled)", got)
	}
}

func TestMergeSessionSpawnWindowAndResumeTitle(t *testing.T) {
	base := Sweatfile{SessionEntry: &SessionEntry{
		SpawnWindow: []string{"old", "{id}"},
		ResumeTitle: ptr("base/{id}"),
	}}
	override := Sweatfile{SessionEntry: &SessionEntry{
		SpawnWindow: []string{"new", "{id}", "{dir}"},
	}}
	merged := base.MergeWith(override)
	if len(merged.SessionEntry.SpawnWindow) != 3 || merged.SessionEntry.SpawnWindow[0] != "new" {
		t.Errorf("SpawnWindow = %v, want override", merged.SessionEntry.SpawnWindow)
	}
	if merged.SessionResumeTitle() != "base/{id}" {
		t.Errorf("ResumeTitle = %q, want inherited base/{id}", merged.SessionResumeTitle())
	}
}
```

(Add a tiny `func ptr(s string) *string { return &s }` helper if the test file lacks one.)

Also extend the existing TOML decode round-trip test (the one asserting `Spawn`/`SpawnEntry` decode, ~line 1310) with `spawn-window = [...]` and `resume-title = "sc/{id}"` lines.

**Step 2: Run** `go test ./internal/sweatfile/ -run 'SpawnWindow|ResumeTitle'` — expect FAIL (fields undefined).

**Step 3: Implement.** In `SessionEntry` (after `SpawnEntry`):

```go
	// SpawnWindow is an argv template exec'd fire-and-forget right after
	// the spawn template returns: it opens a terminal window onto the
	// freshly spawned worker (#149). {id} = the worker's session key,
	// {dir} = the worker worktree; {entry}/{prompt} are rejected by
	// validate. Unset = no window.
	SpawnWindow []string `toml:"spawn-window"`
	// ResumeTitle is the terminal title `sc resume` emits (one OSC 2
	// escape) before exec'ing the attach entrypoint — spawned sessions'
	// ptys have no title-writing shell, so the stale outer title persists
	// without it (#154). {id} = the session key. nil = default "{id}";
	// empty string disables emission.
	ResumeTitle *string `toml:"resume-title"`
```

Accessors (beside `SessionSpawnEntry`):

```go
// SessionSpawnWindow returns the spawn-window argv template, or nil when
// unconfigured — there is no default: opening windows is a desktop
// preference (#149). See FDR 0006.
func (sf Sweatfile) SessionSpawnWindow() []string {
	if sf.SessionEntry != nil && len(sf.SessionEntry.SpawnWindow) > 0 {
		return sf.SessionEntry.SpawnWindow
	}
	return nil
}

// SessionResumeTitle returns the resume title template (#154). Default
// "{id}"; an explicit empty string disables emission.
func (sf Sweatfile) SessionResumeTitle() string {
	if sf.SessionEntry != nil && sf.SessionEntry.ResumeTitle != nil {
		return *sf.SessionEntry.ResumeTitle
	}
	return "{id}"
}
```

In `MergeWith`'s SessionEntry arm, mirror the `Spawn` (len>0 overrides) handling for `SpawnWindow` and the pointer-field handling (non-nil overrides) for `ResumeTitle` — read the existing arm first and copy its exact style.

**Step 4: Regenerate the tommy codec.** Try `just gen-tommy`. The #140 worker may have already fixed it (rebase/check `git log master --oneline -3` mentions #140); if it still fails with missing go.sum entries, use the documented dance from issue #140: `go get github.com/amarbel-llc/tommy/generate@v0.4.0`, run `just gen-tommy`, then `git checkout master -- go.mod go.sum` and `just deps` to restore (verify `git diff go.mod` is empty afterwards).

**Step 5: Run** `go test ./internal/sweatfile/` — expect PASS.

**Step 6: Commit** `feat(sweatfile): spawn-window and resume-title session-entry knobs` (body: one para each, reference #149/#154; no Closes — the features aren't done).

---

### Task 2: Window template rendering

**Files:**
- Modify: `internal/spawn/template.go`
- Test: `internal/spawn/template_test.go`

**Step 1: Failing tests:**

```go
func TestSubstituteWindow(t *testing.T) {
	t.Run("substitutes id and dir in every element", func(t *testing.T) {
		got := SubstituteWindow([]string{"sc-spawn-window", "{id}", "{dir}", "x={id}"},
			"repo/feat", "/wt")
		want := []string{"sc-spawn-window", "repo/feat", "/wt", "x=repo/feat"}
		if !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("nil template renders nil", func(t *testing.T) {
		if got := SubstituteWindow(nil, "id", "/wt"); got != nil {
			t.Errorf("got %q, want nil", got)
		}
	})
}
```

**Step 2:** `go test ./internal/spawn/ -run TestSubstituteWindow` — FAIL (undefined).

**Step 3: Implement** in template.go (beside SubstituteEntry; reuse its replacement style):

```go
// SubstituteWindow renders the spawn-window argv template (#149): {id} →
// the worker's session key, {dir} → the worker worktree, in every element.
// Returns nil for an empty template (knob unset). No {entry} splice — the
// window command is a leaf argv; validate rejects {entry}/{prompt} here.
func SubstituteWindow(template []string, id, dir string) []string {
	if len(template) == 0 {
		return nil
	}
	out := make([]string, len(template))
	for i, el := range template {
		el = strings.ReplaceAll(el, "{id}", id)
		el = strings.ReplaceAll(el, "{dir}", dir)
		out[i] = el
	}
	return out
}
```

**Step 4:** test passes. **Step 5: Commit** `feat(spawn): render the spawn-window argv template`.

---

### Task 3: Validate checks

**Files:**
- Modify: `internal/validate/validate.go` (`CheckSessionEntry`)
- Test: `internal/validate/validate_test.go` (beside the existing spawn checks ~line 338)

**Step 1: Failing tests** (mirror `TestCheckSessionEntrySpawn*` shapes):

```go
func TestCheckSessionEntrySpawnWindowRejectsEntryAndPrompt(t *testing.T) {
	for _, bad := range []string{"{entry}", "{prompt}"} {
		sf := sweatfile.Sweatfile{SessionEntry: &sweatfile.SessionEntry{
			SpawnWindow: []string{"kitty", bad},
		}}
		issues := CheckSessionEntry(sf)
		if len(issues) != 1 || issues[0].Severity != SeverityError ||
			issues[0].Field != "session-entry.spawn-window" {
			t.Fatalf("%s: expected one error issue, got %+v", bad, issues)
		}
	}
}

func TestCheckSessionEntrySpawnWindowWarnsWithoutIDOrDir(t *testing.T) {
	sf := sweatfile.Sweatfile{SessionEntry: &sweatfile.SessionEntry{
		SpawnWindow: []string{"kitty", "--detach"},
	}}
	issues := CheckSessionEntry(sf)
	if len(issues) != 1 || issues[0].Severity != SeverityWarning {
		t.Fatalf("expected one warning, got %+v", issues)
	}
}

func TestCheckSessionEntrySpawnWindowClean(t *testing.T) {
	sf := sweatfile.Sweatfile{SessionEntry: &sweatfile.SessionEntry{
		SpawnWindow: []string{"sc-spawn-window", "{id}", "{dir}"},
	}}
	if issues := CheckSessionEntry(sf); len(issues) != 0 {
		t.Errorf("unexpected issues: %+v", issues)
	}
}
```

**Step 2:** FAIL. **Step 3:** implement in `CheckSessionEntry`, matching the existing spawn checks' Issue construction style: error when any element contains `{entry}` or `{prompt}` ("spawn-window is a leaf argv; it takes {id}/{dir} only"); warning when NO element contains `{id}` or `{dir}` ("window command cannot identify its session"). **Step 4:** PASS. **Step 5: Commit** `feat(validate): spawn-window template checks`.

---

### Task 4: Fire-and-forget window launch in the spawn flow

**Files:**
- Modify: `internal/spawn/spawn.go` (`renderSpawn` ~line 88, `Launch` ~line 41, `LaunchExisting` ~line 75, `launchRendered` ~line 143)
- Test: `internal/spawn/spawn_test.go` (beside `TestLaunchHappyPath`)

**Step 1: Failing tests.** Extend the happy-path fixture sweatfile (`happySweatfile` const) — add to a NEW test's own fixture instead of mutating the shared one:

```go
const windowSweatfile = happySweatfile + `spawn-window = ["sh", "-c", 'printf "%s %s" "$1" "$2" > "$PWD/window.txt"', "sh", "{id}", "{dir}"]
`

func TestLaunchSpawnWindowFires(t *testing.T) {
	home, repoPath := newWorkerFixture(t, windowSweatfile)
	// ... same hello goroutine as TestLaunchHappyPath ...
	res, err := Launch(home, repoPath, "driver/test", "brief", "", 15*time.Second)
	// ... close(stop), assert err nil, drain helloErr ...

	// The window command runs async (fire-and-forget): poll briefly.
	deadline := time.Now().Add(5 * time.Second)
	var data []byte
	for time.Now().Before(deadline) {
		if b, rerr := os.ReadFile(filepath.Join(res.WorktreePath, "window.txt")); rerr == nil {
			data = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	want := res.SessionKey + " " + res.WorktreePath
	if string(data) != want {
		t.Errorf("window.txt = %q, want %q", data, want)
	}
}

const failingWindowSweatfile = happySweatfile + `spawn-window = ["false"]
`

func TestLaunchSpawnWindowFailureDoesNotFailSpawn(t *testing.T) {
	home, repoPath := newWorkerFixture(t, failingWindowSweatfile)
	// ... hello goroutine ...
	if _, err := Launch(home, repoPath, "driver/test", "brief", "", 15*time.Second); err != nil {
		t.Fatalf("Launch failed because of the window command: %v", err)
	}
}
```

(Copy the hello-goroutine block from `TestLaunchHappyPath` verbatim — it's the established pattern in this file.)

**Step 2:** FAIL (window.txt never written). **Step 3: Implement:**

- `renderSpawn` returns the window argv too: signature `(argv, window []string, sessionEnv map[string]string, err error)`; add `window := SubstituteWindow(merged.SessionSpawnWindow(), rp.SessionKey, rp.AbsPath)` (note: SubstituteWindow gets `rp.SessionKey`, same as `{id}` post-#146). Update `Launch`/`LaunchExisting` to thread it into `launchRendered`.
- `launchRendered` gains the `windowArgv []string` param. Immediately after the spawn template's `cmd.Run()` succeeds and BEFORE `chat.WaitForHello`:

```go
	launchSpawnWindow(windowArgv, rp, desc, sessionEnv)
```

```go
// launchSpawnWindow opens the configured terminal window onto the worker
// (#149), fire-and-forget: the window is a convenience side effect, so
// render/start/exit failures are logged warnings, never spawn failures.
// Runs before the hello wait — the user watches the harness boot live.
func launchSpawnWindow(argv []string, rp worktree.ResolvedPath, desc string, sessionEnv map[string]string) {
	if len(argv) == 0 {
		return
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = rp.AbsPath
	cmd.Env = workerEnv(rp, desc, sessionEnv)
	if err := cmd.Start(); err != nil {
		slog.Warn("spawn-window failed to start", "argv", argv, "err", err)
		return
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			slog.Warn("spawn-window exited nonzero", "argv", argv, "err", err)
		}
	}()
}
```

(`log/slog` import; the goroutine is best-effort — a CLI exiting first simply loses the warning.)

**Step 4:** `go test ./internal/spawn/` PASS. **Step 5: Commit** `feat(spawn): exec spawn-window fire-and-forget after worker launch (#149)`.

---

### Task 5: Resume title emission

**Files:**
- Modify: `cmd/spinclass/commands_session.go` (`runResume` — insert before the `shop.Attach` return at ~line 531)
- Test: `cmd/spinclass/commands_session_test.go` (create if absent — check first; there may be an existing test file for this command file)

**Step 1: Failing tests** for a pure helper:

```go
func TestEmitResumeTitle(t *testing.T) {
	base := func(rt *string) sweatfile.Sweatfile {
		return sweatfile.Sweatfile{SessionEntry: &sweatfile.SessionEntry{ResumeTitle: rt}}
	}
	t.Run("default emits the session key", func(t *testing.T) {
		var b bytes.Buffer
		emitResumeTitle(&b, sweatfile.Sweatfile{}, "spinclass/fix-141")
		if got, want := b.String(), "\033]2;spinclass/fix-141\007"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("custom template", func(t *testing.T) {
		var b bytes.Buffer
		s := "sc/{id}"
		emitResumeTitle(&b, base(&s), "repo/branch")
		if got, want := b.String(), "\033]2;sc/repo/branch\007"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("empty template disables", func(t *testing.T) {
		var b bytes.Buffer
		s := ""
		emitResumeTitle(&b, base(&s), "repo/branch")
		if b.Len() != 0 {
			t.Errorf("expected no output, got %q", b.String())
		}
	})
}
```

**Step 2:** FAIL. **Step 3: Implement** in commands_session.go:

```go
// emitResumeTitle writes the OSC 2 terminal title for the session being
// resumed (#154). Spawned sessions' ptys have no title-writing shell, so
// without this the attaching terminal keeps its stale outer title; one
// shot suffices (an interactive shell inside the session overwrites it at
// its next prompt anyway).
func emitResumeTitle(w io.Writer, merged sweatfile.Sweatfile, sessionKey string) {
	tpl := merged.SessionResumeTitle()
	if tpl == "" {
		return
	}
	fmt.Fprintf(w, "\033]2;%s\007", strings.ReplaceAll(tpl, "{id}", sessionKey))
}
```

Call site in `runResume`, right before the `return shop.Attach(...)`:

```go
	// One-shot terminal title before the exec chain (#154); TTY-gated so
	// piped output stays clean.
	if !p.NoAttach && isatty.IsTerminal(os.Stdout.Fd()) {
		emitResumeTitle(os.Stdout, merged, state.SessionKey)
	}
```

(`github.com/mattn/go-isatty` is already a module dep — see shop.go's usage.)

**Step 4:** `go test ./cmd/spinclass/ -run TestEmitResumeTitle` PASS. **Step 5: Commit** `feat(resume): emit OSC-2 session title before attach (#154)`.

---

### Task 6: bats e2e

**Files:**
- Modify: `zz-tests_bats/spawn.bats` (window tests), `zz-tests_bats/` resume/session file (escape-leak assertion — find the file exercising `sc resume`, likely lifecycle.bats or sessions.bats; grep for "resume")

**Step 1: spawn-window tests** in spawn.bats (reuse `create_spawn_repo`; append the window line to the repo sweatfile and commit it, mirroring `write_stub_sweatfile`):

```bash
@test "spawn-window fires with id and dir, in the worker worktree" {
  create_spawn_repo windowrepo "$STUB_DIR/stub-harness.sh"
  cat >>"$WORKER_REPO/sweatfile" <<'EOF'
spawn-window = ["sh", "-c", "printf '%s\n%s\n' \"$1\" \"$2\" > window.txt", "sh", "{id}", "{dir}"]
EOF
  git -C "$WORKER_REPO" add sweatfile
  git -C "$WORKER_REPO" commit -m "window stub"

  SPINCLASS_SESSION_ID=driver/bats run_sc spawn windowrepo --brief "do the thing"
  assert_success
  local wt
  wt=$(echo "$output" | grep -oP 'worktree_path: \K\S+')
  # fire-and-forget: poll briefly for the async write
  local i=0
  while [ ! -f "$wt/window.txt" ] && [ $i -lt 50 ]; do sleep 0.1; i=$((i+1)); done
  run cat "$wt/window.txt"
  assert_output --partial "windowrepo/"
  assert_output --partial "$wt"
}

@test "failing spawn-window does not fail the spawn" {
  create_spawn_repo windowfailrepo "$STUB_DIR/stub-harness.sh"
  cat >>"$WORKER_REPO/sweatfile" <<'EOF'
spawn-window = ["false"]
EOF
  git -C "$WORKER_REPO" add sweatfile
  git -C "$WORKER_REPO" commit -m "failing window stub"

  SPINCLASS_SESSION_ID=driver/bats run_sc spawn windowfailrepo --brief "still works"
  assert_success
}
```

**Step 2: resume escape-leak test** — in the bats file that already exercises resume (find via `grep -rl "resume" zz-tests_bats/*.bats`), add after an existing resume test (bats output is non-TTY, so the gate must suppress emission):

```bash
@test "resume emits no title escape when output is not a TTY" {
  # fixture: same as the file's existing resume test (session sweatfile
  # with start/resume = ["true"], create+start, then resume)
  ...
  run_sc resume "$id"
  assert_success
  refute_output --partial $'\033]2;'
}
```

**Step 3:** `git add zz-tests_bats/` (nix lane needs tracked files), run the single file if a recipe exists for it; otherwise rely on the merge lane. **Step 4: Commit** `test(bats): spawn-window + resume title-escape gating e2e`.

---

### Task 7: Docs

**Files:**
- Modify: `CLAUDE.md` (spawned-worker section + sweatfile `[session-entry]` bullet), `docs/features/0006-spawn-sibling-sessions.md` (short spawn-window paragraph under "The same machinery powers sc fork" or a new subsection; mention #149/#154 and the user-side script pattern), `cmd/spinclass/doc/spinclass-sweatfile.5` (extend the `[session-entry]` spawn section — **rebase first**: the #150 worker is adding that section now; write spawn-window/resume-title as siblings of its spawn/spawn-entry entries, matching its groff conventions)
- If #150 has NOT landed by this task: document the two knobs in the manpage anyway following the file's existing conventions, and note the merge rebase may need a manual conflict resolution against #150's section.

**Steps:** write, render-check the manpage (`mandoc -T utf8 cmd/spinclass/doc/spinclass-sweatfile.5 | grep -A6 spawn-window`), commit `docs: spawn-window + resume-title knobs (#149, #154)` — include `Closes #149` and `Closes #154` HERE (last spinclass commit of the series).

---

### Task 8: Merge

**Steps:** attestation via nothing-but-the-truth (simplify / review / eng:loose-ends / eng:doc-drift — run them for real against the cumulative diff), then `merge-this-session` (git_sync). Known flake: #152 (bats-race `spinclass_clean_removes_merged` timeout) — one retry justified on exactly that signature.

---

### Task 9: ~/eng side (driver does this directly, not a subagent — different repo)

**Files:**
- Modify: `~/eng/home/common.nix` (near `programs.kitty`, ~line 100): add to `home.packages` (or the file's package-list seam) a platform-conditional script:

```nix
  # Terminal window onto a freshly spawned spinclass worker (spinclass#149).
  # Referenced by [session-entry].spawn-window in the spinclass sweatfile.
  # Platform dispatch at eval time: sway tiles the Linux window, PaperWM
  # (Hammerspoon) tiles the macOS one — both passively.
  home.packages = [
    (pkgs.writeShellScriptBin "sc-spawn-window" (if pkgs.stdenv.isDarwin then ''
      exec open -na kitty --args --title "sc/$1" --directory "$2" sc resume "$1"
    '' else ''
      exec kitty --detach --title "sc/$1" --directory "$2" sc resume "$1"
    ''))
  ];
```

(Check how common.nix actually aggregates home.packages first — merge into the existing list/import structure rather than adding a second `home.packages` definition if that conflicts.)

- Modify: `~/eng/rcm/config/spinclass/sweatfile` `[session-entry]`:

```toml
spawn-window = ["sc-spawn-window", "{id}", "{dir}"]
resume-title = "sc/{id}"
```

**Steps:** edit, commit in ~/eng (`spinclass sweatfile + home: spawn-window script and resume-title (spinclass#149/#154)`). **User action required:** rebuild/activate home-manager (`just build-home` or the host's activation flow) and restart sessions to pick up the new spinclass binary — the script and knobs are inert until both are live. State this in the wrap-up; do not attempt activation from the session.

---

### Production validation (post-merge)

Next real spawn on an updated host: window opens at launch with title `sc/<key>`, boot watchable live, hello still gates the tool result; `sc resume` into a worker shows the right title. This doubles as the FDR 0006 promotion soak the user asked for.
