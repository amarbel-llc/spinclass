package main

import (
	"strings"
	"testing"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
)

// TestCloseNixGCIsNotPositionallyEligible is the regression guard for the
// reported misparse: `sc close A B` failed with
// `--nix-gc must be 'true' or 'false', got "B"`.
//
// The CLI framework assigns positional arguments one per NON-Bool param in
// declaration order (go-mcp/command/cli.go). While --nix-gc was a String it
// sat second in that order, so a second target was consumed as its value.
// Declaring it Bool takes it out of positional assignment entirely; the
// handler keeps the tri-state by reading it as *bool.
func TestCloseNixGCIsNotPositionallyEligible(t *testing.T) {
	cmd, ok := buildApp().GetCommand("close")
	if !ok {
		t.Fatal("close command not registered")
	}

	var nixGC *command.Param
	for i := range cmd.Params {
		if cmd.Params[i].Name == "nix-gc" {
			nixGC = &cmd.Params[i]
		}
	}
	if nixGC == nil {
		t.Fatal("close has no --nix-gc param")
	}
	if nixGC.Type != command.Bool {
		t.Errorf("--nix-gc must be command.Bool so positional assignment skips it, got type %v", nixGC.Type)
	}
}

// TestClosePositionalOrder pins which params can absorb a positional argument,
// and in what order. `target` must come first so a lone argument is the
// session; `extra-arg` must be the only other one, so a SECOND positional is
// captured and refused rather than silently discarded (the framework drops
// positionals past the last non-Bool param — purse-first#190).
func TestClosePositionalOrder(t *testing.T) {
	cmd, ok := buildApp().GetCommand("close")
	if !ok {
		t.Fatal("close command not registered")
	}

	var positional []string
	for _, p := range cmd.Params {
		if p.Type != command.Bool {
			positional = append(positional, p.Name)
		}
	}

	want := []string{"target", "extra-arg"}
	if len(positional) != len(want) {
		t.Fatalf("positionally-eligible params = %v, want exactly %v", positional, want)
	}
	for i := range want {
		if positional[i] != want[i] {
			t.Errorf("positional[%d] = %q, want %q (order decides what a bare argument binds to)", i, positional[i], want[i])
		}
	}
}

// A second positional must produce an actionable refusal, not a silent
// partial close: reporting success while leaving sessions alive is the
// failure mode this guard exists to prevent.
func TestErrExtraCloseArg(t *testing.T) {
	if err := errExtraCloseArg(""); err != nil {
		t.Errorf("no extra argument must be accepted, got %v", err)
	}

	err := errExtraCloseArg("madder/plain-poplar")
	if err == nil {
		t.Fatal("an extra argument must be refused")
	}
	for _, want := range []string{"madder/plain-poplar", "one target", "purse-first#190"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}
