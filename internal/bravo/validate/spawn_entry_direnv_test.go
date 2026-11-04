package validate

import (
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

// A confined spawn strips only the exact `direnv exec {dir}` prefix from the
// spawn-entry (spinclass#462). An entry that starts with direnv in any other
// shape would run a confined worker's harness under direnv after all, so
// validate warns about it; the exact prefix and a direnv-free entry do not.
func TestCheckSessionEntryWarnsOnDirenvPrefixAConfinedSpawnCannotStrip(t *testing.T) {
	cases := []struct {
		name  string
		entry []string
		warn  bool
	}{
		{name: "exact prefix, strippable", entry: []string{"direnv", "exec", "{dir}", "clown", "--", "{prompt}"}},
		{name: "no direnv", entry: []string{"clown", "--clown-attach=spawn", "--", "{prompt}"}},
		{name: "direnv exec of a fixed path", entry: []string{"direnv", "exec", "/home/op/repo", "clown", "--", "{prompt}"}, warn: true},
		{name: "direnv with another verb", entry: []string{"direnv", "allow", "{dir}", "clown", "--", "{prompt}"}, warn: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			issues := CheckSessionEntry(sweatfile.Sweatfile{SessionEntry: &sweatfile.SessionEntry{SpawnEntry: c.entry}})
			var found *Issue
			for i := range issues {
				if strings.Contains(issues[i].Message, "confined spawn") {
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
				t.Fatalf("no confined-spawn warning for %q; issues: %+v", c.entry, issues)
			}
			if found.Severity != SeverityWarning || found.Field != "session-entry.spawn-entry" {
				t.Errorf("warning = %+v, want severity warning on session-entry.spawn-entry", *found)
			}
		})
	}
}
