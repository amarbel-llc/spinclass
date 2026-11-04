package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// spinclass#462, the resume half: a session launched confined gowns at the
// door on resume too. The pinned resume entry's exact leading `direnv exec .`
// is dropped, and the rendered [direnv.dotenv] pairs ride in the launch
// environment, so the unit starts with no devShell and still has what a
// trusted layer declared.

var direnvResume = []string{"direnv", "exec", ".", "clown", "--", "--resume", "0b8fd1b2-8d38-4c6e-9f0c-3f2f5b1c9a11"}

// declareDotenv appends a [direnv.dotenv] pair to the fixture repo's sweatfile.
func (f entryFixture) declareDotenv(t *testing.T) {
	t.Helper()
	path := filepath.Join(f.repo, "sweatfile")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, []byte("[direnv.dotenv]\nPILOT_IPC = \"$WORKTREE/.tmp/ipc.sock\"\n")...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResumeOfOnceConfinedSessionDropsDirenvAndCarriesTheDotenv(t *testing.T) {
	f := newEntryFixture(t, "")
	f.declareDotenv(t)
	f.writeState(t, &session.PinnedEntry{
		Start:  []string{"/bin/pinned-start"},
		Resume: direnvResume,
		Env:    map[string]string{"MARK": "pinned"},
	})
	spec := f.markConfined(t, true)
	c := stubAttach(t)

	if err := resumeFeat(t); err != nil {
		t.Fatalf("resume: %v", err)
	}
	se := capturedSessionExecutor(t, c)
	if !reflect.DeepEqual(se.Entrypoint, harnessResume) {
		t.Errorf("entrypoint = %v, want the direnv prefix dropped: %v", se.Entrypoint, harnessResume)
	}
	if got, want := se.Env["PILOT_IPC"], filepath.Join(f.wt, ".tmp", "ipc.sock"); got != want {
		t.Errorf("PILOT_IPC = %q, want the rendered dotenv pair %q", got, want)
	}
	if se.Env["MARK"] != "pinned" {
		t.Errorf("the pinned env was lost: %v", se.Env)
	}
	if got := se.Env["CLOWN_AGENT_SCOPE"]; got != spec {
		t.Errorf("CLOWN_AGENT_SCOPE = %q, want the recorded spec %q", got, spec)
	}
}

// A session never launched confined resumes exactly as pinned: direnv stays,
// and the dotenv reaches it through direnv as before, not through the launch
// environment.
func TestResumeOfNeverConfinedSessionKeepsDirenvAndAddsNoDotenv(t *testing.T) {
	f := newEntryFixture(t, "")
	f.declareDotenv(t)
	f.writeState(t, &session.PinnedEntry{Start: []string{"/bin/pinned-start"}, Resume: direnvResume})
	c := stubAttach(t)

	if err := resumeFeat(t); err != nil {
		t.Fatalf("resume: %v", err)
	}
	se := capturedSessionExecutor(t, c)
	if !reflect.DeepEqual(se.Entrypoint, direnvResume) {
		t.Errorf("entrypoint = %v, want the pinned entry untouched %v", se.Entrypoint, direnvResume)
	}
	if _, ok := se.Env["PILOT_IPC"]; ok {
		t.Errorf("an unconfined resume carried the dotenv in its launch env: %v", se.Env)
	}
}
