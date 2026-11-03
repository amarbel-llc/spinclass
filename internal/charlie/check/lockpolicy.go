package check

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/clown"
	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/bravo/hookrun"
)

// The guarded gate's lock policy (spinclass#449, review findings F1 and F1').
// The nix evaluator runs as the operator uid with the whole filesystem
// readable, and a flake input it fetches is copied into /nix/store,
// world-readable and mounted into every unit, before the lock's narHash is
// compared. A worker-committed lock naming `path:/home/<operator>/...` with a
// wrong hash leaks that path through one failed merge, and so does an input
// flake.nix declares that the lock lacks, since nix fetches it while computing
// the lock it then refuses to write (shown by `just
// explore-guarded-gate-input-leak`). A guarded gate refuses, before anything
// runs, a lock with any node that points at a local path, and a lock that
// lacks an input flake.nix declares. Everything is read from the committed
// tree at HEAD of the build worktree, never through the working tree, so a
// committed symlink is a symlink, not whatever it points at. Policy, not a
// boundary: the boundary is evaluation in a mount namespace that sees only
// the build worktree (spinclass#448).

// flakeLock is the part of flake.lock the policy reads.
type flakeLock struct {
	Nodes map[string]struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
		Locked struct {
			Type string `json:"type"`
			URL  string `json:"url"`
			Path string `json:"path"`
		} `json:"locked"`
		Original struct {
			Type string `json:"type"`
			URL  string `json:"url"`
			Path string `json:"path"`
		} `json:"original"`
	} `json:"nodes"`
	Root string `json:"root"`
}

// refuseUnsafeFlakeLock judges the committed flake.nix and flake.lock at HEAD
// of dir (the build worktree, detached at the pinned sha): no flake.nix means
// nothing to judge; a flake.nix or flake.lock that is not a regular file in
// the tree (a symlink) is refused unread; a missing or unparseable lock is
// refused; a lock node of type `path`, or any URL with a file scheme, is
// refused; and an input flake.nix declares (declaredFlakeInputs, under the
// gate's environment with forbidden as its worker-written directories) that
// the lock's root lacks is refused. Refusals name lock nodes and input names
// only, sanitised, never a path, a URL or a hash.
func refuseUnsafeFlakeLock(ctx context.Context, dir string, forbidden []string) error {
	nix, foundNix, err := git.FileAtRev(dir, "HEAD", "flake.nix")
	if err != nil {
		return errors.New("a guarded gate needs flake.nix to be a regular file in the committed tree, and it could not be read as one; nothing ran")
	}
	if !foundNix {
		return nil
	}
	raw, foundLock, err := git.FileAtRev(dir, "HEAD", "flake.lock")
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
	var bad []string
	for name, node := range lock.Nodes {
		if name == lock.Root || name == "root" {
			continue
		}
		for _, ref := range []struct{ typ, url, path string }{
			{node.Locked.Type, node.Locked.URL, node.Locked.Path},
			{node.Original.Type, node.Original.URL, node.Original.Path},
		} {
			if reason := localPathInput(ref.typ, ref.url, ref.path); reason != "" {
				bad = append(bad, safeName(name)+": "+reason)
				break
			}
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return errors.New("the flake.lock names a flake input on the local filesystem, which the evaluator would copy into the world-readable /nix/store as the operator before the gate could refuse it; a guarded merge accepts only remote inputs; nothing ran\n  " + strings.Join(capList(bad), "\n  "))
	}
	declared, err := declaredFlakeInputs(ctx, nix, forbidden)
	if err != nil {
		return fmt.Errorf("a guarded gate could not list the inputs flake.nix declares, so it cannot tell whether the lock is complete; nothing ran: %w", err)
	}
	rootName := lock.Root
	if rootName == "" {
		rootName = "root"
	}
	var missing []string
	for _, name := range declared {
		if _, ok := lock.Nodes[rootName].Inputs[name]; !ok {
			missing = append(missing, safeName(name))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return errors.New("flake.nix declares flake input(s) the flake.lock lacks, which nix would fetch, as the operator and into the world-readable /nix/store, while computing the lock it then refuses to write; a guarded merge needs a complete lock; nothing ran\n  missing: " + strings.Join(capList(missing), ", "))
	}
	return nil
}

// localPathInput says why an input reference is a local path, or "". It
// names the kind, never the path or URL.
func localPathInput(typ, url, path string) string {
	if typ == "path" {
		return "type path"
	}
	u := strings.ToLower(url)
	switch {
	case strings.HasPrefix(u, "file:"), strings.HasPrefix(u, "path:"), strings.Contains(u, "+file:"):
		return fmt.Sprintf("type %s with a file url", safeName(typ))
	}
	if path != "" && typ != "" {
		return fmt.Sprintf("type %s with a path", safeName(typ))
	}
	return ""
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

// declaredFlakeInputs lists the input names the given flake.nix content
// declares. A seam: tests stand in a list, since the sandbox has no nix.
var declaredFlakeInputs = declaredFlakeInputsViaNix

// declaredFlakeInputsViaNix evaluates `(import flake.nix).inputs`'s attribute
// names with nix under the gate's own scrubbed environment (forbidden as the
// worker-written directories, the same list the gate's command gets, so the
// whole-PATH check and the checked nix apply here too). The new CLI's `nix
// eval` is pure by default and then refuses to import any file, store paths
// included (shown by `just explore-guarded-gate-inputs-eval`), so the
// evaluation is --impure with restrict-eval: the committed content is written
// to a temporary file outside any worktree, copied into the store (its
// content is what the branch already publishes), and handed in as the only
// Nix search-path entry; restricted mode refuses every other path (absolute,
// relative, builtins.storePath) and, with no allowed URI, every fetcher even
// with a hash. getEnv sees the scrubbed environment; currentSystem is the
// host's. No import from derivation; max-call-depth bounds a recursion; the
// output is capped; the merge's ctx and a timeout bound the time; where the
// systemd scope tier exists the evaluation runs in a scope with MemoryMax,
// which bounds memory and is stopped on cancellation. Listing the names
// forces only the inputs attribute set. Any failure is the caller's refusal,
// and the evaluator's message, which quotes the expression, never leaves here.
func declaredFlakeInputsViaNix(ctx context.Context, flakeNix []byte, forbidden []string) ([]string, error) {
	env, err := hookrun.ConfinedGateEnv(os.TempDir(), forbidden)
	if err != nil {
		return nil, err
	}
	nix, err := hookrun.ResolveGateTool("nix", forbidden)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp("", "spinclass-flake-*.nix")
	if err != nil {
		return nil, errors.New("no temporary file for flake.nix")
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(flakeNix); err != nil {
		tmp.Close()
		return nil, errors.New("writing flake.nix for evaluation failed")
	}
	if err := tmp.Close(); err != nil {
		return nil, errors.New("writing flake.nix for evaluation failed")
	}
	ctx, cancel := context.WithTimeout(ctx, declaredInputsTimeout)
	defer cancel()
	add := exec.CommandContext(ctx, nix, "store", "add", "--mode", "flat", tmp.Name())
	add.Dir = os.TempDir()
	add.Env = env
	storePath, err := add.Output()
	if err != nil {
		return nil, errors.New("copying flake.nix into the store for evaluation failed")
	}
	sp := strings.TrimSpace(string(storePath))
	if !strings.HasPrefix(sp, "/nix/store/") {
		return nil, errors.New("copying flake.nix into the store gave no store path")
	}
	argv := []string{
		nix, "eval", "--json",
		"--impure",
		"--option", "restrict-eval", "true",
		"--option", "allowed-uris", "",
		"--option", "allow-import-from-derivation", "false",
		"--option", "max-call-depth", "2000",
		"-I", "f=" + sp,
		"--expr", "import <f>",
		"--apply", "f: builtins.attrNames (f.inputs or { })",
	}
	scopeID := ""
	if id := clown.ScopeIDFromContext(ctx); id != "" {
		scopeID = id + "-inputs"
		if prefix, ok := clown.ScopeArgv(scopeID); ok {
			if p, rErr := hookrun.ResolveGateTool(prefix[0], forbidden); rErr == nil {
				prefix = append([]string{p}, prefix[1:]...)
				// Before the trailing "--": the memory bound the timeout cannot give.
				prefix = append(prefix[:len(prefix)-1], "--property=MemoryMax="+declaredInputsMemoryMax, "--")
				argv = append(prefix, argv...)
			}
		}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = os.TempDir()
	cmd.Env = env
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
	if scopeID != "" && (ctx.Err() != nil || waitErr != nil) {
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
	var names []string
	if err := json.Unmarshal(out, &names); err != nil {
		return nil, errors.New("evaluating flake.nix's inputs gave no name list")
	}
	return names, nil
}

// declaredInputsTimeout bounds the inputs evaluation: a flake.nix whose
// inputs attribute set takes longer than this to force is refused. A
// variable so a test can shorten it.
var declaredInputsTimeout = 60 * time.Second

// declaredInputsMaxOutput caps the name list: a flake.nix that computes
// millions of input names is refused rather than read.
const declaredInputsMaxOutput = 1 << 20

// declaredInputsMemoryMax is the scope's memory bound for the evaluation.
const declaredInputsMemoryMax = "2G"
