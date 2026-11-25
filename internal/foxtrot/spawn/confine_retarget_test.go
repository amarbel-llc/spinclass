package spawn

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// retargetFixture is a confined worktree whose spec the real builder writes.
func retargetFixture(t *testing.T) (state string, rp worktree.ResolvedPath) {
	t.Helper()
	state = t.TempDir()
	wt := filepath.Join(t.TempDir(), "repo", ".worktrees", "feat")
	common := filepath.Join(t.TempDir(), ".git")
	if err := os.MkdirAll(filepath.Join(common, "worktrees", "feat"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := gitCommonDir
	gitCommonDir = func(string) (string, error) { return common, nil }
	t.Cleanup(func() { gitCommonDir = prev })
	return state, worktree.ResolvedPath{AbsPath: wt, SessionKey: "repo/feat"}
}

func readStoredSpec(t *testing.T, state, wt string) (unitSpec, []byte) {
	t.Helper()
	b, err := os.ReadFile(confinement.UnitSpecPath(state, wt))
	if err != nil {
		t.Fatal(err)
	}
	var s unitSpec
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s, b
}

// B-F2 / A-H1, switch goes ON: a spec written with the key holder off binds the
// operator's agent; a resume with the holder on must replace it, and nothing of
// the operator's agent may survive in the stored spec.
func TestRetargetOfASpecWrittenWithTheSwitchOffBindsTheHolderAndDropsTheOperatorsAgent(t *testing.T) {
	state, rp := retargetFixture(t)
	t.Setenv("SSH_AUTH_SOCK", operatorAgent)
	t.Setenv("PIGGY_AUTH_SOCK", operatorPiggy)
	if _, err := writeUnitSpec(rp, "desc", nil, state, "", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := readStoredSpec(t, state, rp.AbsPath)
	if len(before.Sockets) != 1 || before.Sockets[0].Path != operatorAgent {
		t.Fatalf("fixture: the off spec binds %+v, want the operator's agent", before.Sockets)
	}

	holder := holderSocket(t)
	changed, err := RetargetAgentSocket(state, rp.AbsPath, map[string]string{"SSH_AUTH_SOCK": holder}, operatorAgent)
	if err != nil || !changed {
		t.Fatalf("RetargetAgentSocket = %v, %v; want a rewrite", changed, err)
	}
	got, raw := readStoredSpec(t, state, rp.AbsPath)
	if len(got.Sockets) != 1 || got.Sockets[0].Path != holder || got.Sockets[0].Purpose != "key-holder" {
		t.Errorf("sockets = %+v, want only the holder's", got.Sockets)
	}
	if got.Env["SSH_AUTH_SOCK"] != holder {
		t.Errorf("env SSH_AUTH_SOCK = %q, want the holder's", got.Env["SSH_AUTH_SOCK"])
	}
	for _, v := range []string{operatorAgent, operatorPiggy, "piggy-agent"} {
		if strings.Contains(string(raw), v) {
			t.Errorf("the retargeted spec still mentions %q:\n%s", v, raw)
		}
	}
	// Everything but the agent socket and its variable is what the builder wrote.
	got.Sockets, before.Sockets = nil, nil
	delete(got.Env, "SSH_AUTH_SOCK")
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(before)
	if !bytes.Equal(a, b) {
		t.Errorf("the retarget changed more than the agent socket:\n got %s\nwant %s", a, b)
	}
}

// B-F2 scenario B, switch goes OFF: a spec written with the holder on names a
// socket that is gone; a resume with the holder off must restore exactly what the
// builder writes for the switch off, never leave the dead holder path.
func TestRetargetOfASpecWrittenWithTheSwitchOnRestoresTheSwitchOffSpecByteForByte(t *testing.T) {
	state, rp := retargetFixture(t)
	t.Setenv("SSH_AUTH_SOCK", operatorAgent)
	if _, err := writeUnitSpec(rp, "desc", nil, state, "", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	_, offBytes := readStoredSpec(t, state, rp.AbsPath)

	holder := holderSocket(t)
	if _, err := writeUnitSpec(rp, "desc", nil, state, "", false, nil, map[string]string{"SSH_AUTH_SOCK": holder}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(holder); err != nil { // the pair is gone: a reboot
		t.Fatal(err)
	}

	changed, err := RetargetAgentSocket(state, rp.AbsPath, nil, operatorAgent)
	if err != nil || !changed {
		t.Fatalf("RetargetAgentSocket = %v, %v; want a rewrite", changed, err)
	}
	_, got := readStoredSpec(t, state, rp.AbsPath)
	if !bytes.Equal(got, offBytes) {
		t.Errorf("the restored spec differs from the one the builder writes with the switch off:\n got %s\nwant %s", got, offBytes)
	}
	if strings.Contains(string(got), holder) {
		t.Errorf("the dead holder path %s is still in the spec", holder)
	}
}

// With the switch off and no holder in the stored spec, a resume changes nothing.
func TestRetargetWithTheSwitchOffLeavesASwitchOffSpecAlone(t *testing.T) {
	state, rp := retargetFixture(t)
	t.Setenv("SSH_AUTH_SOCK", operatorAgent)
	if _, err := writeUnitSpec(rp, "desc", nil, state, "", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	_, before := readStoredSpec(t, state, rp.AbsPath)
	changed, err := RetargetAgentSocket(state, rp.AbsPath, nil, operatorAgent)
	if err != nil || changed {
		t.Fatalf("RetargetAgentSocket = %v, %v; want no rewrite", changed, err)
	}
	if _, after := readStoredSpec(t, state, rp.AbsPath); !bytes.Equal(before, after) {
		t.Errorf("the spec changed:\n%s\n%s", before, after)
	}
}

// Fail closed: the holder is on but its socket is unusable: the resume is refused
// and the stored spec (and so the operator's agent bind) is left exactly as it
// was for the refusal to be visible, never silently "fixed" to something else.
func TestRetargetWithAnUnusableHolderSocketRefusesAndWritesNothing(t *testing.T) {
	state, rp := retargetFixture(t)
	t.Setenv("SSH_AUTH_SOCK", operatorAgent)
	if _, err := writeUnitSpec(rp, "desc", nil, state, "", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	_, before := readStoredSpec(t, state, rp.AbsPath)
	missing := filepath.Join(t.TempDir(), "agent.sock")
	if _, err := RetargetAgentSocket(state, rp.AbsPath, map[string]string{"SSH_AUTH_SOCK": missing}, operatorAgent); err == nil {
		t.Fatal("an unusable holder socket was accepted")
	} else if !strings.Contains(err.Error(), "key holder") {
		t.Errorf("the refusal does not name the key holder: %v", err)
	}
	if _, after := readStoredSpec(t, state, rp.AbsPath); !bytes.Equal(before, after) {
		t.Errorf("the spec was rewritten on a refusal:\n%s", after)
	}
}
