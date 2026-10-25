package git

import (
	"os"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

// TestMain keeps git's auto maintenance from detaching out of the fixtures'
// commits and merges — see testgit.DisableAutoMaintenance.
func TestMain(m *testing.M) {
	testgit.DisableAutoMaintenance()
	os.Exit(m.Run())
}
