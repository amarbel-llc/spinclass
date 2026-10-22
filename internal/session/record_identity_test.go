package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureReports collects what the frontend reports about records it distrusts.
func captureReports(t *testing.T) *[]string {
	t.Helper()
	var msgs []string
	t.Cleanup(SetReportSink(func(m string) { msgs = append(msgs, m) }))
	return &msgs
}

func anyContains(msgs []string, subs ...string) bool {
outer:
	for _, m := range msgs {
		for _, s := range subs {
			if !strings.Contains(m, s) {
				continue outer
			}
		}
		return true
	}
	return false
}

// dropRecord turns a session into a legacy one: a worktree copy and an index
// symlink, no frontend record.
func dropRecord(t *testing.T, s State) {
	t.Helper()
	if err := os.Remove(recordPath(s.WorktreePath)); err != nil {
		t.Fatal(err)
	}
}

func writeRaw(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDisagreeingWorktreeCopyIsReportedAndTheFrontendRecordUsed(t *testing.T) {
	w1, _ := twoSessions(t)
	forged := w1
	forged.Principal = "evil-principal"
	forgeDisplayCopy(t, w1.WorktreePath, forged)
	msgs := captureReports(t)

	got, err := Read(w1.RepoPath, w1.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if got.Principal != "" {
		t.Errorf("principal = %q, want the frontend record's (empty)", got.Principal)
	}
	if !anyContains(*msgs, "principal", `"evil-principal"`, w1.Key(), "frontend record") {
		t.Errorf("the disagreement was not reported with record, field and both values: %q", *msgs)
	}
}

func TestLegacyRecordMigratesOnceAndIsReported(t *testing.T) {
	w1, _ := twoSessions(t)
	dropRecord(t, w1)
	msgs := captureReports(t)

	got, err := Read(w1.RepoPath, w1.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "w1" {
		t.Fatalf("legacy read = %+v", got)
	}
	if _, err := os.Stat(recordPath(w1.WorktreePath)); err != nil {
		t.Errorf("the legacy record was not migrated into the frontend record: %v", err)
	}
	if !anyContains(*msgs, "migrated legacy session record", w1.Key(), worktreeStatePath(w1.WorktreePath), "worker-writable", "trusted once") {
		t.Errorf("the migration was not reported: %q", *msgs)
	}

	// Once: a later edit of the worktree copy is no longer read.
	forged := w1
	forged.Principal = "evil-principal"
	forgeDisplayCopy(t, w1.WorktreePath, forged)
	again, err := Read(w1.RepoPath, w1.Branch)
	if err != nil || again.Principal != "" {
		t.Errorf("second read trusted the worktree copy: %+v, %v", again, err)
	}
	n := 0
	for _, m := range *msgs {
		if strings.Contains(m, "migrated legacy") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("migration reported %d times, want once", n)
	}
}

func TestLegacyRecordWithMismatchedIdentityIsRefusedNotMigrated(t *testing.T) {
	w1, s2 := twoSessions(t)
	dropRecord(t, w1)
	forgeDisplayCopy(t, w1.WorktreePath, forgedVictimCopy(s2))
	msgs := captureReports(t)

	if _, err := Read(w1.RepoPath, w1.Branch); err == nil {
		t.Fatal("a legacy record naming another session's worktree was accepted")
	}
	if _, err := os.Stat(recordPath(w1.WorktreePath)); err == nil {
		t.Error("a mismatched legacy record was migrated")
	}
	if !anyContains(*msgs, "refusing session record", "worktree_path", w1.WorktreePath, s2.WorktreePath) {
		t.Errorf("the refusal did not name the field and both values: %q", *msgs)
	}
	all, err := ListAll(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if s.Principal == "evil-principal" {
			t.Errorf("ListAll returned the refused legacy record: %+v", s)
		}
	}
}

func TestLegacyRecordWithEscapingBranchIsRefused(t *testing.T) {
	w1, _ := twoSessions(t)
	dropRecord(t, w1)
	forged := w1
	forged.Branch = "../w1"
	forgeDisplayCopy(t, w1.WorktreePath, forged)
	captureReports(t)
	if _, err := Read(w1.RepoPath, w1.Branch); err == nil {
		t.Fatal("a legacy record whose branch contains .. was accepted")
	}
}

func TestTombstoneKeyedForAnotherWorktreeIsRefused(t *testing.T) {
	w1, s2 := twoSessions(t)
	// A tombstone at W1's index path that is a copy of S2's record.
	victim, err := Read(s2.RepoPath, s2.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(indexPath(w1.WorktreePath)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(recordPath(w1.WorktreePath)); err != nil {
		t.Fatal(err)
	}
	writeRaw(t, indexPath(w1.WorktreePath), victim)
	msgs := captureReports(t)

	if _, err := Read(w1.RepoPath, w1.Branch); err == nil {
		t.Error("a tombstone keyed for another worktree was accepted by Read")
	}
	all, err := ListAll(nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := countBranch(all, "victim"); n != 1 {
		t.Errorf("ListAll returned the victim %d times, want 1", n)
	}
	if !anyContains(*msgs, "worktree_path", s2.WorktreePath, "key") {
		t.Errorf("the refusal did not name the field and values: %q", *msgs)
	}
}

func countBranch(states []State, branch string) int {
	n := 0
	for _, s := range states {
		if s.Branch == branch {
			n++
		}
	}
	return n
}

// A tombstone is a copy of a record the closed worker could write; one whose
// branch climbs out of .worktrees is refused even when its own key matches.
func TestTombstoneWithADotDotBranchIsRefused(t *testing.T) {
	w1, _ := twoSessions(t)
	escaped := filepath.Join(w1.RepoPath, "escaped")
	tomb := w1
	tomb.WorktreePath = escaped
	tomb.Branch = "../escaped"
	tomb.DeletedSHA = "deadbeef"
	writeRaw(t, indexPath(escaped), tomb)
	captureReports(t)

	all, err := ListAll(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if s.Branch == "../escaped" {
			t.Errorf("ListAll returned a tombstone whose branch contains ..: %+v", s)
		}
	}
	if _, err := FindByTarget("repo/../escaped"); err == nil {
		t.Error("FindByTarget resolved the escaping tombstone")
	}
}

func TestRecordUnderTheWrongKeyIsRefused(t *testing.T) {
	w1, s2 := twoSessions(t)
	data, err := os.ReadFile(recordPath(s2.WorktreePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath(w1.WorktreePath), data, 0o600); err != nil {
		t.Fatal(err)
	}
	msgs := captureReports(t)
	if _, err := Read(w1.RepoPath, w1.Branch); err == nil {
		t.Error("a record stored under another session's key was accepted")
	}
	if !anyContains(*msgs, "worktree_path", s2.WorktreePath, "key") {
		t.Errorf("not reported: %q", *msgs)
	}
}

func TestBranchesThatEscapeAreRefusedByTheMutators(t *testing.T) {
	w1, s2 := twoSessions(t)
	victimDisplay := mustReadFile(t, worktreeStatePath(s2.WorktreePath))
	for _, bad := range []string{"../victim", "..", "", "a/../victim", "/abs"} {
		if err := Remove(w1.RepoPath, bad); err == nil {
			t.Errorf("Remove accepted branch %q", bad)
		}
		if err := Tombstone(w1.RepoPath, bad, ""); err == nil {
			t.Errorf("Tombstone accepted branch %q", bad)
		}
	}
	if after := mustReadFile(t, worktreeStatePath(s2.WorktreePath)); !bytes.Equal(after, victimDisplay) {
		t.Error("the victim's display copy changed")
	}
	if _, err := Read(s2.RepoPath, s2.Branch); err != nil {
		t.Errorf("the victim was removed: %v", err)
	}
}

// A worker that replaces its .spinclass directory with a symlink to another
// session's must not redirect the frontend's writes or removals there.
func TestSymlinkedSpinclassDirIsNeverFollowed(t *testing.T) {
	w1, s2 := twoSessions(t)
	victimDisplay := mustReadFile(t, worktreeStatePath(s2.WorktreePath))
	captureReports(t)

	dir := filepath.Join(w1.WorktreePath, ".spinclass")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s2.WorktreePath, ".spinclass"), dir); err != nil {
		t.Fatal(err)
	}

	st, err := Read(w1.RepoPath, w1.Branch)
	if err != nil {
		t.Fatal(err)
	}
	st.Description = "changed by the frontend"
	if err := Write(*st); err != nil {
		t.Fatal(err)
	}
	if after := mustReadFile(t, worktreeStatePath(s2.WorktreePath)); !bytes.Equal(after, victimDisplay) {
		t.Errorf("Write followed the planted .spinclass symlink into the victim:\n%s", after)
	}
	if err := Remove(w1.RepoPath, w1.Branch); err != nil {
		t.Fatal(err)
	}
	if after, err := os.ReadFile(worktreeStatePath(s2.WorktreePath)); err != nil || !bytes.Equal(after, victimDisplay) {
		t.Errorf("Remove followed the planted .spinclass symlink into the victim: %v", err)
	}
}
