package check

import (
	"os"
	"testing"

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
	code := m.Run()
	cleanup()
	os.Exit(code)
}
