package spawn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

func bridgeSpecJSON(t *testing.T, mode BridgeMode) string {
	t.Helper()
	spec := buildUnitSpec(unitSpecInput{
		rp:         worktree.ResolvedPath{AbsPath: "/w", SessionKey: "r/b"},
		runtimeDir: "/run/user/1",
		bridge:     mode,
	})
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUnitSpecOmitsBridgeWhenUnset(t *testing.T) {
	if got := bridgeSpecJSON(t, BridgeUnset); strings.Contains(got, `"bridge"`) {
		t.Errorf("bridge must be omitted when unset: an absent field is OFF: %s", got)
	}
}

func TestUnitSpecEmitsBridgeOfferedAndRequired(t *testing.T) {
	for mode, want := range map[BridgeMode]string{
		BridgeOffered:  `"bridge":"offered"`,
		BridgeRequired: `"bridge":"required"`,
	} {
		if got := bridgeSpecJSON(t, mode); !strings.Contains(got, want) {
			t.Errorf("mode %q: spec %s lacks %s", mode, got, want)
		}
	}
}

func TestBridgeModeRefusesAnyOtherValue(t *testing.T) {
	for _, ok := range []BridgeMode{BridgeUnset, BridgeOffered, BridgeRequired} {
		if err := ok.Validate(); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []BridgeMode{"on", "off", "Required", "required ", "true"} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// The operator's one knob, [confinement] bridge, maps to the spec field: unset
// and "off" are the same OFF (no field), "offered" and "required" are emitted
// as written, and anything else is refused by name rather than guessed.
func TestBridgeModeFromKnob(t *testing.T) {
	for knob, want := range map[string]BridgeMode{
		"":         BridgeUnset,
		"off":      BridgeUnset,
		"offered":  BridgeOffered,
		"required": BridgeRequired,
	} {
		got, err := BridgeModeFromKnob(knob)
		if err != nil || got != want {
			t.Errorf("knob %q: got (%q, %v), want (%q, nil)", knob, got, err, want)
		}
	}
	for _, bad := range []string{"on", "true", "Offered", "required ", "enable", "1"} {
		got, err := BridgeModeFromKnob(bad)
		if err == nil || !strings.Contains(err.Error(), `"`+bad+`"`) || !strings.Contains(err.Error(), "off") {
			t.Errorf("knob %q: got (%q, %v), want an error naming the value and the allowed ones", bad, got, err)
		}
	}
}

// writeUnitSpec carries the knob into the spec file clown reads: the field is
// in the written JSON for offered and required, and absent for off.
func TestWriteUnitSpecCarriesTheBridgeKnob(t *testing.T) {
	for _, tc := range []struct {
		mode      BridgeMode
		wantField bool
		wantValue string
	}{
		{BridgeUnset, false, ""},
		{BridgeOffered, true, "offered"},
		{BridgeRequired, true, "required"},
	} {
		t.Run(string(tc.mode)+"-"+tc.wantValue, func(t *testing.T) {
			state := t.TempDir()
			wt := filepath.Join(t.TempDir(), "repo", ".worktrees", "feat")
			common := filepath.Join(t.TempDir(), ".git")
			if err := os.MkdirAll(filepath.Join(common, "worktrees", "feat"), 0o755); err != nil {
				t.Fatal(err)
			}
			prev := gitCommonDir
			gitCommonDir = func(string) (string, error) { return common, nil }
			t.Cleanup(func() { gitCommonDir = prev })
			t.Setenv("SSH_AUTH_SOCK", "")
			rp := worktree.ResolvedPath{AbsPath: wt, SessionKey: "repo/feat"}

			path, err := writeUnitSpec(rp, "desc", nil, state, "", false, nil, nil, tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			if path != confinement.UnitSpecPath(state, wt) {
				t.Errorf("spec path = %q", path)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]any
			if err := json.Unmarshal(b, &raw); err != nil {
				t.Fatal(err)
			}
			got, present := raw["bridge"]
			if present != tc.wantField || (present && got != tc.wantValue) {
				t.Errorf("mode %q: written spec has bridge=%v (present=%v), want present=%v value=%q", tc.mode, got, present, tc.wantField, tc.wantValue)
			}
		})
	}
}
