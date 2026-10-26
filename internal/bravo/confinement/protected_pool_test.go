package confinement

import "testing"

// The pre-minted token pool (FDR 0028 "Token pool") lives under the state base
// and must never be reachable from a unit: a bind of it, inside it, or of an
// ancestor is refused; its siblings (credentials, units) are not protected by
// this list.
func TestTokenPoolIsAProtectedStateDir(t *testing.T) {
	const state = "/home/op/.local/state"
	for _, bind := range []string{
		state + "/spinclass/token-pool",
		state + "/spinclass/token-pool/manifest.json",
		state + "/spinclass",
		state,
	} {
		if err := RefuseProtectedBinds(state, []string{bind}); err == nil {
			t.Errorf("bind %s was not refused", bind)
		}
	}
	for _, bind := range []string{
		state + "/spinclass/units",
		state + "/spinclass/token-pool-other",
	} {
		if err := RefuseProtectedBinds(state, []string{bind}); err != nil {
			t.Errorf("bind %s refused: %v", bind, err)
		}
	}
}
