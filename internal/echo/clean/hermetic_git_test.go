package clean

import (
	"os"
	"testing"

	"code.linenisgreat.com/spinclass/internal/bravo/keyholdersweep"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// TestMain isolates every git invocation in this package's tests from the
// host git configuration (signing agent, hooks, templates) — see
// testgit.SetHermeticEnv.
func TestMain(m *testing.M) {
	cleanup, err := testgit.SetHermeticEnv()
	if err != nil {
		panic(err)
	}
	// No test of this package may look at, let alone stop, the host's real key
	// holder pairs: the sweep is stubbed unless a test installs its own.
	findKeyholderOrphans = func() []keyholdersweep.Pair { return nil }
	stopKeyholderOrphan = func(keyholdersweep.Pair) error { return nil }
	code := m.Run()
	cleanup()
	os.Exit(code)
}
