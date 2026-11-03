package check

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/bravo/hookrun"
)

// The guarded gate's lock policy (spinclass#449, review finding F1). The nix
// evaluator runs as the operator uid with the whole filesystem readable, and a
// flake input it fetches is copied into /nix/store, world-readable and mounted
// into every unit, before the lock's narHash is compared. A worker-committed
// lock naming `path:/home/<operator>/...` with a wrong hash therefore leaks that
// path through one failed merge (shown by `just explore-guarded-gate-input-leak`).
// A guarded gate refuses, before anything runs, a lock with any node that
// points at a local path. Policy, not a boundary: the boundary is evaluation
// in a mount namespace that sees only the build worktree (spinclass#448).

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

// refuseUnsafeFlakeLock returns an error naming every lock node in dir's
// flake.lock that names a local path: type `path`, or any URL with a file
// scheme (`file:`, `git+file:`, and the `+file` forms). The refusal names
// the node and the kind only, never the path or URL or a hash: the merge
// record reaches the worker, and the path is what the worker is after. A
// flake.nix with no flake.lock is refused too: nix refuses it as well under
// --no-update-lock-file, before fetching anything (shown by the PoC recipe),
// and so does nix a flake.nix input the lock lacks; the policy does not parse
// flake.nix itself. A directory with no flake.nix has no lock to judge.
func refuseUnsafeFlakeLock(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "flake.nix")); err != nil {
		return nil
	}
	lockPath := filepath.Join(dir, "flake.lock")
	// Lstat first: a committed symlink would be followed by ReadFile to
	// wherever it points, and a parse error could echo a byte of the target.
	fi, err := os.Lstat(lockPath)
	if err != nil {
		return errors.New("a guarded gate needs a committed flake.lock beside flake.nix, and there is none; nothing ran")
	}
	if !fi.Mode().IsRegular() {
		return errors.New("a guarded gate needs flake.lock to be a regular file, and the committed one is not; nothing ran")
	}
	raw, err := os.ReadFile(lockPath)
	if err != nil {
		return errors.New("a guarded gate could not read flake.lock; nothing ran")
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
				bad = append(bad, name+": "+reason)
				break
			}
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return errors.New("the flake.lock names a flake input on the local filesystem, which the evaluator would copy into the world-readable /nix/store as the operator before the gate could refuse it; a guarded merge accepts only remote inputs; nothing ran\n  " + strings.Join(bad, "\n  "))
	}
	// An input flake.nix declares that the lock lacks is fetched while nix
	// computes the lock it then refuses to write (the PoC's nolock and
	// f1prime_path variants), narHash or not. Refuse it here instead.
	declared, err := declaredFlakeInputs(dir)
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
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return errors.New("flake.nix declares flake input(s) the flake.lock lacks, which nix would fetch, as the operator and into the world-readable /nix/store, while computing the lock it then refuses to write; a guarded merge needs a complete lock; nothing ran\n  missing: " + strings.Join(missing, ", "))
	}
	return nil
}

// declaredFlakeInputs lists the input names dir's flake.nix declares. A seam:
// tests stand in a list, since the sandbox has no nix.
var declaredFlakeInputs = declaredFlakeInputsViaNix

// declaredFlakeInputsViaNix evaluates `(import flake.nix).inputs`'s attribute
// names with nix under the gate's own scrubbed environment. The new CLI's
// `nix eval` is pure by default and then refuses to import any file, store
// paths included (shown by `just explore-guarded-gate-inputs-eval`), so the
// evaluation is --impure with restrict-eval: flake.nix is first copied into
// the store (its committed content, which the branch already publishes) and
// handed in as the only Nix search-path entry, and restricted mode refuses
// every other path (an absolute path, a relative one, builtins.storePath) and,
// with no allowed URI, every fetcher even with a hash. getEnv sees the
// scrubbed environment; currentSystem is the host's. No import from
// derivation; max-call-depth bounds a recursion; the output is capped; the
// timeout bounds the rest (not memory: a flake that allocates without end is
// killed by the timeout, not before). Listing the names forces only the
// inputs attribute set. Any failure is the caller's refusal, and the
// evaluator's message, which quotes the expression, never leaves here.
func declaredFlakeInputsViaNix(dir string) ([]string, error) {
	env, err := hookrun.ConfinedGateEnv(dir, nil)
	if err != nil {
		return nil, err
	}
	nix, err := exec.LookPath("nix")
	if err != nil {
		return nil, fmt.Errorf("nix is not on the frontend's PATH: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), declaredInputsTimeout)
	defer cancel()
	add := exec.CommandContext(ctx, nix, "store", "add", "--mode", "flat", "./flake.nix")
	add.Dir = dir
	add.Env = env
	storePath, err := add.Output()
	if err != nil {
		return nil, errors.New("copying flake.nix into the store for evaluation failed")
	}
	sp := strings.TrimSpace(string(storePath))
	if !strings.HasPrefix(sp, "/nix/store/") {
		return nil, errors.New("copying flake.nix into the store gave no store path")
	}
	cmd := exec.CommandContext(ctx, nix, "eval", "--json",
		"--impure",
		"--option", "restrict-eval", "true",
		"--option", "allowed-uris", "",
		"--option", "allow-import-from-derivation", "false",
		"--option", "max-call-depth", "2000",
		"-I", "f="+sp,
		"--expr", "import <f>",
		"--apply", "f: builtins.attrNames (f.inputs or { })")
	cmd.Dir = dir
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

// declaredInputsMaxOutput caps the name list: a flake.nix that computes
// millions of input names is refused rather than read.
const declaredInputsMaxOutput = 1 << 20

// declaredInputsTimeout bounds the inputs evaluation: a flake.nix whose
// inputs attribute set takes longer than this to force is refused. A
// variable so a test can shorten it.
var declaredInputsTimeout = 60 * time.Second

// localPathInput says why an input reference is a local path, or "". It
// names the kind, never the path or URL.
func localPathInput(typ, url, path string) string {
	if typ == "path" {
		return "type path"
	}
	u := strings.ToLower(url)
	switch {
	case strings.HasPrefix(u, "file:"), strings.HasPrefix(u, "path:"), strings.Contains(u, "+file:"):
		return fmt.Sprintf("type %s with a file url", typ)
	}
	if path != "" && typ != "" {
		return fmt.Sprintf("type %s with a path", typ)
	}
	return ""
}
