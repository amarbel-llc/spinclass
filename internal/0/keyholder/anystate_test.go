package keyholder

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasAnyStateIsNoForAMissingOrEmptyBaseAndYesForAnyEntry(t *testing.T) {
	base := filepath.Join(t.TempDir(), "kh")
	if has, err := HasAnyState(base); has || err != nil {
		t.Fatalf("missing base = (%v, %v), want (false, nil)", has, err)
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if has, err := HasAnyState(base); has || err != nil {
		t.Fatalf("empty base = (%v, %v), want (false, nil)", has, err)
	}
	for _, name := range []string{"0123456789abcdef", "0123456789abcdef.lock", "0123456789abcdef.kind"} {
		p := filepath.Join(base, name)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if has, err := HasAnyState(base); !has || err != nil {
			t.Errorf("base with %s = (%v, %v), want (true, nil)", name, has, err)
		}
		_ = os.Remove(p)
	}
}

func TestHasAnyStateIsYesWithTheErrorWhenTheBaseCannotBeRead(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if has, err := HasAnyState(notADir); !has || err == nil {
		t.Fatalf("an unreadable base = (%v, %v), want (true, an error): not knowing is not evidence of absence", has, err)
	}
}
