package shop

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/worktree"
)

// on-attach and on-detach run `sh -c` in the operator's terminal, outside any
// unit, on every start and resume. Like the session entry, they are pinned
// when the session is created and replayed from the pin, never re-read later.

func hookSweatfile(onAttach, onDetach string) sweatfile.Sweatfile {
	return sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{OnAttach: &onAttach, OnDetach: &onDetach}}
}

func TestAttachPinsTheAttachAndDetachHooksAtCreation(t *testing.T) {
	repo := pinRepo(t)
	wt := filepath.Join(repo, worktree.WorktreesDir, "fresh")
	rp := worktree.ResolvedPath{AbsPath: wt, RepoPath: repo, Branch: "fresh", SessionKey: "repo/fresh"}

	if err := Attach(&bytes.Buffer{}, &mockExecutor{}, rp, hookSweatfile("echo attach", "echo detach"), "", false, true, false, false, false); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	st, err := session.Read(repo, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if st.Entry == nil || st.Entry.OnAttach == nil || *st.Entry.OnAttach != "echo attach" ||
		st.Entry.OnDetach == nil || *st.Entry.OnDetach != "echo detach" {
		t.Errorf("Entry = %+v, want the on-attach and on-detach commands pinned", st.Entry)
	}
}

func TestResumeRunsThePinnedHooksNotTheLiveOnes(t *testing.T) {
	repo := pinRepo(t)
	wt := filepath.Join(repo, worktree.WorktreesDir, "old")
	cmd := exec.Command("git", "worktree", "add", "-b", "old", wt)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	marks := t.TempDir()
	pinnedAttach := "touch " + filepath.Join(marks, "pinned-attach")
	pinnedDetach := "touch " + filepath.Join(marks, "pinned-detach")
	if err := session.Write(session.State{
		RepoPath: repo, WorktreePath: wt, Branch: "old", SessionKey: "repo/old",
		SessionState: session.StateInactive,
		Entry: &session.PinnedEntry{
			Start: []string{"true"}, OnAttach: &pinnedAttach, OnDetach: &pinnedDetach,
		},
	}); err != nil {
		t.Fatal(err)
	}

	rp := worktree.ResolvedPath{AbsPath: wt, RepoPath: repo, Branch: "old", SessionKey: "repo/old"}
	live := hookSweatfile("touch "+filepath.Join(marks, "live-attach"), "touch "+filepath.Join(marks, "live-detach"))
	if err := Attach(&bytes.Buffer{}, &mockExecutor{}, rp, live, "", false, false, false, false, false); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	for _, name := range []string{"pinned-attach", "pinned-detach"} {
		if _, err := os.Stat(filepath.Join(marks, name)); err != nil {
			t.Errorf("the pinned hook did not run (%s missing)", name)
		}
	}
	for _, name := range []string{"live-attach", "live-detach"} {
		if _, err := os.Stat(filepath.Join(marks, name)); err == nil {
			t.Errorf("a hook from the live sweatfile ran (%s exists)", name)
		}
	}
}

// A legacy session (no pin) keeps today's behaviour: its hooks come from the
// sweatfile layers it is handed.
func TestLegacySessionHooksComeFromTheLayers(t *testing.T) {
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
	marks := t.TempDir()
	rp := worktree.ResolvedPath{AbsPath: wt, RepoPath: repo, Branch: "legacy", SessionKey: "repo/legacy"}
	sf := hookSweatfile("touch "+filepath.Join(marks, "attach"), "true")
	if err := Attach(&bytes.Buffer{}, &mockExecutor{}, rp, sf, "", false, false, false, false, false); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := os.Stat(filepath.Join(marks, "attach")); err != nil {
		t.Error("a legacy session's on-attach from the layers did not run")
	}
}
