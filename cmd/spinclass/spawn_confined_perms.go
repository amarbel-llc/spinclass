package main

import (
	"os"

	"code.linenisgreat.com/spinclass/internal/perms"
	"code.linenisgreat.com/spinclass/internal/spawn"
)

// The spawn-session always-ask floor yields for a confined spawn (spinclass#382).
// perms cannot import spawn, so the verdict is installed here, once, for every
// process that evaluates the floor (the PreToolUse hook, perms check, serve).
func init() {
	perms.SpawnConfined = spawnWillConfine
}

// spawnWillConfine reads the `repo` and `confined` arguments of a spawn-session
// call and asks spawn.WillConfine, which mirrors the launch's own decision. A
// missing home, a repo that is not a string, or a confined that is not a bool
// is doubt and keeps the prompt. (`unconfined` is judged by the floor itself,
// before this is consulted.)
func spawnWillConfine(toolInput map[string]any, cwd string) bool {
	repo := ""
	if v, ok := toolInput["repo"]; ok && v != nil {
		s, isString := v.(string)
		if !isString {
			return false
		}
		repo = s
	}
	req := spawn.ConfineDefault
	if v, ok := toolInput["confined"]; ok && v != nil {
		b, isBool := v.(bool)
		if !isBool {
			return false
		}
		if b {
			req = spawn.ConfineRequire
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return spawn.WillConfine(home, cwd, repo, req)
}
