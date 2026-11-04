package apply

import (
	"slices"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

// DotenvPairs is the one rendering of [direnv.dotenv] both the .spinclass/env
// file and a spawned worker's launch environment use (spinclass#462): sorted
// KEY=value pairs with $WORKTREE expanded, nil when nothing is declared.
func TestDotenvPairsExpandsWorktreeAndSorts(t *testing.T) {
	sf := sweatfile.Sweatfile{Direnv: &sweatfile.Direnv{Dotenv: map[string]string{
		"PILOT_IPC": "$WORKTREE/.tmp/ipc.sock",
		"ALPHA":     "plain",
	}}}
	got := DotenvPairs(sf, "/w/.worktrees/x")
	want := []string{"ALPHA=plain", "PILOT_IPC=/w/.worktrees/x/.tmp/ipc.sock"}
	if !slices.Equal(got, want) {
		t.Errorf("DotenvPairs = %q, want %q", got, want)
	}
}

func TestDotenvPairsNilWhenNothingDeclared(t *testing.T) {
	if got := DotenvPairs(sweatfile.Sweatfile{}, "/w"); got != nil {
		t.Errorf("DotenvPairs with no [direnv] = %q, want nil", got)
	}
	if got := DotenvPairs(sweatfile.Sweatfile{Direnv: &sweatfile.Direnv{}}, "/w"); got != nil {
		t.Errorf("DotenvPairs with an empty dotenv = %q, want nil", got)
	}
}
