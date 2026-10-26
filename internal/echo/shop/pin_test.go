package shop

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
)

func pinRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return repo
}

func entrySweatfile(start, resume string, env map[string]string) sweatfile.Sweatfile {
	e := &sweatfile.SessionEntry{Start: []string{start}, Env: env}
	if resume != "" {
		e.Resume = []string{resume}
	}
	return sweatfile.Sweatfile{SessionEntry: e}
}

func TestPinEntry(t *testing.T) {
	got := PinEntry(entrySweatfile("/bin/s", "/bin/r", map[string]string{"K": "v"}))
	want := &session.PinnedEntry{Start: []string{"/bin/s"}, Resume: []string{"/bin/r"}, Env: map[string]string{"K": "v"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PinEntry = %+v, want %+v", got, want)
	}
}

// A fresh session records the entry its sweatfile layers resolved to.
func TestAttachPinsEntryAtCreation(t *testing.T) {
	repo := pinRepo(t)
	wt := filepath.Join(repo, worktree.WorktreesDir, "fresh")
	rp := worktree.ResolvedPath{AbsPath: wt, RepoPath: repo, Branch: "fresh", SessionKey: "repo/fresh"}

	sf := entrySweatfile("/bin/pinned-start", "/bin/pinned-resume", map[string]string{"MARK": "created"})
	if err := Attach(&bytes.Buffer{}, &mockExecutor{}, rp, sf, "", false, true, false, false, false); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	st, err := session.Read(repo, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	want := &session.PinnedEntry{
		Start: []string{"/bin/pinned-start"}, Resume: []string{"/bin/pinned-resume"},
		Env: map[string]string{"MARK": "created"},
	}
	if !reflect.DeepEqual(st.Entry, want) {
		t.Errorf("Entry = %+v, want %+v", st.Entry, want)
	}
}

// A resume (the worktree already exists) must neither re-pin from the sweatfile
// it was handed nor drop the pin it has.
func TestAttachKeepsPinnedEntryOnResume(t *testing.T) {
	repo := pinRepo(t)
	wt := filepath.Join(repo, worktree.WorktreesDir, "old")
	cmd := exec.Command("git", "worktree", "add", "-b", "old", wt)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	pinned := &session.PinnedEntry{Start: []string{"/bin/original"}, Env: map[string]string{"MARK": "original"}}
	if err := session.Write(session.State{
		RepoPath: repo, WorktreePath: wt, Branch: "old", SessionKey: "repo/old",
		SessionState: session.StateInactive, Entry: pinned,
	}); err != nil {
		t.Fatal(err)
	}

	rp := worktree.ResolvedPath{AbsPath: wt, RepoPath: repo, Branch: "old", SessionKey: "repo/old"}
	later := entrySweatfile("/bin/changed-later", "", map[string]string{"MARK": "changed"})
	if err := Attach(&bytes.Buffer{}, &mockExecutor{}, rp, later, "", false, true, false, false, false); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	st, err := session.Read(repo, "old")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.Entry, pinned) {
		t.Errorf("Entry = %+v, want the original pin %+v", st.Entry, pinned)
	}
}

// A legacy session (no pin) stays unpinned: re-pinning on resume would bless
// whatever the layers say now as if it had been resolved at creation.
func TestAttachLeavesLegacySessionUnpinned(t *testing.T) {
	repo := pinRepo(t)
	wt := filepath.Join(repo, worktree.WorktreesDir, "legacy")
	cmd := exec.Command("git", "worktree", "add", "-b", "legacy", wt)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	if err := session.Write(session.State{
		RepoPath: repo, WorktreePath: wt, Branch: "legacy", SessionKey: "repo/legacy",
		SessionState: session.StateInactive,
	}); err != nil {
		t.Fatal(err)
	}

	rp := worktree.ResolvedPath{AbsPath: wt, RepoPath: repo, Branch: "legacy", SessionKey: "repo/legacy"}
	if err := Attach(&bytes.Buffer{}, &mockExecutor{}, rp, entrySweatfile("/bin/x", "", nil), "", false, true, false, false, false); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	st, err := session.Read(repo, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if st.Entry != nil {
		t.Errorf("Entry = %+v, want nil for a legacy session", st.Entry)
	}
}
