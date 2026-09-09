package main

import (
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

// TestCloseTargetIsVariadic pins the contract that replaced the old
// single-target guard: `target` collects EVERY positional (purse-first#190),
// so `sc close A B C D` closes four sessions and no positional can reach a
// later param. A param declared after a variadic is not dead — it becomes
// flag-only — which is why force and nix-gc still follow target rather than
// being reordered ahead of it.
func TestCloseTargetIsVariadic(t *testing.T) {
	cmd, ok := buildApp().GetCommand("close")
	if !ok {
		t.Fatal("close command not registered")
	}

	var variadic []string
	for _, p := range cmd.Params {
		if p.Variadic {
			variadic = append(variadic, p.Name)
		}
	}
	if len(variadic) != 1 || variadic[0] != "target" {
		t.Fatalf("variadic params = %v, want exactly [target]", variadic)
	}

	// Exactly one param may be variadic, and it must be the only one that can
	// absorb a positional; anything else non-Bool after it is flag-only.
	for _, p := range cmd.Params {
		if p.Name == "target" {
			if p.Type != command.String {
				t.Errorf("target's Type is the ELEMENT type and must stay String, got %v", p.Type)
			}
			if p.Completer == nil {
				t.Error("target must keep its completer so `sc close <TAB>` completes at every position")
			}
		}
	}
}
