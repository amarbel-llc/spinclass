package validate

import (
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

// A confined resume strips only the exact `direnv exec .` prefix from the
// pinned resume entry (spinclass#462), so validate warns about a resume that
// starts with direnv in any other shape; the exact prefix and a direnv-free
// entry do not warn.
func TestCheckSessionEntryWarnsOnDirenvPrefixAConfinedResumeCannotStrip(t *testing.T) {
	cases := []struct {
		name  string
		entry []string
		warn  bool
	}{
		{name: "exact prefix, strippable", entry: []string{"direnv", "exec", ".", "clown", "--", "--resume", "{principal}"}},
		{name: "no direnv", entry: []string{"clown", "--", "--resume", "{principal}"}},
		{name: "direnv exec of a fixed path", entry: []string{"direnv", "exec", "/home/op/repo", "clown", "--", "--resume", "{principal}"}, warn: true},
		{name: "direnv with another verb", entry: []string{"direnv", "allow", ".", "clown", "--", "--resume", "{principal}"}, warn: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			issues := CheckSessionEntry(sweatfile.Sweatfile{SessionEntry: &sweatfile.SessionEntry{Resume: c.entry}})
			var found *Issue
			for i := range issues {
				if strings.Contains(issues[i].Message, "confined resume") {
					found = &issues[i]
				}
			}
			if !c.warn {
				if found != nil {
					t.Fatalf("unexpected warning: %+v", *found)
				}
				return
			}
			if found == nil {
				t.Fatalf("no confined-resume warning for %q; issues: %+v", c.entry, issues)
			}
			if found.Severity != SeverityWarning || found.Field != "session-entry.resume" {
				t.Errorf("warning = %+v, want severity warning on session-entry.resume", *found)
			}
		})
	}
}
