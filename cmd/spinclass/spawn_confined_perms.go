package main

import (
	"os"

	"code.linenisgreat.com/spinclass/internal/charlie/perms"
	"code.linenisgreat.com/spinclass/internal/foxtrot/spawn"
)

// The spawn-session always-ask floor yields for a confined spawn (spinclass#382).
// perms cannot import spawn, so the verdict is installed here, once, for every
// process that evaluates the floor (the PreToolUse hook, perms check, serve).
func init() {
	perms.SpawnConfined = spawnWillConfine
	perms.SpawnUnattended = spawnWillBeUnattended
	perms.SpawnAllowJournal = journalSpawnAllowUse
}

// spawnWillBeUnattended is the narrowing carve-out (spinclass#476): a spawn
// whose unconfined worker will run in dontAsk, started by a driver that is not
// itself unattended (or is, and a trusted layer lists it). Every argument must parse cleanly and `confined` must not
// be set (a confined spawn is judged by spawnWillConfine); any doubt is false
// and keeps the prompt.
func spawnWillBeUnattended(toolInput map[string]any, cwd string) bool {
	repo := ""
	if v, ok := toolInput["repo"]; ok && v != nil {
		s, isString := v.(string)
		if !isString {
			return false
		}
		repo = s
	}
	if v, ok := toolInput["confined"]; ok && v != nil {
		if _, isBool := v.(bool); !isBool {
			return false
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	// An unattended driver's spawns ask, unless a trusted layer lists it
	// (TEMPORARY allow-list, spinclass#384 stopgap; see spawn_allowlist.go).
	if spawn.DriverUnattended() {
		if _, err := spawn.DriverMaySpawn(home, cwd, currentPrincipal()); err != nil {
			return false
		}
	}
	return spawn.WillBeUnattended(home, cwd, repo)
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
