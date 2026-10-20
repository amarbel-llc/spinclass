package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestGritToolsRegistered(t *testing.T) {
	app := buildApp()
	for _, n := range []string{"grit_stage", "grit_branch", "grit_commit", "grit_tag", "grit_worktree"} {
		if _, ok := app.GetCommand(n); !ok {
			t.Errorf("%s not registered", n)
		}
	}
	for _, n := range []string{"grit_clone", "grit_merge", "grit_remote", "grit_stash"} {
		if _, ok := app.GetCommand(n); ok {
			t.Errorf("%s must not be registered yet", n)
		}
	}
}

func TestRunGritUnsupportedAndMissingSubcommand(t *testing.T) {
	_, err := runGrit(context.Background(), "tag", json.RawMessage(`{"subcommand":"create"}`))
	if err == nil || !strings.Contains(err.Error(), "not yet supported") {
		t.Fatalf("want not-yet-supported error, got %v", err)
	}
	if _, err = runGrit(context.Background(), "tag", json.RawMessage(`{}`)); err == nil {
		t.Fatal("missing subcommand must error")
	}
}

func TestRunGritCommitNeedsNoSubcommand(t *testing.T) {
	_, err := runGrit(context.Background(), "commit", json.RawMessage(`{}`))
	if err == nil || strings.Contains(err.Error(), "subcommand is required") || strings.Contains(err.Error(), "not yet supported") {
		t.Fatalf("commit must reach the backend (and fail on the missing message), got %v", err)
	}
}
