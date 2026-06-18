// Package testfs provides filesystem must-helpers for spinclass tests:
// thin wrappers over os/json calls that t.Fatal on error instead of
// returning it. They exist so test setup ("write this fixture file",
// "make this dir") reads as one line AND satisfies errcheck — the
// alternative being `if err := os.WriteFile(...); err != nil { t.Fatal(err) }`
// at every call site, or an unchecked bare call that errcheck (golangci-lint
// standard set) flags.
//
// Upstream candidate: dewey's test_ui has a rich assertion surface but no
// filesystem must-helpers; these are proposed for promotion there in
// amarbel-llc/purse-first#158. They take a plain *testing.T (stdlib), not
// test_ui.T.
package testfs

import (
	"encoding/json"
	"os"
	"testing"
)

// MustWriteFile writes data to path or fails the test.
func MustWriteFile(t *testing.T, path string, data []byte, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, perm); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// MustMkdirAll creates dir (and parents) or fails the test.
func MustMkdirAll(t *testing.T, dir string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(dir, perm); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

// MustSymlink creates a symlink at link pointing to target or fails the test.
func MustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
}

// MustUnmarshal decodes JSON data into v or fails the test.
func MustUnmarshal(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal: %v\ndata: %s", err, data)
	}
}
