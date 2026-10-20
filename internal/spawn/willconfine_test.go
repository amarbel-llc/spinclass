package spawn

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSweatfileAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func makeMainCheckout(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

const confinedSweatfile = "[confinement]\nlaunch-unit = true\n"

// WillConfine is the permission layer's mirror of LaunchDetached's confinement
// decision (spinclass#382): true only when the spawn really would launch a
// unit, and false on every doubt so the always-ask floor still applies.
func TestWillConfine(t *testing.T) {
	home := t.TempDir()
	globalSweatfile := filepath.Join(home, ".config", "spinclass", "sweatfile")
	driver := makeMainCheckout(t, filepath.Join(home, "work", "driver"))
	target := makeMainCheckout(t, filepath.Join(home, "other", "target"))
	inDriver := filepath.Join(driver, "sub")
	if err := os.MkdirAll(inDriver, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("no confinement configured asks", func(t *testing.T) {
		if WillConfine(home, inDriver, target) {
			t.Error("an unconfigured spawn must not be treated as confined")
		}
	})

	t.Run("target repo layer is not trusted", func(t *testing.T) {
		writeSweatfileAt(t, filepath.Join(target, "sweatfile"), confinedSweatfile)
		defer os.Remove(filepath.Join(target, "sweatfile"))
		if WillConfine(home, inDriver, target) {
			t.Error("a repo-layer launch-unit is ignored by the spawn and must be ignored here")
		}
	})

	t.Run("trusted layer confines an explicit target", func(t *testing.T) {
		writeSweatfileAt(t, globalSweatfile, confinedSweatfile)
		defer os.Remove(globalSweatfile)
		if !WillConfine(home, inDriver, target) {
			t.Error("a global launch-unit confines every spawn")
		}
	})

	t.Run("omitted repo means the driver's own repo", func(t *testing.T) {
		writeSweatfileAt(t, filepath.Join(home, "work", "sweatfile"), confinedSweatfile)
		defer os.Remove(filepath.Join(home, "work", "sweatfile"))
		if !WillConfine(home, inDriver, "") {
			t.Error("an omitted repo should resolve to the driver's repo and be confined by its parent layer")
		}
	})

	t.Run("driver context confines a sibling target", func(t *testing.T) {
		writeSweatfileAt(t, filepath.Join(home, "work", "sweatfile"), confinedSweatfile)
		defer os.Remove(filepath.Join(home, "work", "sweatfile"))
		if !WillConfine(home, inDriver, target) {
			t.Error("LaunchDetached ORs the driver's context into confinement; the permission must agree")
		}
	})

	t.Run("an unresolvable target asks", func(t *testing.T) {
		writeSweatfileAt(t, globalSweatfile, confinedSweatfile)
		defer os.Remove(globalSweatfile)
		if WillConfine(home, inDriver, filepath.Join(home, "nope", "missing")) {
			t.Error("a target the spawn would refuse must fall back to ask, not allow")
		}
	})

	t.Run("omitted repo outside any repo asks", func(t *testing.T) {
		writeSweatfileAt(t, globalSweatfile, confinedSweatfile)
		defer os.Remove(globalSweatfile)
		if WillConfine(home, t.TempDir(), "") {
			t.Error("no repo to resolve means the spawn errors; that must not be allowed silently")
		}
	})
}
