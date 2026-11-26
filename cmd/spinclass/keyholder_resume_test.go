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

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
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
	// The switch is on exactly when the stub hands out an environment: the
	// boundary check reads it through its own seam, before any pair is started.
	prevWants := keyholderWantsPairFn
	keyholderWantsPairFn = func(_, _, _ string, _ bool) (bool, error) { return kh != nil, nil }
	t.Cleanup(func() { keyholderLaunchEnvFn = prev; keyholderWantsPairFn = prevWants })
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
	// A holder spec as the builder writes it also binds the pair's certificate
	// directory read-only and names the certificate file.
	bindsRO := []string{}
	for _, s := range sockets {
		if s["purpose"] == "key-holder" {
			dir := keyholder.CertDirOfAgentSock(s["path"])
			bindsRO = append(bindsRO, dir)
			withCert := map[string]string{keyholder.CertEnv: filepath.Join(dir, "cert.pub")}
			for k, v := range env {
				withCert[k] = v
			}
			env = withCert
		}
	}
	b, err := json.Marshal(map[string]any{
		"version": 1, "worktree": f.wt, "operator_home": f.home, "result_path": "/state/r.json",
		"binds_rw": []string{f.wt}, "binds_ro": bindsRO, "deny_paths": []string{},
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

// B-F2 / A-H1, option (a): the switch is ON now and the stored spec was written
// with it off, so it binds the operator's agent. clown would refuse the relaunch
// of a changed boundary and would otherwise rebind the operator's socket, so the
// resume is refused BEFORE a pair is started and before attach, naming the change
// and saying to close the session and spawn a new one. The spec is not rewritten.
func TestResumeWithTheKeyHolderOnRefusesAStoredSpecThatBindsTheOperatorsAgent(t *testing.T) {
	f := confinedFixture(t)
	spec := f.storedSpec(t, []map[string]string{{"path": resumeStoredOperatorAgent, "purpose": "piggy-agent"}}, map[string]string{"TMPDIR": "/t"})
	before, _ := os.ReadFile(spec)
	calls := stubKeyholderLaunch(t, map[string]string{"SSH_AUTH_SOCK": shortSocket(t)}, nil, nil)
	c := stubAttach(t)

	err := resumeFeat(t)
	if err == nil {
		t.Fatal("a stored spec binding the operator's agent was resumed with the switch on")
	}
	for _, want := range []string{"the operator's agent socket", "[keyholder] mode", "close this session and spawn a new one"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if c.called {
		t.Error("the refused resume reached attach")
	}
	if len(*calls) != 0 {
		t.Errorf("a pair was started for a relaunch that was refused: %+v", *calls)
	}
	if after, _ := os.ReadFile(spec); string(after) != string(before) {
		t.Error("the stored spec was rewritten")
	}
}

// B-F2 scenario B: the switch is OFF now and the stored spec names a holder
// socket the pair no longer serves: refused, never relaunched on a dead path.
func TestResumeWithTheKeyHolderOffRefusesAStoredSpecNamingAHolder(t *testing.T) {
	f := confinedFixture(t)
	t.Setenv("SSH_AUTH_SOCK", resumeStoredOperatorAgent)
	spec := f.storedSpec(t, []map[string]string{{"path": resumeStoredDeadHolder, "purpose": "key-holder"}}, map[string]string{"SSH_AUTH_SOCK": resumeStoredDeadHolder, "TMPDIR": "/t"})
	before, _ := os.ReadFile(spec)
	stubKeyholderLaunch(t, nil, nil, nil)
	c := stubAttach(t)

	err := resumeFeat(t)
	if err == nil {
		t.Fatal("a stored spec binding a holder was resumed with the switch off")
	}
	for _, want := range []string{"a key holder socket", "close this session and spawn a new one"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if c.called {
		t.Error("the refused resume reached attach")
	}
	if after, _ := os.ReadFile(spec); string(after) != string(before) {
		t.Error("the stored spec was rewritten")
	}
}

// Matching modes resume exactly as before: a holder spec with the switch on, an
// off spec with the switch off. The stored spec is handed to clown untouched.
func TestResumeWhenTheStoredSpecMatchesTheSwitchRelaunchesAsBefore(t *testing.T) {
	t.Run("holder spec, switch on", func(t *testing.T) {
		f := confinedFixture(t)
		holder := shortSocket(t)
		spec := f.storedSpec(t, []map[string]string{{"path": holder, "purpose": "key-holder"}}, map[string]string{"SSH_AUTH_SOCK": holder})
		before, _ := os.ReadFile(spec)
		stubKeyholderLaunch(t, map[string]string{"SSH_AUTH_SOCK": holder}, nil, nil)
		c := stubAttach(t)
		if err := resumeFeat(t); err != nil {
			t.Fatalf("resume: %v", err)
		}
		if !c.called {
			t.Error("the resume never reached attach")
		}
		if after, _ := os.ReadFile(spec); string(after) != string(before) {
			t.Error("the stored spec was changed")
		}
	})
	t.Run("off spec, switch off", func(t *testing.T) {
		f := confinedFixture(t)
		spec := f.storedSpec(t, []map[string]string{{"path": resumeStoredOperatorAgent, "purpose": "piggy-agent"}}, map[string]string{})
		before, _ := os.ReadFile(spec)
		stubKeyholderLaunch(t, nil, nil, nil)
		c := stubAttach(t)
		if err := resumeFeat(t); err != nil {
			t.Fatalf("resume: %v", err)
		}
		if !c.called {
			t.Error("the resume never reached attach")
		}
		if after, _ := os.ReadFile(spec); string(after) != string(before) {
			t.Error("the stored spec was changed")
		}
	})
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
