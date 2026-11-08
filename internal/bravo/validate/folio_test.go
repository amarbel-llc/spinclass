package validate

import (
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

func strp(s string) *string { return &s }

// spinclass#443 (operator ruling 2026-11-02): with confinement enabled, a
// [folio] stanza is warned about in two shapes. env:NAME: names the SERVE's
// environment, not a confined unit's, so it warns in any list and more
// sharply in deny (the guard misses the unit's own value). And home: in an
// allow list means the unit's agent-home for a confined session, so an allow
// list with home: entries and no pwd:-style entry covering the agent-home
// would newly allow that subtree: it warns, per list. Neither warns without
// confinement or without a trusted stanza.
func TestCheckFolioConfinement(t *testing.T) {
	policy := func(read, write, deny []string) sweatfileio.FolioPolicy {
		return sweatfileio.FolioPolicy{Configured: true, Read: read, Write: write, Deny: deny, Unmatched: "ask"}
	}
	t.Run("silent without confinement or a stanza", func(t *testing.T) {
		p := policy([]string{"home:.claude", "env:TMPDIR:"}, nil, []string{"env:X:"})
		if got := CheckFolioConfinement(p, false); len(got) != 0 {
			t.Errorf("confinement off should not warn, got %+v", got)
		}
		if got := CheckFolioConfinement(sweatfileio.FolioPolicy{}, true); len(got) != 0 {
			t.Errorf("no stanza should not warn, got %+v", got)
		}
	})
	t.Run("env: warns in every list, sharper in deny", func(t *testing.T) {
		got := CheckFolioConfinement(policy([]string{"pwd:", "env:TMPDIR:"}, nil, []string{"env:HOME_SECRETS:"}), true)
		if len(got) != 2 {
			t.Fatalf("want one warning for the read env: and one for the deny env:, got %+v", got)
		}
		for _, iss := range got {
			if iss.Severity != SeverityWarning || !strings.Contains(iss.Message, "SERVE") {
				t.Errorf("want a warning naming the serve's environment, got %+v", iss)
			}
		}
		if got[0].Field != "folio.read" || got[0].Value != "env:TMPDIR:" || got[1].Field != "folio.deny" || got[1].Value != "env:HOME_SECRETS:" {
			t.Errorf("fields/values = %+v", got)
		}
		if !strings.Contains(got[1].Message, "NOT covered") || strings.Contains(got[0].Message, "NOT covered") {
			t.Errorf("only the deny warning is the sharp one, got read %q deny %q", got[0].Message, got[1].Message)
		}
	})
	t.Run("home: allow without pwd coverage of the agent-home", func(t *testing.T) {
		got := CheckFolioConfinement(policy([]string{"home:.claude"}, []string{"home:.claude/plans", "pwd:src"}, nil), true)
		if len(got) != 2 {
			t.Fatalf("read and write each have home: and no pwd coverage, got %+v", got)
		}
		if got[0].Field != "folio.read" || got[1].Field != "folio.write" {
			t.Errorf("fields = %q %q", got[0].Field, got[1].Field)
		}
		for _, iss := range got {
			if iss.Severity != SeverityWarning || !strings.Contains(iss.Message, "agent-home") || !strings.Contains(iss.Message, "pwd:") {
				t.Errorf("want a warning naming the agent-home and pwd:, got %+v", iss)
			}
		}
	})
	t.Run("pwd coverage of the agent-home silences it", func(t *testing.T) {
		for _, covering := range []string{"pwd:", "pwd:.tmp", "pwd:.tmp/agent-home", "pwd:*/agent-home", "spinclass:worktree"} {
			if got := CheckFolioConfinement(policy([]string{"home:.claude", covering}, nil, nil), true); len(got) != 0 {
				t.Errorf("%s covers the agent-home, got %+v", covering, got)
			}
		}
		for _, notCovering := range []string{"pwd:src", "pwd:.tmp/other", "pwd:.tmp/agent-home/x", "spinclass:repo", "nix-store:"} {
			if got := CheckFolioConfinement(policy([]string{"home:.claude", notCovering}, nil, nil), true); len(got) != 1 {
				t.Errorf("%s does not cover the agent-home, want one warning, got %+v", notCovering, got)
			}
		}
	})
	t.Run("home: in deny only narrows and does not warn", func(t *testing.T) {
		if got := CheckFolioConfinement(policy([]string{"pwd:"}, nil, []string{"home:.ssh"}), true); len(got) != 0 {
			t.Errorf("a home: deny is not the reach edge, got %+v", got)
		}
	})
}

// [folio] (spinclass#438): every entry must parse under the stanza grammar
// (an unparseable entry is an ERROR, because an ignored deny would widen);
// unmatched must be ask or deny; and the allow lists and unmatched are honoured
// only above the repo, so a repo-level value warns as ignored. A repo-level
// deny is fine: it only narrows.
func TestCheckFolio(t *testing.T) {
	good := sweatfile.Sweatfile{Folio: &sweatfile.Folio{
		Read:      []string{"pwd:", "spinclass:children", "madder:"},
		Write:     []string{"home:.claude/plans"},
		Deny:      []string{"spinclass:worktrees/*/.spinclass/state.json"},
		Unmatched: strp("ask"),
	}}
	if got := CheckFolio(good, true); len(got) != 0 {
		t.Errorf("a valid stanza above the repo should pass, got %+v", got)
	}
	if got := CheckFolio(sweatfile.Sweatfile{}, false); len(got) != 0 {
		t.Errorf("absent table should not warn, got %+v", got)
	}

	got := CheckFolio(good, false)
	if len(got) != 3 {
		t.Fatalf("repo-level read, write and unmatched should each warn once, got %+v", got)
	}
	for _, iss := range got {
		if iss.Severity != SeverityWarning || !strings.Contains(iss.Message, "ignored") {
			t.Errorf("repo-level allow should be a warning saying it is ignored, got %+v", iss)
		}
	}
	if got := CheckFolio(sweatfile.Sweatfile{Folio: &sweatfile.Folio{Deny: []string{"pwd:secret"}}}, false); len(got) != 0 {
		t.Errorf("a repo-level deny is honoured and should not warn, got %+v", got)
	}

	bad := sweatfile.Sweatfile{Folio: &sweatfile.Folio{
		Read:      []string{"pwd:../up", "madder:store"}, // the store refinement is reserved
		Deny:      []string{"nowhere:x"},
		Unmatched: strp("allow"),
	}}
	got = CheckFolio(bad, true)
	if len(got) != 4 {
		t.Fatalf("want one error per bad read (two), bad deny and bad unmatched, got %+v", got)
	}
	for _, iss := range got {
		if iss.Severity != SeverityError {
			t.Errorf("a bad entry is an error, got %+v", iss)
		}
	}
	if got[0].Field != "folio.read" || got[1].Field != "folio.read" || got[2].Field != "folio.deny" || got[3].Field != "folio.unmatched" {
		t.Errorf("fields = %q %q %q %q", got[0].Field, got[1].Field, got[2].Field, got[3].Field)
	}
	if got[0].Value != "pwd:../up" || got[1].Value != "madder:store" {
		t.Errorf("the offending entry should be the issue's value, got %q %q", got[0].Value, got[1].Value)
	}
}

// spinclass#465: proc: entries parse only from the fixed list, and only in a
// read or deny list, never in write.
func TestCheckFolioProc(t *testing.T) {
	ok := sweatfile.Sweatfile{Folio: &sweatfile.Folio{
		Read:      []string{"proc:", "proc:*/stat", "proc:pressure/*"},
		Deny:      []string{"proc:*/cgroup"},
		Unmatched: strp("ask"),
	}}
	if got := CheckFolio(ok, true); len(got) != 0 {
		t.Errorf("valid proc entries should pass, got %+v", got)
	}

	for _, entry := range []string{"proc:*/environ", "proc:*/fd", "proc:*/cmdline", "proc:*/task", "proc:*/mem"} {
		bad := sweatfile.Sweatfile{Folio: &sweatfile.Folio{Read: []string{entry}, Unmatched: strp("ask")}}
		got := CheckFolio(bad, true)
		if len(got) != 1 || got[0].Severity != SeverityError || got[0].Value != entry {
			t.Errorf("%s should be one error naming it, got %+v", entry, got)
		}
	}

	inWrite := sweatfile.Sweatfile{Folio: &sweatfile.Folio{Write: []string{"proc:meminfo"}, Unmatched: strp("ask")}}
	got := CheckFolio(inWrite, true)
	if len(got) != 1 || got[0].Severity != SeverityError || got[0].Field != "folio.write" {
		t.Errorf("proc: in write should be one error on folio.write, got %+v", got)
	}
}
