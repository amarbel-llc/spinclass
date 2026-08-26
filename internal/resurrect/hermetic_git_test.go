package resurrect

import (
	"os"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

// TestMain sandboxes $HOME once for the package. Run exercises
// worktree.Create's full applyWorktreeConfig path (same as internal/shop),
// which reaches claude.TrustWorkspace — that mkdirs under HOME and fails
// inside the nix sandbox where the default HOME (/homeless-shelter) is
// read-only. It also isolates every git invocation from the host git
// configuration (signing agent, hooks, templates) — see
// testgit.SetHermeticEnv.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "resurrect-test-home-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("HOME", home)
	gitCleanup, err := testgit.SetHermeticEnv()
	if err != nil {
		panic(err)
	}
	code := m.Run()
	gitCleanup()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
