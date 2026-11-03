package check

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/clown"
	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/bravo/hookrun"
)

// The guarded gate's lock policy (spinclass#449, review findings F1, F1' and
// F1''). The nix evaluator runs as the operator uid with the whole filesystem
// readable, and a flake input it fetches is copied into /nix/store,
// world-readable and mounted into every unit, before the lock's narHash is
// compared. A worker-committed lock naming `path:/home/<operator>/...` with a
// wrong hash leaks that path through one failed merge; so does an input
// flake.nix declares that the lock lacks, and so does an input the lock holds
// under the same NAME but with a different reference (a remote node named
// `leak`, flake.nix redeclaring `leak` as a path, or a nested override
// `inputs.sub.inputs.leak`), since nix re-fetches any input whose declared
// reference differs from the lock node's `original` while computing the lock
// it then refuses to write (all shown by `just
// explore-guarded-gate-input-leak`). A guarded gate therefore refuses, before
// anything runs, a lock with any node that points at a local path, and a
// lock whose node graph does not match every input flake.nix declares,
// reference for reference, follows and nested overrides included. Everything
// is read from the committed tree at HEAD of the build worktree, never
// through the working tree, so a committed symlink, directory or gitlink is
// what it is, not whatever it points at. Policy, not a boundary: the boundary
// is evaluation in a mount namespace that sees only the build worktree
// (spinclass#448).

// flakeLock is the part of flake.lock the policy reads.
type flakeLock struct {
	Nodes map[string]lockNode `json:"nodes"`
	Root  string              `json:"root"`
}

// lockNode is one node: its inputs (a node id, or a follows path as an array),
// its flake flag (absent means true), and the reference it was locked from.
type lockNode struct {
	Inputs   map[string]json.RawMessage `json:"inputs"`
	Flake    *bool                      `json:"flake"`
	Locked   map[string]any             `json:"locked"`
	Original map[string]any             `json:"original"`
}

// inputSpec is one input as flake.nix declares it, in the shape
// declaredInputsApply has nix itself produce: Original is the reference's
// attribute form, which is what `nix flake lock` writes as a node's
// `original` (the url form goes through builtins.parseFlakeRef, the attribute
// form is the attributes themselves, no reference at all is the indirect
// `flake:<name>`); Follows is set instead of Original for a follows; Inputs
// are the nested overrides.
type inputSpec struct {
	Original map[string]any       `json:"original"`
	Flake    bool                 `json:"flake"`
	Follows  *string              `json:"follows"`
	Inputs   map[string]inputSpec `json:"inputs"`
}

// refuseUnsafeFlakeLock judges the committed flake.nix and flake.lock at HEAD
// of dir (the build worktree, detached at the pinned sha): no flake.nix means
// nothing to judge; a flake.nix or flake.lock that is not a regular file in
// the tree (a symlink, a directory, a gitlink) is refused unread; a missing
// or unparseable lock is refused; a lock node of type `path`, or any URL with
// a file scheme, is refused; and a declared input (declaredFlakeInputs, under
// the gate's environment with forbidden as its worker-written directories)
// that the lock lacks, locks under a different reference or flake flag, or
// whose declared reference is itself local, is refused, nested overrides and
// follows included. Refusals name lock nodes, input paths and reference
// kinds only, sanitised, never a path, a URL or a hash.
func refuseUnsafeFlakeLock(ctx context.Context, dir string, forbidden []string) error {
	nix, foundNix, err := committedRegularFile(dir, "flake.nix")
	if err != nil {
		return errors.New("a guarded gate needs flake.nix to be a regular file in the committed tree, and it could not be read as one; nothing ran")
	}
	if !foundNix {
		return nil
	}
	raw, foundLock, err := committedRegularFile(dir, "flake.lock")
	if err != nil {
		return errors.New("a guarded gate needs flake.lock to be a regular file in the committed tree, and it could not be read as one; nothing ran")
	}
	if !foundLock {
		return errors.New("a guarded gate needs a committed flake.lock beside flake.nix, and there is none; nothing ran")
	}
	var lock flakeLock
	if err := json.Unmarshal(raw, &lock); err != nil {
		return errors.New("a guarded gate could not parse flake.lock as a lock; nothing ran")
	}
	rootName := lock.Root
	if rootName == "" {
		rootName = "root"
	}
	var bad []string
	for name, node := range lock.Nodes {
		if name == rootName {
			continue
		}
		if reason := refusedRef(node.Locked, false); reason != "" {
			bad = append(bad, safeName(name)+": locked "+reason)
		} else if reason := refusedRef(node.Original, true); reason != "" {
			bad = append(bad, safeName(name)+": original "+reason)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return errors.New("the flake.lock names a flake input the guarded gate does not accept (one on the local filesystem, which the evaluator would copy into the world-readable /nix/store as the operator before the gate could refuse it, or a kind outside the allow-list); a guarded merge accepts only github, gitlab and sourcehut inputs and tarball, file and git inputs over https; nothing ran\n  " + strings.Join(capList(bad), "\n  "))
	}
	declared, err := declaredFlakeInputs(ctx, dir, nix, forbidden)
	if err != nil {
		return fmt.Errorf("a guarded gate could not read the inputs flake.nix declares, so it cannot tell whether the lock matches them; nothing ran: %w", err)
	}
	if problems := lockMismatches(lock, rootName, nil, declared); len(problems) > 0 {
		return errors.New("flake.nix declares flake inputs the flake.lock does not hold under the same reference, which nix would fetch, as the operator and into the world-readable /nix/store, while computing the lock it then refuses to write; a guarded merge needs a lock that matches every declared input, follows and nested overrides included; nothing ran\n  " + strings.Join(capList(problems), "\n  "))
	}
	return nil
}

// committedRegularFile is git.FileAtRev at HEAD, with an entry that exists
// but is not a regular file (a directory, a gitlink) an error like a symlink
// rather than "absent": the policy must not pass on a flake.nix it did not
// read (the third review of spinclass#449, A).
func committedRegularFile(dir, path string) (data []byte, found bool, err error) {
	data, found, err = git.FileAtRev(dir, "HEAD", path)
	if err != nil || found {
		return data, found, err
	}
	mode, err := git.ModeAtRev(dir, "HEAD", path)
	if err != nil {
		return nil, false, err
	}
	if mode != "" {
		return nil, false, fmt.Errorf("%s at HEAD is not a regular file (mode %s)", path, mode)
	}
	return nil, false, nil
}

// lockMismatches walks the declared inputs against the lock's node graph from
// node id, prefix being the input path above them, and lists what differs:
// an input the node lacks, a follows that is not the lock's follows path, a
// reference that is not the node's `original` (nix re-fetches on any
// difference), a declared reference that is itself local, a flake flag that
// differs, and the same one level down for every nested override. Labels are
// input paths, sanitised segment by segment.
func lockMismatches(lock flakeLock, nodeID string, prefix []string, declared map[string]inputSpec) []string {
	var out []string
	node, nodeOK := lock.Nodes[nodeID]
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec := declared[name]
		path := append(append([]string(nil), prefix...), name)
		label := inputLabel(path)
		entry, present := node.Inputs[name]
		if !nodeOK || !present {
			out = append(out, label+": missing from the lock")
			continue
		}
		if spec.Follows != nil {
			var follows []string
			if json.Unmarshal(entry, &follows) != nil || strings.Join(follows, "/") != *spec.Follows {
				out = append(out, label+": the lock does not record the declared follows")
			}
			if len(spec.Inputs) > 0 {
				out = append(out, label+": overrides under a follows")
			}
			continue
		}
		var childID string
		if json.Unmarshal(entry, &childID) != nil {
			out = append(out, label+": locked as a follows, declared as a reference")
			continue
		}
		child, ok := lock.Nodes[childID]
		if !ok {
			out = append(out, label+": the lock names a node it lacks")
			continue
		}
		if spec.Original == nil {
			out = append(out, label+": declared without a reference")
			continue
		}
		if reason := refusedRef(spec.Original, true); reason != "" {
			out = append(out, label+": declared "+reason)
		}
		if !reflect.DeepEqual(spec.Original, child.Original) {
			out = append(out, label+": declared "+refKind(spec.Original)+", locked "+refKind(child.Original)+"; the references differ")
		}
		lockedFlake := child.Flake == nil || *child.Flake
		if lockedFlake != spec.Flake {
			out = append(out, label+": the flake flag differs from the lock")
		}
		out = append(out, lockMismatches(lock, childID, path, spec.Inputs)...)
	}
	return out
}

func inputLabel(path []string) string {
	parts := make([]string, len(path))
	for i, p := range path {
		parts[i] = safeName(p)
	}
	return strings.Join(parts, "/")
}

// refKind names a reference by its type only.
func refKind(ref map[string]any) string {
	typ, _ := ref["type"].(string)
	if typ == "" {
		return "an untyped reference"
	}
	return "type " + safeName(typ)
}

// refusedRef says why a reference (a lock node's locked or original
// attributes, or a declared input's canonical form) is not one the gate
// accepts, or "". An allow-list, not a deny-list (the fourth review of
// spinclass#449): accepted are the forge types github, gitlab and sourcehut
// without a host attribute (a host could name a local service), and
// tarball, file and git with an https:// url; a declared reference may also
// be indirect (a registry id, which the operator's registry resolves; a lock
// never records one as locked). Everything else refuses: path, mercurial, a
// git or tarball over ssh, file or http, a scheme-less url, a path attribute
// on any type, and a type nix adds later. It names the kind, never a path,
// a host or a URL.
func refusedRef(ref map[string]any, declared bool) string {
	typ, _ := ref["type"].(string)
	kind := "type " + safeName(typ)
	if _, has := ref["path"]; has {
		return kind + " with a path attribute"
	}
	switch typ {
	case "github", "gitlab", "sourcehut":
		if _, has := ref["host"]; has {
			return kind + " with a host attribute"
		}
		return ""
	case "tarball", "file", "git":
		url, _ := ref["url"].(string)
		if !strings.HasPrefix(strings.ToLower(url), "https://") {
			return kind + " without an https url"
		}
		return ""
	case "indirect":
		if declared {
			return ""
		}
		return kind + " as a locked reference"
	}
	return kind + " is not an accepted input type (github, gitlab, sourcehut, or tarball, file and git over https)"
}

// safeName reduces a worker-chosen name to [A-Za-z0-9_-], at most 64 runes,
// so nothing the worker wrote (an escape sequence, an environment value an
// evaluation echoed) reaches the merge record or a terminal as it stands.
func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= 64 {
			b.WriteString("...")
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('?')
		}
	}
	if b.Len() == 0 {
		return "?"
	}
	return b.String()
}

// capList keeps a refusal short: at most 20 entries, then a count.
func capList(items []string) []string {
	if len(items) <= 20 {
		return items
	}
	return append(items[:20], fmt.Sprintf("and %d more", len(items)-20))
}

// declaredFlakeInputs reads the inputs the given flake.nix content declares,
// evaluated in dir. A seam: tests stand in a map, since the sandbox has no
// nix.
var declaredFlakeInputs = declaredFlakeInputsViaNix

// declaredInputsApply is the --apply expression that has nix canonicalise
// every declared input into an inputSpec: the attribute form of its
// reference exactly as `nix flake lock` records a node's `original` (the url
// form through builtins.parseFlakeRef, the same parser the lock went
// through; a relative path, a string spec, and a url mixed with other
// attributes throw, which refuses the merge), the flake flag defaulted, the
// follows, and the nested overrides recursively. The recipe
// `explore-guarded-gate-inputs-eval` carries a copy as `canon` and runs it
// against a real lock: keep the two in step.
const declaredInputsApply = `f: let
  canon = name: spec:
    if !(builtins.isAttrs spec) then throw "input ${name} is not an attribute set"
    else let rest = removeAttrs spec [ "inputs" "flake" "follows" ]; in {
      original =
        if spec ? follows then (if rest == { } then null else throw "input ${name} mixes follows with a reference")
        else if spec ? type then rest
        else if spec ? url then (if builtins.attrNames rest == [ "url" ] then builtins.parseFlakeRef spec.url else throw "input ${name} mixes url with other attributes")
        else { type = "indirect"; id = name; };
      flake = spec.flake or true;
      follows = spec.follows or null;
      inputs = builtins.mapAttrs canon (spec.inputs or { });
    };
  in builtins.mapAttrs canon (f.inputs or { })`

// declaredFlakeInputsViaNix evaluates flake.nix's inputs with nix under the
// gate's own scrubbed environment (forbidden as the worker-written
// directories, the same list the gate's command gets, so the whole-PATH check
// and the checked nix apply here too). The new CLI's `nix eval` is pure by
// default and then refuses to import any file, store paths included (shown by
// `just explore-guarded-gate-inputs-eval`), so the evaluation is --impure
// with restrict-eval, and the committed content is handed to nix on stdin
// (`-f -`): no temporary file anywhere a worker could race, and no store copy.
// Restricted mode refuses every path (the working directory included, so a
// relative path literal in flake.nix fails) and, with no allowed URI, every
// fetcher even with a hash. getEnv sees the scrubbed environment;
// currentSystem is the host's. No import from derivation; max-call-depth
// bounds a recursion; the output is capped; the merge's ctx and a timeout
// bound the time. The evaluation runs only inside a transient systemd scope
// with MemoryMax, which bounds memory and is stopped on cancellation: no
// scope id, no scope tier, a systemd-run that fails the gate's tool check,
// or a scope that cannot be set up each refuse the evaluation, and with it
// the merge; there is no unscoped fallback (the third review of
// spinclass#449, D). Any failure is the caller's refusal, and the evaluator's
// message, which quotes the expression, never leaves here.
func declaredFlakeInputsViaNix(ctx context.Context, dir string, flakeNix []byte, forbidden []string) (map[string]inputSpec, error) {
	env, err := hookrun.ConfinedGateEnv(dir, forbidden)
	if err != nil {
		return nil, err
	}
	scopeID := clown.ScopeIDFromContext(ctx)
	if scopeID == "" {
		return nil, errors.New("the inputs evaluation runs only in a merge or check context that names a scope")
	}
	scopeID += "-inputs"
	prefix, ok := clown.ScopeArgv(scopeID)
	if !ok {
		return nil, errors.New("the systemd scope tier is unavailable on this host, and the inputs evaluation runs only inside a scope that bounds its memory")
	}
	if len(prefix) < 2 || prefix[len(prefix)-1] != "--" {
		return nil, errors.New("the scope prefix has an unexpected shape")
	}
	systemdRun, err := hookrun.ResolveGateTool(prefix[0], forbidden)
	if err != nil {
		return nil, err
	}
	nix, err := hookrun.ResolveGateTool("nix", forbidden)
	if err != nil {
		return nil, err
	}
	argv := append([]string{systemdRun}, prefix[1:len(prefix)-1]...)
	argv = append(argv,
		"--property=MemoryMax="+declaredInputsMemoryMax, "--",
		nix, "eval", "--json",
		"--impure",
		"--option", "restrict-eval", "true",
		"--option", "allowed-uris", "",
		"--option", "allow-import-from-derivation", "false",
		"--option", "max-call-depth", "2000",
		"-f", "-",
		"--apply", declaredInputsApply,
	)
	ctx, cancel := context.WithTimeout(ctx, declaredInputsTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(flakeNix)
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.New("evaluating flake.nix's inputs could not start")
	}
	if err := cmd.Start(); err != nil {
		return nil, errors.New("evaluating flake.nix's inputs could not start")
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, declaredInputsMaxOutput+1))
	waitErr := cmd.Wait()
	if ctx.Err() != nil || waitErr != nil {
		// A killed systemd-run can leave the evaluator in its scope.
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = clown.ScopeStop(stopCtx, scopeID)
		stopCancel()
	}
	if readErr != nil || waitErr != nil {
		return nil, errors.New("evaluating flake.nix's inputs failed")
	}
	if int64(len(out)) > declaredInputsMaxOutput {
		return nil, errors.New("flake.nix declares more inputs than a lock could hold")
	}
	var specs map[string]inputSpec
	if err := json.Unmarshal(out, &specs); err != nil {
		return nil, errors.New("evaluating flake.nix's inputs gave no input set")
	}
	return specs, nil
}

// declaredInputsTimeout bounds the inputs evaluation: a flake.nix whose
// inputs attribute set takes longer than this to force is refused. A
// variable so a test can shorten it.
var declaredInputsTimeout = 60 * time.Second

// declaredInputsMaxOutput caps the input set: a flake.nix that computes
// millions of inputs is refused rather than read.
const declaredInputsMaxOutput = 1 << 20

// declaredInputsMemoryMax is the scope's memory bound for the evaluation.
const declaredInputsMemoryMax = "2G"
