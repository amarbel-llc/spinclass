package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// MergeDriver is a custom merge driver a rebase would invoke: a `merge=<Name>`
// attribute (or git's `merge.default`) on a path the replay three-way merges,
// bound to the `merge.<Name>.driver` command git would run for it.
type MergeDriver struct {
	Name    string
	Command string
	// Paths are the replayed paths bound to the driver.
	Paths []string
}

// Argv0 is the program git's `sh -c` would exec for the driver: the command's
// first word. Empty when there is none or when it needs shell parsing this
// does not do (quotes, escapes, an env assignment), which a caller cannot
// judge and must leave to git.
func (d MergeDriver) Argv0() string {
	fields := strings.Fields(d.Command)
	if len(fields) == 0 || strings.ContainsAny(fields[0], `"'=\`) {
		return ""
	}
	return fields[0]
}

// Resolves reports whether the driver's program would resolve when git runs
// it from dir (the worktree top, git's cwd for a driver): a path containing a
// separator is looked up relative to dir, a bare word on this process's PATH —
// the PATH git inherits, and so the one it hands the driver. A command whose
// program cannot be judged resolves, so a pre-flight never refuses what it
// cannot see.
func (d MergeDriver) Resolves(dir string) bool {
	argv0 := d.Argv0()
	switch {
	case argv0 == "":
		return true
	case strings.Contains(argv0, "/"):
		p := argv0
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		info, err := os.Stat(p)
		return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
	}
	_, err := exec.LookPath(argv0)
	return err == nil || errors.Is(err, exec.ErrDot)
}

// Unresolved is the refusal for a driver that does not resolve from dir —
// what is missing, what it is bound to, and the remedy — or "" when it does.
func (d MergeDriver) Unresolved(dir string) string {
	if d.Resolves(dir) {
		return ""
	}
	return fmt.Sprintf("merge driver `%s` → `%s` not on PATH (bound to %s); install it or unset merge.%s.driver",
		d.Name, d.Argv0(), strings.Join(d.Paths, ", "), d.Name)
}

// builtinMergeDrivers are the `merge=` attribute values git handles itself;
// they never consult merge.<name>.driver.
var builtinMergeDrivers = map[string]bool{
	"unspecified": true, "unset": true, "set": true,
	"text": true, "binary": true, "union": true,
}

// MergeDriversInPlay resolves every custom merge driver a rebase of dir's
// HEAD onto theirs would invoke, sorted by name: a `merge=` attribute — or,
// for a path with none, git's `merge.default` — bound through
// `merge.<name>.driver` to a path git will three-way merge. Those are the
// paths changed on theirs since the merge base (the replay's checked-out
// side) that some commit of HEAD also touched — per commit, since the replay
// runs commit by commit and a later revert does not spare an earlier edit.
// Attributes are read from theirs' TREE (`check-attr --source`), the
// .gitattributes in effect while the replay's HEAD sits on theirs, not the
// session tree's; config is read from dir, so worktree-scoped config applies
// exactly as it would to the rebase itself.
//
// A driver that is bound but absent is worse than none (spinclass#324): git
// records the path as conflicted with the "ours" content and NO markers, so an
// agent resolving by "stage what's there" silently drops the other side's
// change. Callers pre-flight Resolves/Unresolved before starting the rebase.
//
// The common case — no driver configured at all — costs one subprocess.
// Histories with no merge base have nothing to pre-flight (the rebase itself
// reports that); such a pair yields no drivers and no error.
func MergeDriversInPlay(dir, theirs string) ([]MergeDriver, error) {
	commands, defaultName := mergeDriverConfig(dir)
	if len(commands) == 0 {
		return nil, nil
	}
	base, err := Run(dir, "merge-base", "HEAD", theirs)
	if err != nil {
		return nil, nil
	}
	paths, err := replayedPaths(dir, base, theirs)
	if err != nil || len(paths) == 0 {
		return nil, err
	}
	attrs, err := checkAttrMerge(dir, theirs, paths)
	if err != nil {
		return nil, err
	}

	byName := map[string]*MergeDriver{}
	for _, pa := range attrs {
		name := pa.value
		if name == "unspecified" && defaultName != "" {
			name = defaultName
		}
		cmd, bound := commands[name]
		if builtinMergeDrivers[name] || !bound {
			// git handles builtins itself, and falls back to its default
			// merge for an unbound name — with markers either way.
			continue
		}
		d, seen := byName[name]
		if !seen {
			d = &MergeDriver{Name: name, Command: cmd}
			byName[name] = d
		}
		d.Paths = append(d.Paths, pa.path)
	}

	out := make([]MergeDriver, 0, len(byName))
	for _, d := range byName {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// mergeDriverConfig reads dir's merge.* config in one call: the driver
// commands by name (merge.<name>.driver) and merge.default, "" when unset.
func mergeDriverConfig(dir string) (commands map[string]string, defaultName string) {
	// --get-regexp exits 1 with no output when nothing matches.
	out, _ := Run(dir, "config", "--get-regexp", `^merge\.`)
	commands = map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		key, value, _ := strings.Cut(line, " ")
		switch {
		case key == "merge.default":
			defaultName = value
		case strings.HasPrefix(key, "merge.") && strings.HasSuffix(key, ".driver"):
			commands[strings.TrimSuffix(strings.TrimPrefix(key, "merge."), ".driver")] = value
		}
	}
	return commands, defaultName
}

// replayedPaths is the sorted set of paths the replay of base..HEAD onto
// theirs can three-way merge: changed on theirs since base AND touched by at
// least one commit of HEAD.
func replayedPaths(dir, base, theirs string) ([]string, error) {
	theirsChanged, err := nulPaths(RunStdin(dir, "", "diff", "--name-only", "-z", base, theirs))
	if err != nil {
		return nil, err
	}
	if len(theirsChanged) == 0 {
		return nil, nil
	}
	oursTouched, err := nulPaths(RunStdin(dir, "", "log", "--format=", "--name-only", "-z", base+"..HEAD"))
	if err != nil {
		return nil, err
	}
	var paths []string
	for p := range oursTouched {
		if theirsChanged[p] {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

type pathAttr struct{ path, value string }

// checkAttrMerge reads each path's `merge` attribute as git resolves it in
// the tree at source (`check-attr --source`, git ≥ 2.40), fed over stdin;
// `-z` emits path, attribute, value triples.
func checkAttrMerge(dir, source string, paths []string) ([]pathAttr, error) {
	stdin := strings.Join(paths, "\x00") + "\x00"
	out, err := RunStdin(dir, stdin, "check-attr", "-z", "--stdin", "--source="+source, "merge")
	if err != nil {
		return nil, err
	}
	fields := strings.Split(string(out), "\x00")
	var attrs []pathAttr
	for i := 0; i+2 < len(fields); i += 3 {
		attrs = append(attrs, pathAttr{path: fields[i], value: fields[i+2]})
	}
	return attrs, nil
}

// nulPaths splits NUL-delimited git output into a path set, passing err
// through so a RunStdin call can feed it directly.
func nulPaths(out []byte, err error) (map[string]bool, error) {
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			paths[string(p)] = true
		}
	}
	return paths, nil
}
