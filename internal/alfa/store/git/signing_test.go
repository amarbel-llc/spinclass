package git

import (
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/store"
)

// spinclass#426: the operator ruled that grit-tool commits are accepted
// unsigned. The tools neither request nor suppress a signature: a commit is
// signed only if the git configuration they run under asks for it
// (spinclass-grit(7), COMMIT SIGNATURES). This pins that no commit-creating
// verb adds a signing option. A change to that is a policy change, and must
// update the manpage section with it.
func TestCommitCreatingVerbsAddNoSigningOption(t *testing.T) {
	cases := []struct {
		tool, sub string
		a         args
	}{
		{"commit", "", args{Message: "m"}},
		{"commit", "", args{Amend: true}},
		{"commit", "", args{Message: "m", AllowEmpty: true}},
		{"commit", "", args{Fixup: "abc123"}},
		{"branch", "cherry-pick", args{Commits: []string{"abc123"}}},
		{"branch", "revert", args{Commits: []string{"abc123"}}},
		{"branch", "rebase", args{Upstream: "main"}},
		{"branch", "restack", args{Root: "a", Onto: "main"}},
		{"merge", "", args{Branch: "feature"}},
	}
	for _, c := range cases {
		build := func() ([]string, error) { return buildArgv(store.Op{Tool: c.tool, Subcommand: c.sub}, c.a) }
		if c.tool == "merge" {
			build = func() ([]string, error) { return buildMergeArgv(c.a) }
		}
		argv, err := build()
		if err != nil {
			t.Fatalf("%s %s: %v", c.tool, c.sub, err)
		}
		for _, a := range argv {
			if a == "-S" || strings.HasPrefix(a, "-S") && !strings.HasPrefix(a, "--") ||
				strings.HasPrefix(a, "--gpg-sign") || a == "--no-gpg-sign" ||
				strings.Contains(a, "gpgsign") || strings.Contains(a, "gpg.") {
				t.Errorf("%s %s argv %q carries a signing option %q", c.tool, c.sub, argv, a)
			}
		}
	}
}
