package folio

import (
	"strings"
	"testing"
)

// The stanza grammar (docs/plans/2026-10-27-folio-stanza-design.md, section
// 2): <anchor>:<path-pattern>, a directory-boundary prefix, `*` for exactly
// one segment, an optional trailing /** which is also the default for a
// directory entry. No regexes, no `..` or `~`.
func TestParsePatternAcceptsTheGrammar(t *testing.T) {
	for _, tc := range []struct {
		in     string
		anchor Anchor
		arg    string
		segs   []string
		rec    bool
	}{
		{"pwd:", AnchorPwd, "", nil, true},
		{"nix-store:", AnchorNixStore, "", nil, true},
		{"home:.claude", AnchorHome, "", []string{".claude"}, true},
		{"home:.claude/plans/**", AnchorHome, "", []string{".claude", "plans"}, true},
		{"env:CLAUDE_CODE_TMPDIR:", AnchorEnv, "CLAUDE_CODE_TMPDIR", nil, true},
		{"env:XDG_STATE_HOME:circus", AnchorEnv, "XDG_STATE_HOME", []string{"circus"}, true},
		{"spinclass:worktree", AnchorSpinclass, "worktree", nil, true},
		{"spinclass:repo", AnchorSpinclass, "repo", nil, true},
		{"spinclass:worktrees", AnchorSpinclass, "worktrees", nil, true},
		{"spinclass:children", AnchorSpinclass, "children", nil, true},
		{"spinclass:siblings", AnchorSpinclass, "siblings", nil, true},
		{"spinclass:worktrees/*/.spinclass/state.json", AnchorSpinclass, "worktrees", []string{"*", ".spinclass", "state.json"}, true},
		{"spinclass:worktrees/*/.spinclass/spawn.log", AnchorSpinclass, "worktrees", []string{"*", ".spinclass", "spawn.log"}, true},
		// Ruled 2026-11-02 (option B): madder: is every blob; it has no path.
		{"madder:", AnchorMadder, "", nil, true},
	} {
		t.Run(tc.in, func(t *testing.T) {
			p, err := ParsePattern(tc.in)
			if err != nil {
				t.Fatalf("ParsePattern(%q): %v", tc.in, err)
			}
			if p.Anchor != tc.anchor || p.Arg != tc.arg || strings.Join(p.Segments, "/") != strings.Join(tc.segs, "/") {
				t.Errorf("ParsePattern(%q) = %+v, want anchor %q arg %q segments %v", tc.in, p, tc.anchor, tc.arg, tc.segs)
			}
			if p.String() != tc.in {
				t.Errorf("String() = %q, want the input back", p.String())
			}
		})
	}
}

func TestParsePatternRefusesWhatTheGrammarExcludes(t *testing.T) {
	for _, in := range []string{
		"",
		"pwd",                  // no anchor separator
		"/abs/path",            // no anchor
		"ftp:x",                // unknown anchor
		"env::x",               // empty variable name
		"env:NOT-A-NAME:x",     // invalid variable name
		"spinclass:",           // missing root
		"spinclass:other",      // unknown spinclass root
		"pwd:../x",             // .. inside
		"pwd:a/../b",           // .. inside
		"home:~/x",             // ~ inside
		"pwd:a/*b/c",           // * must be a whole segment
		"pwd:a/**/b",           // ** only trailing
		"pwd:a//b",             // empty segment
		"pwd:/a",               // leading slash: the anchor is the root
		"pwd:a/",               // trailing slash: say /** instead
		"pwd:a/**/**",          // ** only once
		"pwd:*/**/x",           // ** only trailing
		"spinclass:worktrees/", // trailing slash on the root
		"madder:store",         // the <store> refinement is reserved, not yet accepted
		"madder:blobs/x",       // a blob has no path
		"madder:/",             // nor a slash
		"madder:*",             // nor a wildcard
	} {
		t.Run(in, func(t *testing.T) {
			if p, err := ParsePattern(in); err == nil {
				t.Errorf("ParsePattern(%q) = %+v, want an error", in, p)
			}
		})
	}
}
