package setupfingerprint

import (
	"testing"

	"code.linenisgreat.com/spinclass/internal/embeds"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

func cfg(excludes ...string) sweatfile.Sweatfile {
	if len(excludes) == 0 {
		return sweatfile.Sweatfile{}
	}
	return sweatfile.Sweatfile{Git: &sweatfile.Git{Excludes: excludes}}
}

func TestComputeDeterministic(t *testing.T) {
	a := cfg("a", "b")
	h1, s1 := Compute(a)
	h2, s2 := Compute(a)
	if h1 != h2 || s1 != s2 {
		t.Fatalf("Compute not deterministic: (%s,%d) != (%s,%d)", h1, s1, h2, s2)
	}
	if s1 != Scheme {
		t.Fatalf("scheme = %d, want %d", s1, Scheme)
	}
}

func TestComputeSensitiveToConfig(t *testing.T) {
	h1, _ := Compute(cfg("a"))
	h2, _ := Compute(cfg("a", "b"))
	if h1 == h2 {
		t.Fatal("Compute should differ when the config differs")
	}
}

func TestComputeSensitiveToVersion(t *testing.T) {
	t.Cleanup(func() { embeds.SetVersion("", "") })

	embeds.SetVersion("1.0.0", "aaaa")
	h1, _ := Compute(cfg("a"))
	embeds.SetVersion("2.0.0", "aaaa")
	h2, _ := Compute(cfg("a"))
	if h1 == h2 {
		t.Fatal("Compute should differ when the spinclass version differs")
	}

	embeds.SetVersion("2.0.0", "bbbb")
	h3, _ := Compute(cfg("a"))
	if h2 == h3 {
		t.Fatal("Compute should differ when the commit differs")
	}
}

func TestIsStale(t *testing.T) {
	merged := cfg("a", "b")
	hash, scheme := Compute(merged)

	if stale, _ := IsStale(hash, scheme, merged); stale {
		t.Fatal("matching fingerprint should not be stale")
	}
	if stale, reason := IsStale("", 0, merged); !stale || reason == "" {
		t.Fatal("empty recorded fingerprint should be stale with a reason")
	}
	if stale, _ := IsStale("deadbeef", scheme, merged); !stale {
		t.Fatal("mismatched hash should be stale")
	}
	if stale, _ := IsStale(hash, scheme+1, merged); !stale {
		t.Fatal("mismatched scheme should be stale")
	}
}
