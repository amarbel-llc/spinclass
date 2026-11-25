package spawn

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// agentCheckFixture is a confined worktree whose spec the real builder writes.
func agentCheckFixture(t *testing.T) (state string, rp worktree.ResolvedPath) {
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

func writeOffSpec(t *testing.T, state string, rp worktree.ResolvedPath) {
	t.Helper()
	t.Setenv("SSH_AUTH_SOCK", operatorAgent)
	if _, err := writeUnitSpec(rp, "desc", nil, state, "", false, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func writeHolderSpec(t *testing.T, state string, rp worktree.ResolvedPath) string {
	t.Helper()
	holder := holderSocket(t)
	if _, err := writeUnitSpec(rp, "desc", nil, state, "", false, nil, map[string]string{"SSH_AUTH_SOCK": holder}); err != nil {
		t.Fatal(err)
	}
	return holder
}

func asBoundaryError(t *testing.T, err error) *AgentBoundaryError {
	t.Helper()
	var be *AgentBoundaryError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v, want an *AgentBoundaryError", err)
	}
	return be
}

// B-F2 / A-H1, the switch goes ON after a confined session was spawned with it
// off: the stored spec still binds the operator's agent. clown would relaunch it
// with that bind, so spinclass refuses first, names the change and says to close
// the session and spawn a new one. Nothing is rewritten.
func TestAStoredSpecBindingTheOperatorsAgentIsRefusedWhenTheSwitchIsOn(t *testing.T) {
	state, rp := agentCheckFixture(t)
	writeOffSpec(t, state, rp)
	before, _ := os.ReadFile(confinement.UnitSpecPath(state, rp.AbsPath))

	err := CheckAgentBoundary(state, rp.AbsPath, true)
	if err == nil {
		t.Fatal("a spec that binds the operator's agent was accepted with the switch on")
	}
	be := asBoundaryError(t, err)
	if be.Stored != "the operator's agent socket" || !be.SwitchOn {
		t.Errorf("error = %+v, want the operator's agent with the switch on", be)
	}
	for _, want := range []string{"[keyholder] mode", `"on"`, "close this session and spawn a new one"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	after, _ := os.ReadFile(confinement.UnitSpecPath(state, rp.AbsPath))
	if string(before) != string(after) {
		t.Errorf("the check rewrote the stored spec:\n%s\n%s", before, after)
	}
}

// A spec written with no agent at all (the operator had none when it was spawned)
// has no holder either: with the switch on the session would come back without
// its own key, a changed boundary too.
func TestAStoredSpecWithNoAgentSocketIsRefusedWhenTheSwitchIsOn(t *testing.T) {
	state, rp := agentCheckFixture(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	if _, err := writeUnitSpec(rp, "desc", nil, state, "", false, nil, nil); err != nil {
		t.Fatal(err)
	}
	be := asBoundaryError(t, CheckAgentBoundary(state, rp.AbsPath, true))
	if be.Stored != "no agent socket" {
		t.Errorf("Stored = %q, want no agent socket", be.Stored)
	}
}

// B-F2 scenario B: a session spawned with the holder on, the switch now off. The
// holder path in its spec is a dead socket once the pair is gone; the relaunch is
// refused, never started on it.
func TestAStoredSpecBindingAHolderIsRefusedWhenTheSwitchIsOff(t *testing.T) {
	state, rp := agentCheckFixture(t)
	holder := writeHolderSpec(t, state, rp)
	if err := os.Remove(holder); err != nil { // the pair is gone
		t.Fatal(err)
	}
	err := CheckAgentBoundary(state, rp.AbsPath, false)
	if err == nil {
		t.Fatal("a spec that binds a holder socket was accepted with the switch off")
	}
	be := asBoundaryError(t, err)
	if be.Stored != "a key holder socket" || be.SwitchOn {
		t.Errorf("error = %+v, want a holder socket with the switch off", be)
	}
	if !strings.Contains(err.Error(), "close this session and spawn a new one") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
}

// Matching modes relaunch as before: a spec the builder wrote for the mode the
// switch says now is never refused, in either direction.
func TestAStoredSpecThatMatchesTheSwitchIsAccepted(t *testing.T) {
	t.Run("off spec, switch off", func(t *testing.T) {
		state, rp := agentCheckFixture(t)
		writeOffSpec(t, state, rp)
		if err := CheckAgentBoundary(state, rp.AbsPath, false); err != nil {
			t.Errorf("an off spec was refused with the switch off: %v", err)
		}
	})
	t.Run("holder spec, switch on", func(t *testing.T) {
		state, rp := agentCheckFixture(t)
		writeHolderSpec(t, state, rp)
		if err := CheckAgentBoundary(state, rp.AbsPath, true); err != nil {
			t.Errorf("a holder spec was refused with the switch on: %v", err)
		}
	})
}

// A holder spec whose SSH_AUTH_SOCK names some other agent is not the shape the
// builder writes; with the switch on it is refused rather than relaunched.
func TestAHolderSpecWhoseEnvNamesAnotherAgentIsRefusedWhenTheSwitchIsOn(t *testing.T) {
	state, rp := agentCheckFixture(t)
	writeHolderSpec(t, state, rp)
	spec, _ := readSpecForTest(t, state, rp.AbsPath)
	spec.Env["SSH_AUTH_SOCK"] = operatorAgent
	if _, err := confinement.WriteUnitSpec(state, rp.AbsPath, spec); err != nil {
		t.Fatal(err)
	}
	be := asBoundaryError(t, CheckAgentBoundary(state, rp.AbsPath, true))
	if !strings.Contains(be.Detail, "SSH_AUTH_SOCK") {
		t.Errorf("Detail = %q, want the variable named", be.Detail)
	}
}

// With the switch on an unreadable or unparseable spec is refused (fail closed);
// with it off nothing was ever rewritten here, so clown's own parse decides.
func TestAnUnreadableSpecIsRefusedWithTheSwitchOnAndLeftToClownWithItOff(t *testing.T) {
	state, rp := agentCheckFixture(t)
	if err := CheckAgentBoundary(state, rp.AbsPath, true); err == nil {
		t.Error("a missing spec was accepted with the switch on")
	}
	if err := CheckAgentBoundary(state, rp.AbsPath, false); err != nil {
		t.Errorf("a missing spec was refused with the switch off: %v", err)
	}
}
