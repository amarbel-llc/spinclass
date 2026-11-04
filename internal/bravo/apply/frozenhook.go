package apply

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/embeds"
	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/0/safeio"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/landing"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// The per-commit hook's formatter is frozen when the hook is installed (FDR
// 0019, spinclass#459): its leading word is resolved to the absolute path the
// LANDED default branch's devShell provides, by evaluating that branch's flake
// at its current commit (the base a session is cut from, FDR 0024), never the
// branch the session goes on to commit. The dispatcher then runs that path and
// never evaluates the worktree flake at commit time, so a branch cannot change
// what formats its own commits and the hook never fetches an input on the
// branch's behalf (the #449 canary's leak). The path is re-resolved whenever
// the hook is re-installed: sc start, spawn, resurrect, rebuild, and a resume
// whose recorded base has moved (shop.Attach). Every failure leaves the bare
// name with a reason, as before: a formatter is never allowed to block a
// session.

// hookRecordName is the display sidecar, under .spinclass/, of the session's
// FrozenHook record. The frontend copies it into the authoritative session
// record right after the install that wrote it; it is read as data, never
// trusted on its own (the worktree is the worker's).
const hookRecordName = "precommit-hook.json"

// frozenHookTimeout bounds the one devShell evaluation of the base. The same
// devShell is what direnv loads when the session attaches, so a cold cache
// pays here what it would have paid there; a variable so a test can shorten it.
var frozenHookTimeout = 15 * time.Minute

// resolveFormatterPath evaluates the base's devShell and returns the absolute
// path it provides for bin. A seam: tests stand in a function, since the
// sandbox has no nix and no devShell.
var resolveFormatterPath = resolveFormatterPathViaNix

// resolveFrozenHook resolves cmd's formatter for the worktree's session: the
// landed default branch's commit is the base, its committed flake.nix and
// flake.lock must exist and the lock must name no local input, and the
// devShell at that commit must provide the leading word. Any failure yields an
// unfrozen record with the bare name and the reason.
func resolveFrozenHook(worktreePath, cmd string) session.FrozenHook {
	rec := session.FrozenHook{Command: cmd, Bin: leadingWord(cmd), At: time.Now().UTC()}
	unfrozen := func(reason string) session.FrozenHook {
		rec.Frozen = false
		rec.Reason = reason
		return rec
	}
	if filepath.IsAbs(rec.Bin) {
		rec.Frozen = true
		return rec
	}
	repoPath, err := repoRootOf(worktreePath)
	if err != nil {
		return unfrozen("could not locate the repository root: " + err.Error())
	}
	ref, ok := landing.DefaultBranchRef(repoPath)
	if !ok {
		return unfrozen("no default branch could be resolved, so there is no landed base to freeze from")
	}
	sha, err := git.RevParse(repoPath, ref)
	if err != nil {
		return unfrozen(fmt.Sprintf("could not resolve %s: %v", baseLabel(ref), err))
	}
	rec.BaseSha = sha
	rec.BaseLabel = baseLabel(ref)
	if _, found, err := git.FileAtRev(repoPath, sha, "flake.nix"); err != nil || !found {
		return unfrozen(rec.BaseLabel + " has no flake.nix at " + shortSha(sha) + ", so there is no devShell to resolve the formatter from")
	}
	lock, found, err := git.FileAtRev(repoPath, sha, "flake.lock")
	if err != nil || !found {
		return unfrozen(rec.BaseLabel + " has no flake.lock beside its flake.nix at " + shortSha(sha) + "; the formatter is not resolved from an unlocked flake")
	}
	if reason := lockNamesLocalInput(lock); reason != "" {
		return unfrozen(rec.BaseLabel + "'s flake.lock at " + shortSha(sha) + " " + reason + "; the formatter is not resolved from a flake that reaches the local filesystem")
	}
	ctx, cancel := context.WithTimeout(context.Background(), frozenHookTimeout)
	defer cancel()
	path, err := resolveFormatterPath(ctx, repoPath, sha, rec.Bin)
	if err != nil {
		return unfrozen(fmt.Sprintf("evaluating %s's devShell at %s for %s failed: %v", rec.BaseLabel, shortSha(sha), rec.Bin, err))
	}
	if !filepath.IsAbs(path) {
		return unfrozen(fmt.Sprintf("%s's devShell at %s resolved %s to %q, not an absolute path", rec.BaseLabel, shortSha(sha), rec.Bin, path))
	}
	rec.Bin = path
	rec.Frozen = true
	return rec
}

// resolveFormatterPathViaNix runs `nix develop` on the repository's own git
// history at sha (git+file, so committed, landed content is evaluated, never a
// working tree) and asks the devShell where bin is. The lock is neither
// updated nor written: a base whose lock does not match its flake.nix fails
// here rather than being re-locked. The build's gate nix pin (spinclass#454)
// is used when there is one, else the frontend's PATH.
func resolveFormatterPathViaNix(ctx context.Context, repoPath, sha, bin string) (string, error) {
	nix := embeds.GateTool("nix")
	if nix == "" {
		var err error
		if nix, err = exec.LookPath("nix"); err != nil {
			return "", errors.New("nix is not on PATH and this build pins none")
		}
	}
	argv := nixDevelopArgv(nix, repoPath, sha, bin)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = repoPath
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w%s", err, stderrTail(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// nixDevelopArgv is the exact command resolveFormatterPathViaNix runs; a
// function so its shape is tested without nix.
func nixDevelopArgv(nix, repoPath, sha, bin string) []string {
	return []string{
		nix, "develop", "git+file://" + repoPath + "?rev=" + sha,
		"--no-update-lock-file", "--no-write-lock-file",
		"--command", "sh", "-c", `command -v -- "$1"`, "sh", bin,
	}
}

// lockNamesLocalInput says which lock node points at the local filesystem (a
// path type or attribute, or a file-scheme url, in its locked or original
// reference), or "" when none does. The guarded gate's allow-list
// (internal/charlie/check) stays the gate's; this is the one check a devShell
// evaluation of the landed base needs, since nix would copy such an input into
// the world-readable store.
func lockNamesLocalInput(raw []byte) string {
	var lock struct {
		Nodes map[string]struct {
			Locked   map[string]any `json:"locked"`
			Original map[string]any `json:"original"`
		} `json:"nodes"`
		Root string `json:"root"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		return "could not be parsed as a lock"
	}
	root := lock.Root
	if root == "" {
		root = "root"
	}
	for name, node := range lock.Nodes {
		if name == root {
			continue
		}
		for _, ref := range []map[string]any{node.Locked, node.Original} {
			if isLocalRef(ref) {
				return "names input " + name + " on the local filesystem"
			}
		}
	}
	return ""
}

func isLocalRef(ref map[string]any) bool {
	if ref == nil {
		return false
	}
	if typ, _ := ref["type"].(string); typ == "path" {
		return true
	}
	if _, has := ref["path"]; has {
		return true
	}
	url, _ := ref["url"].(string)
	return strings.HasPrefix(strings.ToLower(url), "file:")
}

// FrozenHookStale reports whether rec no longer describes the hook the
// session should have: no record, a hook that was not frozen (the next
// install may manage it), or a landed default branch that has moved past the
// base it was frozen from. The reason is one line for the resume notice and
// `sc rebuild --check`. A caller checks that the hook is active first.
func FrozenHookStale(rec *session.FrozenHook, repoPath string) (stale bool, reason string) {
	if rec == nil {
		return true, "no frozen pre-commit hook recorded (the session predates freezing, spinclass#459)"
	}
	if !rec.Frozen {
		return true, "the pre-commit hook was not frozen (" + rec.Reason + ")"
	}
	if rec.BaseSha == "" {
		return false, ""
	}
	ref, ok := landing.DefaultBranchRef(repoPath)
	if !ok {
		return false, ""
	}
	cur, err := git.RevParse(repoPath, ref)
	if err != nil || cur == rec.BaseSha {
		return false, ""
	}
	return true, fmt.Sprintf("pre-commit hook frozen from %s at %s, %s now at %s", rec.BaseLabel, shortSha(rec.BaseSha), baseLabel(ref), shortSha(cur))
}

// ReinstallPreCommitHook re-runs the hook install alone (the resume refresh,
// spinclass#459), re-resolving the formatter from the now-landed base. nil
// when the hook is inactive, which uninstalls it as Setup would.
func ReinstallPreCommitHook(sf sweatfile.Sweatfile, worktreePath string) (*session.FrozenHook, error) {
	return installPreCommitHook(sf.MergeWith(sweatfile.GetDefault()), worktreePath)
}

// ReadHookRecord reads the sidecar the last install wrote. The sidecar is
// worker-writable: a caller copies it into the frontend's record only right
// after an install it ran itself.
func ReadHookRecord(worktreePath string) (*session.FrozenHook, error) {
	data, err := os.ReadFile(filepath.Join(worktreePath, ".spinclass", hookRecordName))
	if err != nil {
		return nil, err
	}
	var rec session.FrozenHook
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("reading %s: %w", hookRecordName, err)
	}
	return &rec, nil
}

func writeHookRecord(worktreePath string, rec session.FrozenHook) error {
	dir := filepath.Join(worktreePath, ".spinclass")
	if err := safeio.RealDir(dir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return safeio.WriteFile(filepath.Join(dir, hookRecordName), append(data, '\n'), 0o644)
}

func removeHookRecord(worktreePath string) {
	_ = os.Remove(filepath.Join(worktreePath, ".spinclass", hookRecordName))
}

// repoRootOf is the main checkout's root for a worktree: the parent of the
// shared .git directory. The evaluation reads git history, which every
// worktree shares, but nix's git fetcher is pointed at the main checkout.
func repoRootOf(worktreePath string) (string, error) {
	common, err := git.CommonGitDir(worktreePath)
	if err != nil {
		return "", err
	}
	return filepath.Dir(common), nil
}

// leadingWord is a command's first word: the program the dispatcher guards
// on and the one the freeze resolves.
func leadingWord(cmd string) string {
	if i := strings.IndexAny(cmd, " \t\n"); i >= 0 {
		return cmd[:i]
	}
	return cmd
}

// baseLabel is a full ref's human form: origin/master, or master for the
// local branch.
func baseLabel(ref string) string {
	if rest, ok := strings.CutPrefix(ref, "refs/remotes/"); ok {
		return rest
	}
	return strings.TrimPrefix(ref, "refs/heads/")
}

func shortSha(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// stderrTail keeps the end of an evaluator's stderr for the reason line; the
// reason is shown to the operator and recorded in the session, nowhere else.
func stderrTail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	const keep = 400
	if len(s) > keep {
		s = "..." + s[len(s)-keep:]
	}
	return ": " + strings.ReplaceAll(s, "\n", " | ")
}
