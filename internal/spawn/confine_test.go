package spawn

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/confinement"
	"code.linenisgreat.com/spinclass/internal/worktree"
)

// cgroupSystem answers only /proc/self/cgroup; any other call panics on the nil
// embedded interface, which keeps the tests honest about what they read.
type cgroupSystem struct {
	confinement.System
	cgroup string
}

func (c cgroupSystem) ReadFile(path string) ([]byte, error) {
	if path != "/proc/self/cgroup" {
		return nil, os.ErrNotExist
	}
	return []byte("0::" + c.cgroup + "\n"), nil
}

func capabilityScript(t *testing.T, body string) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clown")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{path, "agent-scope-capabilities"}
}

const frontendCgroup = "/user.slice/user-1000.slice/user@1000.service/app.slice/clown.service"

func TestConfinedPreflightRefusesInsideAnAgentUnit(t *testing.T) {
	sys := cgroupSystem{cgroup: "/user.slice/user@1000.service/" + confinement.AgentSlice + "/spinclass-agent-x.service"}
	err := confinedPreflight(sys, capabilityScript(t, `echo '{"version":1,"capabilities":[]}'`))
	if err == nil || !strings.Contains(err.Error(), "inside an agent unit") || !strings.Contains(err.Error(), "spawn-session") {
		t.Fatalf("got %v, want an inside-a-unit refusal naming spawn-session", err)
	}
}

func TestConfinedPreflightRefusesMissingCapabilities(t *testing.T) {
	sys := cgroupSystem{cgroup: frontendCgroup}
	argv := capabilityScript(t, `echo '{"version":1,"capabilities":["agent-unit"]}'`)
	err := confinedPreflight(sys, argv)
	if err == nil {
		t.Fatal("want a refusal")
	}
	for _, c := range []string{"private-network", "frontend-mcp-sockets", "in-unit-moxy"} {
		if !strings.Contains(err.Error(), c) {
			t.Errorf("refusal %q does not name %s", err, c)
		}
	}
	if strings.Contains(err.Error(), "agent-unit") || !strings.Contains(err.Error(), "FDR 0035") {
		t.Errorf("refusal %q should omit reported capabilities and cite the FDR", err)
	}
}

func TestConfinedPreflightRefusesWhenClownCannotAnswer(t *testing.T) {
	sys := cgroupSystem{cgroup: frontendCgroup}
	for name, argv := range map[string][]string{
		"missing command": {filepath.Join(t.TempDir(), "no-such-clown")},
		"nonzero exit":    capabilityScript(t, `exit 2`),
		"bad json":        capabilityScript(t, `echo nope`),
	} {
		err := confinedPreflight(sys, argv)
		if err == nil || !strings.Contains(err.Error(), "in-unit-moxy") {
			t.Errorf("%s: got %v, want a refusal naming every required capability", name, err)
		}
	}
}

func TestConfinedPreflightPassesWithEveryCapability(t *testing.T) {
	sys := cgroupSystem{cgroup: frontendCgroup}
	argv := capabilityScript(t, `echo '{"version":1,"capabilities":["agent-unit","private-network","frontend-mcp-sockets","in-unit-moxy","extra"]}'`)
	if err := confinedPreflight(sys, argv); err != nil {
		t.Fatalf("got %v, want pass", err)
	}
}

func TestBuildUnitSpecShape(t *testing.T) {
	rp := worktree.ResolvedPath{AbsPath: "/w/repo/.worktrees/feat", SessionKey: "repo/feat.x"}
	spec := buildUnitSpec(unitSpecInput{
		rp: rp, operatorHome: "/home/op", stateDir: "/state", commonDir: "/w/repo/.git",
		sshAuthSock: "/run/user/1000/piggy.sock", runtimeDir: "/run/user/1000",
		wayland: []string{"/run/user/1000/wayland-0"},
		egress:  []string{"github.com:443"},
		env:     map[string]string{"SPINCLASS_SESSION_ID": "repo/feat.x"},
	})
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{
		"binds_ro", "binds_rw", "deny_paths", "egress_allow", "env", "operator_home",
		"result_path", "sockets", "unit_name_hint", "version", "worktree",
	}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	if spec.UnitNameHint != "spinclass-agent-repo-feat-x" {
		t.Errorf("unit_name_hint = %q", spec.UnitNameHint)
	}
	if want := confinement.UnitRecordPath("/state", rp.AbsPath); spec.ResultPath != want || strings.HasPrefix(spec.ResultPath, rp.AbsPath) {
		t.Errorf("result_path = %q, want %q outside the worktree", spec.ResultPath, want)
	}
	if want := []string{rp.AbsPath, "/w/repo/.git/objects", "/w/repo/.git/refs", "/w/repo/.git/logs", "/w/repo/.git/worktrees/feat"}; !reflect.DeepEqual(spec.BindsRW, want) {
		t.Errorf("binds_rw = %v", spec.BindsRW)
	}
	if want := []string{"/w/repo/.git/config", "/w/repo/.git/hooks"}; !reflect.DeepEqual(spec.BindsRO, want) {
		t.Errorf("binds_ro = %v", spec.BindsRO)
	}
	if want := []string{"/run/user/1000/bus", "/run/user/1000/systemd", "/run/user/1000/posh", "/tmp/.X11-unix", "/run/user/1000/wayland-0"}; !reflect.DeepEqual(spec.DenyPaths, want) {
		t.Errorf("deny_paths = %v", spec.DenyPaths)
	}
	if want := []unitSocket{{Path: "/run/user/1000/piggy.sock", Purpose: "piggy-agent"}}; !reflect.DeepEqual(spec.Sockets, want) {
		t.Errorf("sockets = %v", spec.Sockets)
	}
}

func TestBuildUnitSpecOmitsSocketsWithoutAgent(t *testing.T) {
	spec := buildUnitSpec(unitSpecInput{rp: worktree.ResolvedPath{AbsPath: "/w", SessionKey: "r/b"}, runtimeDir: "/run/user/1"})
	b, _ := json.Marshal(spec)
	if strings.Contains(string(b), `"sockets"`) {
		t.Errorf("sockets present without SSH_AUTH_SOCK: %s", b)
	}
	if strings.Contains(string(b), `"egress_allow"`) {
		t.Errorf("egress_allow must be omitted when no sweatfile sets one, so clown's default applies: %s", b)
	}
}

// Clown writes the unit record from a goroutine after the unit is active, so it
// can land after claude's hello: the bind retries instead of failing the spawn.
func TestBindPrincipalWaitsForALateUnitRecord(t *testing.T) {
	prevWait, prevPoll, prevRec := principalBindWait, principalBindPoll, recordPrincipal
	principalBindWait, principalBindPoll = 2*time.Second, time.Millisecond
	t.Cleanup(func() { principalBindWait, principalBindPoll, recordPrincipal = prevWait, prevPoll, prevRec })
	calls := 0
	recordPrincipal = func(confinement.System, string, string, string) (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("no agent unit record yet")
		}
		return "p", nil
	}
	if err := bindPrincipalWhenRecorded(t.TempDir(), "r/b", "/w", time.Now()); err != nil {
		t.Fatalf("got %v, want the bind to succeed once the record lands", err)
	}
	if calls != 3 {
		t.Errorf("recordPrincipal called %d times, want 3", calls)
	}
}

func TestBindPrincipalGivesUpAndReportsTheLastError(t *testing.T) {
	prevWait, prevPoll, prevRec := principalBindWait, principalBindPoll, recordPrincipal
	principalBindWait, principalBindPoll = 20*time.Millisecond, time.Millisecond
	t.Cleanup(func() { principalBindWait, principalBindPoll, recordPrincipal = prevWait, prevPoll, prevRec })
	recordPrincipal = func(confinement.System, string, string, string) (string, error) {
		return "", errors.New("record names no principal")
	}
	err := bindPrincipalWhenRecorded(t.TempDir(), "r/b", "/w", time.Now())
	if err == nil || !strings.Contains(err.Error(), "names no principal") {
		t.Fatalf("got %v, want the last error", err)
	}
}

// A record clown wrote to report a failed launch is final: the bind fails at
// once with clown's message instead of waiting out the deadline.
func TestBindPrincipalFailsAtOnceOnClownsLaunchError(t *testing.T) {
	prevWait, prevPoll, prevRec := principalBindWait, principalBindPoll, recordPrincipal
	principalBindWait, principalBindPoll = time.Minute, time.Millisecond
	t.Cleanup(func() { principalBindWait, principalBindPoll, recordPrincipal = prevWait, prevPoll, prevRec })
	calls := 0
	recordPrincipal = func(confinement.System, string, string, string) (string, error) {
		calls++
		return "", fmt.Errorf("%w: no slirp4netns", confinement.ErrUnitLaunchFailed)
	}
	start := time.Now()
	err := bindPrincipalWhenRecorded(t.TempDir(), "r/b", "/w", time.Now())
	if err == nil || !strings.Contains(err.Error(), "no slirp4netns") || calls != 1 || time.Since(start) > 5*time.Second {
		t.Fatalf("got err=%v after %d calls in %v, want an immediate failure with clown's message", err, calls, time.Since(start))
	}
}

func TestBindPrincipalIgnoresARecordFromAnEarlierLaunch(t *testing.T) {
	prevWait, prevPoll, prevRec := principalBindWait, principalBindPoll, recordPrincipal
	principalBindWait, principalBindPoll = 20*time.Millisecond, time.Millisecond
	t.Cleanup(func() { principalBindWait, principalBindPoll, recordPrincipal = prevWait, prevPoll, prevRec })
	called := false
	recordPrincipal = func(confinement.System, string, string, string) (string, error) {
		called = true
		return "stale-principal", nil
	}
	state, wt := t.TempDir(), "/w/repo/.worktrees/feat"
	rec := confinement.UnitRecordPath(state, wt)
	if err := os.MkdirAll(filepath.Dir(rec), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(rec, old, old); err != nil {
		t.Fatal(err)
	}
	if err := bindPrincipalWhenRecorded(state, "repo/feat", wt, time.Now()); err == nil || called {
		t.Fatalf("a record older than the launch must not be bound (err=%v, called=%v)", err, called)
	}
}

func TestBuildUnitSpecKeepsAnExplicitEmptyEgressList(t *testing.T) {
	spec := buildUnitSpec(unitSpecInput{rp: worktree.ResolvedPath{AbsPath: "/w", SessionKey: "r/b"}, runtimeDir: "/run/user/1", egress: []string{}})
	b, _ := json.Marshal(spec)
	if !strings.Contains(string(b), `"egress_allow":[]`) {
		t.Errorf("an explicit [] must be sent as an empty array (deny all), not dropped: %s", b)
	}
}

func TestWriteUnitSpecWritesOutsideTheWorktree(t *testing.T) {
	state := t.TempDir()
	wt := filepath.Join(t.TempDir(), "repo", ".worktrees", "feat")
	prev := gitCommonDir
	gitCommonDir = func(string) (string, error) { return "/c/.git", nil }
	t.Cleanup(func() { gitCommonDir = prev })
	t.Setenv("SSH_AUTH_SOCK", "")
	rp := worktree.ResolvedPath{AbsPath: wt, SessionKey: "repo/feat"}

	path, err := writeUnitSpec(rp, "desc", []string{"a:1"}, state)
	if err != nil {
		t.Fatal(err)
	}
	if path != confinement.UnitSpecPath(state, wt) || strings.HasPrefix(path, wt) {
		t.Errorf("spec path = %q", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var spec unitSpec
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Env["SPINCLASS_WORKTREE"] != wt || spec.Env["SPINCLASS_DESCRIPTION"] != "desc" || len(spec.Env) != 5 {
		t.Errorf("env = %v, want the five SPINCLASS_* identity vars", spec.Env)
	}
}

func TestWorkerEnvScopeOnlyWhenConfined(t *testing.T) {
	rp := worktree.ResolvedPath{AbsPath: "/w/feat", SessionKey: "repo/feat"}
	has := func(env []string, prefix string) bool {
		return slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, prefix) })
	}
	t.Setenv("CLOWN_AGENT_SCOPE", "")
	plain := workerEnv(rp, "d", nil, true)
	if has(plain, "CLOWN_AGENT_SCOPE=/") {
		t.Error("unconfined env carries a scope")
	}
	if !reflect.DeepEqual(plain, workerEnvScoped(rp, "d", nil, true, "")) {
		t.Error("an empty scope path must leave the env unchanged")
	}
	scoped := workerEnvScoped(rp, "d", nil, true, "/state/spec.json")
	if scoped[len(scoped)-1] != "CLOWN_AGENT_SCOPE=/state/spec.json" {
		t.Errorf("scope not last: %v", scoped[len(scoped)-1])
	}
	if !reflect.DeepEqual(scoped[:len(scoped)-1], plain) {
		t.Error("scoped env differs from plain beyond the scope variable")
	}
}
