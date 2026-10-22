package spawn

import (
	"path/filepath"
	"testing"
)

// TestClownBuiltinTaskTemplatesResolve pins that the task templates clown ships
// as builtins resolve through the same path spawn-session uses
// (profileResolver -> clownResolver -> profilelookup.Resolve, then the
// required-server check), against the real linked library with no user
// registry. It fails when the linked clown predates those builtins.
func TestClownBuiltinTaskTemplatesResolve(t *testing.T) {
	prev := profileResolver
	t.Cleanup(func() { profileResolver = prev })
	profileResolver = clownResolver{
		lookup:       newClownResolver().lookup,
		registryPath: filepath.Join(t.TempDir(), "absent-profiles.toml"),
	}

	for _, name := range []string{"reviewer", "porter", "canary", "canary-grit"} {
		t.Run(name, func(t *testing.T) {
			rp, err := resolveProfile(name)
			if err != nil {
				t.Fatalf("resolveProfile(%q): %v", name, err)
			}
			if rp.Name != name {
				t.Errorf("Name = %q, want %q", rp.Name, name)
			}
			// A confined spawn of these must pass SpliceDontAskMode.
			if _, err := SpliceDontAskMode([]string{"clown", "--clown-attach=spawn", "--profile", name, "--", "--permission-mode", "auto", "brief"}); err != nil {
				t.Errorf("SpliceDontAskMode with profile %q: %v", name, err)
			}
			if rp.Digest == "" {
				t.Error("empty digest")
			}
			if rp.KeptServers == nil {
				t.Error("template should save a server selection, so the required-server check runs")
			}
		})
	}
}
