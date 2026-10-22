package spawn

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// A long-lived serve keeps running the binary it started with after a deploy
// replaces the one on PATH, and would keep writing unit specs with the old
// code (spinclass#399). A confined spawn compares the two first.
//
// Two store paths can still be the same build: the clown plugin builds
// spinclass as its own derivation (lib.mkSpinclass, with madder/direnv pins)
// apart from the plain package on PATH. So a differing path is judged by the
// build identity (version plus commit) before it counts as stale.

// buildIdentityTimeout bounds `<onPath> version`.
const buildIdentityTimeout = 5 * time.Second

// Seams for tests.
var (
	runningExecutable = os.Executable
	lookPathSpinclass = func() (string, error) { return exec.LookPath("spinclass") }
	runningBuild      = func() Build { return setRunningBuild }
	readBuildIdentity = readBuildIdentityByExec
)

// setRunningBuild is this process's own build, set once by main at serve start.
var setRunningBuild Build

// SetRunningBuild records this process's version and commit (main.version and
// main.commit, injected at link time).
func SetRunningBuild(version, commit string) {
	setRunningBuild = Build{Version: version, Commit: commit}
}

// Build is a spinclass build identity.
type Build struct {
	Version, Commit string
}

// Known reports whether the build names a real commit. "unknown" (a devshell
// build) and "dev" never count, so two such builds are never equal.
func (b Build) Known() bool {
	switch b.Commit {
	case "", "unknown", "dev":
		return false
	}
	return b.Version != ""
}

// String renders version+commit.
func (b Build) String() string { return b.Version + "+" + b.Commit }

// ServeBinary is the binary this process runs and the spinclass it would exec
// now, both with symlinks resolved (a profile path resolves to its store path).
// The builds are filled only when the paths differ; IdentityErr is set when
// the on-PATH build could not be read.
type ServeBinary struct {
	Running, OnPath           string
	RunningBuild, OnPathBuild Build
	IdentityErr               error
}

// SameBuildDifferentPath reports two paths that are one known build.
func (b ServeBinary) SameBuildDifferentPath() bool {
	return b.Running != b.OnPath && b.IdentityErr == nil &&
		b.RunningBuild.Known() && b.RunningBuild == b.OnPathBuild
}

// Stale reports whether the running binary differs from the one on PATH in a
// way that is not provably the same build.
func (b ServeBinary) Stale() bool {
	return b.Running != b.OnPath && !b.SameBuildDifferentPath()
}

// CurrentServeBinary resolves both paths; an error means freshness cannot be
// judged. A running binary that was deleted (the old store path collected)
// resolves with its " (deleted)" marker kept, so it reads as stale.
func CurrentServeBinary() (ServeBinary, error) {
	running, err := runningExecutable()
	if err != nil {
		return ServeBinary{}, fmt.Errorf("resolving the running binary: %w", err)
	}
	if !strings.HasSuffix(running, " (deleted)") {
		if running, err = filepath.EvalSymlinks(running); err != nil {
			return ServeBinary{}, fmt.Errorf("resolving the running binary: %w", err)
		}
	}
	onPath, err := lookPathSpinclass()
	if err != nil {
		return ServeBinary{}, fmt.Errorf("finding spinclass on PATH: %w", err)
	}
	if onPath, err = filepath.EvalSymlinks(onPath); err != nil {
		return ServeBinary{}, fmt.Errorf("resolving spinclass on PATH: %w", err)
	}
	b := ServeBinary{Running: running, OnPath: onPath}
	if running != onPath {
		b.RunningBuild = runningBuild()
		b.OnPathBuild, b.IdentityErr = readBuildIdentity(onPath)
	}
	return b, nil
}

// readBuildIdentityByExec runs `<path> version` and parses its table.
func readBuildIdentityByExec(path string) (Build, error) {
	ctx, cancel := context.WithTimeout(context.Background(), buildIdentityTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return Build{}, err
	}
	return parseVersionTable(string(out))
}

// parseVersionTable finds the spinclass row of the `version` table
// ("spinclass-<ver>/spinclass  <ver>+<commit>  <commit>") and splits its
// second column at the last "+".
func parseVersionTable(out string) (Build, error) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "spinclass-") || !strings.HasSuffix(fields[0], "/spinclass") {
			continue
		}
		i := strings.LastIndex(fields[1], "+")
		if i <= 0 || i == len(fields[1])-1 {
			return Build{}, fmt.Errorf("unparseable version %q in the version table", fields[1])
		}
		return Build{Version: fields[1][:i], Commit: fields[1][i+1:]}, nil
	}
	return Build{}, fmt.Errorf("no spinclass row in the version output")
}

// refuseStaleServe fails closed: a confined spawn proceeds only when the
// running binary is the one on PATH, or provably the same build.
func refuseStaleServe() error {
	b, err := CurrentServeBinary()
	if err != nil {
		return fmt.Errorf("cannot spawn confined: cannot tell whether this serve runs the current spinclass (%v); restart the session (spinclass#399)", err)
	}
	if b.SameBuildDifferentPath() {
		slog.Info("serve binary differs from spinclass on PATH but is the same build",
			"running", b.Running, "onPath", b.OnPath, "build", b.RunningBuild.String())
		return nil
	}
	if b.IdentityErr != nil {
		return fmt.Errorf("cannot spawn confined: this serve is stale or unverifiable: it runs %s (%s) and spinclass on PATH is %s, "+
			"but cannot read the build identity of %s: %v; restart the session (spinclass#399)",
			b.Running, b.RunningBuild, b.OnPath, b.OnPath, b.IdentityErr)
	}
	if b.Stale() {
		return fmt.Errorf("cannot spawn confined: this serve is stale: it runs %s (%s) but spinclass on PATH is %s (%s); "+
			"restart the session (or reconnect its spinclass MCP) so the unit spec is written by the current code (spinclass#399)",
			b.Running, b.RunningBuild, b.OnPath, b.OnPathBuild)
	}
	return nil
}
