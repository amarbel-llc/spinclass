package spawn

import (
	"encoding/json"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
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
		t.Errorf("bridge must be omitted when unset, so clown's default (offered) applies: %s", got)
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
