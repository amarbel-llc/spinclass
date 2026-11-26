package keyholder

import (
	"path/filepath"
	"strconv"
	"strings"
)

// gitProgramKey is the setting that marks a holder's block of GIT_CONFIG
// entries: its value is spinclass's own signing program, which nobody else
// names. holderGitKeys is that block in the order GitSigningEnv appends it,
// with gitProgramKey at gitProgramAt.
const (
	gitProgramKey = "gpg.ssh.program"
	gitProgramAt  = 3
)

var holderGitKeys = [...]string{"gpg.format", "user.signingkey", "commit.gpgsign", gitProgramKey, "gpg.ssh.allowedSignersFile"}

// IsGitConfigPair says whether name is one of the variables git reads settings
// from the environment with (not GIT_CONFIG_GLOBAL or GIT_CONFIG_NOSYSTEM,
// which name files).
func IsGitConfigPair(name string) bool {
	return name == "GIT_CONFIG_COUNT" || strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_")
}

// StripGitSigning returns environ without what a key holder launch added for
// git signing, for anything that builds a child's environment from its own:
// the block of GIT_CONFIG entries (recognised by its signing program, never by
// a key name alone, so a user's own gpg.format stays), with the other entries
// renumbered, and the two variables the signing program reads. An environ with
// none of it is returned as it is.
func StripGitSigning(environ []string) []string {
	have := map[string]string{}
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		have[k] = v
	}
	count, err := strconv.Atoi(have["GIT_CONFIG_COUNT"])
	if err != nil || count < 0 {
		count = 0
	}
	key := func(i int) string { return have["GIT_CONFIG_KEY_"+strconv.Itoa(i)] }
	value := func(i int) string { return have["GIT_CONFIG_VALUE_"+strconv.Itoa(i)] }

	ours := map[int]bool{}
	for i := range count {
		if key(i) != gitProgramKey || filepath.Base(value(i)) != gitSignProgramName {
			continue
		}
		first := i - gitProgramAt
		for n, want := range holderGitKeys {
			if at := first + n; at >= 0 && at < count && key(at) == want {
				ours[at] = true
			}
		}
	}
	_, hasSession := have[GitSessionEnv]
	_, hasAgent := have[GitAgentEnv]
	if len(ours) == 0 && !hasSession && !hasAgent {
		return environ
	}

	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if k == GitSessionEnv || k == GitAgentEnv || (len(ours) > 0 && IsGitConfigPair(k)) {
			continue
		}
		out = append(out, kv)
	}
	if len(ours) == 0 {
		return out
	}
	kept := 0
	for i := range count {
		if ours[i] {
			continue
		}
		n := strconv.Itoa(kept)
		out = append(out, "GIT_CONFIG_KEY_"+n+"="+key(i), "GIT_CONFIG_VALUE_"+n+"="+value(i))
		kept++
	}
	if kept > 0 {
		out = append(out, "GIT_CONFIG_COUNT="+strconv.Itoa(kept))
	}
	return out
}

// ignoredGitConfigNotice is said when a configured entry env or dotenv sets git
// settings for a key holder worker.
const ignoredGitConfigNotice = "spinclass: ignoring the GIT_CONFIG_* variables that [session-entry].env or [direnv.dotenv] set for this key holder worker (they would turn its commit signing off); put those settings in a git config file instead"

// DropConfiguredGitConfig removes from configured (a session's entry env, with
// its dotenv) the GIT_CONFIG pairs that would collide with the holder's own in
// kh, and returns the notice to print, "" when there was nothing to drop. With
// no signing in kh, configured is left alone.
func DropConfiguredGitConfig(configured, kh map[string]string) string {
	if kh[GitSessionEnv] == "" {
		return ""
	}
	notice := ""
	for k := range configured {
		if IsGitConfigPair(k) {
			delete(configured, k)
			notice = ignoredGitConfigNotice
		}
	}
	return notice
}
