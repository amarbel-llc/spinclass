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

// spawnWillConfine reads the `repo` argument of a spawn-session call and asks
// spawn.WillConfine, which mirrors the launch's own decision. A missing home,
// or a repo argument that is not a string, is doubt and keeps the prompt.
func spawnWillConfine(toolInput map[string]any, cwd string) bool {
	repo := ""
	if v, ok := toolInput["repo"]; ok && v != nil {
		s, isString := v.(string)
		if !isString {
			return false
		}
		repo = s
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return spawn.WillConfine(home, cwd, repo)
}
