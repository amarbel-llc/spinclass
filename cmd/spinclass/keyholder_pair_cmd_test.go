package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

func TestKeyholderPairStartStatusStopWalksTheWholeLifecycleAndLeavesNothing(t *testing.T) {
	f := newKhFake(t)
	khUseFake(t, f)
	var out bytes.Buffer
	prev := keyholderPairOut
	t.Cleanup(func() { keyholderPairOut = prev })
	keyholderPairOut = func() io.Writer { return &out }
	// The test runs without a terminal, as the live recipes do: they say so.
	prevRecipe := keyholderPairRecipe
	t.Cleanup(func() { keyholderPairRecipe = prevRecipe })
	keyholderPairRecipe = func() bool { return true }
	run := func(action string) string {
		t.Helper()
		out.Reset()
		if err := runKeyholderPair(context.Background(), json.RawMessage(`{"action":"`+action+`","session":"repo/live"}`)); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		return out.String()
	}
	if got := run("status"); !strings.HasPrefix(got, "not running") {
		t.Errorf("status before start = %q", got)
	}
	got := run("start")
	if !strings.Contains(got, "public_key ssh-ed25519 ") || !strings.Contains(got, "agent_sock ") {
		t.Errorf("start output %q lacks the key and socket", got)
	}
	if got := run("status"); !strings.HasPrefix(got, "running repo/live") {
		t.Errorf("status after start = %q", got)
	}
	run("stop")
	for u, a := range f.active {
		if a {
			t.Errorf("unit %s is still active after stop", u)
		}
	}
	l, _ := keyholderLauncherFn(keyholder.Tools{})
	if _, err := os.Lstat(keyholder.SessionDir(l.Base, "repo/live")); !os.IsNotExist(err) {
		t.Errorf("stop left the session directory: %v", err)
	}
	if err := runKeyholderPair(context.Background(), json.RawMessage(`{"action":"mint","session":"repo/live"}`)); err == nil {
		t.Error("an unknown action was accepted")
	}
}
