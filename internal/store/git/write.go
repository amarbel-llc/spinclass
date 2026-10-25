package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/store"
)

// refLikeValues are the arguments git could read as an option or revision; they
// are refused when they start with '-'. Paths are not listed: they always follow
// `--`.
func (a args) refLikeValues() []string {
	values := []string{
		a.Ref, a.Name, a.Contains, a.Fixup, a.Squash.target, a.StartPoint,
		a.Upstream, a.Onto, a.RebaseBranch, a.Root,
		a.Remote, a.Branch, a.RemoteBranch, a.Path,
	}
	values = append(values, a.Commits...)
	return append(values, a.Branches...)
}

func buildWriteArgv(op store.Op, a args) ([]string, error) {
	switch op.Tool + " " + op.Subcommand {
	case "stage add":
		if len(a.Paths) == 0 {
			return nil, errors.New("paths is required for add")
		}
		argv := []string{"add"}
		if a.IntentToAdd {
			argv = append(argv, "--intent-to-add")
		}
		return withPaths(argv, a.Paths), nil
	case "stage unstage":
		return withPaths([]string{"reset", "-q"}, a.Paths), nil
	case "stage rm":
		if len(a.Paths) == 0 {
			return nil, errors.New("paths is required for rm")
		}
		argv := []string{"rm"}
		if a.Force {
			argv = append(argv, "--force")
		}
		if a.Recursive {
			argv = append(argv, "-r")
		}
		return withPaths(argv, a.Paths), nil
	case "stage mv":
		if a.Source == "" || a.Destination == "" {
			return nil, errors.New("source and destination are required for mv")
		}
		return []string{"mv", "--", a.Source, a.Destination}, nil
	case "commit ":
		return buildCommitArgv(a)
	case "branch reset-soft":
		ref := a.Ref
		if ref == "" {
			ref = "HEAD~1"
		}
		return []string{"reset", "--soft", ref}, nil
	case "branch reset-hard":
		if a.Ref == "" {
			return nil, errors.New("ref is required for reset-hard")
		}
		return []string{"reset", "--hard", a.Ref}, nil
	case "branch rebase":
		return buildRebaseArgv(a)
	case "branch restack":
		if a.Root == "" || a.Onto == "" {
			return nil, errors.New("root and onto are required for restack")
		}
		return []string{"rebase", "--update-refs", "--onto", a.Onto, a.Root + "^"}, nil
	case "branch cherry-pick":
		if len(a.Commits) == 0 {
			return nil, errors.New("commits is required for cherry-pick")
		}
		argv := []string{"cherry-pick"}
		if a.NoCommit {
			argv = append(argv, "--no-commit")
		}
		return append(argv, a.Commits...), nil
	case "branch revert":
		if len(a.Commits) == 0 {
			return nil, errors.New("commits is required for revert")
		}
		argv := []string{"revert", "--no-edit"}
		if a.NoCommit {
			argv = append(argv, "--no-commit")
		}
		return append(argv, a.Commits...), nil
	}
	return nil, store.Unsupported(op)
}

// buildCommitArgv names no identity: Do stamps the configured one through the
// environment (identityEnv), else git resolves its own.
func buildCommitArgv(a args) ([]string, error) {
	if a.Fixup != "" && a.Squash.target != "" {
		return nil, errors.New("fixup and squash are mutually exclusive")
	}
	argv := []string{"commit"}
	if a.AllowEmpty {
		argv = append(argv, "--allow-empty")
	}
	switch {
	case a.Fixup != "" || a.Squash.target != "":
		if a.Amend {
			return nil, errors.New("amend cannot be combined with fixup or squash")
		}
		if a.Fixup != "" {
			return append(argv, "--fixup="+a.Fixup), nil
		}
		return append(argv, "--squash="+a.Squash.target), nil
	case a.Amend:
		argv = append(argv, "--amend")
		if a.Message == "" {
			return append(argv, "--no-edit"), nil
		}
	case a.Message == "":
		return nil, errors.New("message is required")
	}
	return append(argv, "-m", a.Message), nil
}

func buildCheckoutArgv(dir string, a args) ([]string, error) {
	if a.Ours && a.Theirs {
		return nil, errors.New("ours and theirs are mutually exclusive")
	}
	if len(a.Paths) > 0 {
		argv := []string{"checkout"}
		if a.Ours {
			argv = append(argv, "--ours")
		}
		if a.Theirs {
			argv = append(argv, "--theirs")
		}
		if a.Ref != "" {
			argv = append(argv, a.Ref)
		}
		return withPaths(argv, a.Paths), nil
	}
	if a.Ours || a.Theirs {
		return nil, errors.New("ours and theirs need paths")
	}
	if a.NewBranch {
		return buildNewBranchCheckoutArgv(dir, a)
	}
	if a.Name != "" || a.StartPoint != "" {
		return nil, errors.New("name and start_point are only used with new_branch; to check out an existing branch pass ref")
	}
	if a.Ref == "" {
		return nil, errors.New("ref is required for checkout")
	}
	return []string{"checkout", a.Ref, "--"}, nil
}

// buildNewBranchCheckoutArgv: name (else, in the legacy form, ref) is the new
// branch and start_point where it starts.
func buildNewBranchCheckoutArgv(dir string, a args) ([]string, error) {
	if a.Name != "" && a.Ref != "" {
		return nil, errors.New("with new_branch pass the new branch as name and its start as start_point; ref is not used")
	}
	name := a.newBranchName()
	if name == "" {
		return nil, errors.New("name is required for checkout with new_branch")
	}
	if a.Name == "" {
		if err := refuseLegacyNewBranchRef(dir, name); err != nil {
			return nil, err
		}
	}
	if err := refuseBadNewBranchName(dir, name); err != nil {
		return nil, err
	}
	argv := []string{"checkout", "-b", name}
	if a.StartPoint != "" {
		argv = append(argv, a.StartPoint)
	}
	return argv, nil
}

// refuseLegacyNewBranchRef keeps the old new_branch-with-ref-only form working
// for a plain local-style name and points everything else at name/start_point.
func refuseLegacyNewBranchRef(dir, ref string) error {
	if !plainRefName(ref) || remoteTrackingShape(dir, ref) != "" || strings.HasPrefix(ref, refsPrefix) {
		return fmt.Errorf("with new_branch, ref %q is not a plain new branch name; pass the new branch as name and the ref to start from as start_point", ref)
	}
	return nil
}

func buildRebaseArgv(a args) ([]string, error) {
	control := map[string]bool{"--continue": a.Continue, "--abort": a.Abort, "--skip": a.Skip}
	var requested []string
	for flag, set := range control {
		if set {
			requested = append(requested, flag)
		}
	}
	if len(requested) > 1 {
		return nil, errors.New("continue, abort and skip are mutually exclusive")
	}
	if len(requested) == 1 {
		return []string{"rebase", requested[0]}, nil
	}
	if a.Upstream == "" {
		return nil, errors.New("upstream is required for rebase")
	}
	argv := []string{"rebase"}
	if a.Autosquash {
		argv = append(argv, "--interactive", "--autosquash")
	}
	if a.Autostash {
		argv = append(argv, "--autostash")
	}
	if a.UpdateRefs {
		argv = append(argv, "--update-refs")
	}
	if a.Onto != "" {
		argv = append(argv, "--onto", a.Onto)
	}
	argv = append(argv, a.Upstream)
	if a.RebaseBranch != "" {
		argv = append(argv, a.RebaseBranch)
	}
	return argv, nil
}

// writeEnv keeps every mutating git command non-interactive: editors and
// sequence editors are no-ops and credential prompts cannot hang the tool.
func writeEnv(op store.Op) []string {
	if store.ClassOf(op.Tool, op.Subcommand) == store.ClassRead {
		return nil
	}
	env := []string{"GIT_EDITOR=true", "GIT_TERMINAL_PROMPT=0"}
	if op.Tool == "branch" && (op.Subcommand == "rebase" || op.Subcommand == "restack") {
		env = append(env, "GIT_SEQUENCE_EDITOR=true")
	}
	return env
}

// refuseDefaultBranchOp is the one hard refusal in the tool body (FDR 0036
// section 2): nothing is approvable into a destructive operation on the default
// branch. Ownership is the hook's concern, not checked here.
func refuseDefaultBranchOp(dir string, op store.Op, a args) error {
	var targets []string
	switch op.Tool + " " + op.Subcommand {
	case "branch delete":
		targets = []string{a.Name}
	case "branch reset-hard":
		targets = []string{"HEAD"}
	case "branch rebase":
		if a.Continue || a.Abort || a.Skip {
			return nil
		}
		targets = []string{a.RebaseBranch}
	case "branch restack":
		targets = []string{"HEAD", a.Root}
	case "merge ":
		if a.Continue || a.Abort {
			return nil
		}
		targets = []string{"HEAD"}
	default:
		return nil
	}
	for _, target := range targets {
		if touchesDefaultBranch(dir, target) {
			return fmt.Errorf("refusing %s %s: %q is the default branch (main, master or origin's HEAD); destructive operations on it are never allowed", op.Tool, op.Subcommand, branchLabel(target))
		}
	}
	return nil
}

func branchLabel(target string) string {
	if target == "" {
		return "HEAD"
	}
	return target
}

// touchesDefaultBranch resolves name (empty means HEAD) to its branch the way
// git does, and also checks the literal name so a branch that does not exist
// yet is judged too.
func touchesDefaultBranch(dir, name string) bool {
	if name == "" {
		name = "HEAD"
	}
	if full, err := git.Run(dir, "rev-parse", "--symbolic-full-name", name); err == nil && strings.HasPrefix(full, "refs/heads/") {
		if isDefaultBranch(dir, strings.TrimPrefix(full, "refs/heads/")) {
			return true
		}
	}
	return isDefaultBranch(dir, strings.TrimPrefix(name, "refs/heads/"))
}

// ConflictError reports a rebase, restack, cherry-pick or revert that stopped
// mid-operation. The repository is left in that in-progress state so the
// follow-up verbs (rebase continue/abort/skip) work.
type ConflictError struct {
	Operation string
	ExitCode  int
	Paths     []string
	Output    string
	Err       error
}

func (e *ConflictError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s stopped before completing (git exit status %d); the repository is left in its in-progress state.", e.Operation, e.ExitCode)
	if len(e.Paths) > 0 {
		fmt.Fprintf(&b, "\nconflicted paths: %s", strings.Join(e.Paths, ", "))
	}
	switch e.Operation {
	case "branch rebase", "branch restack":
		b.WriteString("\nresolve and stage the paths, then run branch rebase with continue=true (or abort=true / skip=true).")
	case "merge ", "remote pull":
		b.WriteString("\nresolve and stage the paths, then commit (or abandon the merge with merge abort=true).")
	default:
		b.WriteString("\nresolve and stage the paths, then commit.")
	}
	if e.Output != "" {
		b.WriteString("\n" + e.Output)
	}
	return b.String()
}

func (e *ConflictError) Unwrap() error { return e.Err }

var inProgressMarkers = map[string][]string{
	"branch rebase":      {"rebase-merge", "rebase-apply"},
	"branch restack":     {"rebase-merge", "rebase-apply"},
	"branch cherry-pick": {"CHERRY_PICK_HEAD"},
	"branch revert":      {"REVERT_HEAD"},
	"merge ":             {"MERGE_HEAD"},
	"remote pull":        {"MERGE_HEAD", "rebase-merge", "rebase-apply"},
}

// describeFailure turns a failed git run into an error carrying git's own
// output, and a *ConflictError when the operation left itself in progress.
func describeFailure(dir string, op store.Op, err error, out string) error {
	operation := op.Tool + " " + op.Subcommand
	for _, marker := range inProgressMarkers[operation] {
		if !gitPathExists(dir, marker) {
			continue
		}
		paths, _ := git.UnmergedPaths(dir)
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		return &ConflictError{Operation: operation, ExitCode: exitCode, Paths: paths, Output: out, Err: err}
	}
	if out != "" {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return err
}

func gitPathExists(dir, name string) bool {
	path, err := git.Run(dir, "rev-parse", "--git-path", name)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	_, statErr := os.Stat(path)
	return statErr == nil
}
