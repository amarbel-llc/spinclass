package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/store"
)

// runGritPermsWith drives `sc grit-perms` with stdin as the invocation JSON and
// the given caller session (nil means no resolvable session).
func runGritPermsWith(t *testing.T, tool, stdin string, s *store.Session) (stdout string, err error) {
	t.Helper()
	out := &bytes.Buffer{}
	oldIn, oldOut, oldSession := gritPermsStdin, gritPermsStdout, gritPermsSession
	t.Cleanup(func() { gritPermsStdin, gritPermsStdout, gritPermsSession = oldIn, oldOut, oldSession })
	gritPermsStdin = func() io.Reader { return strings.NewReader(stdin) }
	gritPermsStdout = func() io.Writer { return out }
	gritPermsSession = func() (store.Session, error) {
		if s == nil {
			return store.Session{}, errors.New("no frontend principal mapping")
		}
		return *s, nil
	}
	raw, _ := json.Marshal(gritPermsFlags{Tool: tool})
	err = runGritPerms(context.Background(), raw)
	return out.String(), err
}

func TestGritPermsStaticFallbackAllowsReadsAndAsksOnWrites(t *testing.T) {
	out, err := runGritPermsWith(t, "stage", `{"subcommand":"status"}`, nil)
	if err != nil || strings.TrimSpace(out) != "allow" {
		t.Fatalf("read with no session: out=%q err=%v, want allow", out, err)
	}
	out, err = runGritPermsWith(t, "stage", `{"subcommand":"add","paths":["a"]}`, nil)
	if err == nil || strings.TrimSpace(out) != "ask" {
		t.Fatalf("write with no session: out=%q err=%v, want ask and a nonzero exit", out, err)
	}
}

func TestGritPermsFloorAlwaysAsks(t *testing.T) {
	out, err := runGritPermsWith(t, "branch", `{"subcommand":"delete","name":"x","force":true}`, nil)
	if err == nil || strings.TrimSpace(out) != "ask" {
		t.Fatalf("force: out=%q err=%v, want ask", out, err)
	}
}

func TestGritPermsStashAllowedWithResolvedSession(t *testing.T) {
	repo := isolatedGitRepo(t)
	s := store.Session{Dir: repo, Branch: "glad-buckeye"}
	out, err := runGritPermsWith(t, "stash", `{"subcommand":"save"}`, &s)
	if err != nil || strings.TrimSpace(out) != "allow" {
		t.Fatalf("stash in a resolved session: out=%q err=%v, want allow", out, err)
	}
}

func TestGritPermsFailsClosed(t *testing.T) {
	for name, tc := range map[string]struct{ tool, stdin string }{
		"unknown tool":         {"nope", `{"subcommand":"status"}`},
		"malformed json":       {"stage", `{`},
		"missing subcommand":   {"stage", `{}`},
		"non-object arguments": {"stage", `[]`},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := runGritPermsWith(t, tc.tool, tc.stdin, nil)
			if err == nil {
				t.Fatalf("want a nonzero exit, got out=%q", out)
			}
			if strings.TrimSpace(out) == "allow" {
				t.Fatalf("must never print allow, got %q", out)
			}
		})
	}
}

func TestGritPermsIsCLIOnly(t *testing.T) {
	cmd, ok := buildApp().GetCommand("grit-perms")
	if !ok {
		t.Fatal("grit-perms is not registered")
	}
	if cmd.Run != nil || cmd.RunCLI == nil {
		t.Fatalf("must be RunCLI-only (Run=%v RunCLI=%v)", cmd.Run != nil, cmd.RunCLI != nil)
	}
}
