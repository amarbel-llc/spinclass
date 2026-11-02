package folioeval

import (
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/folio"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

func compile(t *testing.T, p sweatfileio.FolioPolicy) *Policy {
	t.Helper()
	p.Configured = true
	if p.Unmatched == "" {
		p.Unmatched = "ask"
	}
	c, err := Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var roots = Roots{
	Pwd:             "/w/repo/.worktrees/me",
	Home:            "/home/u",
	NixStore:        "/nix/store",
	Env:             func(n string) string { return map[string]string{"TMP": "/tmp/claude-1", "EMPTY": ""}[n] },
	Worktree:        "/w/repo/.worktrees/me",
	Repo:            "/w/repo",
	Worktrees:       "/w/repo/.worktrees",
	Children:        []string{"/w/repo/.worktrees/kid", "/w/other/.worktrees/kid2"},
	SiblingsParent:  "/w",
	SiblingsExclude: "/w/repo",
}

func TestCompileRefusesWhatSCValidateRefuses(t *testing.T) {
	for name, p := range map[string]sweatfileio.FolioPolicy{
		"unconfigured":  {Unmatched: "ask"},
		"bad read":      {Configured: true, Read: []string{"pwd:../x"}, Unmatched: "ask"},
		"bad deny":      {Configured: true, Deny: []string{"nowhere:x"}, Unmatched: "ask"},
		"no unmatched":  {Configured: true, Read: []string{"pwd:"}},
		"bad unmatched": {Configured: true, Unmatched: "allow"},
	} {
		if c, err := Compile(p); err == nil {
			t.Errorf("%s: want an error, got %+v", name, c)
		}
	}
}

func TestEvaluateAnchorsAndOps(t *testing.T) {
	p := compile(t, sweatfileio.FolioPolicy{
		Read:  []string{"pwd:", "nix-store:", "home:.claude", "env:TMP:", "env:EMPTY:", "spinclass:siblings", "spinclass:children", "spinclass:repo/docs"},
		Write: []string{"pwd:", "home:.claude/plans"},
	})
	for _, tc := range []struct {
		op, path, verdict string
	}{
		{"read", "/w/repo/.worktrees/me/a.go", "allow"},
		{"write", "/w/repo/.worktrees/me/a.go", "allow"},
		{"exec", "/w/repo/.worktrees/me/a.go", "ask"}, // no exec allow list
		{"read", "/w/repo/.worktrees/me", "allow"},    // the root itself
		{"read", "/w/repo/.worktrees/mean/x", "ask"},  // directory boundary
		{"read", "/nix/store/abc-x/bin/x", "allow"},
		{"write", "/nix/store/abc-x", "ask"},
		{"read", "/home/u/.claude/settings.json", "allow"},
		{"write", "/home/u/.claude/settings.json", "ask"},
		{"write", "/home/u/.claude/plans/p.md", "allow"},
		{"read", "/home/u/.claudette", "ask"},
		{"read", "/tmp/claude-1/x", "allow"},
		{"read", "/x", "ask"},                // EMPTY matches nothing, and / is nobody's root
		{"read", "/w/other/README", "allow"}, // sibling repo
		{"read", "/w/repo/README", "ask"},    // the main checkout is not a sibling
		{"read", "/w/repo/docs/a.md", "allow"},
		{"read", "/w/repo/.worktrees/kid/.spinclass/spawn.log", "allow"}, // held child, nothing denied
		{"read", "/w/repo/.worktrees/stranger/x", "ask"},
		{"read", "/w/other/.worktrees/kid2/x", "allow"},
		{"read", "relative/path", "ask"},
	} {
		if v, reason := p.Evaluate(roots, tc.op, tc.path); v != tc.verdict {
			t.Errorf("%s %s = %s (%s), want %s", tc.op, tc.path, v, reason, tc.verdict)
		}
	}
}

// The ruling (2026-11-01, "css selector-like"): specificity is the ordered
// pair (literal segments, wildcard segments) of the entry under its anchor;
// more literals wins, then more wildcards; a full tie goes to deny. The
// circus#399 case is the worked example: a coordinator may read every
// session worktree of the repo including .spinclass/spawn.log, but nothing
// else under .spinclass/.
func TestEvaluateSpecificityIsACSSLikeTupleAndDenyWinsTies(t *testing.T) {
	p := compile(t, sweatfileio.FolioPolicy{
		Read: []string{"spinclass:worktrees", "spinclass:worktrees/*/.spinclass/spawn.log", "pwd:a/*/c"},
		Deny: []string{"spinclass:worktrees/*/.spinclass", "pwd:secret", "pwd:", "pwd:a/b"},
	})
	for _, tc := range []struct {
		path, verdict, reason string
	}{
		// (2,1) spawn.log beats (1,1) .spinclass which beats (0,0) worktrees.
		{"/w/repo/.worktrees/kid/.spinclass/spawn.log", "allow", "read spinclass:worktrees/*/.spinclass/spawn.log"},
		{"/w/repo/.worktrees/kid/.spinclass/state.json", "deny", "deny spinclass:worktrees/*/.spinclass"},
		{"/w/repo/.worktrees/kid/.spinclass", "deny", "deny spinclass:worktrees/*/.spinclass"},
		{"/w/repo/.worktrees/kid/README", "allow", "read spinclass:worktrees"},
		// (0,0) against (0,0): a full tie goes to deny.
		{"/w/repo/.worktrees/me/README", "deny", "deny pwd:"},
		{"/w/repo/.worktrees/me/secret/k", "deny", "deny pwd:secret"},
		// Equal literals, the entry with more wildcards wins: allow (2,1)
		// beats deny (2,0), as (2,1) > (2,0) in CSS order.
		{"/w/repo/.worktrees/me/a/b/c/x", "allow", "read pwd:a/*/c"},
		// Fewer literals never beats more, whatever the wildcards: deny (2,0)
		// beats allow (0,0) and the allow (2,1) does not match a/b/d.
		{"/w/repo/.worktrees/me/a/b/d", "deny", "deny pwd:a/b"},
	} {
		v, reason := p.Evaluate(roots, "read", tc.path)
		if v != tc.verdict || reason != tc.reason {
			t.Errorf("%s = %s (%s), want %s (%s)", tc.path, v, reason, tc.verdict, tc.reason)
		}
	}
}

func TestSpecificityOrder(t *testing.T) {
	spec := func(s string) Specificity {
		t.Helper()
		p, err := folio.ParsePattern(s)
		if err != nil {
			t.Fatal(err)
		}
		return SpecificityOf(p)
	}
	for _, tc := range []struct {
		more, less string
	}{
		{"pwd:a", "pwd:"},
		{"pwd:a/b", "pwd:a/*"},
		{"pwd:a/*", "pwd:a"},
		{"pwd:a/b", "pwd:*/*/*"},
		{"spinclass:worktrees/*/.spinclass", "spinclass:worktrees"},
		{"spinclass:siblings/x", "spinclass:siblings"},
	} {
		if m, l := spec(tc.more), spec(tc.less); !m.Beats(l) || l.Beats(m) {
			t.Errorf("%s %v should beat %s %v", tc.more, m, tc.less, l)
		}
	}
	if a, b := spec("pwd:a/*"), spec("pwd:*/a"); a.Beats(b) || b.Beats(a) {
		t.Errorf("%v and %v are a full tie", a, b)
	}
}

// Blob resources (ruled 2026-11-02, option B): only a madder: entry matches
// a madder://blobs/<digest> URI, and it matches every one; no file pattern
// does, and madder: matches no file. The tuple rule applies unchanged: every
// madder: entry is (0,0), so one in deny beats one in an allow list. Without
// a madder: entry in the op's list the policy's unmatched verdict applies.
func TestEvaluateBlobIsTheMadderAnchorAlone(t *testing.T) {
	const blob = "madder://blobs/blake2b256-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for name, tc := range map[string]struct {
		policy           sweatfileio.FolioPolicy
		op, uri, verdict string
		reason           string
	}{
		"opted in":           {sweatfileio.FolioPolicy{Read: []string{"pwd:", "madder:"}}, "read", blob, "allow", "read madder:"},
		"write not opted":    {sweatfileio.FolioPolicy{Read: []string{"madder:"}}, "write", blob, "ask", "unmatched"},
		"write opted":        {sweatfileio.FolioPolicy{Write: []string{"madder:"}}, "write", blob, "allow", "write madder:"},
		"exec has no allow":  {sweatfileio.FolioPolicy{Read: []string{"madder:"}}, "exec", blob, "ask", "unmatched"},
		"file patterns only": {sweatfileio.FolioPolicy{Read: []string{"pwd:", "nix-store:", "spinclass:siblings"}}, "read", blob, "ask", "unmatched"},
		"deny wins the tie":  {sweatfileio.FolioPolicy{Read: []string{"madder:"}, Deny: []string{"madder:"}}, "read", blob, "deny", "deny madder:"},
		"denied unmatched":   {sweatfileio.FolioPolicy{Read: []string{"pwd:"}, Unmatched: "deny"}, "read", blob, "deny", "unmatched"},
		"not a blob uri":     {sweatfileio.FolioPolicy{Read: []string{"madder:"}}, "read", "madder://blobs/", "ask", "unmatched: resource is not a madder://blobs/<digest> URI"},
		"wrong scheme":       {sweatfileio.FolioPolicy{Read: []string{"madder:"}}, "read", "file:///w/repo/.worktrees/me/a.go", "ask", "unmatched: resource is not a madder://blobs/<digest> URI"},
		"a path in the uri":  {sweatfileio.FolioPolicy{Read: []string{"madder:"}}, "read", "madder://blobs/x/y", "ask", "unmatched: resource is not a madder://blobs/<digest> URI"},
	} {
		t.Run(name, func(t *testing.T) {
			p := compile(t, tc.policy)
			if v, reason := p.EvaluateBlob(tc.op, tc.uri); v != tc.verdict || reason != tc.reason {
				t.Errorf("%s %s = %s (%s), want %s (%s)", tc.op, tc.uri, v, reason, tc.verdict, tc.reason)
			}
		})
	}
	// madder: in every list never reaches a file.
	p := compile(t, sweatfileio.FolioPolicy{Read: []string{"madder:"}, Deny: []string{"madder:"}})
	if v, reason := p.Evaluate(roots, "read", "/w/repo/.worktrees/me/a.go"); v != "ask" || reason != "unmatched" {
		t.Errorf("a file against madder: entries = %s (%s), want ask (unmatched)", v, reason)
	}
}

// A confined unit's HOME is the agent-home clown stages at
// <worktree>/.tmp/agent-home, and the worktree is bound into the unit at its
// own path, so a path the in-unit moxy names under its HOME is the same path
// the broker sees and lies under the worktree: pwd: covers it, no translation
// is needed (clown cmd/clown/agentscope.go stageAgentHome, agentscope
// unit.go). The evaluator expands home: to whatever Roots.Home says; here that
// is the operator's HOME. Which HOME a session gets is the broker's choice
// (cmd/spinclass homeForSubject: the unit's agent-home for a confined session,
// ruled 2026-11-02), tested there. Pinned here so the path rule is a
// decision, not an accident (spinclass#443).
func TestEvaluateAgentHomeInsideTheWorktreeIsPwd(t *testing.T) {
	const agentClaude = "/w/repo/.worktrees/me/.tmp/agent-home/.claude/settings.json"
	const agentCreds = "/w/repo/.worktrees/me/.tmp/agent-home/.claude/.credentials.json"

	covered := compile(t, sweatfileio.FolioPolicy{
		Read: []string{"pwd:"},
		Deny: []string{"pwd:.tmp/agent-home/.claude/.credentials.json"},
	})
	for _, tc := range []struct{ path, verdict string }{
		{agentClaude, "allow"},
		{agentCreds, "deny"},
	} {
		if v, reason := covered.Evaluate(roots, "read", tc.path); v != tc.verdict {
			t.Errorf("%s = %s (%s), want %s", tc.path, v, reason, tc.verdict)
		}
	}

	// home: is the evaluating process's HOME, here the operator's: an entry
	// written as home:.claude does not reach the unit's agent-home.
	homeOnly := compile(t, sweatfileio.FolioPolicy{Read: []string{"home:.claude"}})
	if v, reason := homeOnly.Evaluate(roots, "read", "/home/u/.claude/settings.json"); v != "allow" {
		t.Errorf("operator home .claude = %s (%s), want allow", v, reason)
	}
	if v, reason := homeOnly.Evaluate(roots, "read", agentClaude); v != "ask" || reason != "unmatched" {
		t.Errorf("agent-home .claude under home: only = %s (%s), want ask (unmatched)", v, reason)
	}
}

func TestEvaluateUnmatchedIsThePolicys(t *testing.T) {
	p := compile(t, sweatfileio.FolioPolicy{Read: []string{"pwd:"}, Unmatched: "deny"})
	if v, reason := p.Evaluate(roots, "read", "/elsewhere"); v != "deny" || !strings.HasPrefix(reason, "unmatched") {
		t.Errorf("got %s (%s)", v, reason)
	}
	if v, _ := p.Evaluate(Roots{}, "read", "/w/repo/.worktrees/me/x"); v != "deny" {
		t.Errorf("empty roots match nothing, got %s", v)
	}
}
