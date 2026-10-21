package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/store"
)

// talksToRemote is true for the verbs that contact a remote. They run through
// remoteRunner so none of them can fall back to the ambient ssh-agent (FDR 0036
// section 5).
func talksToRemote(op store.Op) bool {
	switch op.Tool + " " + op.Subcommand {
	case "remote fetch", "remote pull", "remote push", "remote push-stack", "tag push", "clone ":
		return true
	}
	return false
}

// agentEnvVars name the ambient ssh-agent. A session pushes through the
// worktree-scoped credential helper FDR 0028 wrote; where none was minted the
// verb fails with git's own auth error rather than borrowing the operator's
// agent.
var agentEnvVars = map[string]bool{"SSH_AUTH_SOCK": true, "SSH_AGENT_PID": true, "GIT_SSH": true, "GIT_SSH_COMMAND": true}

// agentlessSSHCommand is the ssh git runs for a remote verb. Command-line -o
// options beat ~/.ssh/config, so an IdentityAgent set there cannot reach an
// agent; BatchMode keeps it from prompting. Key files ssh finds on its own
// (default identities, IdentityFile entries in the config) are not an agent
// and are not blocked here.
const agentlessSSHCommand = "ssh -o IdentityAgent=none -o IdentitiesOnly=yes -o BatchMode=yes"

// remoteEnv is base without the ssh-agent variables, plus extra, with git's
// terminal prompting off and ssh pinned to agentlessSSHCommand.
func remoteEnv(base, extra []string) []string {
	env := make([]string, 0, len(base)+len(extra)+2)
	for _, assignment := range base {
		name, _, _ := strings.Cut(assignment, "=")
		if !agentEnvVars[name] {
			env = append(env, assignment)
		}
	}
	return append(append(env, extra...), "GIT_SSH_COMMAND="+agentlessSSHCommand, "GIT_TERMINAL_PROMPT=0")
}

// remoteRunner runs git for a remote verb. It is a variable so a test can
// observe the environment git is given.
var remoteRunner = runRemoteGit

// runRemoteGit runs git with the session worktree as its working directory, so
// the worktree-scoped credential.helper and url.insteadOf apply.
func runRemoteGit(ctx context.Context, dir string, env []string, argv ...string) (string, error) {
	if h, ok := git.HardeningFor(dir); ok {
		argv = append(append([]string{}, h.Args...), argv...)
		env = append(append([]string{}, env...), h.Env...)
	}
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = dir
	cmd.Env = remoteEnv(os.Environ(), env)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("git %s: %w", strings.Join(argv, " "), err)
	}
	return text, nil
}

// rejectOptionLike refuses a ref-like value git could read as an option.
func rejectOptionLike(a args) error {
	for _, v := range a.refLikeValues() {
		if strings.HasPrefix(v, "-") {
			return fmt.Errorf("refusing option-like value %q", v)
		}
	}
	return nil
}

// buildAnyArgv builds the argv of any verb; dir and the session are needed by
// the verbs that resolve refs or paths while building.
func buildAnyArgv(dir string, s store.Session, op store.Op, a args) ([]string, error) {
	switch op.Tool + " " + op.Subcommand {
	case "remote fetch", "remote pull", "remote push", "tag push", "tag create", "tag delete",
		"merge ", "clone ", "worktree remove":
		if err := rejectOptionLike(a); err != nil {
			return nil, err
		}
		return buildRemoteFamilyArgv(dir, s, op, a)
	}
	if op.Tool == "branch" {
		switch op.Subcommand {
		case "create", "delete", "checkout":
			if err := rejectOptionLike(a); err != nil {
				return nil, err
			}
			return buildBranchFormsArgv(dir, op.Subcommand, a)
		}
	}
	return buildArgv(op, a)
}

func buildRemoteFamilyArgv(dir string, s store.Session, op store.Op, a args) ([]string, error) {
	switch op.Tool + " " + op.Subcommand {
	case "remote fetch":
		if err := requireConfiguredRemote(dir, a.Remote); err != nil {
			return nil, err
		}
		argv := []string{"fetch"}
		if a.All {
			argv = append(argv, "--all")
		}
		if a.Prune {
			argv = append(argv, "--prune")
		}
		if a.Tags {
			argv = append(argv, "--tags")
		}
		if !a.All && a.Remote != "" {
			argv = append(argv, a.Remote)
		}
		return argv, nil
	case "remote pull":
		if err := requireConfiguredRemote(dir, a.Remote); err != nil {
			return nil, err
		}
		// Spelled out either way: with neither set, git refuses a diverged pull.
		argv := []string{"pull", "--no-rebase"}
		if a.Rebase {
			argv = []string{"pull", "--rebase"}
		}
		argv = append(argv, defaultRemote(a.Remote))
		if a.Branch != "" {
			argv = append(argv, a.Branch)
		}
		return argv, nil
	case "remote push":
		return buildPushArgv(dir, a)
	case "tag push":
		return buildTagPushArgv(dir, a)
	case "tag create":
		return buildTagCreateArgv(a)
	case "tag delete":
		if a.Name == "" {
			return nil, errors.New("name is required for delete")
		}
		return []string{"tag", "--delete", a.Name}, nil
	case "merge ":
		return buildMergeArgv(a)
	case "clone ":
		return buildCloneArgv(a)
	case "worktree remove":
		return buildWorktreeRemoveArgv(dir, s, a)
	}
	return nil, store.Unsupported(op)
}

func defaultRemote(name string) string {
	if name == "" {
		return "origin"
	}
	return name
}

func shortBranch(name string) string { return strings.TrimPrefix(name, "refs/heads/") }

// requireConfiguredRemote accepts only a remote name this repository
// configures: a URL or transport string would send the worktree's credentials
// to a host nobody configured. An empty name is git's own default.
func requireConfiguredRemote(dir, name string) error {
	if name == "" || configuredRemote(dir, name) {
		return nil
	}
	return fmt.Errorf("remote %q is not a configured remote of this repository; only configured remote names are accepted", name)
}

func configuredRemote(dir, name string) bool {
	out, err := git.Run(dir, "remote")
	if err != nil {
		return false
	}
	for _, remote := range strings.Fields(out) {
		if remote == name {
			return true
		}
	}
	return false
}

// pushRefs resolves the local branch to push (default HEAD) to its full ref and
// the short name of the remote branch it lands on.
func pushRefs(dir string, a args) (source, destination string, err error) {
	name := a.Branch
	if name == "" {
		name = "HEAD"
	}
	if name != "HEAD" && !plainRefName(name) {
		return "", "", fmt.Errorf("branch %q must be a plain branch name", name)
	}
	full, err := git.Run(dir, "rev-parse", "--symbolic-full-name", name)
	if err != nil || strings.Contains(full, "\n") || !strings.HasPrefix(full, "refs/heads/") {
		return "", "", fmt.Errorf("%q does not resolve to a local branch", name)
	}
	destination = shortBranch(a.RemoteBranch)
	if destination == "" {
		destination = shortBranch(full)
	}
	if !plainRefName(destination) {
		return "", "", fmt.Errorf("remote_branch %q must be a plain branch name", a.RemoteBranch)
	}
	return full, destination, nil
}

var leaseSHAPattern = regexp.MustCompile(`^[0-9a-fA-F]{4,64}$`)

// leaseFlag carries moxy's force_with_lease and lease_ref_sha as given (moxy
// #390): both make the explicit --force-with-lease=<ref>:<sha> form, which
// needs no remote-tracking ref, and force_with_lease alone is the boolean form.
func leaseFlag(a args, destination string) (string, error) {
	if !a.ForceWithLease {
		if a.LeaseRefSHA != "" {
			return "", errors.New("lease_ref_sha needs force_with_lease")
		}
		if a.ForceIfIncludes {
			return "", errors.New("force_if_includes needs force_with_lease")
		}
		return "", nil
	}
	if a.Force {
		return "", errors.New("force and force_with_lease are mutually exclusive")
	}
	if a.LeaseRefSHA == "" {
		return "--force-with-lease", nil
	}
	if !leaseSHAPattern.MatchString(a.LeaseRefSHA) {
		return "", fmt.Errorf("lease_ref_sha %q is not a commit hash", a.LeaseRefSHA)
	}
	return "--force-with-lease=refs/heads/" + destination + ":" + a.LeaseRefSHA, nil
}

func buildPushArgv(dir string, a args) ([]string, error) {
	if err := requireConfiguredRemote(dir, a.Remote); err != nil {
		return nil, err
	}
	remote := defaultRemote(a.Remote)
	if a.Delete {
		name := shortBranch(a.RemoteBranch)
		if name == "" {
			name = shortBranch(a.Branch)
		}
		if !plainRefName(name) {
			return nil, errors.New("branch or remote_branch names the remote branch to delete")
		}
		return []string{"push", "--delete", remote, "refs/heads/" + name}, nil
	}
	source, destination, err := pushRefs(dir, a)
	if err != nil {
		return nil, err
	}
	lease, err := leaseFlag(a, destination)
	if err != nil {
		return nil, err
	}
	argv := []string{"push"}
	if a.SetUpstream {
		argv = append(argv, "--set-upstream")
	}
	if a.Force {
		argv = append(argv, "--force")
	}
	if lease != "" {
		argv = append(argv, lease)
	}
	if a.ForceIfIncludes {
		argv = append(argv, "--force-if-includes")
	}
	return append(argv, remote, source+":refs/heads/"+destination), nil
}

func buildTagPushArgv(dir string, a args) ([]string, error) {
	if a.Name == "" || !plainRefName(a.Name) {
		return nil, errors.New("name is required for push and must be a plain tag name")
	}
	if err := requireConfiguredRemote(dir, a.Remote); err != nil {
		return nil, err
	}
	argv := []string{"push"}
	if a.Force {
		argv = append(argv, "--force")
	}
	if a.Delete {
		argv = append(argv, "--delete")
	}
	return append(argv, defaultRemote(a.Remote), "refs/tags/"+a.Name), nil
}

// buildTagCreateArgv signs by default, as moxy's tag does. Signing needs a key
// the session may not have; git's own error then reaches the caller, and
// lightweight or sign=false opt out.
func buildTagCreateArgv(a args) ([]string, error) {
	if a.Name == "" {
		return nil, errors.New("name is required for create")
	}
	signed := a.Sign == nil || *a.Sign
	argv := []string{"tag"}
	if a.Force {
		argv = append(argv, "--force")
	}
	ref := a.Ref
	if ref == "" {
		ref = "HEAD"
	}
	if a.Lightweight {
		if a.Message != "" || (a.Sign != nil && *a.Sign) {
			return nil, errors.New("lightweight excludes sign and message")
		}
		return append(argv, a.Name, ref), nil
	}
	if a.Message == "" {
		return nil, errors.New("message is required for an annotated or signed tag (or set lightweight)")
	}
	if signed {
		argv = append(argv, "--sign")
	} else {
		argv = append(argv, "--annotate")
	}
	return append(argv, "-m", a.Message, a.Name, ref), nil
}

func buildMergeArgv(a args) ([]string, error) {
	if a.Abort && a.Continue {
		return nil, errors.New("abort and continue are mutually exclusive")
	}
	if a.Abort {
		return []string{"merge", "--abort"}, nil
	}
	if a.Continue {
		return []string{"merge", "--continue"}, nil
	}
	if a.Branch == "" {
		return nil, errors.New("branch is required for merge")
	}
	argv := []string{"merge"}
	if a.NoFF {
		argv = append(argv, "--no-ff")
	}
	if a.Squash.on {
		argv = append(argv, "--squash")
	}
	return append(argv, a.Branch), nil
}

func buildCloneArgv(a args) ([]string, error) {
	if a.Source == "" || a.Destination == "" {
		return nil, errors.New("source and destination are required for clone")
	}
	if a.Depth < 0 {
		return nil, errors.New("depth must not be negative")
	}
	argv := []string{"clone"}
	if a.Bare {
		argv = append(argv, "--bare")
	}
	if a.Branch != "" {
		argv = append(argv, "--branch", a.Branch)
	}
	if a.Depth > 0 {
		argv = append(argv, "--depth", strconv.Itoa(a.Depth))
	}
	return append(argv, "--", a.Source, a.Destination), nil
}

// buildWorktreeRemoveArgv refuses to remove the caller's own worktree
// whatever the caller's standing: it is the directory the tool runs in.
func buildWorktreeRemoveArgv(dir string, s store.Session, a args) ([]string, error) {
	if a.Path == "" {
		return nil, errors.New("path is required for remove")
	}
	path := absoluteUnder(dir, a.Path)
	own := []string{s.Dir}
	if top, err := git.Run(dir, "rev-parse", "--show-toplevel"); err == nil {
		own = append(own, top)
	}
	for _, candidate := range own {
		if candidate != "" && sameDir(path, candidate) {
			return nil, fmt.Errorf("refusing worktree remove: %q is the worktree this call runs in", a.Path)
		}
	}
	argv := []string{"worktree", "remove"}
	if a.Force {
		argv = append(argv, "--force")
	}
	return append(argv, path), nil
}

func absoluteUnder(dir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}

// refuseDefaultBranchPush is the body refusal for a confined or unresolved
// caller pushing, or deleting, the default branch. It ignores force: for such a
// caller no human can have approved it. A resolved caller is asked by the hook
// instead (FDR 0036 section 5).
func refuseDefaultBranchPush(s store.Session, dir string, op store.Op, a args) error {
	if !s.Confinement.Confined() || op.Tool != "remote" || (op.Subcommand != "push" && op.Subcommand != "push-stack") {
		return nil
	}
	if name, hit := pushesDefaultBranch(dir, op, a); hit {
		return fmt.Errorf("refusing remote %s: %q is the default branch (main, master or origin's HEAD); a confined caller never pushes or deletes it", op.Subcommand, branchLabel(name))
	}
	return nil
}

// pushesDefaultBranch reports the first push source or destination that is the
// default branch.
func pushesDefaultBranch(dir string, op store.Op, a args) (string, bool) {
	var names []string
	if op.Subcommand == "push-stack" {
		names = a.Branches
	} else {
		names = []string{a.Branch}
		if a.RemoteBranch != "" {
			names = append(names, a.RemoteBranch)
		}
	}
	for _, name := range names {
		if touchesDefaultBranch(dir, name) {
			return name, true
		}
	}
	return "", false
}

// pushStackStep is one branch of a stack push.
type pushStackStep struct {
	branch  string
	refspec string
}

type pushStackResult struct {
	Branch string `json:"branch"`
	Status string `json:"status"`
	Output string `json:"output,omitempty"`
}

type pushStackReport struct {
	Phase   string            `json:"phase"`
	Results []pushStackResult `json:"results"`
}

// pushStack pushes the branches bottom to top with --force-with-lease, after a
// dry run of every one. Nothing is pushed unless the whole dry run passed. The
// default branch is refused for every caller: the verb always force-pushes.
func pushStack(ctx context.Context, s store.Session, dir string, op store.Op, a args) (store.Result, error) {
	if len(a.Branches) == 0 {
		return store.Result{}, errors.New("branches is required for push-stack")
	}
	if err := rejectOptionLike(a); err != nil {
		return store.Result{}, err
	}
	if err := requireConfiguredRemote(dir, a.Remote); err != nil {
		return store.Result{}, err
	}
	if name, hit := pushesDefaultBranch(dir, op, a); hit {
		return store.Result{}, fmt.Errorf("refusing remote push-stack: %q is the default branch (main, master or origin's HEAD); push-stack force-pushes and never touches it", branchLabel(name))
	}
	var steps []pushStackStep
	for _, name := range a.Branches {
		source, destination, err := pushRefs(dir, args{Branch: name})
		if err != nil {
			return store.Result{}, err
		}
		steps = append(steps, pushStackStep{branch: name, refspec: source + ":refs/heads/" + destination})
	}
	remote := defaultRemote(a.Remote)
	push := func(step pushStackStep, dryRun bool) (string, error) {
		argv := []string{"push"}
		if dryRun {
			argv = append(argv, "--dry-run")
		}
		argv = append(argv, "--force-with-lease", remote, step.refspec)
		return remoteRunner(ctx, dir, writeEnv(op), withoutHooksForConfined(s, op, argv)...)
	}

	preflight := pushStackReport{Phase: "preflight"}
	passed := true
	for _, step := range steps {
		out, err := push(step, true)
		result := pushStackResult{Branch: step.branch, Status: "ok", Output: out}
		if err != nil {
			result.Status, passed = "failed", false
		}
		preflight.Results = append(preflight.Results, result)
	}
	if !passed {
		return store.Result{}, reportError(preflight)
	}

	report := pushStackReport{Phase: "push"}
	failed := false
	for _, step := range steps {
		if failed {
			report.Results = append(report.Results, pushStackResult{Branch: step.branch, Status: "skipped"})
			continue
		}
		out, err := push(step, false)
		result := pushStackResult{Branch: step.branch, Status: "ok", Output: out}
		if err != nil {
			result.Status, failed = "failed", true
		}
		report.Results = append(report.Results, result)
	}
	if failed {
		return store.Result{}, reportError(report)
	}
	return store.Result{Text: reportText(report)}, nil
}

func reportText(report pushStackReport) string {
	raw, err := json.Marshal(report)
	if err != nil {
		return fmt.Sprintf("%s: %v", report.Phase, err)
	}
	return string(raw)
}

func reportError(report pushStackReport) error { return errors.New(reportText(report)) }
