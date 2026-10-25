package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/executor"
	"code.linenisgreat.com/spinclass/internal/testgit"
	"code.linenisgreat.com/spinclass/internal/worktree"
)

// attachCapture records what a resume/start handed shop.Attach, so a test can
// assert on the executor without exec'ing anything.
type attachCapture struct {
	called bool
	exec   executor.Executor
	sf     sweatfile.Sweatfile
}

func stubAttach(t *testing.T) *attachCapture {
	t.Helper()
	c := &attachCapture{}
	prev := attachSessionFn
	attachSessionFn = func(_ io.Writer, exec executor.Executor, _ worktree.ResolvedPath, sf sweatfile.Sweatfile, _ string, _, _, _, _, _ bool) error {
		c.called, c.exec, c.sf = true, exec, sf
		return nil
	}
	t.Cleanup(func() { attachSessionFn = prev })
	return c
}

type entryFixture struct {
	home, repo, wt, sentinel string
}

// newEntryFixture builds a main checkout whose own sweatfile (R) configures a
// start/resume/env, plus a linked worktree whose sweatfile (W) is wBody with
// $SENTINEL substituted. HOME and XDG_STATE_HOME are sandboxed.
func newEntryFixture(t *testing.T, wBody string) entryFixture {
	t.Helper()
	testgit.RequireGit(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	repo := filepath.Join(home, "work", "repo")
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")
	sentinel := filepath.Join(home, "SENTINEL")

	writeFile := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(filepath.Join(repo, "sweatfile"), `[session-entry]
start = ["/bin/repo-start"]
resume = ["/bin/repo-resume"]
[session-entry.env]
MARK = "repo"
`)
	writeFile(filepath.Join(wt, "sweatfile"), strings.ReplaceAll(wBody, "$SENTINEL", sentinel))
	return entryFixture{home: home, repo: repo, wt: wt, sentinel: sentinel}
}

const hostileWorktreeSweatfile = `[session-entry]
start = ["sh", "-c", "touch $SENTINEL"]
resume = ["sh", "-c", "touch $SENTINEL"]
[session-entry.env]
MARK = "worktree"
PATH = "/worktree/evil"
[hooks]
pre-merge = "touch $SENTINEL"
repair = "touch $SENTINEL"
disable-merge = true
`

func (f entryFixture) writeState(t *testing.T, entry *session.PinnedEntry) {
	t.Helper()
	if err := session.Write(session.State{
		RepoPath: f.repo, WorktreePath: f.wt, Branch: "feat", SessionKey: "repo/feat",
		SessionState: session.StateInactive, StartedAt: time.Now().UTC(), Entry: entry,
	}); err != nil {
		t.Fatal(err)
	}
}

func resumeFeat(t *testing.T) error {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"id": "repo/feat", "yes": true})
	return runResume(context.Background(), args)
}

func capturedSessionExecutor(t *testing.T, c *attachCapture) executor.SessionExecutor {
	t.Helper()
	if !c.called {
		t.Fatal("shop.Attach was never reached")
	}
	se, ok := c.exec.(executor.SessionExecutor)
	if !ok {
		t.Fatalf("executor = %T, want executor.SessionExecutor", c.exec)
	}
	return se
}

// The sentinel tests: a worktree sweatfile that names a command never causes it
// to run, and `sc resume` replays the entry pinned at creation.
func TestResumeUsesPinnedEntryAndIgnoresWorktreeSweatfile(t *testing.T) {
	f := newEntryFixture(t, hostileWorktreeSweatfile)
	pinned := &session.PinnedEntry{
		Start:  []string{"/bin/pinned-start"},
		Resume: []string{"/bin/pinned-resume"},
		Env:    map[string]string{"MARK": "pinned"},
	}
	f.writeState(t, pinned)
	c := stubAttach(t)

	if err := resumeFeat(t); err != nil {
		t.Fatalf("resume: %v", err)
	}
	se := capturedSessionExecutor(t, c)
	if !reflect.DeepEqual(se.Entrypoint, pinned.Resume) {
		t.Errorf("entrypoint = %v, want the pinned resume %v", se.Entrypoint, pinned.Resume)
	}
	if !reflect.DeepEqual(se.Env, pinned.Env) {
		t.Errorf("env = %v, want the pinned env %v", se.Env, pinned.Env)
	}
	if _, err := os.Stat(f.sentinel); err == nil {
		t.Error("a command named in the worktree sweatfile ran")
	}
	if c.sf.DisableMergeEnabled() {
		t.Error("the worktree sweatfile's disable-merge reached the merged sweatfile")
	}
	if cmd := c.sf.PreMergeHookCommand(); cmd != nil {
		t.Errorf("worktree pre-merge hook reached the merged sweatfile: %q", *cmd)
	}
}

func TestResumeIgnoresCorruptWorktreeSweatfile(t *testing.T) {
	f := newEntryFixture(t, "[session-entry\n=== not toml\n")
	pinned := &session.PinnedEntry{Start: []string{"/bin/pinned-start"}, Resume: []string{"/bin/pinned-resume"}}
	f.writeState(t, pinned)
	c := stubAttach(t)

	if err := resumeFeat(t); err != nil {
		t.Fatalf("a corrupt worktree sweatfile failed resume: %v", err)
	}
	if se := capturedSessionExecutor(t, c); !reflect.DeepEqual(se.Entrypoint, pinned.Resume) {
		t.Errorf("entrypoint = %v, want %v", se.Entrypoint, pinned.Resume)
	}
}

// A session created before the pin falls back to the global, ancestor and
// main-checkout sweatfiles: never the worktree's.
func TestResumeLegacySessionUsesMainCheckoutLayers(t *testing.T) {
	f := newEntryFixture(t, hostileWorktreeSweatfile)
	f.writeState(t, nil)
	c := stubAttach(t)

	if err := resumeFeat(t); err != nil {
		t.Fatalf("resume: %v", err)
	}
	se := capturedSessionExecutor(t, c)
	if want := []string{"/bin/repo-resume"}; !reflect.DeepEqual(se.Entrypoint, want) {
		t.Errorf("entrypoint = %v, want %v", se.Entrypoint, want)
	}
	if se.Env["MARK"] != "repo" || se.Env["PATH"] != "" {
		t.Errorf("env = %v, want the main checkout's", se.Env)
	}
	if _, err := os.Stat(f.sentinel); err == nil {
		t.Error("a command named in the worktree sweatfile ran")
	}
}

func TestEntryForReportsLegacySession(t *testing.T) {
	merged := sweatfile.Sweatfile{SessionEntry: &sweatfile.SessionEntry{Start: []string{"/bin/s"}}}
	var buf bytes.Buffer
	got := entryFor(&session.State{}, "/r/repo", merged, &buf)
	if !reflect.DeepEqual(got.Start, []string{"/bin/s"}) {
		t.Errorf("Start = %v", got.Start)
	}
	want := "spinclass: no session entrypoint pinned at creation; using the global/ancestor sweatfiles and /r/repo/sweatfile\n"
	if buf.String() != want {
		t.Errorf("note = %q, want %q", buf.String(), want)
	}

	buf.Reset()
	pinned := &session.PinnedEntry{Start: []string{"/bin/p"}}
	if got := entryFor(&session.State{Entry: pinned}, "/r/repo", merged, &buf); !reflect.DeepEqual(got, *pinned) || buf.Len() != 0 {
		t.Errorf("pinned: got %+v, note %q", got, buf.String())
	}
}

// `sc start` of a worktree that does not exist yet uses the layers Attach is
// about to pin; of an existing one, the pin it already has.
func TestStartEntryPinnedOrFresh(t *testing.T) {
	f := newEntryFixture(t, hostileWorktreeSweatfile)
	merged := sweatfile.Sweatfile{SessionEntry: &sweatfile.SessionEntry{Start: []string{"/bin/merged-start"}}}
	var buf bytes.Buffer

	fresh := worktree.ResolvedPath{AbsPath: filepath.Join(f.repo, ".worktrees", "new"), RepoPath: f.repo, Branch: "new"}
	if got := startEntry(fresh, merged, &buf); !reflect.DeepEqual(got.Start, []string{"/bin/merged-start"}) || buf.Len() != 0 {
		t.Errorf("fresh: got %+v, note %q", got, buf.String())
	}

	pinned := &session.PinnedEntry{Start: []string{"/bin/pinned-start"}}
	f.writeState(t, pinned)
	existing := worktree.ResolvedPath{AbsPath: f.wt, RepoPath: f.repo, Branch: "feat"}
	if got := startEntry(existing, merged, &buf); !reflect.DeepEqual(got, *pinned) || buf.Len() != 0 {
		t.Errorf("existing pinned: got %+v, note %q", got, buf.String())
	}

	f.writeState(t, nil)
	if got := startEntry(existing, merged, &buf); !reflect.DeepEqual(got.Start, []string{"/bin/merged-start"}) || !strings.Contains(buf.String(), "no session entrypoint pinned") {
		t.Errorf("existing legacy: got %+v, note %q", got, buf.String())
	}
}
