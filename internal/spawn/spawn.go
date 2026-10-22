package spawn

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/shop"
	"code.linenisgreat.com/spinclass/internal/spawnhandshake"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/worktree"
)

// DefaultHelloDeadline bounds how long Launch waits for the worker's
// SessionStart hello. It is FDR 0006's documented tuning lever:
// comfortably past typical harness startup (10–30s), far inside MCP
// request timeouts. Raise it (or grow a knob) if cold nix-cache session
// starts exceed the window in practice.
const DefaultHelloDeadline = 60 * time.Second

// Driver identifies the spawning session (FDR 0032 D1). Principal is the
// authority link and the hello target: the driver's per-instance key, which
// is also its chat JID localpart. SessionKey is the driver's spinclass
// session key when it has one, recorded for display only (sc list's
// spawned-by column) and empty for a driver outside any worktree
// (spinclass#332).
type Driver struct {
	SessionKey string
	Principal  string
}

// Result describes a successfully launched worker session.
type Result struct {
	SessionKey    string // <repo-dirname>/<branch> — the worker's chat target
	WorktreePath  string // absolute path to the worker's worktree
	MultiplexerID string // the worker's session key (= SPINCLASS_SESSION_ID, #146)
	PoshSessionID string // the worker's posh/clown session id from the hello (reattach target); empty if the worker reported none
}

// Pending is a spawned worker whose worktree exists and whose detached entry
// has been exec'd, but whose SessionStart hello has not yet been awaited. The
// async spawn path (spinclass#266) returns SessionKey/WorktreePath to the caller
// immediately and hands the Pending to WaitHello in a background job; the sync
// path (Launch) awaits it inline.
type Pending struct {
	SessionKey   string // <repo-dirname>/<branch> — the worker's chat target
	WorktreePath string // absolute path to the worker's worktree
	RepoPath     string // the worker repo's main checkout (for a timeout reap)
	Branch       string // the worker's branch (for a timeout reap)

	rp         worktree.ResolvedPath
	driver     Driver
	desc       string
	startTime  time.Time
	window     []string
	sessionEnv map[string]string
	confine    *confinedLaunch // nil unless the worker was launched confined (FDR 0035)
}

// LaunchDetached is the SYNCHRONOUS prefix of a spawn: validate the spawn
// templates, create the worker worktree (shop.Create), write its session state,
// and exec the detached harness entry. It returns as soon as the worker is
// booting — its SessionKey/WorktreePath are known — WITHOUT waiting for the
// hello. Pass the returned Pending to WaitHello (inline for the sync path, or in
// a background job for the async path, spinclass#266).
//
// model, when non-empty, requests a specific model alias (e.g. "opus") for the
// worker's provider — spliced into the spawn-entry template via
// renderSpawn/SpliceModelFlag; "" runs the entry unmodified. The branch name and
// ResolvedPath are produced exactly as `sc start` does (worktree.ResolvePath).
//
// driver identifies the spawning session (FDR 0032 D1): its Principal is the
// hello target and is always present; its SessionKey is display-only and may
// be empty for a driver outside any worktree (spinclass#332).
//
// profile, when non-empty, names the clown profile to boot the worker with
// (FDR 0034); "" falls back to the merged sweatfile's [session-entry]
// spawn-profile, and no profile at all leaves the spawn completely unchanged
// (no resolver call). A requested profile is resolved before shop.Create, so a
// refusal litters no worktree, and its name and digest are recorded in the
// worker's session state. A confined spawn instead requires a profile, reads
// the spawn-profile default only from layers above the repo, and accepts only a
// profile that leaves no grit tool reachable (requireConfinedProfile).
func LaunchDetached(home, repoPath string, driver Driver, brief, desc, model, profile string) (Pending, error) {
	return LaunchDetachedWith(home, repoPath, driver, brief, desc, model, profile, ConfineDefault)
}

// LaunchDetachedWith is LaunchDetached with an explicit confinement request
// (the spawn's `confined` / `unconfined` parameters): see resolveConfined.
func LaunchDetachedWith(home, repoPath string, driver Driver, brief, desc, model, profile string, req ConfineRequest) (Pending, error) {
	var descArgs []string
	if desc != "" {
		descArgs = []string{desc}
	}
	rp, err := worktree.ResolvePath(repoPath, descArgs)
	if err != nil {
		return Pending{}, err
	}

	// Validate the spawn templates BEFORE creating anything: bad config
	// (missing spawn-entry, no {entry} splice, or — with a model requested —
	// no "--" separator to splice the model flag into) must not litter a
	// worktree. Rendering never reads the worktree's own sweatfile (a worker
	// can write it): only the global, ancestor and main-checkout layers.
	argv, window, sessionEnv, merged, targetEnforce, targetConfine, err := renderSpawn(home, rp, brief, model, profile)
	if err != nil {
		return Pending{}, err
	}
	// The target repo path is caller-chosen, so its hierarchy alone cannot decide
	// the switch: it is on if the driver's own context says so too.
	enforce := targetEnforce || driverContextEnforces(home)

	// Same rule for confinement (FDR 0035). Refusals come before anything is
	// created; an unconfined spawn does none of this.
	confined, err := resolveConfined(targetConfine.policy, driverContextConfinement(home), req)
	if err != nil {
		return Pending{}, err
	}
	effective := effectiveProfile(profile, merged)
	var resolved ResolvedProfile
	var narrowing *narrowingBase
	if confined {
		// A confined worker needs a profile that leaves no grit tool reachable
		// (FDR 0035). Config-only and read-only, so it comes first: nothing below
		// writes state, and a refusal here does not depend on the host.
		if len(targetConfine.ignoredSpawnProfile) > 0 {
			slog.Warn("ignored [session-entry] spawn-profile in sweatfiles that are not trusted: a confined spawn reads it only from sweatfiles above the repo",
				"paths", targetConfine.ignoredSpawnProfile)
		}
		if effective, resolved, narrowing, err = requireConfinedProfile(profile, targetConfine.spawnProfile); err != nil {
			return Pending{}, err
		}
		// renderSpawn spliced the merged default; the confined profile may differ.
		if argv, err = SpliceProfileFlag(argv, effective); err != nil {
			return Pending{}, err
		}
		if err := confinedPreflight(confinementHost, agentScopeCapabilitiesArgv); err != nil {
			return Pending{}, err
		}
		if err := refuseStaleServe(); err != nil {
			return Pending{}, err
		}
		// No operator sits at a confined unit's tty (F8a), so the worker must
		// never be able to wait on a permission prompt: dontAsk denies instead.
		// Before anything is created, so a spawn-entry that cannot take the flag
		// litters nothing.
		if argv, err = SpliceDontAskMode(argv); err != nil {
			return Pending{}, err
		}
	}
	if enforce && effective == "" {
		// No profile was requested, so Authorize would never run; a profile the
		// spawn-entry already carries would reach the worker unchecked.
		if flag := embeddedProfileFlag(argv); flag != "" {
			return Pending{}, fmt.Errorf("spawn refused: the spawn-entry carries %q but no profile was requested; "+
				"when profile rights are enforced a profile may only be requested explicitly (the profile parameter or [session-entry].spawn-profile), so it can be authorized", flag)
		}
	}

	var authorizingGrant string
	var undoLink func()
	if effective != "" {
		if !confined { // a confined spawn resolved its profile above
			resolved, err = resolveProfile(effective)
			if err != nil {
				return Pending{}, err
			}
		}
		if enforce {
			// The name that was authorized must be exactly the name that was
			// spliced into the argv.
			if resolved.Name != effective {
				return Pending{}, fmt.Errorf("profile %q refused: it resolved to %q, and only the requested name may be authorized", effective, resolved.Name)
			}
			// Before shop.Create: a refusal litters no worktree. The child's
			// session key is known from ResolvePath, so its link can be minted
			// now and bound to it.
			authorizingGrant, undoLink, err = enforceProfileRight(driver, resolved, rp.SessionKey)
			if err != nil {
				return Pending{}, err
			}
		}
	}
	// The link is minted before the worktree exists, so a later failure must not
	// leave it bound to a session that will never run.
	undoOnFailure := func() {
		if undoLink != nil {
			undoLink()
		}
	}

	// The worker repo's own sweatfile decides whether a stale base is
	// tolerable. There is deliberately no spawn-session parameter for it: a
	// driver agent must not be able to wave away its worker's stale toolchain,
	// so only the repo's owner can opt out (spinclass#250).
	if _, err := shop.Create(io.Discard, rp, shop.CreateOpts{
		AllowStaleBase:    merged.AllowStaleBase(),
		AllowNoCredential: merged.AllowNoCredential(),
	}, nil); err != nil {
		undoOnFailure()
		return Pending{}, fmt.Errorf("creating worker worktree: %w", err)
	}

	var confine *confinedLaunch
	if confined {
		stateDir, err := confinementStateDir()
		if err != nil {
			undoOnFailure()
			return Pending{}, err
		}
		// The frontend creates the worker's hello dir before the unit starts: a
		// bind needs an existing source, and the unit cannot create it.
		handshakeDir := spawnhandshake.SessionDir(stateDir, rp.AbsPath)
		if err := os.MkdirAll(handshakeDir, 0o755); err != nil {
			undoOnFailure()
			return Pending{}, fmt.Errorf("creating the worker's handshake dir: %w", err)
		}
		scopePath, err := writeUnitSpec(rp, desc, targetConfine.egress, stateDir, handshakeDir)
		if err != nil {
			undoOnFailure()
			return Pending{}, err
		}
		confine = &confinedLaunch{scopePath: scopePath, stateDir: stateDir, handshakeDir: handshakeDir}
	}

	p, err := startDetachedEntry(rp, driver, desc, argv, window, sessionEnv, resolved, authorizingGrant, narrowing, shop.PinEntry(merged), confine)
	if err != nil {
		undoOnFailure()
		return Pending{}, err
	}
	return p, nil
}

// embeddedProfileFlag returns the first clown --profile flag in argv's clown
// section (before the literal "--" provider-args separator), or "".
func embeddedProfileFlag(argv []string) string {
	for _, a := range argv {
		switch {
		case a == "--":
			return ""
		case a == "--profile", a == "-profile",
			strings.HasPrefix(a, "--profile="), strings.HasPrefix(a, "-profile="):
			return a
		}
	}
	return ""
}

// driverCwd is the spawning process's working directory; a seam for tests.
var driverCwd = os.Getwd

// driverContextEnforces reports whether [profile-rights] enforce is on in the
// DRIVER's own context: the hierarchy of the repo containing the driver's cwd
// (its main checkout, found from a worktree too), or just the global and
// home-level layers when the driver is outside any repo. Every hierarchy loads
// the global user sweatfile, which is always a trusted layer, so a global true
// holds whatever repo the driver names as the target. The switch is sticky-on,
// so OR-ing this with the target's answer can only widen enforcement.
//
// An unreadable hierarchy fails closed (enforced): the alternative is an agent
// that can corrupt a sweatfile to switch the check off. A cwd that is not in a
// repo is not an error, just "no driver repo".
func driverContextEnforces(home string) bool {
	repo := driverRepo(home)
	h, err := sweatfileio.LoadHierarchy(home, repo)
	if err != nil {
		slog.Warn("could not load the driver's sweatfile hierarchy; treating profile rights as enforced", "repo", repo, "err", err)
		return true
	}
	on, _ := sweatfileio.TrustedEnforceProfileRights(h, home, repo)
	return on
}

// effectiveProfile is the explicit profile parameter, else the merged
// sweatfile's spawn-profile default, else "" (no profile).
func effectiveProfile(explicit string, merged sweatfile.Sweatfile) string {
	if explicit != "" {
		return explicit
	}
	return merged.SessionSpawnProfile()
}

// Launch is the SYNCHRONOUS spawn: LaunchDetached then WaitHello. deadline 0
// means DefaultHelloDeadline. It blocks until the worker's SessionStart hello
// (FDR 0006). On a hello timeout the worktree and its session state are
// intentionally left behind for inspection (`sc close` / `close-child-session`).
// Used by the `sc spawn` CLI and by the async tool's no-clown fallback.
func Launch(home, repoPath string, driver Driver, brief, desc, model, profile string, deadline time.Duration) (Result, error) {
	return LaunchWith(home, repoPath, driver, brief, desc, model, profile, ConfineDefault, deadline)
}

// LaunchWith is Launch with an explicit confinement request.
func LaunchWith(home, repoPath string, driver Driver, brief, desc, model, profile string, req ConfineRequest, deadline time.Duration) (Result, error) {
	p, err := LaunchDetachedWith(home, repoPath, driver, brief, desc, model, profile, req)
	if err != nil {
		return Result{}, err
	}
	return WaitHello(p, deadline)
}

// renderSpawn loads the WORKER repo's sweatfile hierarchy (its harness decides
// the spawn-entry, not the driver's) and renders the detached-harness argv. It
// also returns the hierarchy's [session-entry].env for the exec's environment.
// Unless [session-entry].disable-auto-mode is set, AutoModeArgs are spliced
// into the entry (claude provider only, silently skipped otherwise — see
// SpliceAutoMode) so a spawned worker starts in claude's auto permission mode. When model is non-empty, the resolved provider's model
// flag (from [session-entry.model-flags]) and resolved model value — for the
// "claude" provider, [session-entry.model-ids][alias] (the full model ID,
// built in via sweatfile.GetDefault() for sonnet/opus/haiku/fable); the raw
// alias for any other provider — are ALSO spliced into the entry immediately
// after its literal "--" provider-args separator, via SpliceModelFlag,
// BEFORE {prompt}/{dir} substitution; model == "" leaves that part of the
// entry unmodified. Safe to call before the worktree exists:
// the hierarchy is the global, ancestor and main-checkout layers only, never the
// worker's own worktree sweatfile.
// It also returns the merged config itself, because the caller needs the
// worker repo's [hooks].allow-stale-base before creating the worktree and
// re-loading the hierarchy for one boolean would be a second full walk of the
// sweatfile chain.
//
// The effective profile (explicit parameter, else [session-entry].spawn-profile)
// is spliced LAST, after the auto-mode and model splices, via SpliceProfileFlag:
// those two key off resolveProvider(entry), and placing the profile after them
// keeps their behavior identical to a spawn with no profile.
func renderSpawn(home string, rp worktree.ResolvedPath, brief, model, profile string) (argv, window []string, sessionEnv map[string]string, merged sweatfile.Sweatfile, enforce bool, confine targetConfinement, err error) {
	hierarchy, err := sweatfileio.LoadHierarchy(home, rp.RepoPath)
	if err != nil {
		return nil, nil, nil, merged, false, confine, fmt.Errorf("loading worker sweatfile hierarchy: %w", err)
	}
	// [confinement] follows the same trust rule as [profile-rights].
	var untrustedConfine []string
	confine.policy, confine.egress, untrustedConfine = sweatfileio.TrustedConfinement(hierarchy, home, rp.RepoPath)
	if len(untrustedConfine) > 0 {
		slog.Warn("ignored [confinement] in sweatfiles that are not trusted: only sweatfiles above the repo are trusted",
			"paths", untrustedConfine)
	}
	confine.spawnProfile, confine.ignoredSpawnProfile = sweatfileio.TrustedSpawnProfile(hierarchy, home, rp.RepoPath)
	// The enforcement switch is read from layers above the repo only: an agent
	// can edit the repo's own sweatfile, and must not be able to turn it off.
	enforce, untrusted := sweatfileio.TrustedEnforceProfileRights(hierarchy, home, rp.RepoPath)
	if len(untrusted) > 0 {
		slog.Warn("ignored [profile-rights] in sweatfiles that are not trusted: only sweatfiles above the repo are trusted",
			"paths", untrusted)
	}
	// GetDefault() as the base layer is what actually delivers the built-in
	// [session-entry.model-ids] default (sonnet/opus/haiku/fable) to a spawn
	// whose worker repo has no sweatfile override — same pattern as
	// worktree.go/commands_plugin.go/validate.go/hooks.go. The other fields
	// GetDefault() sets (Git, StartCommands, Claude) don't collide with
	// anything renderSpawn/LaunchDetached reads off merged.
	merged = sweatfile.GetDefault().MergeWith(hierarchy.Merged)

	entry := merged.SessionSpawnEntry()
	if !merged.SessionAutoModeDisabled() {
		entry = SpliceAutoMode(entry)
	}
	if model != "" {
		entry, err = SpliceModelFlag(entry, model, merged.SessionModelFlags(), merged.SessionModelIDs())
		if err != nil {
			return nil, nil, nil, merged, false, confine, err
		}
	}
	if effective := effectiveProfile(profile, merged); effective != "" {
		entry, err = SpliceProfileFlag(entry, effective)
		if err != nil {
			return nil, nil, nil, merged, false, confine, err
		}
	}

	// FDR-0017 Piece 1: spinclass no longer wraps the worker in a multiplexer.
	// The detached-harness argv (clown's --clown-attach=spawn) is *expected* to
	// self-detach, but launchRendered no longer relies on that — startDetached
	// runs it in its own session and never waits on it, so a non-detaching entry
	// cannot wedge the launcher. {id} is not substituted here — the worker
	// derives its identity from SPINCLASS_SESSION_ID in the exec env (workerEnv).
	argv = SubstituteEntry(entry, brief, rp.AbsPath)
	window = SubstituteWindow(merged.SessionSpawnWindow(), rp.SessionKey, rp.AbsPath)
	return argv, window, merged.SessionEnv(), merged, enforce, confine, nil
}

// startDetached launches argv in its OWN session (setsid) with stdio wired to
// logPath — never the launcher's stdin/stdout/stderr — and does NOT wait for it
// to exit. It is the mechanism that makes every spawn "always detach": the
// child neither blocks the launcher (we never wait on its exit) nor tethers the
// launcher's stdio (so `sc spawn` / `sc fork` and the MCP tools return and their
// output pipes close as soon as the hello arrives), and it survives the
// launcher process exiting because it is a new session leader. A well-behaved
// entry (clown --clown-attach=spawn) still forks the worker and exits promptly;
// a misbehaving one that never returns can no longer wedge or tether the
// launcher. The child's own dup of the log fd keeps it writable after the
// launcher closes its copy; the reaper goroutine prevents a zombie in the
// long-lived serve process.
func startDetached(argv []string, dir string, env []string, logPath string) error {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("creating spawn log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("opening spawn log %s: %w", logPath, err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = nil // /dev/null: no controlling stdin
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("starting %q: %w", argv, err)
	}
	_ = logFile.Close() // the child holds its own dup; drop the launcher's copy
	go func() { _ = cmd.Wait() }()
	return nil
}

// launchSpawnWindow opens the configured terminal window onto the worker
// (#149), fire-and-forget: the window is a convenience side effect, so a
// start failure is a logged warning, never a spawn failure. Detached like the
// entry (startDetached) so the window process never tethers the launcher's
// stdio — otherwise `sc spawn` would not exit until the window did.
func launchSpawnWindow(argv []string, rp worktree.ResolvedPath, desc string, sessionEnv map[string]string) {
	if len(argv) == 0 {
		return
	}
	logPath := filepath.Join(rp.AbsPath, ".spinclass", "spawn-window.log")
	if err := startDetached(argv, rp.AbsPath, workerEnv(rp, desc, sessionEnv, false), logPath); err != nil {
		slog.Warn("spawn-window failed to start", "argv", argv, "err", err)
	}
}

// workerEnv builds the spawn exec's process environment. It MIRRORS
// executor.SessionExecutor.Attach's env construction (internal/executor/
// session.go) — keep the var list in sync. Deliberately duplicated rather
// than shared: the executor mutates the driver process env via os.Setenv
// (it execs in-place), while spawn must scope the worker identity to
// cmd.Env so the DRIVER's own SPINCLASS_* vars never leak to the worker
// through a multiplexer that propagates client env.
//
// os/exec keeps the LAST occurrence of a duplicated key, so order encodes
// precedence: inherited driver environ, then user [session-entry].env, then
// the spinclass-owned identity vars (appended last — authoritative).
//
// scrubDesktopEnv additionally drops the display and session-bus variables
// (harnessScrubbedEnvVars) from the inherited environ; the detached harness
// passes true, the spawn-window launch false because a window needs a display.
// An explicit [session-entry].env entry still sets them back, since userEnv is
// appended after the scrub.
func workerEnv(rp worktree.ResolvedPath, desc string, userEnv map[string]string, scrubDesktopEnv bool) []string {
	return workerEnvScoped(rp, desc, userEnv, scrubDesktopEnv, "")
}

// identityVars are the spinclass-owned SPINCLASS_* identity variables of a
// worker; a confined spawn also hands them to the unit through its launch spec.
func identityVars(rp worktree.ResolvedPath, desc string) []string {
	// Split session key ("repo/branch") into individual env vars — same
	// derivation as SessionExecutor.Attach.
	repo, branch := rp.SessionKey, ""
	if i := strings.Index(rp.SessionKey, "/"); i >= 0 {
		repo, branch = rp.SessionKey[:i], rp.SessionKey[i+1:]
	}
	return []string{
		"SPINCLASS_SESSION_ID=" + rp.SessionKey,
		"SPINCLASS_REPO=" + repo,
		"SPINCLASS_BRANCH=" + branch,
		"SPINCLASS_WORKTREE=" + rp.AbsPath,
		"SPINCLASS_DESCRIPTION=" + desc,
	}
}

// workerEnvScoped is workerEnv plus, when scopePath is non-empty, the
// CLOWN_AGENT_SCOPE spec path that makes clown launch the agent in a unit
// (FDR 0035). It is appended last so nothing inherited or configured can name
// a different spec.
func workerEnvScoped(rp worktree.ResolvedPath, desc string, userEnv map[string]string, scrubDesktopEnv bool, scopePath string) []string {
	// Strip the driver's inherited CLOWN_SESSION_ID/CLAUDE_SESSION_ID so the
	// worker's clown re-derives its channel from the worker's own
	// SPINCLASS_SESSION_ID (appended below). Otherwise the worker arms the
	// driver's channel and directed chat wakes to it are dropped (#169).
	env := session.StripInheritedSessionIDs(os.Environ())
	if scrubDesktopEnv {
		env = dropEnvVars(env, harnessScrubbedEnvVars)
	}
	for k, v := range userEnv {
		env = append(env, k+"="+v)
	}

	tmpDir := filepath.Join(rp.AbsPath, ".tmp")

	env = append(env, identityVars(rp, desc)...)
	env = append(env, "TMPDIR="+tmpDir, "CLAUDE_CODE_TMPDIR="+tmpDir)
	if scopePath != "" {
		env = append(env, "CLOWN_AGENT_SCOPE="+scopePath)
	}
	return env
}

// harnessScrubbedEnvVars are the desktop-session variables the detached
// harness does not inherit (FDR 0034, "Where the card root is real"). This is
// hygiene only, not a security boundary: the sockets stay reachable by path
// under the same uid. SSH_AUTH_SOCK and SSH_ASKPASS are deliberately kept —
// workers may reach the agent.
var harnessScrubbedEnvVars = []string{"DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS"}

// dropEnvVars returns env without any KEY=value entry whose KEY is in keys.
func dropEnvVars(env, keys []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if !slices.Contains(keys, k) {
			out = append(out, e)
		}
	}
	return out
}

// newWorkerState builds the worker's pre-hello session state. A non-nil
// narrowing (a user profile accepted as a narrowing of its base builtin) is
// recorded as an unsigned link: a claim, not a signature (session.ProfileNarrowingLink).
func newWorkerState(rp worktree.ResolvedPath, driver Driver, desc string, profile ResolvedProfile, authorizingGrant string, narrowing *narrowingBase) session.State {
	st := session.State{
		PID:                0,
		SessionState:       session.StateActive,
		RepoPath:           rp.RepoPath,
		WorktreePath:       rp.AbsPath,
		Branch:             rp.Branch,
		SessionKey:         rp.SessionKey,
		Description:        desc,
		SpawnedBy:          driver.SessionKey,
		SpawnedByPrincipal: driver.Principal,
		Profile:            profile.Name,
		ProfileDigest:      profile.Digest,
		AuthorizingGrant:   authorizingGrant,
		StartedAt:          time.Now().UTC(),
		Env: map[string]string{
			"SPINCLASS_SESSION_ID": rp.SessionKey,
		},
	}
	if narrowing != nil {
		st.ProfileNarrowing = &session.ProfileNarrowingLink{
			Grantee:       rp.SessionKey,
			Profile:       profile.Name,
			ProfileDigest: profile.Digest,
			Base:          narrowing.Name,
			Prev:          narrowing.Digest,
			Unsigned:      true,
		}
	}
	if driver.Principal != "" {
		st.Holders = []string{driver.Principal}
		// The spawner holds every right (FDR 0032 D13); record it so the
		// record is explicit rather than leaning on the implicit-spawner rule.
		st.HandleRights = map[string]string{driver.Principal: session.AllHandleRights}
	}
	return st
}

// startDetachedEntry writes the worker's pre-hello session state and execs the
// detached harness entry, returning a Pending (worktree booting, hello not yet
// awaited). The SessionStart hook adopts the state (PID + HelloSentAt) when the
// worker boots; pre-hello PID is 0 (nothing attached yet).
func startDetachedEntry(rp worktree.ResolvedPath, driver Driver, desc string, argv, window []string, sessionEnv map[string]string, profile ResolvedProfile, authorizingGrant string, narrowing *narrowingBase, entry *session.PinnedEntry, confine *confinedLaunch) (Pending, error) {
	st := newWorkerState(rp, driver, desc, profile, authorizingGrant, narrowing)
	// The worker's own `sc resume` replays the entry resolved here, at creation,
	// from the operator-controlled layers; it never re-reads a sweatfile.
	st.Entry = entry
	if err := session.Write(st); err != nil {
		return Pending{}, fmt.Errorf("writing worker session state: %w", err)
	}

	// Captured BEFORE the exec so a hello racing worker startup still satisfies
	// the gate (WaitHello reads it back off the Pending).
	startTime := time.Now()

	// spinclass GUARANTEES detachment rather than trusting the spawn-entry to
	// self-detach: startDetached launches the entry in its own session with
	// stdio redirected to a log and never waits for it to exit. Readiness is
	// proven SOLELY by the hello handshake (WaitHello, bounded by deadline), so
	// an entry that never returns — a blocking direnv build, a foregrounded
	// clown/posh attach — can no longer wedge the launcher. A well-behaved
	// clown --clown-attach=spawn still forks the worker and exits promptly.
	logPath := filepath.Join(rp.AbsPath, ".spinclass", "spawn.log")
	scopePath := ""
	if confine != nil {
		scopePath = confine.scopePath
	}
	if err := startDetached(argv, rp.AbsPath, workerEnvScoped(rp, desc, sessionEnv, true, scopePath), logPath); err != nil {
		return Pending{}, fmt.Errorf("spawn template %q failed: %w", argv, err)
	}

	return Pending{
		SessionKey:   rp.SessionKey,
		WorktreePath: rp.AbsPath,
		RepoPath:     rp.RepoPath,
		Branch:       rp.Branch,
		rp:           rp,
		driver:       driver,
		desc:         desc,
		startTime:    startTime,
		window:       window,
		sessionEnv:   sessionEnv,
		confine:      confine,
	}, nil
}

// WaitHello blocks on the pending worker's SessionStart hello (bounded by
// deadline; 0 means DefaultHelloDeadline), then opens the spawn-window and
// returns the Result. Called inline by Launch (sync) or in a background job by
// the async spawn tool (spinclass#266). On timeout it returns the handshake
// error and opens no window (a failed spawn shows no window).
func WaitHello(p Pending, deadline time.Duration) (Result, error) {
	if deadline <= 0 {
		deadline = DefaultHelloDeadline
	}

	// A confined worker's principal is bound to its session from the unit record
	// clown writes, so the agent never names itself (FDR 0035). The bind comes
	// BEFORE the hello wait: it needs only that record, and a late or lost hello
	// must not leave a running confined worker unbound (the first real confined
	// spawn timed out at the hello and never bound).
	if p.confine != nil {
		if err := bindPrincipalWhenRecorded(p.confine.stateDir, p.rp.SessionKey, p.rp.AbsPath, p.startTime); err != nil {
			return Result{}, fmt.Errorf("confined worker %s was launched but its principal is not bound to the session: %w", p.rp.SessionKey, err)
		}
	}

	// The window opens AFTER the hello (not before) so it can attach to the
	// now-ready session: the posh id is a crypto-random UUID unknowable until the
	// worker boots (direction B), so {attach-id} can only be resolved here.
	helloDir := spawnhandshake.SharedDir()
	if p.confine != nil && p.confine.handshakeDir != "" {
		helloDir = p.confine.handshakeDir // the worker's own bound dir (a unit cannot write the shared one)
	}
	poshID, err := spawnhandshake.WaitForHelloIn(helloDir, p.driver.Principal, p.rp.SessionKey, p.startTime, deadline)
	if err != nil {
		return Result{}, err
	}

	launchSpawnWindow(substituteAttachID(p.window, poshID), p.rp, p.desc, p.sessionEnv)

	return Result{
		SessionKey:    p.rp.SessionKey,
		WorktreePath:  p.rp.AbsPath,
		MultiplexerID: p.rp.SessionKey,
		PoshSessionID: poshID,
	}, nil
}

// SpawnLogActiveWithin reports whether the worker's spawn.log was written within
// the given window — a best-effort "is the worker still booting?" signal for the
// async spawn's reap-if-dead timeout policy (spinclass#266). spinclass does not
// track the worker PID (it guarantees detachment; the entry exits promptly), so
// this log-activity heuristic is the spinclass-only liveness proxy. It errs
// toward "active" (a missing/unreadable log reads as NOT active → reapable):
// recent writes mean the harness is still doing work, so the worker is not
// yanked mid-boot.
func SpawnLogActiveWithin(worktreePath string, within time.Duration) bool {
	info, err := os.Stat(filepath.Join(worktreePath, ".spinclass", "spawn.log"))
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) <= within
}
