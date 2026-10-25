package sweatfileio

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

func writeTransitionTTL(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[profile-rights]\ntransition-ttl = \""+value+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The transition window is read like the rest of [profile-rights]: only from
// layers above the repo, the nearest trusted one winning, 24h by default
// (#407). A repo-layer value is reported, never honoured.
func TestTrustedTransitionTTLHonoursOnlyLayersAboveRepo(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(home, ".config", "spinclass", "sweatfile")
	parent := filepath.Join(home, "work", "sweatfile")
	repoFile := filepath.Join(repo, "sweatfile")

	resolve := func() (time.Duration, []string, error) {
		t.Helper()
		h, err := LoadHierarchy(home, repo)
		if err != nil {
			t.Fatal(err)
		}
		return TrustedTransitionTTL(h, home, repo)
	}

	if ttl, ignored, err := resolve(); err != nil || ttl != sweatfile.DefaultTransitionTTL || ignored != nil {
		t.Fatalf("unset: got (%v, %v, %v), want the 24h default", ttl, ignored, err)
	}
	if sweatfile.DefaultTransitionTTL != 24*time.Hour {
		t.Errorf("DefaultTransitionTTL = %v, want 24h", sweatfile.DefaultTransitionTTL)
	}

	writeTransitionTTL(t, global, "48h")
	writeTransitionTTL(t, parent, "2h")
	writeTransitionTTL(t, repoFile, "168h")
	ttl, ignored, err := resolve()
	if err != nil || ttl != 2*time.Hour || !reflect.DeepEqual(ignored, []string{repoFile}) {
		t.Errorf("got (%v, %v, %v), want (2h, [%s], nil)", ttl, ignored, err, repoFile)
	}

	writeTransitionTTL(t, parent, "30s")
	if _, _, err := resolve(); err == nil || !strings.Contains(err.Error(), "transition-ttl") {
		t.Errorf("a trusted value below the floor must refuse naming the key, got %v", err)
	}
	writeTransitionTTL(t, parent, "200h")
	if _, _, err := resolve(); err == nil {
		t.Error("a trusted value above the 168h cap must refuse")
	}
}

func TestParseTransitionTTL(t *testing.T) {
	for _, ok := range []string{"1m", "24h", "168h"} {
		if _, err := sweatfile.ParseTransitionTTL(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "0", "59s", "169h", "-1h", "a day"} {
		if _, err := sweatfile.ParseTransitionTTL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
