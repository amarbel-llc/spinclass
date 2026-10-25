package basebranch

import (
	"os"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

// TestMain keeps git's auto maintenance from detaching out of the fixtures'
// fetches and pushes — see testgit.DisableAutoMaintenance.
func TestMain(m *testing.M) {
	testgit.DisableAutoMaintenance()
	os.Exit(m.Run())
}
