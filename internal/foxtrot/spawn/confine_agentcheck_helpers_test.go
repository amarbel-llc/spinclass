package spawn

import (
	"encoding/json"
	"os"
	"testing"

	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// readSpecForTest reads the stored unit spec back for a test.
func readSpecForTest(t *testing.T, state, wt string) (unitSpec, []byte) {
	t.Helper()
	b, err := os.ReadFile(confinement.UnitSpecPath(state, wt))
	if err != nil {
		t.Fatal(err)
	}
	var s unitSpec
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s, b
}
