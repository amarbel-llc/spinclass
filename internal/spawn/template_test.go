package spawn

import (
	"slices"
	"strings"
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

func TestSubstituteSpawn(t *testing.T) {
	zmxDefault := []string{"zmx", "run", "{id}", "--", "{entry}"}

	t.Run("zmx default with clown entry", func(t *testing.T) {
		entry := SubstituteEntry([]string{"clown", "{prompt}"}, "fix the thing", "/wt")
		got, err := SubstituteSpawn(zmxDefault, "wt-name", "/wt", entry)
		if err != nil {
			t.Fatalf("SubstituteSpawn: %v", err)
		}
		want := []string{"zmx", "run", "wt-name", "--", "clown", "fix the thing"}
		if !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
		// The brief must remain a single argv element — no shell joining.
		if len(got) != 6 {
			t.Errorf("got %d elements, want 6: %q", len(got), got)
		}
	})

	t.Run("dir substituted in spawn template", func(t *testing.T) {
		tpl := []string{"tmux", "new-session", "-d", "-c", "{dir}", "-s", "{id}", "{entry}"}
		got, err := SubstituteSpawn(tpl, "sess", "/the/worktree", []string{"true"})
		if err != nil {
			t.Fatalf("SubstituteSpawn: %v", err)
		}
		want := []string{"tmux", "new-session", "-d", "-c", "/the/worktree", "-s", "sess", "true"}
		if !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("multiple elements after splice preserved in order", func(t *testing.T) {
		tpl := []string{"wrapper", "{entry}", "--detach", "{id}"}
		got, err := SubstituteSpawn(tpl, "name", "/wt", []string{"clown", "-p", "brief text"})
		if err != nil {
			t.Fatalf("SubstituteSpawn: %v", err)
		}
		want := []string{"wrapper", "clown", "-p", "brief text", "--detach", "name"}
		if !slices.Equal(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("empty entry errors naming the knob", func(t *testing.T) {
		_, err := SubstituteSpawn(zmxDefault, "id", "/wt", nil)
		if err == nil {
			t.Fatal("expected error for empty entry, got nil")
		}
		if !strings.Contains(err.Error(), "[session-entry].spawn-entry") {
			t.Errorf("error %q does not name [session-entry].spawn-entry", err)
		}
	})

	t.Run("missing {entry} element errors", func(t *testing.T) {
		_, err := SubstituteSpawn([]string{"zmx", "run", "{id}"}, "id", "/wt", []string{"clown"})
		if err == nil {
			t.Fatal("expected error for missing {entry}, got nil")
		}
		if !strings.Contains(err.Error(), "{entry}") {
			t.Errorf("error %q does not mention {entry}", err)
		}
	})

	t.Run("embedded {entry} is not a splice point", func(t *testing.T) {
		// Only an element exactly equal to "{entry}" splices; an embedded
		// occurrence cannot be expanded element-wise, so it errors.
		_, err := SubstituteSpawn([]string{"sh", "-c", "exec {entry}"}, "id", "/wt", []string{"clown"})
		if err == nil {
			t.Fatal("expected error for embedded {entry}, got nil")
		}
	})
}
