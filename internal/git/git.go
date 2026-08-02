package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var ErrAmbiguousDefaultBranch = errors.New("both main and master branches exist")

func Run(repoPath string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", repoPath}, args...)
	cmd := exec.Command("git", cmdArgs...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimRight(string(exitErr.Stderr), "\n"))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func RunEnv(repoPath string, env []string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", repoPath}, args...)
	cmd := exec.Command("git", cmdArgs...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// RunEnvContext is RunEnv bound to ctx: the git subprocess is killed once ctx
// is done. Every other runner here uses exec.Command with no deadline, which is
// fine for local plumbing but not for anything that talks to a remote — see
// FetchContext.
func RunEnvContext(ctx context.Context, repoPath string, env []string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", repoPath}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func RunPassthrough(repoPath string, args ...string) error {
	return RunPassthroughEnv(repoPath, nil, args...)
}

func RunPassthroughEnv(repoPath string, env []string, args ...string) error {
	cmdArgs := append([]string{"-C", repoPath}, args...)
	cmd := exec.Command("git", cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd.Run()
}

func BranchCurrent(repoPath string) (string, error) {
	return Run(repoPath, "branch", "--show-current")
}

// RevParse resolves ref (e.g. "HEAD") to its full commit sha in repoPath.
func RevParse(repoPath, ref string) (string, error) {
	return Run(repoPath, "rev-parse", ref)
}

// RemoteURL returns the fetch URL configured for the "origin" remote of the
// repo (or worktree) at repoPath. It errors when no origin remote exists.
func RemoteURL(repoPath string) (string, error) {
	return Run(repoPath, "remote", "get-url", "origin")
}

// Remotes returns the repo's configured remote names, or nil when it has none.
// A repo with no remote has no upstream it can be stale against, which is the
// first thing any freshness check has to establish.
func Remotes(repoPath string) []string {
	out, err := Run(repoPath, "remote")
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// FetchContext updates repoPath's remote-tracking ref for branch from remote,
// bounded by ctx.
//
// The refspec is spelled out rather than relying on `git fetch <remote>
// <branch>` to update the tracking ref as a side effect: that only happens when
// remote.<name>.fetch happens to cover the ref, and callers read the tracking
// ref back to classify ancestry, so it has to be updated unconditionally.
// Naming the destination also keeps the write off any LOCAL branch, so git's
// refusal to update a checked-out branch can never apply here. The leading `+`
// is the conventional force for a remote-tracking ref — it lets an upstream
// history rewrite be recorded and still touches no local branch.
//
// Every interactive credential path is disabled. An ssh passphrase prompt and a
// git credential helper both read /dev/tty rather than stdin, so a nil Stdin is
// NOT protection — and a hang here is close to undiagnosable: under
// spawn-session stdio is a log file, and the only symptom is the hello deadline
// expiring with nothing to show for it.
func FetchContext(ctx context.Context, repoPath, remote, branch string) (string, error) {
	refspec := "+refs/heads/" + branch + ":refs/remotes/" + remote + "/" + branch
	return RunEnvContext(ctx, repoPath, []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/false",
		"SSH_ASKPASS=/bin/false",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes",
	}, "fetch", "--no-tags", remote, refspec)
}

func CommitsAhead(worktreePath, base, branch string) int {
	out, err := Run(worktreePath, "rev-list", base+".."+branch, "--count")
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(out)
	return n
}

func StatusPorcelain(path string) string {
	out, err := Run(path, "status", "--porcelain")
	if err != nil {
		return ""
	}
	return out
}

// UnmergedPaths returns the worktree's files with unresolved merge-conflict
// status (index stages 1/2/3), one path per entry, or nil when the worktree is
// conflict-free. A non-empty result means a rebase, merge, or stash pop left
// conflicts that must be resolved before any commit — git itself refuses to
// commit unmerged entries, so a caller about to auto-commit (e.g. a repair
// formatter) must check this first to avoid staging conflict markers.
func UnmergedPaths(path string) ([]string, error) {
	out, err := Run(path, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func RevListLeftRight(path string) (ahead, behind int) {
	out, err := Run(path, "rev-list", "--left-right", "--count", "@{upstream}...HEAD")
	if err != nil {
		return 0, 0
	}
	parts := strings.Fields(out)
	if len(parts) != 2 {
		return 0, 0
	}
	behind, _ = strconv.Atoi(parts[0])
	ahead, _ = strconv.Atoi(parts[1])
	return ahead, behind
}

func Upstream(path string) string {
	out, err := Run(path, "rev-parse", "--abbrev-ref", "@{upstream}")
	if err != nil {
		return ""
	}
	return out
}

func LastCommitDate(path string) string {
	out, err := Run(path, "log", "-1", "--format=%cs")
	if err != nil {
		return "n/a"
	}
	return out
}

func HasDirtyTracked(repoPath string) bool {
	cmd := exec.Command("git", "-C", repoPath, "diff", "--quiet")
	if err := cmd.Run(); err != nil {
		return true
	}
	cmd = exec.Command("git", "-C", repoPath, "diff", "--cached", "--quiet")
	if err := cmd.Run(); err != nil {
		return true
	}
	return false
}

func CheckoutFile(repoPath, file string) error {
	_, err := Run(repoPath, "checkout", "--", file)
	return err
}

func ResetFile(repoPath, file string) error {
	_, err := Run(repoPath, "reset", "HEAD", "--", file)
	return err
}

func WorktreeRemove(repoPath, worktreePath string) error {
	_, err := Run(repoPath, "worktree", "remove", worktreePath)
	return err
}

func WorktreeForceRemove(repoPath, worktreePath string) error {
	_, err := Run(repoPath, "worktree", "remove", "--force", worktreePath)
	return err
}

func BranchDelete(repoPath, branch string) (string, error) {
	return Run(repoPath, "branch", "-d", branch)
}

func BranchForceDelete(repoPath, branch string) (string, error) {
	return Run(repoPath, "branch", "-D", branch)
}

func BranchExists(repoPath, branch string) bool {
	_, err := Run(repoPath, "rev-parse", "--verify", "refs/heads/"+branch)
	return err == nil
}

func RemoteBranchExists(repoPath, branch string) bool {
	_, err := Run(repoPath, "rev-parse", "--verify", "refs/remotes/origin/"+branch)
	return err == nil
}

// BranchWorktree returns the absolute path of the worktree that currently has
// branch checked out, or "" when none does.
//
// Backed by `git worktree list --porcelain`, whose records are blank-line
// separated and always open with `worktree <path>`. A `branch refs/heads/<name>`
// line appears only when a branch is actually checked out, so detached and bare
// records never match — which is exactly what keeps spinclass's own transient
// build and landing worktrees (created via WorktreeAddDetached) from being
// mistaken for a holder of the branch they were cut from.
//
// Callers should use the returned path directly rather than comparing it
// against a path of their own: git reports its own canonicalization, which
// need not match a caller's string form of the same directory.
func BranchWorktree(repoPath, branch string) (string, error) {
	out, err := Run(repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	want := "branch refs/heads/" + branch
	current := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			current = strings.TrimPrefix(line, "worktree ")
		case line == want:
			return current, nil
		}
	}
	return "", nil
}

// BranchSetTo points refs/heads/<branch> at target (`git branch -f`).
//
// The caller MUST have established that the move is a fast-forward first: git
// branch -f does not check, and nothing in this package should be able to
// rewind a branch by accident. git does refuse when branch is checked out in
// some worktree, but treat that as a backstop rather than the guard — a caller
// is expected to have routed the checked-out case to MergeFFOnly already.
func BranchSetTo(repoPath, branch, target string) error {
	_, err := Run(repoPath, "branch", "-f", branch, target)
	return err
}

// MergeFFOnly runs `git -C dir merge --ff-only ref`, advancing dir's
// checked-out branch without ever writing a merge commit. It fails when the
// move would not be a fast-forward, and when the working tree has changes in
// the way.
func MergeFFOnly(dir, ref string) (string, error) {
	return Run(dir, "merge", "--ff-only", ref)
}

func DefaultBranch(repoPath string) (string, error) {
	hasMain := BranchExists(repoPath, "main")
	hasMaster := BranchExists(repoPath, "master")

	if hasMain && hasMaster {
		return "", ErrAmbiguousDefaultBranch
	}
	if hasMaster {
		return "master", nil
	}
	if hasMain {
		return "main", nil
	}

	out, err := Run(repoPath, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil {
		branch := strings.TrimPrefix(out, "refs/remotes/origin/")
		if branch != "" {
			return branch, nil
		}
	}
	return BranchCurrent(repoPath)
}

func NewestFileTime(path string) time.Time {
	var newest time.Time
	_ = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		// Skip .git directories
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}
		if !info.IsDir() && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return newest
}

// CommonGitDir returns the ABSOLUTE path of the repo's shared .git directory
// (`git rev-parse --git-common-dir`, made absolute), which every worktree of a
// repo shares. NOT the same as CommonDir, which strips the trailing ".git" and
// returns the main-checkout ROOT. Per-repo coordination files (e.g. the merge
// lock, spinclass#235) live inside the git dir so they never appear in
// worktree status.
func CommonGitDir(path string) (string, error) {
	out, err := Run(path, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(path, out)
	}
	return filepath.Clean(out), nil
}

// IsAncestor reports whether ancestor is an ancestor of commit in the repo at
// dir (`git merge-base --is-ancestor`). git exits 1 when the answer is "no"
// and >1 on real errors (bad ref, corrupt repo); both surface as an error from
// Run, and IsAncestor deliberately treats ANY error as "not an ancestor". That
// is the conservative choice for the merge queue: a false "no" routes to the
// rebase-landing path, where a genuine error resurfaces loudly from the
// worktree add or rebase, whereas a false "yes" would ff-merge a sha the moved
// default branch cannot fast-forward to.
func IsAncestor(dir, ancestor, commit string) bool {
	_, err := Run(dir, "merge-base", "--is-ancestor", ancestor, commit)
	return err == nil
}

func CommonDir(worktreePath string) (string, error) {
	out, err := CommonGitDir(worktreePath)
	if err != nil {
		return "", err
	}
	// Strip trailing .git to get the repo root
	if filepath.Base(out) == ".git" {
		out = filepath.Dir(out)
	}
	return out, nil
}

func Pull(repoPath string) (string, error) {
	return Run(repoPath, "pull")
}

func Push(repoPath string) (string, error) {
	return Run(repoPath, "push")
}

func Rebase(repoPath, onto string) (string, error) {
	return Run(repoPath, "rebase", onto)
}

// WorktreeAddFrom runs `git -C fromPath worktree add -b newBranch newPath`
// so the new worktree branches from fromPath's current HEAD.
func WorktreeAddFrom(fromPath, newBranch, newPath string) error {
	_, err := Run(fromPath, "worktree", "add", "-b", newBranch, newPath)
	return err
}

// WorktreePrune runs `git -C repoPath worktree prune`, clearing admin entries
// for worktrees whose directories no longer exist (e.g. crash-orphaned build
// worktrees). Best-effort; errors are returned for the caller to ignore.
func WorktreePrune(repoPath string) error {
	_, err := Run(repoPath, "worktree", "prune")
	return err
}

// WorktreeAddDetached runs `git -C fromPath worktree add --detach newPath ref`,
// checking ref out into a detached-HEAD worktree at newPath (no branch, so the
// same ref/branch can stay checked out elsewhere). Prunes stale admin entries
// first so a crash-orphaned path of the same name does not block the add.
func WorktreeAddDetached(fromPath, newPath, ref string) error {
	_ = WorktreePrune(fromPath)
	_, err := Run(fromPath, "worktree", "add", "--detach", newPath, ref)
	return err
}

// IsWorktree returns true if path contains a .git file (not directory),
// indicating it is a git worktree rather than the main repository.
func IsWorktree(path string) bool {
	info, err := os.Lstat(filepath.Join(path, ".git"))
	if err != nil {
		return false
	}
	return !info.IsDir()
}
