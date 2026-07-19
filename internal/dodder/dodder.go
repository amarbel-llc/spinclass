// Package dodder wraps the per-worktree dodder repository init flow.
// The dodder binary path is supplied at build time via internal/embeds
// + lib.mkSpinclass; when empty, every operation here is a no-op.
//
// A dodder repository is layered over the per-worktree madder blob
// store from FDR 0003: when that store already exists, dodder reuses it
// via `-blob_store-id .default`; otherwise dodder's own embedded madder
// creates a default store. See FDR 0008.
package dodder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/madder"
)

// Public contract knobs, mirroring internal/madder. ExcludePattern
// lands in `.git/info/exclude` and AllowRule lands in Claude Code's
// permission allow-list when dodder activation runs.
const (
	ExcludePattern = ".dodder/"
	AllowRule      = "Bash(dodder:*)"
)

// configSeedRel is the marker file dodder writes on a successful init
// (`dodder init` is not idempotent — it fails on a second run, the same
// way madder's does), so spinclass checks for this file before invoking
// init. Verified via `just explore-dodder-init-plain`.
const configSeedRel = ".dodder/local/share/config-seed"

// repoIDUnsafe matches characters not allowed in a derived repo-id; they
// are collapsed to '-'.
var repoIDUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// RepoReady reports whether the per-worktree dodder repository at
// worktreePath has already been initialised.
func RepoReady(worktreePath string) bool {
	_, err := os.Stat(filepath.Join(worktreePath, configSeedRel))
	return err == nil
}

// Init initialises the per-worktree dodder repository at worktreePath
// using dodderBin as the dodder binary. Skipped if dodderBin is empty
// or the repo is already ready.
//
// The repository signs with the user's existing agent key (resolved via
// `dodder info-ssh_agent`); a locked or empty agent is a hard error so
// no divergent per-worktree identity is generated (FDR 0008). When the
// madder default store already exists at worktreePath it is reused via
// `-blob_store-id .default`; otherwise dodder's embedded madder creates
// its own default store.
//
// Both DODDER_CEILING_DIRECTORIES and MADDER_CEILING_DIRECTORIES are
// scoped to the init invocation so dodder cannot walk up into a parent
// repo's .dodder/ or .madder/ during discovery; exporting them into the
// session would be too broad.
func Init(worktreePath, dodderBin, madderBin string) error {
	if dodderBin == "" {
		return nil
	}
	if RepoReady(worktreePath) {
		return nil
	}

	key, err := resolvePrivateKey(worktreePath, dodderBin)
	if err != nil {
		return err
	}

	args := []string{
		"init",
		"-encryption", "none",
		"-repo_id", ".",
		"-private_key", key,
	}
	if madder.StoreReady(worktreePath) {
		args = append(args, "-blob_store-id", ".default")
	}
	args = append(args, deriveRepoID(worktreePath))

	cmd := exec.Command(dodderBin, args...)
	cmd.Dir = worktreePath
	cmd.Env = ceilingEnv(worktreePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dodder init: %w\n%s", err, out)
	}
	return nil
}

// resolvePrivateKey returns the markl.Id of the key spinclass should
// sign the per-worktree repo with, taken from the agent dodder sees.
//
// It runs `dodder info-ssh_agent` and returns the first whitespace
// token of the first non-empty stdout line. An error from the command
// (e.g. a locked agent) or empty output is returned as an error, which
// the caller treats as a hard failure.
//
// Limitation (FDR 0008 followup): when the agent serves multiple keys
// this takes the first one. `info-pivy_agent` and/or a sweatfile-
// configured key id are the planned disambiguators.
func resolvePrivateKey(worktreePath, dodderBin string) (string, error) {
	cmd := exec.Command(dodderBin, "info-ssh_agent")
	cmd.Dir = worktreePath
	cmd.Env = ceilingEnv(worktreePath)
	out, err := cmd.Output()
	if err != nil {
		msg := ""
		if ee, ok := err.(*exec.ExitError); ok {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf(
			"dodder info-ssh_agent: %w\n%s\n(is pivy-agent unlocked?)",
			err, msg,
		)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf(
		"dodder info-ssh_agent returned no keys; unlock pivy-agent and retry",
	)
}

// ceilingEnv returns the process environment with both ceiling vars
// pinned to worktreePath, bounding dodder's and madder's store/repo
// discovery walk-up.
func ceilingEnv(worktreePath string) []string {
	return append(
		os.Environ(),
		"DODDER_CEILING_DIRECTORIES="+worktreePath,
		"MADDER_CEILING_DIRECTORIES="+worktreePath,
	)
}

// deriveRepoID turns the worktree directory name into a dodder repo-id,
// collapsing disallowed characters to '-'.
func deriveRepoID(worktreePath string) string {
	base := filepath.Base(worktreePath)
	id := repoIDUnsafe.ReplaceAllString(base, "-")
	id = strings.Trim(id, "-")
	if id == "" {
		return "spinclass-worktree"
	}
	return id
}

// LinkInto atomically (re)points `<binDir>/dodder` at binPath so the
// build-time-pinned binary is reachable via PATH inside session shells
// and tools that can't see the burned-in absolute path. Callers wire
// binDir to a directory already on the session PATH (e.g.
// `<git-common-dir>/spinclass/bin/`).
//
// No-op when binPath is empty. Uses tempfile+rename so concurrent
// invocations don't race on a partial-state path.
//
// NB: a near-duplicate of madder.LinkInto; consolidating the two behind
// a shared shim helper is an FDR 0008 followup, intentionally deferred.
func LinkInto(binDir, binPath string) error {
	if binPath == "" {
		return nil
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("creating shim dir: %w", err)
	}

	link := filepath.Join(binDir, "dodder")
	tmpName := filepath.Join(binDir, fmt.Sprintf(".tmp-dodder-%d-%d", os.Getpid(), time.Now().UnixNano()))
	if err := os.Symlink(binPath, tmpName); err != nil {
		return fmt.Errorf("creating temp symlink: %w", err)
	}
	if err := os.Rename(tmpName, link); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("renaming temp to %s: %w", link, err)
	}
	return nil
}
