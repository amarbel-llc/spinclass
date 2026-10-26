package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

func stubConfinementBind(t *testing.T, st *session.State, findErr, recErr error) (*bytes.Buffer, *[]string) {
	t.Helper()
	prevFind, prevRec, prevDir, prevOut := confinementBindFind, confinementBindRecord, confinementBindStateDir, confinementBindOut
	t.Cleanup(func() {
		confinementBindFind, confinementBindRecord, confinementBindStateDir, confinementBindOut = prevFind, prevRec, prevDir, prevOut
	})
	var out bytes.Buffer
	var recorded []string
	confinementBindFind = func(string) (*session.State, error) { return st, findErr }
	confinementBindStateDir = func() (string, error) { return "/state", nil }
	confinementBindRecord = func(_ confinement.System, stateDir, key, wt string) (string, error) {
		recorded = append(recorded, stateDir+"|"+key+"|"+wt)
		return "fafb74e2", recErr
	}
	confinementBindOut = func() io.Writer { return &out }
	return &out, &recorded
}

func TestConfinementBindWritesTheMappingFromTheUnitRecord(t *testing.T) {
	st := &session.State{SessionKey: "clown/smart-hemlock", WorktreePath: "/w/clown/.worktrees/smart-hemlock"}
	out, recorded := stubConfinementBind(t, st, nil, nil)
	if err := runConfinementBind(context.Background(), json.RawMessage(`{"session":"clown/smart-hemlock"}`)); err != nil {
		t.Fatal(err)
	}
	if want := "/state|clown/smart-hemlock|/w/clown/.worktrees/smart-hemlock"; len(*recorded) != 1 || (*recorded)[0] != want {
		t.Errorf("recorded %v, want [%s]", *recorded, want)
	}
	if !strings.Contains(out.String(), "bound principal fafb74e2 to session clown/smart-hemlock") {
		t.Errorf("output %q", out.String())
	}
}

func TestConfinementBindRefusesWithoutASessionOrWithAFailingRecord(t *testing.T) {
	st := &session.State{SessionKey: "clown/x", WorktreePath: "/w"}
	stubConfinementBind(t, st, nil, errors.New("no agent unit record"))
	if err := runConfinementBind(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("an empty session must be refused")
	}
	if err := runConfinementBind(context.Background(), json.RawMessage(`{"session":"clown/x"}`)); err == nil || !strings.Contains(err.Error(), "no agent unit record") {
		t.Errorf("got %v, want the record error", err)
	}
	stubConfinementBind(t, nil, errors.New("no such session"), nil)
	if err := runConfinementBind(context.Background(), json.RawMessage(`{"session":"nope"}`)); err == nil {
		t.Error("an unknown session must be refused")
	}
}

func TestConfinementBindIsCLIOnly(t *testing.T) {
	// The mapping is what the grant walk trusts: it must not be an MCP tool.
	cmd, ok := buildApp().GetCommand("confinement-bind")
	if !ok {
		t.Fatal("command is not registered")
	}
	if cmd.Run != nil || cmd.RunCLI == nil {
		t.Fatalf("must be RunCLI-only (Run=%v RunCLI=%v)", cmd.Run != nil, cmd.RunCLI != nil)
	}
}
