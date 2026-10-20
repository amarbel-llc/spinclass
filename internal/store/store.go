// Package store is the backend-neutral layer of the grit store tools (FDR
// 0036): the verb vocabulary, the verb-class table that drives permission
// classification, and the Backend interface a backing store implements. It
// knows nothing about git; internal/store/git is the one backend.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrUnsupported is returned (wrapped) when a backend does not implement a
// verb or parameter. It is never a silent no-op.
var ErrUnsupported = errors.New("unsupported by this store backend")

// Verdict is the permission-relevant reading of an invocation. The zero value
// is Ask so an uninitialised verdict fails closed.
type Verdict int

const (
	Ask Verdict = iota
	Allow
)

func (v Verdict) String() string {
	if v == Allow {
		return "allow"
	}
	return "ask"
}

// Class is a verb's permission class (FDR 0036 section 2). The zero value is
// ClassFloor so an unknown verb fails closed.
type Class int

const (
	// ClassFloor always asks (moxy's each-use).
	ClassFloor Class = iota
	// ClassRead always allows (moxy's always-allow).
	ClassRead
	// ClassWrite allows iff the target is session-owned (moxy's dynamic).
	ClassWrite
)

// Ref names a line of history, mark or workspace in the backend's own syntax.
type Ref string

// Session is the caller context an operation runs in.
type Session struct {
	// Dir is the worktree the operation executes in.
	Dir string
	// Branch is the session's own branch, empty when unknown.
	Branch string
}

// Op is one tool invocation: a tool, its subcommand, and the raw JSON
// arguments exactly as the agent sent them.
type Op struct {
	Tool       string
	Subcommand string
	Args       json.RawMessage
}

// Result is a successful operation's output.
type Result struct {
	Text string
}

// Capabilities maps each tool to the subcommands a backend implements.
type Capabilities map[string][]string

// Supports reports whether the tool's subcommand is implemented.
func (c Capabilities) Supports(tool, subcommand string) bool {
	for _, s := range c[tool] {
		if s == subcommand {
			return true
		}
	}
	return false
}

// Backend is a backing-store plugin.
type Backend interface {
	Name() string
	Capabilities() Capabilities
	Do(ctx context.Context, s Session, op Op) (Result, error)
	// Owned reports whether ref belongs to the session's namespace.
	Owned(s Session, ref Ref) bool
	// Classify performs no side effect and is shared by the tool and the hook.
	Classify(s Session, op Op) Verdict
}

type verb struct{ tool, subcommand string }

// verbClasses is the FDR 0036 section 2 table. Tools with no subcommand
// (commit, merge, clone) use the empty subcommand.
var verbClasses = map[verb]Class{
	{"stage", "status"}:  ClassRead,
	{"stage", "diff"}:    ClassRead,
	{"stage", "add"}:     ClassWrite,
	{"stage", "unstage"}: ClassWrite,
	{"stage", "mv"}:      ClassWrite,
	{"stage", "rm"}:      ClassWrite,

	{"commit", ""}: ClassWrite,
	{"merge", ""}:  ClassWrite,
	{"clone", ""}:  ClassWrite,

	{"branch", "log"}:         ClassRead,
	{"branch", "rev-parse"}:   ClassRead,
	{"branch", "diff"}:        ClassRead,
	{"branch", "create"}:      ClassWrite,
	{"branch", "checkout"}:    ClassWrite,
	{"branch", "delete"}:      ClassWrite,
	{"branch", "reset-soft"}:  ClassWrite,
	{"branch", "reset-hard"}:  ClassWrite,
	{"branch", "rebase"}:      ClassWrite,
	{"branch", "restack"}:     ClassWrite,
	{"branch", "cherry-pick"}: ClassWrite,
	{"branch", "revert"}:      ClassWrite,

	{"tag", "list"}:   ClassRead,
	{"tag", "verify"}: ClassRead,
	{"tag", "create"}: ClassWrite,
	{"tag", "push"}:   ClassWrite,
	{"tag", "delete"}: ClassWrite,

	{"worktree", "list"}:   ClassRead,
	{"worktree", "remove"}: ClassFloor,

	{"remote", "fetch"}:      ClassRead,
	{"remote", "pull"}:       ClassWrite,
	{"remote", "push"}:       ClassWrite,
	{"remote", "push-stack"}: ClassWrite,

	// list is ClassWrite, not ClassRead: a read verb allows any repo_path, and a
	// shelf is private to its own worktree, so even listing needs the session
	// worktree binding.
	{"stash", "list"}:  ClassWrite,
	{"stash", "save"}:  ClassWrite,
	{"stash", "apply"}: ClassWrite,
	{"stash", "drop"}:  ClassWrite,
}

// ClassOf returns the verb's class; an unknown verb is ClassFloor.
func ClassOf(tool, subcommand string) Class {
	return verbClasses[verb{tool, subcommand}]
}

// StaticVerdict is the ownership-free reading: read verbs Allow, everything
// else Ask. It is also the fallback when no session can be resolved.
func StaticVerdict(op Op) Verdict {
	if ClassOf(op.Tool, op.Subcommand) == ClassRead {
		return Allow
	}
	return Ask
}

// Unsupported wraps ErrUnsupported with the offending verb.
func Unsupported(op Op) error {
	if op.Subcommand == "" {
		return fmt.Errorf("%s: %w", op.Tool, ErrUnsupported)
	}
	return fmt.Errorf("%s %s: %w", op.Tool, op.Subcommand, ErrUnsupported)
}
