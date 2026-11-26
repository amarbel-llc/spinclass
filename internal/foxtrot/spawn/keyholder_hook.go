package spawn

import (
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// KeyholderEnv, when set (cmd/spinclass sets it), returns the environment that
// points a new worker at its own key holder in place of the operator's agent,
// starting the pair if the [keyholder] switch is on; nil when it is off. An
// error refuses the spawn: a worker that was meant to run on its own key must
// not launch on the operator's. undo is non-nil only when THIS call created the
// pair; the spawn calls it if the launch then fails, since no session will exist
// to close the pair.
var KeyholderEnv func(home, repoPath, sessionKey string) (env map[string]string, undo func(), err error)

// KeyholderPreflight, when set (cmd/spinclass sets it), refuses a spawn whose
// key holder cannot start, before anything is created. nil error when the
// [keyholder] switch is off.
var KeyholderPreflight func(home, repoPath, sessionKey string) error

// holderNotice is where a key holder worker's ignored settings are said.
var holderNotice io.Writer = os.Stderr

// dotenvForHolderWorker drops the dotenv's GIT_CONFIG pairs when kh carries the
// worker's commit signing: appended after it, they would turn signing off.
func dotenvForHolderWorker(dotenv []string, kh map[string]string) []string {
	pairs := map[string]string{}
	for _, kv := range dotenv {
		k, v, _ := strings.Cut(kv, "=")
		pairs[k] = v
	}
	notice := keyholder.DropConfiguredGitConfig(pairs, kh)
	if notice == "" {
		return dotenv
	}
	fmt.Fprintln(holderNotice, notice)
	return slices.DeleteFunc(slices.Clone(dotenv), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return keyholder.IsGitConfigPair(k)
	})
}

// withKeyholderEnv returns sessionEnv with kh laid over it, every other
// variable that names the operator's agent emptied, including one a configured
// entry env set, and a configured GIT_CONFIG pair dropped with a notice;
// sessionEnv itself is never modified.
func withKeyholderEnv(sessionEnv, kh map[string]string) map[string]string {
	if len(kh) == 0 {
		return sessionEnv
	}
	out := make(map[string]string, len(sessionEnv)+len(kh))
	maps.Copy(out, sessionEnv)
	if notice := keyholder.DropConfiguredGitConfig(out, kh); notice != "" {
		fmt.Fprintln(holderNotice, notice)
	}
	for k := range out {
		if keyholder.IsOperatorAgentVar(k) {
			out[k] = ""
		}
	}
	maps.Copy(out, kh)
	return out
}
