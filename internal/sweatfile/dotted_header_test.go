package sweatfile_test

import (
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
