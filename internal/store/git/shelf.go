package git

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/store"
)

// Shelf is the git backend's stash emulation (FDR 0036 section 4): one
// directory per entry under <worktree>/.spinclass/stash/<id>/, holding
// meta.json, tracked.patch, an optional index.patch and an untracked/ copy.
// refs/stash is never read or written, so a worktree's shelf is invisible to
// every other worktree of the repository.
type Shelf struct{}

// NewShelf returns the git backend's shelf.
func NewShelf() Shelf { return Shelf{} }

var _ store.Shelf = Shelf{}

// Shelf returns the backend's shelf.
func (Backend) Shelf() store.Shelf { return Shelf{} }

var _ store.ShelfBackend = Backend{}

const (
	trackedPatchName = "tracked.patch"
	indexPatchName   = "index.patch"
	untrackedDirName = "untracked"
	metaName         = "meta.json"
)

var errNothingToStash = errors.New("no local changes to save")

type shelfMeta struct {
	ID               int             `json:"id"`
	Message          string          `json:"message"`
	BaseSHA          string          `json:"base_sha"`
	Branch           string          `json:"branch"`
	Time             time.Time       `json:"time"`
	IncludeUntracked bool            `json:"include_untracked"`
	HasIndex         bool            `json:"has_index"`
	Paths            []string        `json:"paths,omitempty"`
	TrackedPaths     []string        `json:"tracked_paths"`
	AddedPaths       []string        `json:"added_paths,omitempty"`
	Untracked        []untrackedFile `json:"untracked,omitempty"`
}

// untrackedFile is one manifest row: Symlink is the link target when the saved
// file was a symlink, otherwise Mode is the permission bits to restore.
type untrackedFile struct {
	Path    string `json:"path"`
	Mode    uint32 `json:"mode"`
	Symlink string `json:"symlink,omitempty"`
}

func (m shelfMeta) entry() store.ShelfEntry {
	return store.ShelfEntry{
		ID:             m.ID,
		Message:        m.Message,
		Branch:         m.Branch,
		BaseSHA:        m.BaseSHA,
		Time:           m.Time,
		TrackedFiles:   len(m.TrackedPaths),
		UntrackedFiles: len(m.Untracked),
		HasIndex:       m.HasIndex,
	}
}

func shelfRoot(worktree string) string {
	return filepath.Join(worktree, ".spinclass", "stash")
}

func worktreeRoot(dir string) (string, error) {
	root, err := git.Run(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not inside a git worktree: %w", err)
	}
	return root, nil
}

// entryIDs lists the entry numbers under a shelf root, newest (highest) first.
// Hidden in-progress directories and strays are ignored.
func entryIDs(shelfDir string) ([]int, error) {
	items, err := os.ReadDir(shelfDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, item := range items {
		id, convErr := strconv.Atoi(item.Name())
		if convErr == nil && id >= 1 && item.IsDir() {
			ids = append(ids, id)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ids)))
	return ids, nil
}

// CountShelfEntries counts a worktree's shelf entries from the filesystem
// alone, so it works on a worktree git can no longer describe.
func CountShelfEntries(worktree string) (int, error) {
	ids, err := entryIDs(shelfRoot(worktree))
	return len(ids), err
}

// ShelfNotEmpty is true when the worktree's shelf has entries or cannot be
// read. Teardown paths that would delete the worktree use it to fail closed:
// a shelf they cannot prove empty is a shelf they must not destroy.
func ShelfNotEmpty(worktree string) bool {
	entries, err := CountShelfEntries(worktree)
	return err != nil || entries > 0
}

func readMeta(entryDir string) (shelfMeta, error) {
	raw, err := os.ReadFile(filepath.Join(entryDir, metaName))
	if err != nil {
		return shelfMeta{}, err
	}
	var m shelfMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return shelfMeta{}, err
	}
	return m, nil
}

func (Shelf) List(dir string) ([]store.ShelfEntry, error) {
	root, err := worktreeRoot(dir)
	if err != nil {
		return nil, err
	}
	ids, err := entryIDs(shelfRoot(root))
	if err != nil {
		return nil, err
	}
	entries := make([]store.ShelfEntry, 0, len(ids))
	for _, id := range ids {
		meta, readErr := readMeta(filepath.Join(shelfRoot(root), strconv.Itoa(id)))
		if readErr != nil {
			entries = append(entries, store.ShelfEntry{ID: id, Message: "(unreadable entry: " + readErr.Error() + ")"})
			continue
		}
		entries = append(entries, meta.entry())
	}
	return entries, nil
}

var positionRef = regexp.MustCompile(`^stash@\{(\d+)\}$`)

// resolveEntry maps a stash_ref to an entry ID: empty is the newest,
// stash@{N} the Nth newest, a bare integer an entry ID.
func resolveEntry(shelfDir, ref string) (int, error) {
	ids, err := entryIDs(shelfDir)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, errors.New("no stash entries")
	}
	if ref == "" {
		return ids[0], nil
	}
	if match := positionRef.FindStringSubmatch(ref); match != nil {
		position, _ := strconv.Atoi(match[1])
		if position >= len(ids) {
			return 0, fmt.Errorf("%s is not a stash entry (%d entries)", ref, len(ids))
		}
		return ids[position], nil
	}
	if id, convErr := strconv.Atoi(ref); convErr == nil {
		for _, existing := range ids {
			if existing == id {
				return id, nil
			}
		}
	}
	return 0, fmt.Errorf("%q is not a stash entry; use stash@{N} or an entry number from stash list", ref)
}

func (Shelf) Save(dir string, req store.ShelfSaveRequest) (store.ShelfEntry, error) {
	root, err := worktreeRoot(dir)
	if err != nil {
		return store.ShelfEntry{}, err
	}
	if unmerged, _ := git.UnmergedPaths(root); len(unmerged) > 0 {
		return store.ShelfEntry{}, fmt.Errorf("cannot stash with unresolved conflicts: %s", strings.Join(unmerged, ", "))
	}
	base, err := git.RevParse(root, "HEAD")
	if err != nil {
		return store.ShelfEntry{}, fmt.Errorf("cannot stash before the first commit: %w", err)
	}
	changed, added, err := trackedChanges(root, req.Paths)
	if err != nil {
		return store.ShelfEntry{}, err
	}
	trackedPatch, err := diffPatch(root, false, req.Paths)
	if err != nil {
		return store.ShelfEntry{}, err
	}
	indexPatch, err := diffPatch(root, true, req.Paths)
	if err != nil {
		return store.ShelfEntry{}, err
	}
	var untracked []untrackedFile
	if req.IncludeUntracked {
		if untracked, err = listUntracked(root, req.Paths); err != nil {
			return store.ShelfEntry{}, err
		}
	}
	if len(changed) == 0 && len(untracked) == 0 {
		return store.ShelfEntry{}, errNothingToStash
	}

	ids, err := entryIDs(shelfRoot(root))
	if err != nil {
		return store.ShelfEntry{}, err
	}
	nextID := 1
	if len(ids) > 0 {
		nextID = ids[0] + 1
	}
	meta := shelfMeta{
		ID:               nextID,
		Message:          shelfMessage(root, req.Message),
		BaseSHA:          base,
		Branch:           currentBranchLabel(root),
		Time:             time.Now().UTC(),
		IncludeUntracked: req.IncludeUntracked,
		HasIndex:         len(indexPatch) > 0,
		Paths:            req.Paths,
		TrackedPaths:     changed,
		AddedPaths:       added,
		Untracked:        untracked,
	}
	if err := writeEntry(root, meta, trackedPatch, indexPatch); err != nil {
		return store.ShelfEntry{}, err
	}

	if err := discardTrackedChanges(root, changed, added); err != nil {
		return meta.entry(), fmt.Errorf("saved as stash entry %d but could not restore the working tree: %w", meta.ID, err)
	}
	removeUntracked(root, untracked)
	return meta.entry(), nil
}

func currentBranchLabel(root string) string {
	if branch, err := git.BranchCurrent(root); err == nil && branch != "" {
		return branch
	}
	return "(no branch)"
}

// shelfMessage mirrors git stash: "On <branch>: <message>", or the WIP form
// naming the HEAD commit when no message is given.
func shelfMessage(root, message string) string {
	branch := currentBranchLabel(root)
	if message != "" {
		return "On " + branch + ": " + message
	}
	head, _ := git.Run(root, "log", "-1", "--format=%h %s")
	return "WIP on " + branch + ": " + head
}

// patchDiffArgs pins every diff option that user config could change and so
// break a later `git apply` (prefixes, external drivers, rename detection).
func patchDiffArgs(staged bool, paths []string, extra ...string) []string {
	argv := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/"}
	argv = append(argv, extra...)
	if staged {
		argv = append(argv, "--cached")
	}
	argv = append(argv, "HEAD")
	return withPaths(argv, paths)
}

func diffPatch(root string, staged bool, paths []string) ([]byte, error) {
	return git.RunStdin(root, "", patchDiffArgs(staged, paths, "--binary", "--full-index")...)
}

const submoduleMode = "160000"

// trackedChanges lists the tracked paths differing from HEAD (staged or not),
// and the subset that HEAD does not have. Submodule changes are refused.
func trackedChanges(root string, paths []string) (changed, added []string, err error) {
	out, err := git.RunStdin(root, "", patchDiffArgs(false, paths, "--raw", "-z")...)
	if err != nil {
		return nil, nil, err
	}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		parts := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(parts) < 5 {
			return nil, nil, fmt.Errorf("unexpected git diff --raw record %q", fields[i])
		}
		path := fields[i+1]
		if parts[0] == submoduleMode || parts[1] == submoduleMode {
			return nil, nil, fmt.Errorf("stash does not support submodule changes (%s)", path)
		}
		changed = append(changed, path)
		if parts[4][0] == 'A' {
			added = append(added, path)
		}
	}
	return changed, added, nil
}

func isShelfPath(rel string) bool {
	return rel == ".spinclass" || strings.HasPrefix(rel, ".spinclass/")
}

func listUntracked(root string, paths []string) ([]untrackedFile, error) {
	out, err := git.RunStdin(root, "", withPaths([]string{"ls-files", "--others", "--exclude-standard", "-z"}, paths)...)
	if err != nil {
		return nil, err
	}
	var files []untrackedFile
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" || isShelfPath(rel) || strings.HasSuffix(rel, "/") {
			continue
		}
		info, statErr := os.Lstat(filepath.Join(root, rel))
		if statErr != nil {
			return nil, statErr
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, linkErr := os.Readlink(filepath.Join(root, rel))
			if linkErr != nil {
				return nil, linkErr
			}
			files = append(files, untrackedFile{Path: rel, Symlink: target})
		case info.Mode().IsRegular():
			files = append(files, untrackedFile{Path: rel, Mode: uint32(info.Mode().Perm())})
		}
	}
	return files, nil
}

// writeEntry assembles the entry in a hidden directory and renames it into
// place, so a crash never leaves a half-written entry visible to List.
func writeEntry(root string, meta shelfMeta, trackedPatch, indexPatch []byte) error {
	shelfDir := shelfRoot(root)
	if err := os.MkdirAll(shelfDir, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(shelfDir, ".saving-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	if err := os.WriteFile(filepath.Join(staging, trackedPatchName), trackedPatch, 0o644); err != nil {
		return err
	}
	if len(indexPatch) > 0 {
		if err := os.WriteFile(filepath.Join(staging, indexPatchName), indexPatch, 0o644); err != nil {
			return err
		}
	}
	for _, file := range meta.Untracked {
		if err := copyUntracked(filepath.Join(root, file.Path), filepath.Join(staging, untrackedDirName, file.Path), file); err != nil {
			return err
		}
	}
	rawMeta, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(staging, metaName), rawMeta, 0o644); err != nil {
		return err
	}
	return os.Rename(staging, filepath.Join(shelfDir, strconv.Itoa(meta.ID)))
}

func copyUntracked(from, to string, file untrackedFile) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if file.Symlink != "" {
		return os.Symlink(file.Symlink, to)
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(to, fs.FileMode(file.Mode))
}

func nulList(paths []string) string {
	return strings.Join(paths, "\x00") + "\x00"
}

// runWithPathList runs a git command that reads literal paths from stdin, which
// keeps the argument list bounded and the paths exact.
func runWithPathList(root string, paths []string, args ...string) error {
	if len(paths) == 0 {
		return nil
	}
	argv := append([]string{"--literal-pathspecs"}, args...)
	argv = append(argv, "--pathspec-from-file=-", "--pathspec-file-nul")
	_, err := git.RunStdin(root, nulList(paths), argv...)
	return err
}

func without(all, exclude []string) []string {
	skip := map[string]bool{}
	for _, path := range exclude {
		skip[path] = true
	}
	var kept []string
	for _, path := range all {
		if !skip[path] {
			kept = append(kept, path)
		}
	}
	return kept
}

// discardTrackedChanges returns the shelved paths to HEAD in the index and the
// working tree, like the tail of git stash push.
func discardTrackedChanges(root string, changed, added []string) error {
	if err := runWithPathList(root, added, "rm", "-f", "-q"); err != nil {
		return err
	}
	return runWithPathList(root, without(changed, added), "checkout", "HEAD")
}

func removeUntracked(root string, files []untrackedFile) {
	for _, file := range files {
		if os.Remove(filepath.Join(root, file.Path)) != nil {
			continue
		}
		for rel := filepath.Dir(file.Path); rel != "." && rel != ""; rel = filepath.Dir(rel) {
			if os.Remove(filepath.Join(root, rel)) != nil {
				break
			}
		}
	}
}

func loadEntry(root, ref string) (string, shelfMeta, error) {
	id, err := resolveEntry(shelfRoot(root), ref)
	if err != nil {
		return "", shelfMeta{}, err
	}
	entryDir := filepath.Join(shelfRoot(root), strconv.Itoa(id))
	meta, err := readMeta(entryDir)
	if err != nil {
		return "", shelfMeta{}, fmt.Errorf("stash entry %d is unreadable: %w", id, err)
	}
	return entryDir, meta, nil
}

func (Shelf) Apply(dir, ref string) (store.ShelfEntry, error) {
	root, err := worktreeRoot(dir)
	if err != nil {
		return store.ShelfEntry{}, err
	}
	entryDir, meta, err := loadEntry(root, ref)
	if err != nil {
		return store.ShelfEntry{}, err
	}
	entry := meta.entry()
	if unmerged, _ := git.UnmergedPaths(root); len(unmerged) > 0 {
		return entry, fmt.Errorf("cannot apply with unresolved conflicts: %s", strings.Join(unmerged, ", "))
	}
	if existing := existingUntracked(root, meta.Untracked); len(existing) > 0 {
		return entry, fmt.Errorf("cannot apply stash entry %d: these untracked files already exist: %s", meta.ID, strings.Join(existing, ", "))
	}

	if len(meta.TrackedPaths) > 0 {
		if err := applyTracked(root, entryDir, meta); err != nil {
			unmerged, _ := git.UnmergedPaths(root)
			if len(unmerged) == 0 {
				return entry, fmt.Errorf("applying stash entry %d: %w", meta.ID, err)
			}
			_ = restoreUntracked(root, entryDir, meta.Untracked)
			return entry, &store.ShelfConflictError{Entry: entry, Paths: unmerged, Detail: err.Error()}
		}
	}
	if err := restoreUntracked(root, entryDir, meta.Untracked); err != nil {
		return entry, fmt.Errorf("restoring untracked files of stash entry %d: %w", meta.ID, err)
	}
	return entry, nil
}

// applyTracked replays tracked.patch with a 3-way merge (which also stages),
// then resets those paths so the changes come back unstaged, and re-stages
// exactly what was staged at save time from index.patch.
func applyTracked(root, entryDir string, meta shelfMeta) error {
	if _, err := git.Run(root, "apply", "--3way", filepath.Join(entryDir, trackedPatchName)); err != nil {
		return err
	}
	if err := runWithPathList(root, meta.TrackedPaths, "reset", "-q"); err != nil {
		return err
	}
	if meta.HasIndex {
		if _, err := git.Run(root, "apply", "--cached", filepath.Join(entryDir, indexPatchName)); err != nil {
			return err
		}
	}
	return nil
}

func existingUntracked(root string, files []untrackedFile) []string {
	var existing []string
	for _, file := range files {
		if _, err := os.Lstat(filepath.Join(root, file.Path)); err == nil {
			existing = append(existing, file.Path)
		}
	}
	return existing
}

func restoreUntracked(root, entryDir string, files []untrackedFile) error {
	for _, file := range files {
		from := filepath.Join(entryDir, untrackedDirName, file.Path)
		to := filepath.Join(root, file.Path)
		if file.Symlink != "" {
			if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(file.Symlink, to); err != nil {
				return err
			}
			continue
		}
		if err := copyUntracked(from, to, file); err != nil {
			return err
		}
	}
	return nil
}

func (Shelf) Drop(dir, ref string) (store.ShelfEntry, error) {
	root, err := worktreeRoot(dir)
	if err != nil {
		return store.ShelfEntry{}, err
	}
	id, err := resolveEntry(shelfRoot(root), ref)
	if err != nil {
		return store.ShelfEntry{}, err
	}
	entryDir := filepath.Join(shelfRoot(root), strconv.Itoa(id))
	entry := store.ShelfEntry{ID: id}
	if meta, readErr := readMeta(entryDir); readErr == nil {
		entry = meta.entry()
	}
	if err := os.RemoveAll(entryDir); err != nil {
		return entry, err
	}
	return entry, nil
}

// doStash runs a stash verb against the worktree containing dir.
func doStash(dir string, op store.Op, a args) (store.Result, error) {
	shelf := NewShelf()
	switch op.Subcommand {
	case "save":
		entry, err := shelf.Save(dir, store.ShelfSaveRequest{Message: a.Message, IncludeUntracked: a.IncludeUntracked, Paths: a.Paths})
		if err != nil {
			return store.Result{}, err
		}
		return store.Result{Text: fmt.Sprintf("Saved stash entry %d: %s", entry.ID, entry.Message)}, nil
	case "list":
		entries, err := shelf.List(dir)
		if err != nil {
			return store.Result{}, err
		}
		return store.Result{Text: formatShelfList(entries)}, nil
	case "apply":
		entry, err := shelf.Apply(dir, a.StashRef)
		if err != nil {
			return store.Result{}, err
		}
		return store.Result{Text: fmt.Sprintf("Applied stash entry %d (kept): %s", entry.ID, entry.Message)}, nil
	case "drop":
		entry, err := shelf.Drop(dir, a.StashRef)
		if err != nil {
			return store.Result{}, err
		}
		return store.Result{Text: fmt.Sprintf("Dropped stash entry %d: %s", entry.ID, entry.Message)}, nil
	}
	return store.Result{}, store.Unsupported(op)
}

func formatShelfList(entries []store.ShelfEntry) string {
	if len(entries) == 0 {
		return "no stash entries"
	}
	var b strings.Builder
	for position, entry := range entries {
		fmt.Fprintf(&b, "stash@{%d} (entry %d): %s [%d tracked, %d untracked", position, entry.ID, entry.Message, entry.TrackedFiles, entry.UntrackedFiles)
		if entry.HasIndex {
			b.WriteString(", index")
		}
		b.WriteString("]\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
