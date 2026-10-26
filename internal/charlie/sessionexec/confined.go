package sessionexec

import (
	"fmt"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// refuseOnceConfined refuses an operator shell in a once-confined worktree
// (spinclass#400): it would run direnv and project tooling the worker could have
// planted. There is no override. An unknowable state directory counts as
// confined (fail closed).
func refuseOnceConfined(stateDir string, stateDirErr error, st *session.State) error {
	key := st.Key()
	if stateDirErr != nil {
		return fmt.Errorf("sc exec: cannot tell whether %s was launched confined (%v); refusing (spinclass#400): resume it with `sc resume %s` or close it with `sc close %s`", key, stateDirErr, key, key)
	}
	if !confinement.LaunchedConfined(stateDir, st.WorktreePath) {
		return nil
	}
	return fmt.Errorf("sc exec: %s was launched confined; an operator shell in its worktree would run direnv and project tooling the worker could have planted (spinclass#400): resume it with `sc resume %s` (which relaunches it confined through clown) or close it with `sc close %s`", key, key, key)
}

// checkNotOnceConfined applies refuseOnceConfined against the real state dir.
func checkNotOnceConfined(st *session.State) error {
	stateDir, err := confinement.DefaultStateDir()
	return refuseOnceConfined(stateDir, err, st)
}
