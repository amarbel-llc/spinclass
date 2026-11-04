package spawn

import (
	"strings"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
)

// stripDirenvExec drops the exact `direnv exec <wtPath>` prefix from a rendered
// spawn-entry argv (spinclass#462): a confined worker gowns at the unit door
// with the environment spinclass and clown state, never the devShell or the
// .envrc chain direnv would load landside. Only that one shape is recognised,
// as elements, never by shell parsing; anything else is returned unchanged
// (sc validate warns about a direnv entry of another shape). The second
// result says whether a prefix was dropped.
func stripDirenvExec(argv []string, wtPath string) ([]string, bool) {
	if len(argv) > 3 && argv[0] == "direnv" && argv[1] == "exec" && argv[2] == wtPath {
		return argv[3:], true
	}
	return argv, false
}

// confinedSpecEnv is spinclass's whole contribution to a confined unit's
// environment (the spec's `env`, FDR 0035 "Interface with clown"): the rendered
// [direnv.dotenv] pairs, then the tmp dirs, then the identity variables, later
// entries winning, so the identity is authoritative over a dotenv entry of the
// same name. With the dotenv carried here the unit needs no `.envrc` to get
// what a trusted layer put in [direnv.dotenv].
func confinedSpecEnv(rp worktree.ResolvedPath, desc string, dotenv []string) map[string]string {
	env := map[string]string{}
	for _, kv := range dotenv {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	for _, kv := range session.TmpDirEnv(rp.AbsPath) {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	for _, kv := range identityVars(rp, desc) {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	return env
}
