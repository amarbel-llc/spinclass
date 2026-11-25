package main

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
)

// launchCall is one call the key holder seam saw.
type launchCall struct {
	spawned bool
	session string
}

// stubKeyholderLaunch replaces the key holder entry point `sc start` and `sc
// resume` call, returning kh (nil = switch off or a driver) and recording what
// each call site asked.
func stubKeyholderLaunch(t *testing.T, kh map[string]string, undo func(), err error) *[]launchCall {
	t.Helper()
	var calls []launchCall
	prev := keyholderLaunchEnvFn
	keyholderLaunchEnvFn = func(_, _, sessionKey string, spawned bool, _ io.Writer) (map[string]string, func(), error) {
		calls = append(calls, launchCall{spawned: spawned, session: sessionKey})
		return kh, undo, err
	}
	t.Cleanup(func() { keyholderLaunchEnvFn = prev })
	return &calls
}

// shortSocket binds a real unix socket under a short directory.
func shortSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "khr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	p := filepath.Join(dir, "agent.sock")
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return p
}

const (
	resumeStoredOperatorAgent = "/run/user/1000/piggy-agent.sock"
	resumeStoredDeadHolder    = "/state/keyholder/gone/agent/agent.sock"
)

// storedSpec writes the launch spec a confined spawn left behind, with the given
// sockets and env, over the fixture's placeholder spec.
func (f entryFixture) storedSpec(t *testing.T, sockets []map[string]string, env map[string]string) string {
	t.Helper()
	spec := f.markConfined(t, true)
	b, err := json.Marshal(map[string]any{
		"version": 1, "worktree": f.wt, "operator_home": f.home, "result_path": "/state/r.json",
		"binds_rw": []string{f.wt}, "binds_ro": []string{}, "deny_paths": []string{},
		"sockets": sockets, "env": env,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return spec
}

type storedSpecView struct {
	Sockets []struct{ Path, Purpose string } `json:"sockets"`
	Env     map[string]string                `json:"env"`
}

func readSpecView(t *testing.T, path string) (storedSpecView, string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v storedSpecView
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v, string(b)
}

func confinedFixture(t *testing.T) entryFixture {
	t.Helper()
	f := newEntryFixture(t, "")
	f.writeState(t, &session.PinnedEntry{Start: []string{"/bin/s"}, Resume: harnessResume})
	return f
}

// B-F2 / A-H1: the switch is ON now, the stored spec was written with it off and
// binds the operator's agent: the resume must replace it with the holder's.
func TestResumeWithTheKeyHolderOnRewritesAStoredSpecThatBindsTheOperatorsAgent(t *testing.T) {
	f := confinedFixture(t)
	spec := f.storedSpec(t, []map[string]string{{"path": resumeStoredOperatorAgent, "purpose": "piggy-agent"}}, map[string]string{"TMPDIR": "/t"})
	holder := shortSocket(t)
	stubKeyholderLaunch(t, map[string]string{"SSH_AUTH_SOCK": holder}, nil, nil)
	c := stubAttach(t)

	if err := resumeFeat(t); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !c.called {
		t.Fatal("the resume never reached attach")
	}
	v, raw := readSpecView(t, spec)
	if len(v.Sockets) != 1 || v.Sockets[0].Path != holder || v.Sockets[0].Purpose != "key-holder" {
		t.Errorf("sockets = %+v, want only the holder's", v.Sockets)
	}
	if v.Env["SSH_AUTH_SOCK"] != holder || v.Env["TMPDIR"] != "/t" {
		t.Errorf("env = %v, want SSH_AUTH_SOCK=holder and the rest kept", v.Env)
	}
	if strings.Contains(raw, resumeStoredOperatorAgent) || strings.Contains(raw, "piggy-agent") {
		t.Errorf("the operator's agent is still in the stored spec:\n%s", raw)
	}
}

// B-F2 scenario B: the switch is OFF now and the stored spec names a holder
// socket that is gone: the resume must restore the switch-off shape.
func TestResumeWithTheKeyHolderOffRestoresAStoredSpecNamingADeadHolder(t *testing.T) {
	f := confinedFixture(t)
	t.Setenv("SSH_AUTH_SOCK", resumeStoredOperatorAgent)
	spec := f.storedSpec(t, []map[string]string{{"path": resumeStoredDeadHolder, "purpose": "key-holder"}}, map[string]string{"SSH_AUTH_SOCK": resumeStoredDeadHolder, "TMPDIR": "/t"})
	stubKeyholderLaunch(t, nil, nil, nil)
	c := stubAttach(t)

	if err := resumeFeat(t); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !c.called {
		t.Fatal("the resume never reached attach")
	}
	v, raw := readSpecView(t, spec)
	if len(v.Sockets) != 1 || v.Sockets[0].Path != resumeStoredOperatorAgent || v.Sockets[0].Purpose != "piggy-agent" {
		t.Errorf("sockets = %+v, want the switch-off shape (the operator's agent)", v.Sockets)
	}
	if _, has := v.Env["SSH_AUTH_SOCK"]; has {
		t.Errorf("env still names SSH_AUTH_SOCK: %v", v.Env)
	}
	if strings.Contains(raw, resumeStoredDeadHolder) {
		t.Errorf("the dead holder path is still in the stored spec:\n%s", raw)
	}
}

// Fail closed: the switch is on but the holder's socket is unusable. The resume
// is refused, nothing is attached, and a pair this resume just started is stopped.
func TestResumeWithAnUnusableHolderSocketRefusesAndStopsTheNewPair(t *testing.T) {
	f := confinedFixture(t)
	spec := f.storedSpec(t, []map[string]string{{"path": resumeStoredOperatorAgent, "purpose": "piggy-agent"}}, map[string]string{})
	before, _ := os.ReadFile(spec)
	undone := false
	stubKeyholderLaunch(t, map[string]string{"SSH_AUTH_SOCK": filepath.Join(t.TempDir(), "absent.sock")}, func() { undone = true }, nil)
	c := stubAttach(t)

	err := resumeFeat(t)
	if err == nil || !strings.Contains(err.Error(), "key holder") {
		t.Fatalf("resume = %v, want a refusal naming the key holder", err)
	}
	if c.called {
		t.Error("a session with an unusable holder was attached")
	}
	if !undone {
		t.Error("the pair this resume started was left running")
	}
	if after, _ := os.ReadFile(spec); string(after) != string(before) {
		t.Error("the stored spec was rewritten on a refusal")
	}
}

// Call sites (clarabell's ask): `sc start` reports spawned=false and `sc resume`
// reports whether the record names a spawner, so a worker the operator resumes at
// a terminal is still a worker.
func TestStartAndResumeReportTheirLaunchKindToTheKeyHolder(t *testing.T) {
	f := newEntryFixture(t, "")
	f.writeState(t, &session.PinnedEntry{Start: []string{"/bin/s"}, Resume: []string{"/bin/r"}})
	stubAttach(t)

	calls := stubKeyholderLaunch(t, nil, nil, nil)
	rp := worktree.ResolvedPath{AbsPath: f.wt, RepoPath: f.repo, SessionKey: "repo/feat", Branch: "feat"}
	if err := attachSession(rp, startArgs{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0].spawned || (*calls)[0].session != "repo/feat" {
		t.Errorf("sc start asked %+v, want one call with spawned=false", *calls)
	}

	for _, tc := range []struct {
		name      string
		spawnedBy string
		want      bool
	}{{"a session nobody spawned", "", false}, {"a spawned worker", "repo/driver", true}} {
		t.Run(tc.name, func(t *testing.T) {
			if err := session.Write(session.State{
				RepoPath: f.repo, WorktreePath: f.wt, Branch: "feat", SessionKey: "repo/feat",
				SessionState: session.StateInactive, StartedAt: time.Now().UTC(), SpawnedBy: tc.spawnedBy,
				Entry: &session.PinnedEntry{Start: []string{"/bin/s"}, Resume: []string{"/bin/r"}},
			}); err != nil {
				t.Fatal(err)
			}
			calls := stubKeyholderLaunch(t, nil, nil, nil)
			if err := resumeFeat(t); err != nil {
				t.Fatalf("resume: %v", err)
			}
			if len(*calls) != 1 || (*calls)[0].spawned != tc.want {
				t.Errorf("sc resume of %s asked %+v, want spawned=%v", tc.name, *calls, tc.want)
			}
		})
	}
}
