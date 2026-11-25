package spawn

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

const (
	operatorAgent = "/run/user/1000/piggy-agent.sock"
	operatorPiggy = "/run/user/1000/piggy-forwarded.sock"
)

// holderSocket binds a real unix socket in a short directory (unix socket paths
// are capped near 108 bytes) laid out as a pair's is (<session dir>/agent/
// agent.sock, with the certificate directory <session dir>/cert beside it) and
// returns its path.
func holderSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "kh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.MkdirAll(filepath.Join(dir, "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "cert"), 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "agent", "agent.sock")
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return p
}

// holderEnv is the environment a started pair hands the spawn: the agent socket
// and the certificate path.
func holderEnv(holder string) map[string]string {
	return map[string]string{
		"SSH_AUTH_SOCK":   holder,
		keyholder.CertEnv: filepath.Join(keyholder.CertDirOfAgentSock(holder), "cert.pub"),
	}
}

func TestBuildUnitSpecWithAHolderBindsOnlyTheHoldersSocketAndSetsTheEnv(t *testing.T) {
	in := offSpecInput()
	in.holderSock = "/state/keyholder/h/agent/agent.sock"
	env := in.env
	spec := buildUnitSpec(in)

	if len(spec.Sockets) != 1 || spec.Sockets[0].Path != in.holderSock || spec.Sockets[0].Purpose != "key-holder" {
		t.Fatalf("sockets = %+v, want exactly the holder's, purpose key-holder", spec.Sockets)
	}
	if got := spec.Env["SSH_AUTH_SOCK"]; got != in.holderSock {
		t.Errorf("env SSH_AUTH_SOCK = %q, want the holder's socket", got)
	}
	if _, polluted := env["SSH_AUTH_SOCK"]; polluted {
		t.Error("buildUnitSpec wrote into the caller's env map")
	}
	for _, s := range spec.Sockets {
		if s.Path == in.sshAuthSock {
			t.Errorf("the operator's socket %s is bound", s.Path)
		}
	}
}

// whole-builder proof: the real entry point (writeUnitSpec), with the operator's
// agent in the environment and a prior sticky spec that bound it, binds nothing
// of the operator's when the key holder is on.
func TestWriteUnitSpecWithTheKeyHolderOnNeverBindsTheOperatorsAgent(t *testing.T) {
	state := t.TempDir()
	wt := filepath.Join(t.TempDir(), "repo", ".worktrees", "feat")
	common := filepath.Join(t.TempDir(), ".git")
	if err := os.MkdirAll(filepath.Join(common, "worktrees", "feat"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := gitCommonDir
	gitCommonDir = func(string) (string, error) { return common, nil }
	t.Cleanup(func() { gitCommonDir = prev })
	rp := worktree.ResolvedPath{AbsPath: wt, SessionKey: "repo/feat"}

	// An earlier (key holder off) spec bound the operator's agent: sticky.
	t.Setenv("SSH_AUTH_SOCK", operatorAgent)
	t.Setenv("PIGGY_AUTH_SOCK", operatorPiggy)
	if _, err := writeUnitSpec(rp, "desc", nil, state, "", false, nil, nil, BridgeUnset); err != nil {
		t.Fatal(err)
	}

	for _, ssh := range []string{operatorAgent, ""} { // set, and gone (the sticky path)
		t.Setenv("SSH_AUTH_SOCK", ssh)
		holder := holderSocket(t)
		path, err := writeUnitSpec(rp, "desc", nil, state, "", false, nil, holderEnv(holder), BridgeUnset)
		if err != nil {
			t.Fatalf("SSH_AUTH_SOCK=%q: %v", ssh, err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{operatorAgent, operatorPiggy, "piggy-agent"} {
			if strings.Contains(string(b), v) {
				t.Errorf("SSH_AUTH_SOCK=%q: the spec mentions %q:\n%s", ssh, v, b)
			}
		}
		var spec unitSpec
		if err := json.Unmarshal(b, &spec); err != nil {
			t.Fatal(err)
		}
		if len(spec.Sockets) != 1 || spec.Sockets[0].Path != holder || spec.Sockets[0].Purpose != "key-holder" {
			t.Errorf("SSH_AUTH_SOCK=%q: sockets = %+v, want only the holder's", ssh, spec.Sockets)
		}
		if spec.Env["SSH_AUTH_SOCK"] != holder {
			t.Errorf("SSH_AUTH_SOCK=%q: env SSH_AUTH_SOCK = %q, want %q", ssh, spec.Env["SSH_AUTH_SOCK"], holder)
		}
	}
}

// fail closed: the key holder is on but there is no live holder socket: the
// launch is refused, no spec is written, and the operator's agent is never used.
func TestWriteUnitSpecWithTheKeyHolderOnButNoLiveSocketRefusesTheLaunch(t *testing.T) {
	state := t.TempDir()
	wt := filepath.Join(t.TempDir(), "repo", ".worktrees", "feat")
	common := filepath.Join(t.TempDir(), ".git")
	if err := os.MkdirAll(filepath.Join(common, "worktrees", "feat"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := gitCommonDir
	gitCommonDir = func(string) (string, error) { return common, nil }
	t.Cleanup(func() { gitCommonDir = prev })
	t.Setenv("SSH_AUTH_SOCK", operatorAgent)
	rp := worktree.ResolvedPath{AbsPath: wt, SessionKey: "repo/feat"}

	regular := filepath.Join(t.TempDir(), "not-a-socket")
	if err := os.WriteFile(regular, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, kh := range map[string]map[string]string{
		"no socket named":       {},
		"empty socket":          {"SSH_AUTH_SOCK": ""},
		"socket does not exist": {"SSH_AUTH_SOCK": filepath.Join(t.TempDir(), "gone.sock")},
		"not a socket":          {"SSH_AUTH_SOCK": regular},
		"relative":              {"SSH_AUTH_SOCK": "agent.sock"},
	} {
		path, err := writeUnitSpec(rp, "desc", nil, state, "", false, nil, kh, BridgeUnset)
		if err == nil || !strings.Contains(err.Error(), "key holder") {
			t.Errorf("%s: err = %v, want a refusal naming the key holder", name, err)
		}
		if path != "" {
			t.Errorf("%s: a spec was written at %s", name, path)
		}
	}
	if _, err := os.Stat(confinement.UnitSpecPath(state, wt)); err == nil {
		t.Error("a refused launch left a spec on disk")
	}
}

// off is unchanged: a nil key holder environment keeps today's operator bind.
func TestWriteUnitSpecWithTheKeyHolderOffKeepsTheOperatorsSocket(t *testing.T) {
	state := t.TempDir()
	wt := filepath.Join(t.TempDir(), "repo", ".worktrees", "feat")
	common := filepath.Join(t.TempDir(), ".git")
	if err := os.MkdirAll(filepath.Join(common, "worktrees", "feat"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := gitCommonDir
	gitCommonDir = func(string) (string, error) { return common, nil }
	t.Cleanup(func() { gitCommonDir = prev })
	t.Setenv("SSH_AUTH_SOCK", operatorAgent)
	rp := worktree.ResolvedPath{AbsPath: wt, SessionKey: "repo/feat"}
	path, err := writeUnitSpec(rp, "desc", nil, state, "", false, nil, nil, BridgeUnset)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var spec unitSpec
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	if len(spec.Sockets) != 1 || spec.Sockets[0].Path != operatorAgent || spec.Sockets[0].Purpose != "piggy-agent" {
		t.Errorf("off: sockets = %+v, want today's operator bind", spec.Sockets)
	}
	if _, set := spec.Env["SSH_AUTH_SOCK"]; set {
		t.Error("off: the spec sets SSH_AUTH_SOCK")
	}
}
