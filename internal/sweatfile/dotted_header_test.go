package sweatfile_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/amarbel-llc/spinclass/internal/sweatfile"
	"github.com/amarbel-llc/spinclass/internal/sweatfileio"
)

// TestStandaloneDottedHeadersConsumed is the regression gate for issue #113:
// a standalone dotted sub-table header ([direnv.dotenv] with no bare [direnv]
// parent) is valid TOML — the parent table is implicit — and must decode
// fully (fields populated, nothing undecoded, so `sc validate` stays quiet).
// Fixed by tommy v0.3.2 (tommy#113: the generated decoder synthesizes the
// implicit parent); this test pins the regen.
func TestStandaloneDottedHeadersConsumed(t *testing.T) {
	cases := []struct {
		name  string
		input string
		check func(t *testing.T, sf *Sweatfile)
	}{
		{
			name: "direnv.dotenv without bare [direnv]",
			input: `
[hooks]
pre-merge = "just"

[direnv.dotenv]
GOLANGCI_LINT_CACHE = "$WORKTREE/.tmp/golangci-lint"
`,
			check: func(t *testing.T, sf *Sweatfile) {
				if sf.Direnv == nil || sf.Direnv.Dotenv["GOLANGCI_LINT_CACHE"] == "" {
					t.Errorf("Direnv.Dotenv not populated: %+v", sf.Direnv)
				}
			},
		},
		{
			name: "session-entry.env without bare [session-entry]",
			input: `
[session-entry.env]
FOO = "bar"
`,
			check: func(t *testing.T, sf *Sweatfile) {
				if sf.SessionEntry == nil || sf.SessionEntry.Env["FOO"] != "bar" {
					t.Errorf("SessionEntry.Env not populated: %+v", sf.SessionEntry)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := sweatfileio.Parse([]byte(tc.input))
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			if u := doc.Undecoded(); len(u) != 0 {
				t.Errorf("standalone dotted header left undecoded keys: %v", u)
			}
			tc.check(t, doc.Data())
		})
	}
}

// TestScalarBeforeSubtable is the regression gate for issue #196: a parent
// table with a scalar key declared BEFORE its sub-table header must still
// decode the sub-table. The failing form is the standard [parent.subtable]
// header (not an alternate spelling), and the drop was silent — the document
// parsed as valid TOML and the sub-table value was discarded at decode.
func TestScalarBeforeSubtable(t *testing.T) {
	input := `
[direnv]
envrc = ["source_up", "use flake"]

[direnv.dotenv]
PIGGY_STORE_DIR = "/x"
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if u := doc.Undecoded(); len(u) != 0 {
		t.Errorf("scalar-before-subtable left undecoded keys: %v", u)
	}
	sf := doc.Data()
	if sf.Direnv == nil {
		t.Fatalf("Direnv nil")
	}
	if len(sf.Direnv.Envrc) != 2 {
		t.Errorf("Direnv.Envrc not populated: %+v", sf.Direnv.Envrc)
	}
	if got := sf.Direnv.Dotenv["PIGGY_STORE_DIR"]; got != "/x" {
		t.Errorf("Direnv.Dotenv[PIGGY_STORE_DIR] = %q, want %q (sub-table dropped)", got, "/x")
	}
}

// TestScalarBeforeSubtableWithPrecedingTable parses a document shaped like the
// real spinclass worktree sweatfile: a [hooks] table with scalar keys appears
// BEFORE [direnv], which itself carries an envrc scalar before [direnv.dotenv].
// This is closer to the real #196 trigger than the bare TestScalarBeforeSubtable.
func TestScalarBeforeSubtableWithPrecedingTable(t *testing.T) {
	input := `
[hooks]
pre-commit = "conformist-pre-commit"
pre-merge  = "just"

[direnv]
envrc = ["source_up"]

[direnv.dotenv]
GOLANGCI_LINT_CACHE = "$WORKTREE/.tmp/golangci-lint"
ISSUE196_PROBE = "$WORKTREE/probe"
`
	doc, err := sweatfileio.Parse([]byte(input))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if u := doc.Undecoded(); len(u) != 0 {
		t.Errorf("left undecoded keys: %v", u)
	}
	sf := doc.Data()
	if sf.Direnv == nil {
		t.Fatalf("Direnv nil")
	}
	if got := sf.Direnv.Dotenv["GOLANGCI_LINT_CACHE"]; got == "" {
		t.Errorf("Dotenv[GOLANGCI_LINT_CACHE] dropped")
	}
	if got := sf.Direnv.Dotenv["ISSUE196_PROBE"]; got != "$WORKTREE/probe" {
		t.Errorf("Dotenv[ISSUE196_PROBE] = %q, want $WORKTREE/probe (dropped)", got)
	}
}

// TestScalarBeforeSubtableHierarchy reproduces the exact #196 scenario as the
// reporter observed it: a parent sweatfile contributes an inherited
// direnv.dotenv key via the bare [direnv] form, and the repo sweatfile adds its
// own dotenv key via the scalar-before-subtable form ([direnv] with envrc, then
// [direnv.dotenv]). After the hierarchy merge the .spinclass/env must carry
// BOTH keys; the report was that only the inherited key survived.
func TestScalarBeforeSubtableHierarchy(t *testing.T) {
	home := t.TempDir()
	repoDir := filepath.Join(home, "eng", "repos", "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Parent: inherited dotenv key via bare [direnv] form (the eng/sweatfile
	// pattern, which the report confirmed working).
	parentPath := filepath.Join(home, "eng", "sweatfile")
	writeSweatfile(t, parentPath, `
[direnv.dotenv]
INHERITED_KEY = "/parent"
`)

	// Repo: scalar-before-subtable form (the failing shape).
	repoSweatfile := filepath.Join(repoDir, "sweatfile")
	writeSweatfile(t, repoSweatfile, `
[direnv]
envrc = ["source_up", "use flake"]

[direnv.dotenv]
PIGGY_STORE_DIR = "/x"
`)

	result, err := sweatfileio.LoadHierarchy(home, repoDir)
	if err != nil {
		t.Fatalf("LoadHierarchy returned error: %v", err)
	}

	merged := result.Merged
	if merged.Direnv == nil {
		t.Fatalf("merged Direnv nil")
	}
	if got := merged.Direnv.Dotenv["INHERITED_KEY"]; got != "/parent" {
		t.Errorf("Dotenv[INHERITED_KEY] = %q, want /parent", got)
	}
	if got := merged.Direnv.Dotenv["PIGGY_STORE_DIR"]; got != "/x" {
		t.Errorf("Dotenv[PIGGY_STORE_DIR] = %q, want /x (child sub-table dropped in merge)", got)
	}
}
