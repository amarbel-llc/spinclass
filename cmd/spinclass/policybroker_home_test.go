package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/policybroker"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// brokerHomeFixture is a worktree, an operator HOME, and a state dir, with the
// worktree optionally marked as launched confined (a unit spec exists for it),
// which is what the broker classifies its subject by.
type brokerHomeFixture struct {
	opHome, worktree, stateDir string
}

func newBrokerHomeFixture(t *testing.T, confined bool) brokerHomeFixture {
	t.Helper()
	real := func(d string) string {
		r, err := filepath.EvalSymlinks(d)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	f := brokerHomeFixture{opHome: real(t.TempDir()), worktree: real(t.TempDir()), stateDir: real(t.TempDir())}
	t.Setenv("HOME", f.opHome)
	if confined {
		spec := confinement.UnitSpecPath(f.stateDir, f.worktree)
		if err := os.MkdirAll(filepath.Dir(spec), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(spec, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f brokerHomeFixture) verdict(t *testing.T, policy sweatfileio.FolioPolicy, path string) (string, string) {
	t.Helper()
	policy.Configured = true
	ev, err := newFolioEvaluator(policyBrokerPlan{Path: "unused", SessionKey: "r/b", Policy: policy, StateDir: f.stateDir, Worktree: f.worktree})
	if err != nil {
		t.Fatal(err)
	}
	v, reason, err := ev.Evaluate(context.Background(), policybroker.Check{Op: "read", Resource: policybroker.Resource{Kind: "file", URI: "file://" + path}})
	if err != nil {
		t.Fatal(err)
	}
	return v, reason
}

// spinclass#443 (operator ruling 2026-11-02, option iii): for a CONFINED
// subject home: is the unit's HOME, the agent-home staged under the worktree;
// for an unconfined subject it stays the serve's HOME. Allow and deny both
// follow it, and deny still wins a full tie.
func TestHomeAnchorIsTheUnitsHomeForAConfinedSubject(t *testing.T) {
	policy := sweatfileio.FolioPolicy{Read: []string{"home:.claude"}, Deny: []string{"home:.ssh"}, Unmatched: "deny"}
	for _, tc := range []struct {
		name         string
		confined     bool
		inAgentHome  bool // the path is under <worktree>/.tmp/agent-home, else under the operator's HOME
		rel, verdict string
		reason       string
	}{
		{"confined, agent-home allow", true, true, ".claude/settings.json", "allow", "read home:.claude"},
		{"confined, agent-home deny", true, true, ".ssh/id", "deny", "deny home:.ssh"},
		{"confined, operator home is not its home", true, false, ".claude/settings.json", "deny", "unmatched"},
		{"confined, operator deny is not its home either", true, false, ".ssh/id", "deny", "unmatched"},
		{"unconfined, operator home allow", false, false, ".claude/settings.json", "allow", "read home:.claude"},
		{"unconfined, operator home deny", false, false, ".ssh/id", "deny", "deny home:.ssh"},
		{"unconfined, agent-home path is not home", false, true, ".claude/settings.json", "deny", "unmatched"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBrokerHomeFixture(t, tc.confined)
			root := f.opHome
			if tc.inAgentHome {
				root = confinement.AgentHomeOf(f.worktree)
			}
			if v, reason := f.verdict(t, policy, filepath.Join(root, tc.rel)); v != tc.verdict || reason != tc.reason {
				t.Errorf("%s = %s (%s), want %s (%s)", tc.rel, v, reason, tc.verdict, tc.reason)
			}
		})
	}

	t.Run("deny wins a full tie", func(t *testing.T) {
		f := newBrokerHomeFixture(t, true)
		tie := sweatfileio.FolioPolicy{Read: []string{"home:"}, Deny: []string{"home:"}, Unmatched: "ask"}
		if v, reason := f.verdict(t, tie, filepath.Join(confinement.AgentHomeOf(f.worktree), "x")); v != "deny" || reason != "deny home:" {
			t.Errorf("tie = %s (%s), want deny (deny home:)", v, reason)
		}
	})
}

// Symlinks are resolved before matching, on both sides: a link inside the
// agent-home that points out of it makes the path an outside path, so home:
// does not match it; and a worktree reached through a symlink names the same
// agent-home as its real path.
func TestHomeAnchorForAConfinedSubjectResolvesSymlinks(t *testing.T) {
	policy := sweatfileio.FolioPolicy{Read: []string{"home:"}, Unmatched: "deny"}

	t.Run("a link out of the agent-home", func(t *testing.T) {
		f := newBrokerHomeFixture(t, true)
		home := confinement.AgentHomeOf(f.worktree)
		outside, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(home, "out")); err != nil {
			t.Fatal(err)
		}
		if v, reason := f.verdict(t, policy, filepath.Join(home, "out", "secret")); v != "deny" || reason != "unmatched" {
			t.Errorf("through a link out of the agent-home = %s (%s), want deny (unmatched)", v, reason)
		}
		if v, _ := f.verdict(t, policy, filepath.Join(home, "plain")); v != "allow" {
			t.Errorf("a plain file in the agent-home = %s, want allow", v)
		}
	})

	t.Run("the worktree named through a link", func(t *testing.T) {
		f := newBrokerHomeFixture(t, true)
		link := filepath.Join(t.TempDir(), "wt-link")
		if err := os.Symlink(f.worktree, link); err != nil {
			t.Fatal(err)
		}
		// The unit spec is keyed by the worktree path the serve was started in.
		viaLink := brokerHomeFixture{opHome: f.opHome, worktree: link, stateDir: f.stateDir}
		spec := confinement.UnitSpecPath(f.stateDir, link)
		if err := os.MkdirAll(filepath.Dir(spec), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(spec, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(confinement.AgentHomeOf(f.worktree), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{
			filepath.Join(confinement.AgentHomeOf(f.worktree), "a"), // the real path
			filepath.Join(confinement.AgentHomeOf(link), "a"),       // the linked path
		} {
			if v, reason := viaLink.verdict(t, policy, p); v != "allow" {
				t.Errorf("%s = %s (%s), want allow", p, v, reason)
			}
		}
	})
}
