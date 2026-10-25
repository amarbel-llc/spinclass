// Package remote routes "host:"-prefixed session targets to sweatfile-declared
// remote hosts: target grammar parsing and attach-argv construction over the
// [[remotes]] config (sweatfile.Remote). Prefix routing happens at the CLI
// boundary — no replicated index, no daemon. See
// docs/plans/2026-06-06-remote-sessions-design.md.
package remote

import (
	"fmt"
	"regexp"
	"strings"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

// CheckDest refuses a remote whose ssh destination begins with "-": ssh would
// read it as an option, and some options (-oProxyCommand, -oLocalCommand) run
// a command on this machine.
func CheckDest(r sweatfile.Remote) error {
	if dest := r.Dest(); strings.HasPrefix(dest, "-") {
		return fmt.Errorf("remote %q: ssh destination %q begins with \"-\", which ssh would read as an option; refusing to run ssh (set `ssh` to a host name or user@host)", r.Name, dest)
	}
	return nil
}

// targetPattern is the host:-prefix grammar: a non-empty prefix without ':'
// or '/' (so paths like .worktrees/x and a/b:c never route remotely),
// followed by a non-empty session id which may itself contain colons.
var targetPattern = regexp.MustCompile(`^([^:/]+):(.+)$`)

// defaultAttach is the attach argv template used when a remote declares no
// `attach` of its own; the remote spinclass owns sweatfile/entrypoint
// semantics from there. The "--" ends ssh's option parsing before the
// destination.
var defaultAttach = []string{"ssh", "-t", "--", "{ssh}", "spinclass", "resume", "{id}"}

// ParseTarget splits a session target into its host prefix and remote
// session id. ok is false for plain local targets, empty components, and
// prefixes containing '/' — those resolve locally as today.
func ParseTarget(target string) (host, id string, ok bool) {
	m := targetPattern.FindStringSubmatch(target)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// AttachArgv builds the argv that reattaches session id on remote r:
// r.Attach when declared, otherwise the default ssh template. {ssh} and
// {id} are replaced literally in every element (the exec-start {arg}
// mechanism); the result is exec'd directly, no shell. A destination CheckDest
// refuses yields no argv.
func AttachArgv(r sweatfile.Remote, id string) ([]string, error) {
	if err := CheckDest(r); err != nil {
		return nil, err
	}
	template := r.Attach
	if len(template) == 0 {
		template = defaultAttach
	}
	argv := make([]string, len(template))
	for i, el := range template {
		el = strings.ReplaceAll(el, "{ssh}", r.Dest())
		el = strings.ReplaceAll(el, "{id}", id)
		argv[i] = el
	}
	return argv, nil
}
