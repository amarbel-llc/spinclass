package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

func TestRunImplicitSessionKeyPrintsBareKey(t *testing.T) {
	repo := lazyKeyFixture(t)
	var stdout, stderr bytes.Buffer

	code := runImplicitSessionKey(&stdout, &stderr, repo, "0b6a1f7e-3c2d-4e5f-9a8b-7c6d5e4f3a2b")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 {
		t.Fatalf("stdout %q is not a single bare line", out)
	}
	key := strings.TrimSuffix(out, "\n")
	if filepath.Dir(key) != "repo" || len(filepath.Base(key)) != 16 {
		t.Errorf("key %q, want repo/<16hex>", key)
	}
	if len(implicitStateFiles(t, repo)) != 0 {
		t.Error("query must not materialize state")
	}
}

func TestRunImplicitSessionKeyRefusalExitsThree(t *testing.T) {
	testgit.RequireGit(t)
	var stdout, stderr bytes.Buffer

	code := runImplicitSessionKey(&stdout, &stderr, t.TempDir(), "sid")
	if code != exitImplicitSessionRefused {
		t.Fatalf("exit = %d, want %d", code, exitImplicitSessionRefused)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.HasPrefix(stderr.String(), "implicit-session-key: refused: not-toplevel") {
		t.Errorf("stderr = %q", stderr.String())
	}
}
