package sweatfile

import "testing"

// TestStandaloneDottedHeadersConsumed is the regression gate for issue #113:
// a standalone dotted sub-table header ([direnv.dotenv] with no bare [direnv]
// parent) is valid TOML — the parent table is implicit — but the
// tommy-generated decoder only consumes the dotted header inside the branch
// entered when the explicit parent header exists, so the fields surface as
// unknown from `sc validate`. The fix lives in tommy's codegen; unskip after
// the tommy bump + `just gen-tommy` regen.
func TestStandaloneDottedHeadersConsumed(t *testing.T) {
	t.Skipf("known tommy codegen bug (#113): standalone dotted headers are not consumed; unskip after the tommy fix + `just gen-tommy` regen (verified failing 2026-06-06: undecoded [direnv.dotenv] / [session-entry.env], structs nil)")

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
			doc, err := Parse([]byte(tc.input))
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
