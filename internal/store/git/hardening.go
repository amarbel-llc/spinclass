package git

import (
	"path/filepath"
	"strings"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/store"
)

// confinedHardening is how the frontend runs git for a confined caller (FDR
// 0036 section 9.5). The session worktree is writable from the confined unit,
// and the frontend's git runs with that worktree as its working directory, so
// git must neither discover the repository from it nor run any command the
// repository's configuration names.
//
// What it does:
//   - Given a Repo from session state, GIT_DIR, GIT_COMMON_DIR and
//     GIT_WORK_TREE are set explicitly. Git then reads neither the worktree's
//     .git pointer file nor the gitdir's commondir file, both of which the unit
//     can rewrite to point at a repository it controls.
//   - GIT_CONFIG_NOSYSTEM is set. The frontend's own global config is kept: it
//     is the operator's file (not writable from the unit) and carries the
//     commit identity, signing and gpg settings a commit needs.
//   - -c overrides neutralise the config keys that run a command from a single
//     fixed name: core.fsmonitor, core.sshCommand, core.pager, core.editor,
//     protocol.ext.allow, core.attributesFile.
//   - credential.helper is reset and then set to a get-only reader of the
//     session credential file (getOnlyCredentialHelper), so a remote verb
//     still authenticates as before and no other helper runs. With no minted
//     credential no helper runs and the verb fails with git's own
//     authentication error, as it already did.
//   - Pagers, editors, system attributes and optional index locks are turned
//     off through the environment.
//
// What it cannot do: git has no way to unset an arbitrary config NAME, so the
// per-name command hooks (filter.<name>.clean/smudge/process, diff.<name>.
// textconv and .command, merge.<name>.driver, credential.<url>.helper,
// alias.*) cannot be neutralised by an override. They are closed by where the
// config lives instead: the repository config and the worktree's config.worktree
// are read-only in the unit (FDR 0035), the system config is dropped here and
// the global config is the operator's. diff and the shelf's diffs also pass
// --no-ext-diff --no-textconv. Aliases cannot shadow the builtin verbs this
// backend runs.
// getOnlyCredentialHelper answers git's get from the credential file and
// ignores store and erase. A plain `store --file` helper rewrites the file
// after every successful push (lockfile and rename); the unit's
// InaccessiblePaths mount sits on the old inode, so a rewrite from the frontend
// would leave the new, readable file in the unit's view (round K, FDR 0035).
func getOnlyCredentialHelper(credFile string) string {
	quoted := "'" + strings.ReplaceAll(credFile, "'", `'\''`) + "'"
	return `!f() { test "$1" = get && exec git credential-store --file=` + quoted + ` get; }; f`
}

func confinedHardening(s store.Session) (git.Hardening, bool) {
	if !s.Confinement.Confined() {
		return git.Hardening{}, false
	}
	h := git.Hardening{
		Env: []string{
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_ATTR_NOSYSTEM=1",
			"GIT_OPTIONAL_LOCKS=0",
			"GIT_PAGER=cat",
			"GIT_EDITOR=true",
		},
		Args: []string{
			"-c", "core.fsmonitor=false",
			"-c", "core.sshCommand=",
			"-c", "core.pager=cat",
			"-c", "core.editor=true",
			"-c", "protocol.ext.allow=never",
			"-c", "core.attributesFile=/dev/null",
			"-c", "diff.external=",
			"-c", "credential.helper=",
		},
	}
	if s.Repo.CredentialFile != "" {
		h.Args = append(h.Args, "-c", "credential.helper="+getOnlyCredentialHelper(s.Repo.CredentialFile))
	}
	if s.Dir != "" {
		h.Env = append(h.Env, "GIT_CEILING_DIRECTORIES="+filepath.Dir(s.Dir))
	}
	if s.Repo.GitDir != "" && s.Repo.CommonDir != "" && s.Dir != "" {
		h.Env = append(h.Env,
			"GIT_DIR="+s.Repo.GitDir,
			"GIT_COMMON_DIR="+s.Repo.CommonDir,
			"GIT_WORK_TREE="+s.Dir,
		)
	}
	return h, true
}

// confineGit registers confinedHardening for the run and returns the release.
// The explicit repository covers only the session worktree: a dir outside it
// (a repo_path the ownership checks are about to refuse) gets the neutralisers
// alone, never the session's repository, so those checks still see the path
// they were asked about.
func confineGit(s store.Session, dir string) (release func()) {
	h, ok := confinedHardening(s)
	if !ok {
		return func() {}
	}
	var releases []func()
	if s.Dir != "" {
		releases = append(releases, git.Confine(s.Dir, h))
	}
	if s.Dir == "" || !withinDir(s.Dir, absoluteUnder(s.Dir, dir)) {
		bare := s
		bare.Repo.GitDir, bare.Repo.CommonDir = "", ""
		hb, _ := confinedHardening(bare)
		releases = append(releases, git.Confine(dir, hb))
	}
	return func() {
		for _, r := range releases {
			r()
		}
	}
}

// withoutExternalDiffForConfined keeps a confined caller's diff from running a
// diff driver (the flags are redundant with the -c overrides on purpose).
func withoutExternalDiffForConfined(s store.Session, argv []string) []string {
	if !s.Confinement.Confined() || len(argv) == 0 || argv[0] != "diff" {
		return argv
	}
	return append([]string{"diff", "--no-ext-diff", "--no-textconv"}, argv[1:]...)
}
