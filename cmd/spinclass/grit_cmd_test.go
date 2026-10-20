package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGritStashToolRoundtrip(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(repo, "gitconfig"))
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(repo))
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"config", "commit.gpgsign", "false"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	file := filepath.Join(repo, "a.txt")
	if err := os.WriteFile(file, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "a.txt"}, {"commit", "-m", "first"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(file, []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	run := func(args string) string {
		t.Helper()
		out, err := runGrit(context.Background(), "stash", json.RawMessage(args))
		if err != nil {
			t.Fatalf("stash %s: %v", args, err)
		}
		return out
	}
	run(`{"subcommand":"save","message":"wip"}`)
	if got, _ := os.ReadFile(file); string(got) != "one\n" {
		t.Fatalf("save must restore the tree, got %q", got)
	}
	if out := run(`{"subcommand":"list"}`); !strings.Contains(out, "On main: wip") {
		t.Fatalf("list: %q", out)
	}
	run(`{"subcommand":"apply","stash_ref":"stash@{0}"}`)
	if got, _ := os.ReadFile(file); string(got) != "two\n" {
		t.Fatalf("apply must restore the change, got %q", got)
	}
	run(`{"subcommand":"drop"}`)
	if out := run(`{"subcommand":"list"}`); out != "no stash entries" {
		t.Fatalf("list after drop: %q", out)
	}
}

func TestGritToolsRegistered(t *testing.T) {
	app := buildApp()
	for _, n := range []string{"grit_stage", "grit_branch", "grit_commit", "grit_tag", "grit_worktree", "grit_stash"} {
		if _, ok := app.GetCommand(n); !ok {
			t.Errorf("%s not registered", n)
		}
	}
	for _, n := range []string{"grit_clone", "grit_merge", "grit_remote"} {
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
