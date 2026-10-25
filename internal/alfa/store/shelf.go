package store

import (
	"fmt"
	"strings"
	"time"
)

// ShelfEntry describes one saved set of working changes (FDR 0036 section 4).
// ID is stable for the entry's life; the newest entry has the highest ID.
type ShelfEntry struct {
	ID             int
	Message        string
	Branch         string
	BaseSHA        string
	Time           time.Time
	TrackedFiles   int
	UntrackedFiles int
	HasIndex       bool
}

// ShelfSaveRequest names what to shelve. Paths limits the tracked and untracked
// changes taken; empty means everything.
type ShelfSaveRequest struct {
	Message          string
	IncludeUntracked bool
	Paths            []string
}

// Shelf is the stash emulation contract: a per-worktree set of entries that
// never touches any store-wide stash, so one session cannot see or pop
// another's. dir is any directory inside the worktree. A ref names an entry:
// empty is the newest, "stash@{N}" is the Nth newest, a bare integer is an
// entry ID.
type Shelf interface {
	Save(dir string, req ShelfSaveRequest) (ShelfEntry, error)
	// List returns the entries newest first.
	List(dir string) ([]ShelfEntry, error)
	// Apply restores an entry and keeps it.
	Apply(dir, ref string) (ShelfEntry, error)
	Drop(dir, ref string) (ShelfEntry, error)
}

// ShelfBackend is implemented by backends that provide a Shelf.
type ShelfBackend interface {
	Shelf() Shelf
}

// ShelfConflictError reports an apply that left conflict markers in the working
// tree. The entry is kept.
type ShelfConflictError struct {
	Entry  ShelfEntry
	Paths  []string
	Detail string
}

func (e *ShelfConflictError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "applying stash entry %d produced conflicts; conflict markers are in the working tree and the entry was kept.", e.Entry.ID)
	if len(e.Paths) > 0 {
		fmt.Fprintf(&b, "\nconflicted paths: %s", strings.Join(e.Paths, ", "))
	}
	b.WriteString("\nresolve and stage the paths, then run stash drop to discard the entry.")
	if e.Detail != "" {
		b.WriteString("\n" + e.Detail)
	}
	return b.String()
}
