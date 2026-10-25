package session

// The frontend record (FDR 0035, amending FDR 0001).
//
// A confined worker's agent unit binds its worktree read-write, so everything
// under <worktree>/.spinclass is the worker's to rewrite. The authoritative copy
// of a session's State therefore lives in frontend state the unit cannot see:
//
//	<state>/spinclass/records/<hash>.json   live session (this file)
//	<state>/spinclass/index/<hash>.json     tombstone (a regular file), or the
//	                                        live session's index symlink
//
// where <hash> is indexFilename(worktree path). The worktree's own
// .spinclass/state.json is a DISPLAY-ONLY mirror: the frontend writes it for the
// worker to read and never acts on what it finds there, except once, to migrate a
// session recorded before the frontend record existed (legacyLoad).
//
// A record is found by its key, never by a path it names: every reader checks
// that the record's WorktreePath hashes to the key it was found under and that
// RepoPath and Branch agree with that WorktreePath (checkIdentity), and refuses
// and reports a record that does not.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"code.linenisgreat.com/spinclass/internal/0/safeio"
	"code.linenisgreat.com/spinclass/internal/0/sessionlog"
)

// recordsDir is the frontend-only directory of authoritative session records.
func recordsDir() string {
	return filepath.Join(xdgStateBase(), "spinclass", "records")
}

// recordPath is the authoritative record of the session at worktreeAbsPath.
func recordPath(worktreeAbsPath string) string {
	return filepath.Join(recordsDir(), indexFilename(worktreeAbsPath))
}

// RecordPath is recordPath for callers outside the package (tests that need
// to damage the authoritative record rather than its display copy).
func RecordPath(worktreeAbsPath string) string { return recordPath(worktreeAbsPath) }

// InUnit reports whether this process runs inside a confined agent unit (clown
// sets CLOWN_UNIT there and nowhere else, FDR 0035 "CLOWN_UNIT"). Such a process
// cannot reach the frontend record: it reads and writes only the display copy,
// and what it reads there is NOT authority.
func InUnit() bool { return os.Getenv("CLOWN_UNIT") != "" }

// report says, on stderr and in the lifecycle log, that the frontend refused or
// distrusted something a worker could have written. Every message names the
// record and the field. A refused identity names the path values involved; a
// worktree copy's disagreement names only which side sets the field, never a
// value (it may be a secret). The default sink prints each distinct
// message once per process, so a watching `sc list` does not repeat itself.
var report = defaultReport

var reported sync.Map

var (
	reportPrintMu sync.Mutex
	reportPrint   = func(line string) { fmt.Fprintln(os.Stderr, line) }
)

func defaultReport(msg string) {
	sessionlog.Errorf("%s", msg)
	if _, dup := reported.LoadOrStore(msg, true); dup {
		return
	}
	reportPrintMu.Lock()
	defer reportPrintMu.Unlock()
	reportPrint("spinclass: " + msg)
}

// RouteReports sends the default sink's printed lines to print instead of
// stderr until restore runs, for a command whose terminal or stdout belongs to
// a renderer (#405: an ndjson-crap stream must carry nothing but records).
// Logging and the once-per-process dedup are unchanged. print runs under a
// lock, one line at a time.
func RouteReports(print func(line string)) (restore func()) {
	reportPrintMu.Lock()
	defer reportPrintMu.Unlock()
	prev := reportPrint
	reportPrint = print
	return func() {
		reportPrintMu.Lock()
		defer reportPrintMu.Unlock()
		reportPrint = prev
	}
}

// SetReportSink replaces where reports go and returns a function restoring the
// previous sink. For tests.
func SetReportSink(sink func(msg string)) (restore func()) {
	prev := report
	report = sink
	return func() { report = prev }
}

// validBranch is the shape a session branch (its worktree directory name) may
// take: non-empty, relative, and made of plain path elements, so joining it onto
// <repo>/.worktrees can never leave that directory.
func validBranch(branch string) error {
	if branch == "" || strings.ContainsRune(branch, 0) {
		return errors.New("empty or contains a NUL")
	}
	if filepath.IsAbs(branch) || filepath.Clean(branch) != branch {
		return errors.New("not a clean relative path")
	}
	for _, el := range strings.Split(branch, "/") {
		if el == "" || el == "." || el == ".." {
			return fmt.Errorf("contains the path element %q", el)
		}
	}
	return nil
}

// checkIdentity reports why s cannot be the record of the session at wt. A
// normal session's worktree is exactly <repo>/.worktrees/<branch>; an implicit
// one is keyed by its checkout and carries no such relation.
func checkIdentity(s *State, wt string) error {
	if s.WorktreePath == "" || filepath.Clean(s.WorktreePath) != filepath.Clean(wt) {
		return fmt.Errorf("field worktree_path is %q, but this record is stored for %q", s.WorktreePath, wt)
	}
	if s.Kind == KindImplicit {
		return nil
	}
	if !filepath.IsAbs(s.RepoPath) {
		return fmt.Errorf("field repo_path is %q, which is not absolute", s.RepoPath)
	}
	if err := validBranch(s.Branch); err != nil {
		return fmt.Errorf("field branch is %q: %w", s.Branch, err)
	}
	if want := worktreeFromRepoBranch(s.RepoPath, s.Branch); want != filepath.Clean(s.WorktreePath) {
		return fmt.Errorf("field worktree_path is %q, but its repo_path %q and branch %q give %q", s.WorktreePath, s.RepoPath, s.Branch, want)
	}
	return nil
}

// checkRecordLocation is checkIdentity for a record found under index/records
// file name `name`: the key must be the hash of the record's own worktree path.
func checkRecordLocation(s *State, name string) error {
	if s.WorktreePath == "" {
		return errors.New("field worktree_path is empty")
	}
	if got := indexFilename(s.WorktreePath); got != name {
		return fmt.Errorf("field worktree_path is %q, which hashes to key %s, but the record is stored under key %s", s.WorktreePath, got, name)
	}
	return checkIdentity(s, s.WorktreePath)
}

func readRecordFile(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("record %s does not parse: %w", path, err)
	}
	return &s, nil
}

// writeRecordFile atomically replaces wt's frontend record with s.
func writeRecordFile(wt string, s *State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(recordsDir(), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(recordsDir(), ".rec-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, recordPath(wt)); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// refusal reports that a record was refused and returns the error to hand back.
func refusal(where string, err error) error {
	report(fmt.Sprintf("refusing session record %s: %v", where, err))
	return fmt.Errorf("session record %s refused: %w", where, err)
}

// loadByWorktree resolves the session at wt: a tombstone in the index wins, then
// the frontend record, then (once) a legacy worktree-only record. os.ErrNotExist
// when none exists. Inside a unit, which cannot see the frontend state, only the
// display copy is found, and it is not authority.
func loadByWorktree(wt string) (*State, error) {
	wt = filepath.Clean(wt)
	name := indexFilename(wt)

	if info, err := os.Lstat(indexPath(wt)); err == nil && info.Mode().IsRegular() {
		return readTombstone(indexPath(wt), name)
	}
	rec, err := readRecordFile(filepath.Join(recordsDir(), name))
	switch {
	case err == nil:
		if cerr := checkRecordLocation(rec, name); cerr != nil {
			return nil, refusal(filepath.Join(recordsDir(), name), cerr)
		}
		compareDisplay(rec)
		return rec, nil
	case errors.Is(err, os.ErrNotExist) || InUnit():
		return legacyLoad(wt)
	default:
		return nil, err
	}
}

// readTombstone loads and checks a tombstone: a copy of a record, made from a
// file the closed worker could write.
func readTombstone(path, name string) (*State, error) {
	s, err := readRecordFile(path)
	if err != nil {
		return nil, err
	}
	if cerr := checkRecordLocation(s, name); cerr != nil {
		return nil, refusal(path, cerr)
	}
	s.isTombstone = true
	return s, nil
}

// legacyLoad reads the worktree-local state.json of a session that has no
// frontend record: a session recorded before the record existed. Its content was
// worker-writable, so it is trusted ONCE, after the identity checks, and
// migrated into the frontend record at this first frontend touch (reported).
// Inside a unit nothing is migrated.
func legacyLoad(wt string) (*State, error) {
	path := worktreeStatePath(wt)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("record %s does not parse: %w", path, err)
	}
	if cerr := checkIdentity(&s, wt); cerr != nil {
		return nil, refusal(path, cerr)
	}
	if InUnit() {
		return &s, nil
	}
	if s.Kind != KindImplicit && s.SessionKey != "" {
		if want := filepath.Base(s.RepoPath) + "/" + s.Branch; s.SessionKey != want {
			return nil, refusal(path, fmt.Errorf("field session_key is %q, but its repo_path and branch give %q", s.SessionKey, want))
		}
	}
	if err := writeRecordFile(wt, &s); err != nil {
		return nil, fmt.Errorf("migrating legacy session record %s: %w", path, err)
	}
	report(fmt.Sprintf("migrated legacy session record %s from %s; its content was worker-writable and is trusted once", s.Key(), path))
	return &s, nil
}

// authorityFields are the State fields the frontend acts on; compareDisplay
// reports a worktree copy that differs on any of them.
var authorityFields = []struct {
	name string
	get  func(*State) any
}{
	{"repo_path", func(s *State) any { return s.RepoPath }},
	{"worktree_path", func(s *State) any { return s.WorktreePath }},
	{"branch", func(s *State) any { return s.Branch }},
	{"session_key", func(s *State) any { return s.SessionKey }},
	{"kind", func(s *State) any { return s.Kind }},
	{"principal", func(s *State) any { return s.Principal }},
	{"spawned_by", func(s *State) any { return s.SpawnedBy }},
	{"spawned_by_principal", func(s *State) any { return s.SpawnedByPrincipal }},
	{"spawner_released", func(s *State) any { return s.SpawnerReleased }},
	{"holders", func(s *State) any { return s.Holders }},
	{"pending_handles", func(s *State) any { return s.PendingHandles }},
	{"handle_rights", func(s *State) any { return s.HandleRights }},
	{"profile", func(s *State) any { return s.Profile }},
	{"profile_digest", func(s *State) any { return s.ProfileDigest }},
	{"profile_rebinds", func(s *State) any { return s.ProfileRebinds }},
	{"entry", func(s *State) any { return s.Entry }},
	{"credential", func(s *State) any { return s.Credential }},
	{"close_request", func(s *State) any { return s.CloseRequest }},
	{"restart_request", func(s *State) any { return s.RestartRequest }},
}

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// whichSideSets says which copy sets a disagreeing field, given each copy's JSON
// form, without the values: an authority field can carry a secret (a pinned
// credential's revoke command, an entry's env) and the report goes to stderr and
// the lifecycle log.
func whichSideSets(frontend, worktree string) string {
	unset := func(j string) bool {
		switch j {
		case "null", `""`, "[]", "{}", "0", "false":
			return true
		}
		return false
	}
	switch {
	case unset(frontend):
		return "the worktree copy sets it, the frontend record does not"
	case unset(worktree):
		return "the frontend record sets it, the worktree copy does not"
	default:
		return "both copies set it, to different values"
	}
}

// compareDisplay reports every authority field on which the worktree copy of rec
// disagrees with the frontend record. The frontend record is what is used; the
// copy is never trusted. Inside a unit there is nothing to compare against.
func compareDisplay(rec *State) {
	if InUnit() {
		return
	}
	path := worktreeStatePath(rec.WorktreePath)
	data, err := os.ReadFile(path)
	if err != nil {
		return // no copy is fine: it is display-only
	}
	var shown State
	if err := json.Unmarshal(data, &shown); err != nil {
		report(fmt.Sprintf("worktree copy %s of session record %s does not parse (%v); the frontend record is used", path, rec.Key(), err))
		return
	}
	for _, f := range authorityFields {
		if want, got := f.get(rec), f.get(&shown); !reflect.DeepEqual(want, got) && jsonString(want) != jsonString(got) {
			report(fmt.Sprintf("worktree copy %s of session record %s disagrees with the frontend record on %s: %s; the frontend record is used",
				path, rec.Key(), f.name, whichSideSets(jsonString(want), jsonString(got))))
		}
	}
}

// writeDisplayCopy writes the display-only mirror of s into its worktree. It
// refuses a symlinked .spinclass directory, and replaces (never follows) a
// symlink planted at state.json.
func writeDisplayCopy(wt string, s State) error {
	dir := filepath.Join(wt, ".spinclass")
	if err := safeio.RealDir(dir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(worktreeStatePath(wt), data)
}

// removeDisplayCopy deletes the display copy (and a stale lock) and the
// .spinclass directory when that leaves it empty. A .spinclass that is a symlink
// is left alone: removing "through" it would reach another session's files.
func removeDisplayCopy(wt string) {
	dir := filepath.Join(wt, ".spinclass")
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return
	}
	statePath := worktreeStatePath(wt)
	_ = os.Remove(statePath)
	_ = os.Remove(statePath + ".lock") // a lock left by a build that kept it here
	_ = os.Remove(dir)
}
