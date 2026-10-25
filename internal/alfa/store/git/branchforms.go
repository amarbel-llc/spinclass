package git

import (
	"errors"
	"fmt"
	"strings"

	"code.linenisgreat.com/spinclass/internal/0/git"
)

// Accepted forms for branch create, checkout and delete (FDR 0036, #393).
//
//   - create: name is the new branch, start_point the optional start (default
//     HEAD).
//   - checkout of an existing branch: ref. checkout with new_branch: name is the
//     new branch and start_point the start; the legacy form, ref alone, names the
//     new branch and is accepted only when ref is a plain local-style name.
//   - delete: the short branch name, or the same with a leading refs/heads/.
//
// A new branch is never named like a remote-tracking ref, and no refs/ prefix
// is accepted for one.

const (
	headsPrefix = "refs/heads/"
	refsPrefix  = "refs/"
)

// normalizeBranchDeleteName reduces a delete target to the short name git
// deletes: a leading refs/heads/ is dropped, any other refs/ prefix refused.
func normalizeBranchDeleteName(name string) (string, error) {
	if short, found := strings.CutPrefix(name, headsPrefix); found {
		return short, nil
	}
	if strings.HasPrefix(name, refsPrefix) {
		return "", fmt.Errorf("branch delete takes a short branch name or refs/heads/<name>, not %q; other refs/ prefixes are refused", name)
	}
	return name, nil
}

// newBranchName is the branch checkout -b would create: name when given, else
// (the legacy form) ref.
func (a args) newBranchName() string {
	if a.Name != "" {
		return a.Name
	}
	return a.Ref
}

// remoteTrackingShape reports why name looks like a remote-tracking ref, or ""
// when it does not: it resolves under refs/remotes/, or starts with a configured
// remote's name and a slash.
func remoteTrackingShape(dir, name string) string {
	if _, err := git.Run(dir, "rev-parse", "--verify", "-q", "refs/remotes/"+name); err == nil {
		return "it resolves as the remote-tracking ref refs/remotes/" + name
	}
	out, err := git.Run(dir, "remote")
	if err != nil {
		return ""
	}
	for _, remote := range strings.Fields(out) {
		if strings.HasPrefix(name, remote+"/") {
			return fmt.Sprintf("it starts with the configured remote name %q", remote)
		}
	}
	return ""
}

// refuseBadNewBranchName refuses a new branch name that git would happily create
// but that can only be a mistake: a refs/ path, or a remote-tracking lookalike
// such as origin/master.
func refuseBadNewBranchName(dir, name string) error {
	if strings.HasPrefix(name, refsPrefix) {
		return fmt.Errorf("refusing to create a branch named %q: pass the short branch name, not a refs/ path", name)
	}
	if why := remoteTrackingShape(dir, name); why != "" {
		return fmt.Errorf("refusing to create a branch named %q: %s. To branch from it, pass name=<new branch> and start_point=%s", name, why, name)
	}
	return nil
}

func buildBranchFormsArgv(dir, subcommand string, a args) ([]string, error) {
	switch subcommand {
	case "create":
		if a.Name == "" {
			return nil, errors.New("name is required for create")
		}
		if err := refuseBadNewBranchName(dir, a.Name); err != nil {
			return nil, err
		}
		argv := []string{"branch", a.Name}
		if a.StartPoint != "" {
			argv = append(argv, a.StartPoint)
		}
		return argv, nil
	case "delete":
		if a.Name == "" {
			return nil, errors.New("name is required for delete")
		}
		short, err := normalizeBranchDeleteName(a.Name)
		if err != nil {
			return nil, err
		}
		deleteFlag := "-d"
		if a.Force {
			deleteFlag = "-D"
		}
		return []string{"branch", deleteFlag, short}, nil
	}
	return buildCheckoutArgv(dir, a)
}

// branchConfirmation makes create, new-branch checkout and delete always return
// a sentence, never an empty result. Create always states the sha (git's own
// output is silent or only a tracking note, which is kept after it); the others
// keep git's output when it printed any.
func branchConfirmation(dir, subcommand string, a args, out string) string {
	if strings.TrimSpace(out) != "" && subcommand != "create" {
		return out
	}
	var name, verb string
	switch {
	case subcommand == "create":
		name, verb = a.Name, "created branch"
	case subcommand == "checkout" && a.NewBranch && len(a.Paths) == 0:
		name, verb = a.newBranchName(), "created and checked out branch"
	case subcommand == "delete":
		short, _ := normalizeBranchDeleteName(a.Name)
		return "deleted branch " + short
	default:
		return out
	}
	confirmation := verb + " " + name
	if sha, err := git.Run(dir, "rev-parse", "--short", "refs/heads/"+name); err == nil {
		confirmation = fmt.Sprintf("%s %s at %s", verb, name, sha)
	}
	if strings.TrimSpace(out) != "" {
		return confirmation + "\n" + out
	}
	return confirmation
}
