package main

import (
	"strings"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/sweatfile"
)

func TestBuildPluginCommandParams(t *testing.T) {
	regex := "^[A-Z]+-[0-9]+$"
	sc := sweatfile.StartCommand{
		Name:        "jira",
		Description: "Start a JIRA session",
		ArgName:     "ticket",
		ArgHelp:     "JIRA ticket ID",
		ArgRegex:    &regex,
		Completion:  []string{"printf", "FOO-1\tfirst\nFOO-2\tsecond\n"},
		Prompt:      []string{"printf", "%s", "# hi {arg}"},
	}

	cmd := buildPluginCommand("start-jira", sc)

	if cmd.Name != "start-jira" {
		t.Errorf("Name = %q, want start-jira", cmd.Name)
	}
	if cmd.Description.Short != "Start a JIRA session" {
		t.Errorf("Short = %q", cmd.Description.Short)
	}
	if len(cmd.Params) != 4 {
		t.Fatalf("expected 4 params (arg + 3 standard), got %d", len(cmd.Params))
	}
	arg := cmd.Params[0]
	if arg.Name != "ticket" {
		t.Errorf("arg.Name = %q, want ticket", arg.Name)
	}
	if !arg.Required {
		t.Error("arg.Required should be true")
	}
	if arg.Completer == nil {
		t.Error("arg.Completer should be non-nil")
	}
	wantNames := []string{"ticket", "description", "merge-on-close", "no-attach"}
	for i, want := range wantNames {
		if cmd.Params[i].Name != want {
			t.Errorf("Params[%d].Name = %q, want %q", i, cmd.Params[i].Name, want)
		}
	}
}

func TestBuildPluginCommandDefaultsArgName(t *testing.T) {
	sc := sweatfile.StartCommand{Name: "foo", Prompt: []string{"echo"}}
	cmd := buildPluginCommand("start-foo", sc)
	if cmd.Params[0].Name != "arg" {
		t.Errorf("expected default arg name 'arg', got %q", cmd.Params[0].Name)
	}
	if cmd.Description.Short == "" {
		t.Error("Short description should fall back to a non-empty default")
	}
}

func TestPluginCompleterParsesTabSeparated(t *testing.T) {
	sc := sweatfile.StartCommand{
		Completion: []string{"printf", "%s", "FOO-1\tfirst issue\nFOO-2\tsecond issue\nFOO-3\n"},
	}
	completer := pluginCompleter(sc)
	if completer == nil {
		t.Fatal("expected non-nil completer")
	}
	result := completer()
	if len(result) != 3 {
		t.Fatalf("expected 3 results, got %d: %v", len(result), result)
	}
	if result["FOO-1"] != "first issue" {
		t.Errorf("FOO-1 = %q, want 'first issue'", result["FOO-1"])
	}
	if result["FOO-3"] != "" {
		t.Errorf("FOO-3 = %q, want empty (no tab)", result["FOO-3"])
	}
}

func TestPluginCompleterNilWhenUnset(t *testing.T) {
	if pluginCompleter(sweatfile.StartCommand{}) != nil {
		t.Error("expected nil completer when sc.Completion is empty")
	}
}

func TestPluginCompleterReturnsNilOnFailure(t *testing.T) {
	sc := sweatfile.StartCommand{Completion: []string{"false"}}
	completer := pluginCompleter(sc)
	if completer == nil {
		t.Fatal("expected non-nil completer")
	}
	if got := completer(); got != nil {
		t.Errorf("expected nil on command failure, got %v", got)
	}
}

func TestRunPluginPromptSubstitutesArg(t *testing.T) {
	sc := sweatfile.StartCommand{
		Prompt: []string{"printf", "%s", "# Hello {arg}\n"},
	}
	out, err := runPluginPrompt(sc, "world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "# Hello world") {
		t.Errorf("expected substituted output, got %q", out)
	}
}

func TestRunPluginPromptPropagatesError(t *testing.T) {
	sc := sweatfile.StartCommand{Prompt: []string{"false"}}
	_, err := runPluginPrompt(sc, "x")
	if err == nil {
		t.Error("expected error from failing prompt command")
	}
}

func TestRunPluginPromptEmptyPrompt(t *testing.T) {
	out, err := runPluginPrompt(sweatfile.StartCommand{}, "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "" {
		t.Errorf("expected empty output, got %q", out)
	}
}
