package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// A-H1: `sc keyholder-pair start|stop` can replace or end any session's holder,
// so it is the operator's act: both handles on a terminal, or the variable the
// live recipes set. Status only reads. None of this is a wall between unconfined
// sessions of one account (the variable can be set by any of them); it keeps the
// plain call, a script and an accident out.
func TestKeyholderPairStartAndStopNeedATerminalOrTheRecipesVariable(t *testing.T) {
	f := newKhFake(t)
	khUseFake(t, f)
	var out bytes.Buffer
	prevOut, prevTTY, prevRecipe := keyholderPairOut, operatorHandleIsTerminal, keyholderPairRecipe
	t.Cleanup(func() { keyholderPairOut, operatorHandleIsTerminal, keyholderPairRecipe = prevOut, prevTTY, prevRecipe })
	keyholderPairOut = func() io.Writer { return &out }
	tty, recipe := false, false
	operatorHandleIsTerminal = func() bool { return tty }
	keyholderPairRecipe = func() bool { return recipe }
	run := func(action string) error {
		out.Reset()
		return runKeyholderPair(context.Background(), json.RawMessage(`{"action":"`+action+`","session":"repo/restricted"}`))
	}

	for _, action := range []string{"start", "stop"} {
		err := run(action)
		if err == nil || !strings.Contains(err.Error(), "terminals") || !strings.Contains(err.Error(), keyholderPairRecipeEnv) {
			t.Errorf("%s with no terminal and no recipe variable = %v, want a refusal naming both ways", action, err)
		}
	}
	if len(f.started) != 0 {
		t.Errorf("a refused call started %v", f.started)
	}
	if err := run("status"); err != nil {
		t.Errorf("status needs neither a terminal nor the variable: %v", err)
	}

	tty = true
	if err := run("start"); err != nil {
		t.Errorf("start at a terminal: %v", err)
	}
	tty, recipe = false, true
	if err := run("stop"); err != nil {
		t.Errorf("stop with the recipes' variable: %v", err)
	}
}
