package spawn

import (
	"slices"
	"testing"
)

func TestSubstituteEntry(t *testing.T) {
	tests := []struct {
		name   string
		entry  []string
		brief  string
		wtPath string
		want   []string
	}{
		{
			name:  "prompt placeholder replaced",
			entry: []string{"clown", "{prompt}"},
			brief: "fix the thing",
			want:  []string{"clown", "fix the thing"},
		},
		{
			name:   "dir placeholder replaced",
			entry:  []string{"harness", "--cwd", "{dir}", "{prompt}"},
			brief:  "go",
			wtPath: "/work/tree",
			want:   []string{"harness", "--cwd", "/work/tree", "go"},
		},
		{
			name:   "placeholder embedded within an element",
			entry:  []string{"sh", "-c", "cd {dir} && run '{prompt}'"},
			brief:  "do it",
			wtPath: "/wt",
			want:   []string{"sh", "-c", "cd /wt && run 'do it'"},
		},
		{
			name:  "brief with spaces and quotes stays one element",
			entry: []string{"clown", "{prompt}"},
			brief: `fix the "weird" thing; rm -rf $HOME`,
			want:  []string{"clown", `fix the "weird" thing; rm -rf $HOME`},
		},
		{
			// {dir} must be substituted BEFORE {prompt}: a brief that
			// happens to contain the literal text "{dir}" must survive
			// verbatim, not get the worktree path injected.
			name:   "brief containing literal {dir} survives verbatim",
			entry:  []string{"clown", "--cwd", "{dir}", "{prompt}"},
			brief:  "the template uses {dir} — fix its docs",
			wtPath: "/work/tree",
			want:   []string{"clown", "--cwd", "/work/tree", "the template uses {dir} — fix its docs"},
		},
		{
			name:  "nil entry stays nil-length",
			entry: nil,
			brief: "x",
			want:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SubstituteEntry(tt.entry, tt.brief, tt.wtPath)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d elements %q, want %d %q", len(got), got, len(tt.want), tt.want)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSubstituteWindow(t *testing.T) {
	t.Run("id and dir substituted in every element", func(t *testing.T) {
		got := SubstituteWindow([]string{"sc-spawn-window", "{id}", "{dir}", "x={id}"},
			"repo/feat", "/wt")
		want := []string{"sc-spawn-window", "repo/feat", "/wt", "x=repo/feat"}
		if !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("nil renders nil", func(t *testing.T) {
		if got := SubstituteWindow(nil, "id", "/wt"); got != nil {
			t.Errorf("got %q, want nil", got)
		}
	})
}
