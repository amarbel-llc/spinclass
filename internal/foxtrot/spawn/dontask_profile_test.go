package spawn

import (
	"reflect"
	"strings"
	"testing"
)

// TestSpliceDontAskModeSeesThroughClownFlags pins the J2 canary failure: a
// profiled spawn-entry (--profile NAME selects the provider indirectly) must
// be recognised as claude when the profile's provider is claude, and its
// preexisting permission mode replaced, never duplicated.
func TestSpliceDontAskModeSeesThroughClownFlags(t *testing.T) {
	withResolver(t, &fakeResolver{profiles: map[string]ResolvedProfile{
		"canary-grit": {Name: "canary-grit", Provider: "claude"},
		"gpt-ish":     {Name: "gpt-ish", Provider: "codex"},
		"bare":        {Name: "bare"}, // no provider recorded: clown's default, claude
	}})
	dontAsk := []string{"--permission-mode", "dontAsk"}
	cases := []struct {
		name    string
		in      []string
		want    []string
		wantErr string
	}{
		{
			name: "plain claude",
			in:   []string{"clown", "--clown-attach=spawn", "--", "brief"},
			want: append(append([]string{"clown", "--clown-attach=spawn", "--"}, dontAsk...), "brief"),
		},
		{
			name: "the canary J2 entry",
			in:   []string{"direnv", "exec", "/wt", "clown", "--clown-attach=spawn", "--profile", "canary-grit", "--", "--model", "claude-sonnet-5-5", "--permission-mode", "auto", "brief"},
			want: append(append([]string{"direnv", "exec", "/wt", "clown", "--clown-attach=spawn", "--profile", "canary-grit", "--"}, dontAsk...), "--model", "claude-sonnet-5-5", "brief"),
		},
		{
			name: "--profile=NAME",
			in:   []string{"clown", "--profile=canary-grit", "--", "brief"},
			want: append(append([]string{"clown", "--profile=canary-grit", "--"}, dontAsk...), "brief"),
		},
		{
			name: "profile without a recorded provider defaults to claude",
			in:   []string{"clown", "--profile", "bare", "--", "brief"},
			want: append(append([]string{"clown", "--profile", "bare", "--"}, dontAsk...), "brief"),
		},
		{
			name: "preexisting dontAsk is not duplicated",
			in:   []string{"clown", "--", "--permission-mode", "dontAsk", "brief"},
			want: append(append([]string{"clown", "--"}, dontAsk...), "brief"),
		},
		{
			name: "preexisting acceptEdits is replaced",
			in:   []string{"clown", "--profile", "canary-grit", "--", "--permission-mode=acceptEdits", "brief"},
			want: append(append([]string{"clown", "--profile", "canary-grit", "--"}, dontAsk...), "brief"),
		},
		{
			name: "explicit claude provider beats a non-claude profile",
			in:   []string{"clown", "--profile", "gpt-ish", "--provider", "claude", "--", "brief"},
			want: append(append([]string{"clown", "--profile", "gpt-ish", "--provider", "claude", "--"}, dontAsk...), "brief"),
		},
		{
			name:    "explicit non-claude provider still refuses",
			in:      []string{"clown", "--provider", "codex", "--", "brief"},
			wantErr: "not claude",
		},
		{
			name:    "profile whose provider is not claude still refuses",
			in:      []string{"clown", "--profile", "gpt-ish", "--", "brief"},
			wantErr: "not claude",
		},
		{
			name:    "unresolvable profile fails closed",
			in:      []string{"clown", "--profile", "nope", "--", "brief"},
			wantErr: "nope",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := SpliceDontAskMode(c.in)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got  %v\nwant %v", got, c.want)
			}
		})
	}
}

// TestSpliceDontAskModeOtherBinaryStillRefuses: a spawn-entry whose launcher is
// not clown but names a non-claude provider keeps failing closed.
func TestSpliceDontAskModeOtherBinaryStillRefuses(t *testing.T) {
	_, err := SpliceDontAskMode([]string{"other-agent", "--provider=codex", "--", "brief"})
	if err == nil || !strings.Contains(err.Error(), "not claude") {
		t.Fatalf("got %v", err)
	}
}
