package spawn

import (
	"os"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/sweatfileio"
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

const confinedSweatfile = "[confinement]\nenabled = true\n"

// willConfine is WillConfine with no explicit request: the default decision.
func willConfine(home, cwd, repo string) bool {
	return WillConfine(home, cwd, repo, ConfineDefault)
}

// The two switches and the request, end to end through the sweatfile layers:
// a spawn is confined iff enabled && (default-spawn || it asks for confinement),
// with `unconfined` opting out of the default only.
func TestWillConfineSwitches(t *testing.T) {
	const (
		enabledOnly  = "[confinement]\nenabled = true\n"
		optIn        = "[confinement]\nenabled = true\ndefault-spawn = false\n"
		defaultNoGat = "[confinement]\ndefault-spawn = true\n"
		legacy       = "[confinement]\nlaunch-unit = true\n"
	)
	cases := []struct {
		name  string
		body  string
		req   ConfineRequest
		wants bool
	}{
		{"feature off, default", "", ConfineDefault, false},
		{"feature off, confined requested", "", ConfineRequire, false},
		{"default-spawn without enabled is off", defaultNoGat, ConfineDefault, false},
		{"enabled defaults the spawn to confined", enabledOnly, ConfineDefault, true},
		{"enabled, opt out", enabledOnly, ConfineOptOut, false},
		{"enabled, confined requested", enabledOnly, ConfineRequire, true},
		{"opt-in mode: default is unconfined", optIn, ConfineDefault, false},
		{"opt-in mode: confined requested", optIn, ConfineRequire, true},
		{"opt-in mode: opt out is unconfined", optIn, ConfineOptOut, false},
		{"launch-unit alias is both switches", legacy, ConfineDefault, true},
		{"launch-unit alias, opt out", legacy, ConfineOptOut, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			driver := makeMainCheckout(t, filepath.Join(home, "work", "driver"))
			if tc.body != "" {
				writeSweatfileAt(t, filepath.Join(home, ".config", "spinclass", "sweatfile"), tc.body)
			}
			if got := WillConfine(home, driver, "", tc.req); got != tc.wants {
				t.Errorf("WillConfine = %v, want %v", got, tc.wants)
			}
		})
	}
}

// resolveConfined is the one decision LaunchDetached and WillConfine share; a
// confined request while the feature is off is an error, not a silent downgrade.
func TestResolveConfined(t *testing.T) {
	on := sweatfileio.ConfinementPolicy{Enabled: true, DefaultSpawn: true}
	optIn := sweatfileio.ConfinementPolicy{Enabled: true, DefaultSpawn: false}
	off := sweatfileio.ConfinementPolicy{}

	got, err := resolveConfined(optIn, on, ConfineDefault)
	if err != nil || !got {
		t.Errorf("a confined-by-default driver context confines the child: (%v, %v)", got, err)
	}
	if got, err := resolveConfined(optIn, off, ConfineDefault); err != nil || got {
		t.Errorf("opt-in target, no driver default: (%v, %v), want unconfined", got, err)
	}
	if got, err := resolveConfined(off, on, ConfineDefault); err != nil || !got {
		t.Errorf("the feature is on if either context enables it: (%v, %v)", got, err)
	}
	if _, err := resolveConfined(off, off, ConfineRequire); err == nil {
		t.Error("requesting confinement with the feature off must be an error")
	}
	if got, err := resolveConfined(off, off, ConfineOptOut); err != nil || got {
		t.Errorf("opting out with the feature off is just unconfined: (%v, %v)", got, err)
	}
}

func TestParseConfineRequest(t *testing.T) {
	for _, tc := range []struct {
		confined, unconfined bool
		want                 ConfineRequest
		wantErr              bool
	}{
		{false, false, ConfineDefault, false},
		{true, false, ConfineRequire, false},
		{false, true, ConfineOptOut, false},
		{true, true, ConfineDefault, true},
	} {
		got, err := ParseConfineRequest(tc.confined, tc.unconfined)
		if (err != nil) != tc.wantErr || (err == nil && got != tc.want) {
			t.Errorf("ParseConfineRequest(%v, %v) = (%v, %v), want (%v, err=%v)",
				tc.confined, tc.unconfined, got, err, tc.want, tc.wantErr)
		}
	}
}

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
		if willConfine(home, inDriver, target) {
			t.Error("an unconfigured spawn must not be treated as confined")
		}
	})

	t.Run("target repo layer is not trusted", func(t *testing.T) {
		writeSweatfileAt(t, filepath.Join(target, "sweatfile"), confinedSweatfile)
		defer os.Remove(filepath.Join(target, "sweatfile"))
		if willConfine(home, inDriver, target) {
			t.Error("a repo-layer launch-unit is ignored by the spawn and must be ignored here")
		}
	})

	t.Run("trusted layer confines an explicit target", func(t *testing.T) {
		writeSweatfileAt(t, globalSweatfile, confinedSweatfile)
		defer os.Remove(globalSweatfile)
		if !willConfine(home, inDriver, target) {
			t.Error("a global launch-unit confines every spawn")
		}
	})

	t.Run("omitted repo means the driver's own repo", func(t *testing.T) {
		writeSweatfileAt(t, filepath.Join(home, "work", "sweatfile"), confinedSweatfile)
		defer os.Remove(filepath.Join(home, "work", "sweatfile"))
		if !willConfine(home, inDriver, "") {
			t.Error("an omitted repo should resolve to the driver's repo and be confined by its parent layer")
		}
	})

	t.Run("driver context confines a sibling target", func(t *testing.T) {
		writeSweatfileAt(t, filepath.Join(home, "work", "sweatfile"), confinedSweatfile)
		defer os.Remove(filepath.Join(home, "work", "sweatfile"))
		if !willConfine(home, inDriver, target) {
			t.Error("LaunchDetached ORs the driver's context into confinement; the permission must agree")
		}
	})

	t.Run("an unresolvable target asks", func(t *testing.T) {
		writeSweatfileAt(t, globalSweatfile, confinedSweatfile)
		defer os.Remove(globalSweatfile)
		if willConfine(home, inDriver, filepath.Join(home, "nope", "missing")) {
			t.Error("a target the spawn would refuse must fall back to ask, not allow")
		}
	})

	t.Run("omitted repo outside any repo asks", func(t *testing.T) {
		writeSweatfileAt(t, globalSweatfile, confinedSweatfile)
		defer os.Remove(globalSweatfile)
		if willConfine(home, t.TempDir(), "") {
			t.Error("no repo to resolve means the spawn errors; that must not be allowed silently")
		}
	})
}
