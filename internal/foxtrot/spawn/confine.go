package spawn

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/0/spawnhandshake"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	"code.linenisgreat.com/spinclass/internal/charlie/worktree"
)

// Seams for tests; production values are the real host.
var (
	// confinementHost is how the spawn reads /proc/self/cgroup and the unit's
	// main process environment.
	confinementHost confinement.System = confinement.Host{}
	// agentScopeCapabilitiesArgv is the capability query (FDR 0035, "Interface
	// with clown"); argv[0] is resolved on PATH.
	agentScopeCapabilitiesArgv = []string{"clown", "agent-scope-capabilities"}
	// confinementStateDir is the XDG state base for specs, unit records and
	// principal mappings.
	confinementStateDir = confinement.DefaultStateDir
	// gitCommonDir resolves the shared git dir of a worktree.
	gitCommonDir = func(worktree string) (string, error) {
		out, err := git.Run(worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
		return strings.TrimSpace(out), err
	}
	// recordPrincipal binds the worker's principal to its session after the hello.
	recordPrincipal = confinement.RecordPrincipalFromUnit
)

// principalBindWait and principalBindPoll bound how long the spawn waits for
// clown to write the unit record. Clown writes it from a goroutine after the
// provider has started inside the unit, so it is not guaranteed to land before
// claude's hello.
var (
	// Clown waits up to 2 minutes for the provider to start before it writes the
	// record (or an error record); the extra 30s is margin for the rename.
	principalBindWait = 150 * time.Second
	principalBindPoll = 200 * time.Millisecond
)

// bindPrincipalWhenRecorded binds the worker's principal once its unit record
// exists and was written after the launch began. A record absent, still an
// earlier launch's (older than since), or lacking a principal is retried until
// the deadline; the last error is returned then.
func bindPrincipalWhenRecorded(stateDir, sessionKey, worktree string, since time.Time) error {
	deadline := time.Now().Add(principalBindWait)
	for {
		err := bindPrincipalOnce(stateDir, sessionKey, worktree, since)
		if err == nil || errors.Is(err, confinement.ErrUnitLaunchFailed) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(principalBindPoll)
	}
}

func bindPrincipalOnce(stateDir, sessionKey, worktree string, since time.Time) error {
	if fi, err := os.Stat(confinement.UnitRecordPath(stateDir, worktree)); err == nil && fi.ModTime().Before(since) {
		return fmt.Errorf("the unit record predates this launch (%s): waiting for clown to rewrite it", fi.ModTime().Format(time.RFC3339))
	}
	_, err := recordPrincipal(confinementHost, stateDir, sessionKey, worktree)
	return err
}

// confinedLaunch is what a confined spawn carries from the spec write to the
// hello: the spec path handed to clown and the state dir the principal mapping
// goes under.
type confinedLaunch struct {
	scopePath string
	stateDir  string
	// handshakeDir is the per-session directory bound read-write into the unit
	// for the worker's hello (spawnhandshake.SessionDir); the driver reads it.
	handshakeDir string
}

// targetConfinement is the [confinement] answer for the worker repo.
type targetConfinement struct {
	policy sweatfileio.ConfinementPolicy
	egress []string
	// spawnProfile is [session-entry].spawn-profile read from layers above the
	// repo only, the default a confined spawn may use; ignoredSpawnProfile are
	// the repo/worktree layers that set it and were not honoured.
	spawnProfile        string
	ignoredSpawnProfile []string
}

// driverRepo is the repo whose hierarchy describes the DRIVER's own context:
// the repo containing the driver's cwd, else home.
func driverRepo(home string) string {
	if cwd, err := driverCwd(); err == nil {
		if root, err := git.DetectRepo(cwd); err == nil {
			return root
		}
	}
	return home
}

// driverContextConfinement is the [confinement] policy of the DRIVER's own
// context, the analogue of driverContextEnforces: resolveConfined ORs it with
// the target's, so a confined driver context makes its children confined, and
// an unreadable hierarchy fails closed (enabled, confined by default).
func driverContextConfinement(home string) sweatfileio.ConfinementPolicy {
	repo := driverRepo(home)
	h, err := sweatfileio.LoadHierarchy(home, repo)
	if err != nil {
		slog.Warn("could not load the driver's sweatfile hierarchy; treating confinement as on", "repo", repo, "err", err)
		return sweatfileio.ConfinementPolicy{Enabled: true, DefaultSpawn: true}
	}
	policy, _, _ := sweatfileio.TrustedConfinement(h, home, repo)
	return policy
}

// confinedPreflight runs before any worktree exists, so a refusal litters
// nothing.
func confinedPreflight(sys confinement.System, capabilitiesArgv []string) error {
	inside, err := confinement.InsideAgentUnit(sys)
	if err != nil || inside {
		return errors.New("cannot spawn confined from inside an agent unit: the child's frontend would start inside this unit (FDR 0032 D7); " +
			"spawn through the spawn-session MCP tool, which runs in the frontend")
	}
	have, err := clownCapabilities(capabilitiesArgv)
	if err != nil {
		return fmt.Errorf("cannot spawn confined: could not query clown's agent-scope capabilities (%v); missing: %s; "+
			"the switch waits for a clown build that reports them (FDR 0035)", err, strings.Join(confinement.RequiredCapabilities, ", "))
	}
	if missing := confinement.MissingCapabilities(have); len(missing) > 0 {
		return fmt.Errorf("cannot spawn confined: clown does not report the required capabilities: %s; "+
			"the switch waits for a clown build that reports them (FDR 0035)", strings.Join(missing, ", "))
	}
	return nil
}

// clownCapabilities runs the capability query and returns the reported list.
func clownCapabilities(argv []string) ([]string, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	var resp struct {
		Version      int      `json:"version"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("unparsable output: %w", err)
	}
	if resp.Version != 1 {
		return nil, fmt.Errorf("capability report version %d, want 1", resp.Version)
	}
	return resp.Capabilities, nil
}

// unitSocket and unitSpec are the clown FDR 0020 v1 launch spec. clown refuses
// unknown fields, so these keys are exactly the contract.
type unitSocket struct {
	Path    string `json:"path"`
	Purpose string `json:"purpose"`
}

type unitSpec struct {
	Version      int          `json:"version"`
	Worktree     string       `json:"worktree"`
	OperatorHome string       `json:"operator_home"`
	UnitNameHint string       `json:"unit_name_hint"`
	ResultPath   string       `json:"result_path"`
	BindsRW      []string     `json:"binds_rw"`
	BindsRO      []string     `json:"binds_ro"`
	Sockets      []unitSocket `json:"sockets,omitempty"`
	DenyPaths    []string     `json:"deny_paths"`
	// EgressAllow is a pointer so "unset" (omitted: clown's own default list
	// applies) differs from an explicit empty list (deny everything).
	EgressAllow *[]string         `json:"egress_allow,omitempty"`
	Env         map[string]string `json:"env"`
}

// unitSpecInput is everything buildUnitSpec needs, resolved by the caller so
// the builder itself touches no host state.
type unitSpecInput struct {
	rp           worktree.ResolvedPath
	operatorHome string
	stateDir     string
	commonDir    string
	sshAuthSock  string
	runtimeDir   string   // /run/user/<uid>
	wayland      []string // existing wayland-* entries under runtimeDir
	egress       []string
	env          map[string]string
	handshakeDir string // per-session hello dir, bound rw; "" binds nothing
}

var unitNameUnsafe = regexp.MustCompile(`[^a-zA-Z0-9-]`)

func buildUnitSpec(in unitSpecInput) unitSpec {
	wt := in.rp.AbsPath
	spec := unitSpec{
		Version:      1,
		Worktree:     wt,
		OperatorHome: in.operatorHome,
		UnitNameHint: "spinclass-agent-" + unitNameUnsafe.ReplaceAllString(in.rp.SessionKey, "-"),
		ResultPath:   confinement.UnitRecordPath(in.stateDir, wt),
		BindsRW: []string{
			wt,
			filepath.Join(in.commonDir, "objects"),
			filepath.Join(in.commonDir, "worktrees", filepath.Base(wt)),
		},
		BindsRO: []string{
			filepath.Join(in.commonDir, "config"),
			filepath.Join(in.commonDir, "hooks"),
			// refs and logs are readable (git must resolve refs) but not
			// writable: a ref the unit creates could be pushed by raw git with
			// the session credential. Ref writes go through grit (the frontend).
			filepath.Join(in.commonDir, "refs"),
			filepath.Join(in.commonDir, "logs"),
			// core.hooksPath points here (the per-commit hook, FDR 0019) and
			// sits inside the rw worktree bind: left writable, the unit could
			// rewrite code the frontend's git later runs (F9).
			filepath.Join(wt, ".spinclass", "hooks"),
			// The frontend runs grit's git with the session worktree as its
			// cwd, so git discovers the repo through these two unit-writable
			// files and would run whatever commands they configure
			// (core.fsmonitor, core.sshCommand, credential.helper=!cmd, ...)
			// outside the unit. config.worktree is nested RO over the rw
			// worktrees/<name> dir; the .git pointer file over the rw worktree.
			// Both must exist when the unit starts (writeUnitSpec ensures it).
			filepath.Join(in.commonDir, "worktrees", filepath.Base(wt), "config.worktree"),
			filepath.Join(wt, ".git"),
		},
		DenyPaths: append([]string{
			filepath.Join(in.runtimeDir, "bus"),
			filepath.Join(in.runtimeDir, "systemd"),
			filepath.Join(in.runtimeDir, "posh"),
			"/tmp/.X11-unix",
		}, in.wayland...),
		Env: in.env,
	}
	// The minted credential is not in the unit's tree at all: auth writes it
	// to frontend state (confinement.CredentialPath), hidden by ProtectHome, and
	// only grit's helper in the frontend reads it. A deny entry on a file inside
	// the rw worktree bind was lost when the frontend replaced that file (round
	// K, FDR 0035), so there is none.
	if in.egress != nil {
		list := append([]string{}, in.egress...)
		spec.EgressAllow = &list
	}
	if in.handshakeDir != "" {
		// The unit's state dir is not the frontend's, so the worker's hello
		// goes to a directory of its own bound at the same path; only this one
		// directory is writable, so a unit cannot fake a sibling's hello.
		spec.BindsRW = append(spec.BindsRW, in.handshakeDir)
		env := map[string]string{}
		maps.Copy(env, in.env)
		env[spawnhandshake.DirEnv] = in.handshakeDir
		spec.Env = env
	}
	if in.sshAuthSock != "" {
		spec.Sockets = []unitSocket{{Path: in.sshAuthSock, Purpose: "piggy-agent"}}
	}
	return spec
}

// ensureWorktreeConfigFile creates <common>/worktrees/<name>/config.worktree
// (empty) when absent. auth.Inject writes it when a credential is minted, but a
// session that never minted has none, and the unit spec binds that path
// read-only: a bind source that does not exist would fail the unit start. An
// existing file is left untouched (O_APPEND without truncation).
func ensureWorktreeConfigFile(commonDir, wt string) error {
	dir := filepath.Join(commonDir, "worktrees", filepath.Base(wt))
	f, err := os.OpenFile(filepath.Join(dir, "config.worktree"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("ensuring the worktree's config.worktree exists for its read-only bind: %w", err)
	}
	return f.Close()
}

// writeUnitSpec resolves the host-dependent inputs, builds the spec and writes
// it beside the unit record, outside the worktree. It returns the path to put
// in CLOWN_AGENT_SCOPE.
func writeUnitSpec(rp worktree.ResolvedPath, desc string, egress []string, stateDir, handshakeDir string) (string, error) {
	commonDir, err := gitCommonDir(rp.AbsPath)
	if err != nil || commonDir == "" {
		return "", fmt.Errorf("resolving the git common dir of %s: %v", rp.AbsPath, err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the operator's home: %w", err)
	}
	if err := ensureWorktreeConfigFile(commonDir, rp.AbsPath); err != nil {
		return "", err
	}
	runtimeDir := fmt.Sprintf("/run/user/%d", confinementHost.Getuid())
	wayland, _ := filepath.Glob(filepath.Join(runtimeDir, "wayland-*"))

	env := map[string]string{}
	for _, kv := range identityVars(rp, desc) {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	spec := buildUnitSpec(unitSpecInput{
		rp: rp, operatorHome: home, stateDir: stateDir, commonDir: commonDir,
		sshAuthSock: os.Getenv("SSH_AUTH_SOCK"), runtimeDir: runtimeDir, wayland: wayland,
		egress: egress, env: env, handshakeDir: handshakeDir,
	})
	path, err := confinement.WriteUnitSpec(stateDir, rp.AbsPath, spec)
	if err != nil {
		return "", fmt.Errorf("writing the agent unit spec: %w", err)
	}
	return path, nil
}
