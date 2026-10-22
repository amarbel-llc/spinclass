package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"code.linenisgreat.com/purse-first/libs/go-mcp/server"
)

func stubSweep(t *testing.T, revoked int, errs []error) *string {
	t.Helper()
	var gotRepo string
	old := revokeOrphansSweep
	revokeOrphansSweep = func(_ context.Context, repoPath string, _ io.Writer) (int, []error) {
		gotRepo = repoPath
		return revoked, errs
	}
	t.Cleanup(func() { revokeOrphansSweep = old })
	return &gotRepo
}

func TestRevokeOrphansReportsRevokedCount(t *testing.T) {
	got := stubSweep(t, 2, nil)
	var out bytes.Buffer
	if err := reportRevokeOrphans(context.Background(), "/r/posh", &out); err != nil {
		t.Fatal(err)
	}
	if *got != "/r/posh" || !strings.Contains(out.String(), "revoked 2 orphaned credential(s)") {
		t.Errorf("repo %q, output %q", *got, out.String())
	}
}

func TestRevokeOrphansFailsAndNamesWhatRemains(t *testing.T) {
	stubSweep(t, 0, []error{errors.New("posh/smooth-maple: agent down")})
	var out bytes.Buffer
	err := reportRevokeOrphans(context.Background(), "/r/posh", &out)
	if err == nil {
		t.Fatal("a failed revoke must exit nonzero")
	}
	if !strings.Contains(out.String(), "posh/smooth-maple: agent down") {
		t.Errorf("output %q does not name the failed session", out.String())
	}
}

func TestRevokeOrphansIsNotAnMCPTool(t *testing.T) {
	app := buildApp()
	cmd, ok := app.GetCommand("revoke-orphans")
	if !ok {
		t.Fatal("command is not registered")
	}
	if cmd.Run != nil || cmd.RunCLI == nil {
		t.Fatalf("must be RunCLI-only (Run=%v RunCLI=%v)", cmd.Run != nil, cmd.RunCLI != nil)
	}
	registry := server.NewToolRegistryV1()
	app.RegisterMCPToolsV1(registry)
	tools, err := registry.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) == 0 {
		t.Fatal("empty tool list: the assertion would be vacuous")
	}
	for _, tool := range tools {
		if strings.Contains(tool.Name, "revoke-orphans") || strings.Contains(tool.Name, "revoke_orphans") {
			t.Fatalf("revoke-orphans is exposed as MCP tool %q", tool.Name)
		}
	}
}
