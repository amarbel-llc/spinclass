package keyholder

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// A holder worker's git signs with its holder's key without a flag. The
// settings travel in the worker's ENVIRONMENT (git's GIT_CONFIG_COUNT pairs),
// never in a git config file: only the worker's own processes get them, so the
// operator's git in the same worktree (a merge from his terminal, which
// rebases) keeps signing as he configured it, and there is nothing to undo
// when the holder goes.

const (
	// GitSessionEnv and GitAgentEnv are what the signing program reads: the
	// session it names when signing fails, and the socket it signs with whatever
	// SSH_AUTH_SOCK its caller was given.
	GitSessionEnv = "SPINCLASS_KEYHOLDER_SESSION"
	GitAgentEnv   = "SPINCLASS_KEYHOLDER_AGENT"
	// gitBaseEnv is how many GIT_CONFIG entries the environment had before the
	// holder's own: a holder worker that launches another one replaces its
	// entries instead of stacking them.
	gitBaseEnv = "SPINCLASS_KEYHOLDER_GIT_BASE"

	gitSignProgramName = "keyholder-git-sign"
	allowedSignersName = "allowed_signers"
)

// GitSignProgram is the program a holder worker's git runs to sign. It sits
// beside the key holder state root, not in a pair's directory: it must still be
// there to say what is wrong after a pair is gone.
func GitSignProgram(base string) string {
	return filepath.Join(filepath.Dir(base), gitSignProgramName)
}

// AllowedSignersPath is the pair's allowed-signers file, which makes
// `git verify-commit` work inside the worker. It goes with the pair.
func AllowedSignersPath(pairDir string) string { return filepath.Join(pairDir, allowedSignersName) }

// gitSignScript runs the pinned ssh-keygen. A failed signature says which key
// holder did not sign and what to type, where git would show only
// ssh-keygen's words.
func gitSignScript(keygen string) string {
	quoted := "'" + strings.ReplaceAll(keygen, "'", `'\''`) + "'"
	return `#!/bin/sh
# Written by spinclass: how a key holder worker's git signs.
keygen=` + quoted + `
if [ "${1:-}" = "-Y" ] && [ "${2:-}" = "sign" ]; then
	SSH_AUTH_SOCK="${` + GitAgentEnv + `:-}" "$keygen" "$@" && exit 0
	s="${` + GitSessionEnv + `:-}"
	printf '%s\n' \
		"spinclass: the key holder of session $s did not sign (agent socket ${` + GitAgentEnv + `:-unset}; ssh-keygen's own words are above)." \
		"  next: at a terminal run 'sc session-key $s'. If it shows no holder, run 'sc keyholder-stop $s', then 'sc resume $s'." \
		"  to commit unsigned this once: git -c commit.gpgsign=false commit" >&2
	exit 1
fi
exec "$keygen" "$@"
`
}

// GitSigningEnv writes the signing program and the pair's allowed-signers file,
// and returns the variables that make git in a worker sign with the pair's key.
// environ is the launching process's environment: the GIT_CONFIG entries it
// already carries are kept (all returned, so a hand-off to another process has
// them too). otherSigners is the content of an allowed-signers file the
// operator's git already names, kept so those signatures still verify inside
// the worker.
func GitSigningEnv(base, sessionKey, keygen string, pair Pair, environ []string, otherSigners []byte) (map[string]string, error) {
	fields := strings.Fields(pair.PublicKey)
	if len(fields) < 2 {
		return nil, errors.New("keyholder: the pair has no public key to sign commits with")
	}
	key := fields[0] + " " + fields[1]

	program := GitSignProgram(base)
	if err := writeFileAtomic(program, []byte(gitSignScript(keygen)), 0o700); err != nil {
		return nil, fmt.Errorf("keyholder: writing the git signing program: %w", err)
	}
	signers := []byte("* namespaces=\"git\" " + key + "\n")
	signers = append(signers, otherSigners...)
	allowed := AllowedSignersPath(pair.Dir)
	if err := writeFileAtomic(allowed, signers, 0o600); err != nil {
		return nil, fmt.Errorf("keyholder: writing the allowed signers: %w", err)
	}

	have := map[string]string{}
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		have[k] = v
	}
	count, err := strconv.Atoi(have["GIT_CONFIG_COUNT"])
	if err != nil || count < 0 {
		count = 0
	}
	if inherited, err := strconv.Atoi(have[gitBaseEnv]); err == nil && inherited >= 0 && inherited < count {
		count = inherited
	}

	out := map[string]string{
		GitSessionEnv: sessionKey,
		GitAgentEnv:   pair.AgentSock,
		gitBaseEnv:    strconv.Itoa(count),
	}
	for i := range count {
		for _, name := range []string{"GIT_CONFIG_KEY_" + strconv.Itoa(i), "GIT_CONFIG_VALUE_" + strconv.Itoa(i)} {
			out[name] = have[name]
		}
	}
	for _, kv := range [][2]string{
		{"gpg.format", "ssh"},
		{"user.signingkey", "key::" + key},
		{"commit.gpgsign", "true"},
		{"gpg.ssh.program", program},
		{"gpg.ssh.allowedSignersFile", allowed},
	} {
		out["GIT_CONFIG_KEY_"+strconv.Itoa(count)] = kv[0]
		out["GIT_CONFIG_VALUE_"+strconv.Itoa(count)] = kv[1]
		count++
	}
	out["GIT_CONFIG_COUNT"] = strconv.Itoa(count)
	return out, nil
}

// writeFileAtomic replaces path with data at exactly mode, by a rename in its
// directory.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Chmod(mode)
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	return os.Rename(f.Name(), path)
}
