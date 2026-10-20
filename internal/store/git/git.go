// Package git is the git backend of the grit store tools (FDR 0036). It
// implements the read verbs, the owned-branch write verbs (stage, commit,
// branch) and the stash shelf; remote, merge, tag mutation, clone and worktree
// remove return store.ErrUnsupported.
package git

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/store"
)

// Backend is the git store backend.
type Backend struct{}

// New returns the git backend.
func New() Backend { return Backend{} }

var _ store.Backend = Backend{}

func (Backend) Name() string { return "git" }

var branchVerbs = []string{
	"log", "rev-parse", "diff", "create", "delete", "reset-soft", "reset-hard",
	"checkout", "rebase", "restack", "cherry-pick", "revert",
}

func (Backend) Capabilities() store.Capabilities {
	return store.Capabilities{
		"stage":    {"status", "diff", "add", "unstage", "rm", "mv"},
		"commit":   {""},
		"branch":   branchVerbs,
		"tag":      {"list", "verify"},
		"worktree": {"list"},
		"stash":    {"list", "save", "apply", "drop"},
	}
}

// args is the union of the implemented verbs' parameters, named as moxy's grit
// tools name them.
type args struct {
	Paths        []string `json:"paths"`
	Staged       bool     `json:"staged"`
	StatOnly     bool     `json:"stat_only"`
	ContextLines *int     `json:"context_lines"`
	Untracked    string   `json:"untracked"`
	Ignored      bool     `json:"ignored"`
	Ref          string   `json:"ref"`
	MaxCount     int      `json:"max_count"`
	Oneline      bool     `json:"oneline"`
	Pattern      string   `json:"pattern"`
	Contains     string   `json:"contains"`
	Sort         string   `json:"sort"`
	Name         string   `json:"name"`
	RepoPath     string   `json:"repo_path"`

	Message      string   `json:"message"`
	Amend        bool     `json:"amend"`
	AllowEmpty   bool     `json:"allow_empty"`
	Fixup        string   `json:"fixup"`
	Squash       string   `json:"squash"`
	StartPoint   string   `json:"start_point"`
	Force        bool     `json:"force"`
	NewBranch    bool     `json:"new_branch"`
	Ours         bool     `json:"ours"`
	Theirs       bool     `json:"theirs"`
	Commits      []string `json:"commits"`
	NoCommit     bool     `json:"no_commit"`
	Upstream     string   `json:"upstream"`
	Onto         string   `json:"onto"`
	RebaseBranch string   `json:"rebase_branch"`
	Autosquash   bool     `json:"autosquash"`
	Autostash    bool     `json:"autostash"`
	UpdateRefs   bool     `json:"update_refs"`
	Continue     bool     `json:"continue"`
	Abort        bool     `json:"abort"`
	Skip         bool     `json:"skip"`
	Root         string   `json:"root"`
	Source       string   `json:"source"`
	Destination  string   `json:"destination"`
	Recursive    bool     `json:"recursive"`
	IntentToAdd  bool     `json:"intent_to_add"`

	IncludeUntracked bool   `json:"include_untracked"`
	StashRef         string `json:"stash_ref"`
}

func (b Backend) Do(ctx context.Context, s store.Session, op store.Op) (store.Result, error) {
	if !b.Capabilities().Supports(op.Tool, op.Subcommand) {
		return store.Result{}, store.Unsupported(op)
	}
	var a args
	if len(op.Args) > 0 {
		if err := json.Unmarshal(op.Args, &a); err != nil {
			return store.Result{}, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	dir := a.RepoPath
	if dir == "" {
		dir = s.Dir
	}
	if dir == "" {
		return store.Result{}, fmt.Errorf("no repository: pass repo_path or run inside a session")
	}

	if err := b.refuseUnownedForConfined(s, op); err != nil {
		return store.Result{}, err
	}

	if op.Tool == "stash" {
		return doStash(dir, op, a)
	}

	argv, err := buildArgv(op, a)
	if err != nil {
		return store.Result{}, err
	}
	if err := refuseDefaultBranchOp(dir, op, a); err != nil {
		return store.Result{}, err
	}
	argv = withoutHooksForConfined(s, op, argv)
	out, err := git.RunEnvContext(ctx, dir, append(writeEnv(op), identityEnv(s, op)...), argv...)
	if err != nil {
		return store.Result{}, describeFailure(dir, op, err, out)
	}
	if op.Tool == "tag" && op.Subcommand == "list" && a.MaxCount > 0 {
		out = firstLines(out, a.MaxCount)
	}
	return store.Result{Text: out}, nil
}

func buildArgv(op store.Op, a args) ([]string, error) {
	for _, v := range a.refLikeValues() {
		if strings.HasPrefix(v, "-") {
			return nil, fmt.Errorf("refusing option-like value %q", v)
		}
	}
	switch op.Tool + " " + op.Subcommand {
	case "stage status":
		argv := []string{"status", "--short", "--branch"}
		if a.Untracked != "" {
			switch a.Untracked {
			case "no", "normal", "all":
				argv = append(argv, "--untracked-files="+a.Untracked)
			default:
				return nil, fmt.Errorf("untracked must be no, normal or all, got %q", a.Untracked)
			}
		}
		if a.Ignored {
			argv = append(argv, "--ignored")
		}
		return withPaths(argv, a.Paths), nil
	case "stage diff":
		argv := []string{"diff"}
		if a.Staged {
			argv = append(argv, "--cached")
		}
		return withPaths(diffFlags(argv, a), a.Paths), nil
	case "branch diff":
		argv := diffFlags([]string{"diff"}, a)
		if a.Ref != "" {
			argv = append(argv, a.Ref)
		}
		return withPaths(argv, a.Paths), nil
	case "branch log":
		argv := []string{"log"}
		if a.MaxCount > 0 {
			argv = append(argv, "-n", strconv.Itoa(a.MaxCount))
		}
		if a.Oneline {
			argv = append(argv, "--oneline")
		}
		if a.Ref != "" {
			argv = append(argv, a.Ref)
		}
		return withPaths(argv, a.Paths), nil
	case "branch rev-parse":
		if a.Ref == "" {
			return nil, fmt.Errorf("ref is required for rev-parse")
		}
		return []string{"rev-parse", "--verify", a.Ref}, nil
	case "tag list":
		sort := a.Sort
		if sort == "" {
			sort = "-creatordate"
		}
		argv := []string{"tag", "--list", "--sort=" + sort}
		if a.Contains != "" {
			argv = append(argv, "--contains", a.Contains)
		}
		if a.Pattern != "" {
			argv = append(argv, a.Pattern)
		}
		return argv, nil
	case "tag verify":
		if a.Name == "" {
			return nil, fmt.Errorf("name is required for verify")
		}
		return []string{"tag", "--verify", a.Name}, nil
	case "worktree list":
		return []string{"worktree", "list", "--porcelain"}, nil
	}
	return buildWriteArgv(op, a)
}

func diffFlags(argv []string, a args) []string {
	if a.StatOnly {
		argv = append(argv, "--stat")
	}
	if a.ContextLines != nil {
		argv = append(argv, "--unified="+strconv.Itoa(*a.ContextLines))
	}
	return argv
}

func withPaths(argv, paths []string) []string {
	if len(paths) == 0 {
		return argv
	}
	return append(append(argv, "--"), paths...)
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n")
}
