package check

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/0/clown"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/bravo/hookrun"
)

const remoteOnlyLock = `{"nodes":{"nixpkgs":{"locked":{"lastModified":1,"narHash":"sha256-x","owner":"NixOS","repo":"nixpkgs","rev":"abc","type":"github"},"original":{"owner":"NixOS","repo":"nixpkgs","type":"github"}},"tb":{"flake":false,"locked":{"narHash":"sha256-y","type":"tarball","url":"https://example.invalid/x.tar.gz"},"original":{"type":"tarball","url":"https://example.invalid/x.tar.gz"}},"root":{"inputs":{"nixpkgs":"nixpkgs","tb":"tb"}}},"root":"root","version":7}`

const pathInputLock = `{"nodes":{"leak":{"flake":false,"locked":{"lastModified":1,"narHash":"sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","path":"/home/op/.ssh","type":"path"},"original":{"path":"/home/op/.ssh","type":"path"}},"root":{"inputs":{"leak":"leak"}}},"root":"root","version":7}`

// nestedLock holds a subflake `sub` whose own input `leak` is remote, and a
// follows `e` onto nixpkgs: the shapes the F1” review named.
const nestedLock = `{"nodes":{"nixpkgs":{"locked":{"lastModified":1,"narHash":"sha256-x","owner":"NixOS","repo":"nixpkgs","rev":"abc","type":"github"},"original":{"owner":"NixOS","repo":"nixpkgs","type":"github"}},"leak":{"flake":false,"locked":{"lastModified":1,"narHash":"sha256-x","owner":"NixOS","repo":"nix","rev":"abc","type":"github"},"original":{"owner":"NixOS","repo":"nix","type":"github"}},"sub":{"inputs":{"leak":"leak"},"locked":{"lastModified":1,"narHash":"sha256-x","owner":"x","repo":"sub","rev":"abc","type":"github"},"original":{"owner":"x","repo":"sub","type":"github"}},"root":{"inputs":{"nixpkgs":"nixpkgs","sub":"sub","e":["nixpkgs"]}}},"root":"root","version":7}`

// commitTree is a repository whose HEAD holds files and symlinks: the policy
// reads the committed tree, so every fixture is a commit.
func commitTree(t *testing.T, files, links map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "t@t")
	runGit(t, dir, "config", "user.name", "t")
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "tree")
	return dir
}

// stubDeclaredInputs stands in the inputs flake.nix declares, canonicalised:
// the test sandbox has no nix to evaluate them.
func stubDeclaredInputs(t *testing.T, specs map[string]inputSpec, err error) {
	t.Helper()
	prev := declaredFlakeInputs
	declaredFlakeInputs = func(context.Context, string, []byte, []string) (map[string]inputSpec, error) { return specs, err }
	t.Cleanup(func() { declaredFlakeInputs = prev })
}

// specsFromLock is what nix canonicalises an honest flake.nix to for the named
// inputs of a lock: the nodes' own originals and flake flags, so the lock
// matches by construction (node ids equal input names in these fixtures).
func specsFromLock(t *testing.T, lockJSON string, names ...string) map[string]inputSpec {
	t.Helper()
	var lock flakeLock
	if err := json.Unmarshal([]byte(lockJSON), &lock); err != nil {
		t.Fatal(err)
	}
	specs := map[string]inputSpec{}
	for _, name := range names {
		node, ok := lock.Nodes[name]
		if !ok {
			t.Fatalf("no node %q in the lock", name)
		}
		specs[name] = inputSpec{Original: node.Original, Flake: node.Flake == nil || *node.Flake}
	}
	return specs
}

func github(owner, repo string) map[string]any {
	return map[string]any{"type": "github", "owner": owner, "repo": repo}
}

func str(s string) *string { return &s }

// An input flake.nix declares that the lock lacks is fetched while nix
// computes the lock it then refuses (spinclass#449 F1'): refused here first,
// naming the input only; an evaluation failure fails closed.
func TestGuardedGateLockPolicyRefusesAnInputTheLockLacks(t *testing.T) {
	dir := commitTree(t, map[string]string{"flake.nix": "{ outputs = _: {}; }", "flake.lock": remoteOnlyLock}, nil)
	stubDeclaredInputs(t, specsFromLock(t, remoteOnlyLock, "nixpkgs", "tb"), nil)
	if err := refuseUnsafeFlakeLock(context.Background(), dir, nil); err != nil {
		t.Fatalf("every declared input is in the lock: %v", err)
	}
	specs := specsFromLock(t, remoteOnlyLock, "nixpkgs")
	specs["leak\x1b[31m"] = inputSpec{Original: github("x", "y"), Flake: true}
	specs["/home/op"] = inputSpec{Original: github("x", "z"), Flake: true}
	stubDeclaredInputs(t, specs, nil)
	err := refuseUnsafeFlakeLock(context.Background(), dir, nil)
	if err == nil || !strings.Contains(err.Error(), "?home?op: missing from the lock") || !strings.Contains(err.Error(), "leak??31m: missing from the lock") ||
		strings.Contains(err.Error(), "tb") || strings.Contains(err.Error(), "\x1b") || strings.Contains(err.Error(), "nixpkgs:") {
		t.Fatalf("err = %v, want a refusal naming only the missing inputs, sanitised", err)
	}
	stubDeclaredInputs(t, nil, errors.New("evaluating flake.nix's inputs failed"))
	if err := refuseUnsafeFlakeLock(context.Background(), dir, nil); err == nil || !strings.Contains(err.Error(), "cannot tell") {
		t.Fatalf("err = %v, want fail closed on an evaluation failure", err)
	}
}

// Completeness by name is not enough (spinclass#449 F1”, confirmed by the
// leak recipe's namematch and nested_override variants): nix re-fetches an
// input whose declared reference differs from the lock node's original, so a
// lock holding a remote node under the name flake.nix declares as a path is
// refused, and so is any other difference (another owner, an added hash, the
// flake flag, a follows the lock does not record), one level down for a
// nested override too. The refusal names input paths and reference types
// only.
func TestGuardedGateLockPolicyRefusesADeclaredReferenceTheLockDoesNotMatch(t *testing.T) {
	pathRef := map[string]any{"type": "path", "path": "/home/op/.ssh", "narHash": "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	for _, tc := range []struct {
		name   string
		lock   string
		specs  func(t *testing.T) map[string]inputSpec
		refuse []string // "" = accepted
	}{
		{"matches the lock", remoteOnlyLock, func(t *testing.T) map[string]inputSpec { return specsFromLock(t, remoteOnlyLock, "nixpkgs", "tb") }, nil},
		{"matches a nested lock with a follows", nestedLock, func(t *testing.T) map[string]inputSpec {
			specs := specsFromLock(t, nestedLock, "nixpkgs", "sub")
			sub := specs["sub"]
			sub.Inputs = specsFromLock(t, nestedLock, "leak")
			specs["sub"] = sub
			specs["e"] = inputSpec{Follows: str("nixpkgs"), Flake: true}
			return specs
		}, nil},
		{"same name, a path declared", remoteOnlyLock, func(t *testing.T) map[string]inputSpec {
			specs := specsFromLock(t, remoteOnlyLock, "tb")
			specs["nixpkgs"] = inputSpec{Original: pathRef, Flake: true}
			return specs
		}, []string{"nixpkgs: declared as type path", "nixpkgs: declared type path, locked type github"}},
		{"same type, another owner", remoteOnlyLock, func(t *testing.T) map[string]inputSpec {
			specs := specsFromLock(t, remoteOnlyLock, "tb")
			specs["nixpkgs"] = inputSpec{Original: github("evil", "nixpkgs"), Flake: true}
			return specs
		}, []string{"nixpkgs: declared type github, locked type github; the references differ"}},
		{"a hash added to a remote reference", remoteOnlyLock, func(t *testing.T) map[string]inputSpec {
			specs := specsFromLock(t, remoteOnlyLock, "tb")
			ref := github("NixOS", "nixpkgs")
			ref["narHash"] = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
			specs["nixpkgs"] = inputSpec{Original: ref, Flake: true}
			return specs
		}, []string{"nixpkgs: declared type github, locked type github; the references differ"}},
		{"the flake flag differs", remoteOnlyLock, func(t *testing.T) map[string]inputSpec {
			specs := specsFromLock(t, remoteOnlyLock, "nixpkgs", "tb")
			tb := specs["tb"]
			tb.Flake = true
			specs["tb"] = tb
			return specs
		}, []string{"tb: the flake flag differs from the lock"}},
		{"a follows the lock locked as a node", remoteOnlyLock, func(t *testing.T) map[string]inputSpec {
			specs := specsFromLock(t, remoteOnlyLock, "tb")
			specs["nixpkgs"] = inputSpec{Follows: str("tb"), Flake: true}
			return specs
		}, []string{"nixpkgs: the lock does not record the declared follows"}},
		{"a follows onto another input", nestedLock, func(t *testing.T) map[string]inputSpec {
			specs := specsFromLock(t, nestedLock, "nixpkgs", "sub")
			specs["e"] = inputSpec{Follows: str("sub"), Flake: true}
			return specs
		}, []string{"e: the lock does not record the declared follows"}},
		{"a reference where the lock has a follows", nestedLock, func(t *testing.T) map[string]inputSpec {
			specs := specsFromLock(t, nestedLock, "nixpkgs", "sub")
			specs["e"] = inputSpec{Original: github("x", "e"), Flake: true}
			return specs
		}, []string{"e: locked as a follows, declared as a reference"}},
		{"a nested override to a path", nestedLock, func(t *testing.T) map[string]inputSpec {
			specs := specsFromLock(t, nestedLock, "nixpkgs", "sub")
			sub := specs["sub"]
			sub.Inputs = map[string]inputSpec{"leak": {Original: pathRef, Flake: false}}
			specs["sub"] = sub
			return specs
		}, []string{"sub/leak: declared as type path", "sub/leak: declared type path, locked type github"}},
		{"a nested override the lock lacks", nestedLock, func(t *testing.T) map[string]inputSpec {
			specs := specsFromLock(t, nestedLock, "nixpkgs", "sub")
			sub := specs["sub"]
			sub.Inputs = map[string]inputSpec{"nope": {Original: github("x", "y"), Flake: true}}
			specs["sub"] = sub
			return specs
		}, []string{"sub/nope: missing from the lock"}},
		{"declared without a reference", remoteOnlyLock, func(t *testing.T) map[string]inputSpec {
			specs := specsFromLock(t, remoteOnlyLock, "tb")
			specs["nixpkgs"] = inputSpec{Flake: true}
			return specs
		}, []string{"nixpkgs: declared without a reference"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubDeclaredInputs(t, tc.specs(t), nil)
			dir := commitTree(t, map[string]string{"flake.nix": "{ outputs = _: {}; }", "flake.lock": tc.lock}, nil)
			err := refuseUnsafeFlakeLock(context.Background(), dir, nil)
			if len(tc.refuse) == 0 {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("accepted, want a refusal")
			}
			for _, want := range tc.refuse {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to contain %q", err, want)
				}
			}
			for _, secret := range []string{"/home/op", ".ssh", "AAAA", "evil", "NixOS"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the refusal repeats %q, which the worker must not see: %v", secret, err)
				}
			}
		})
	}
}

// The guarded gate refuses a lock that would make the evaluator copy a local
// path into the store (spinclass#449 F1), and a flake with no lock at all.
// The PoC's runs map onto: no lock (refused here as nix would, before any
// fetch); a wrong-narHash lock with a path input, whether or not an output
// references it (the same lock, refused before any evaluation). The refusal
// names the node and the kind, never the path or a hash.
func TestGuardedGateLockPolicy(t *testing.T) {
	stubDeclaredInputs(t, nil, nil)
	for _, tc := range []struct {
		name    string
		lock    string // "" = no flake.lock
		noFlake bool
		refuse  string // "" = accepted
	}{
		{"remote inputs only (github, https tarball)", remoteOnlyLock, false, ""},
		{"no flake.nix at all", "", true, ""},
		{"no flake.lock", "", false, "there is none"},
		{"wrong-narHash lock with a path input", pathInputLock, false, "leak: type path"},
		{"git+file url", `{"nodes":{"priv":{"locked":{"narHash":"sha256-A=","rev":"abc","type":"git","url":"file:///home/op/private"},"original":{"type":"git","url":"git+file:///home/op/private"}},"root":{"inputs":{"priv":"priv"}}},"root":"root","version":7}`, false, "priv: type git with a file url"},
		{"tarball file url", `{"nodes":{"tb":{"locked":{"narHash":"sha256-A=","type":"tarball","url":"file:///home/op/x.tar"},"original":{"type":"tarball","url":"file:///home/op/x.tar"}},"root":{"inputs":{"tb":"tb"}}},"root":"root","version":7}`, false, "tb: type tarball with a file url"},
		{"malformed lock fails closed", `{not json`, false, "could not parse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{}
			if !tc.noFlake {
				files["flake.nix"] = "{ outputs = _: {}; }"
			}
			if tc.lock != "" {
				files["flake.lock"] = tc.lock
			}
			err := refuseUnsafeFlakeLock(context.Background(), commitTree(t, files, nil), nil)
			switch {
			case tc.refuse == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.refuse != "" && (err == nil || !strings.Contains(err.Error(), tc.refuse)):
				t.Fatalf("err = %v, want a refusal containing %q", err, tc.refuse)
			}
			if err != nil {
				for _, secret := range []string{"/home/op", "sha256-A", "AAAA"} {
					if strings.Contains(err.Error(), secret) {
						t.Errorf("the refusal repeats %q, which the worker must not see: %v", secret, err)
					}
				}
			}
		})
	}
}

// A committed flake.lock or flake.nix that is not a regular file is refused
// unread: a symlink (the second review's finding A), and a directory or a
// gitlink, which git lists but FileAtRev does not read (the third review's
// A). The policy reads the tree, so the link's target is never opened and
// nothing of it reaches the record.
func TestGuardedGateLockPolicyRefusesNonRegularFiles(t *testing.T) {
	stubDeclaredInputs(t, nil, nil)
	target := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(target, []byte("SECRET-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct{ files, links map[string]string }{
		"symlinked lock":      {map[string]string{"flake.nix": "{ outputs = _: {}; }"}, map[string]string{"flake.lock": target}},
		"symlinked flake.nix": {map[string]string{"flake.lock": remoteOnlyLock}, map[string]string{"flake.nix": target}},
	} {
		t.Run(name, func(t *testing.T) {
			err := refuseUnsafeFlakeLock(context.Background(), commitTree(t, tc.files, tc.links), nil)
			if err == nil || !strings.Contains(err.Error(), "regular file") {
				t.Fatalf("err = %v, want a refusal of the non-regular file", err)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Errorf("the refusal echoes the target: %v", err)
			}
		})
	}
	t.Run("flake.nix is a directory", func(t *testing.T) {
		dir := commitTree(t, map[string]string{"flake.lock": remoteOnlyLock}, nil)
		if err := os.MkdirAll(filepath.Join(dir, "flake.nix"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "flake.nix", "x"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", "-A")
		runGit(t, dir, "commit", "-q", "-m", "dir")
		if err := refuseUnsafeFlakeLock(context.Background(), dir, nil); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("err = %v, want a refusal of the directory", err)
		}
	})
	t.Run("flake.nix is a gitlink", func(t *testing.T) {
		dir := commitTree(t, map[string]string{"flake.lock": remoteOnlyLock}, nil)
		head := runGit(t, dir, "rev-parse", "HEAD")
		runGit(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",flake.nix")
		runGit(t, dir, "commit", "-q", "-m", "gitlink")
		if err := refuseUnsafeFlakeLock(context.Background(), dir, nil); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("err = %v, want a refusal of the gitlink", err)
		}
	})
}

// The policy reads the committed tree, not the working tree: an uncommitted
// edit to flake.lock in the build worktree changes nothing.
func TestGuardedGateLockPolicyReadsTheCommittedTree(t *testing.T) {
	stubDeclaredInputs(t, nil, nil)
	dir := commitTree(t, map[string]string{"flake.nix": "{ outputs = _: {}; }", "flake.lock": pathInputLock}, nil)
	if err := os.WriteFile(filepath.Join(dir, "flake.lock"), []byte(remoteOnlyLock), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := refuseUnsafeFlakeLock(context.Background(), dir, nil); err == nil || !strings.Contains(err.Error(), "leak: type path") {
		t.Fatalf("err = %v, want the committed lock judged, not the working tree's", err)
	}
}

// The evaluation runs under the gate's own tool checks (the second review's
// finding B): a PATH entry under a worker-written directory refuses it before
// any nix runs.
func TestDeclaredFlakeInputsViaNixUsesTheGateToolChecks(t *testing.T) {
	defer hookrun.SetConfinedGateTools("git", "sh")()
	wt := t.TempDir()
	bin := filepath.Join(wt, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", os.Getenv("PATH")+":"+bin)
	_, err := declaredFlakeInputsViaNix(scopedCtx(), t.TempDir(), []byte("{ outputs = _: {}; }"), []string{wt})
	if err == nil || !strings.Contains(err.Error(), "PATH") || !strings.Contains(err.Error(), wt) {
		t.Fatalf("err = %v, want the PATH refusal naming the worktree", err)
	}
}

// The evaluation runs only inside a transient systemd scope that bounds its
// memory (the third review's D): no scope id in the context, and no scope
// tier on the host, each refuse it before any tool is looked up; there is no
// unscoped fallback.
func TestDeclaredFlakeInputsViaNixNeedsAScope(t *testing.T) {
	defer hookrun.SetConfinedGateTools("git", "sh")()
	if _, err := declaredFlakeInputsViaNix(context.Background(), t.TempDir(), []byte("{ outputs = _: {}; }"), nil); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("err = %v, want a refusal for the missing scope id", err)
	}
	t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")
	if _, err := declaredFlakeInputsViaNix(scopedCtx(), t.TempDir(), []byte("{ outputs = _: {}; }"), nil); err == nil || !strings.Contains(err.Error(), "scope tier") {
		t.Fatalf("err = %v, want a refusal for the missing scope tier", err)
	}
}

func scopedCtx() context.Context {
	return clown.WithLocalScope(context.Background(), "lockpolicy-test")
}

// skipWithoutNixAndScope skips where the sandbox has no nix or no systemd
// user bus, which is where every Go test of this repo runs (godyn; there is
// no go.mod to run `go test` outside it), so these tests are exercised by a
// reviewer with both on a Linux host, by `just
// explore-guarded-gate-inputs-eval` (the same command line in bash, with the
// same shapes and the same canonicalisation) and by the live canary, not by
// the merge gate.
func skipWithoutNixAndScope(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("no nix on PATH")
	}
	if _, ok := clown.ScopeArgv("probe"); !ok {
		t.Skip("scope tier unavailable (no systemd user bus)")
	}
}

// With a real nix the inputs are read from flake.nix itself, on stdin, under
// restricted evaluation, and canonicalised to the lock's original shape.
func TestDeclaredFlakeInputsViaNix(t *testing.T) {
	skipWithoutNixAndScope(t)
	flake := `{ inputs.a.url = "github:x/y"; inputs.b = { url = "github:x/z"; flake = false; inputs.n.follows = "a"; }; outputs = _: {}; }`
	specs, err := declaredFlakeInputsViaNix(scopedCtx(), t.TempDir(), []byte(flake), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]inputSpec{
		"a": {Original: github("x", "y"), Flake: true, Inputs: map[string]inputSpec{}},
		"b": {Original: github("x", "z"), Flake: false, Inputs: map[string]inputSpec{"n": {Follows: str("a"), Flake: true, Inputs: map[string]inputSpec{}}}},
	}
	got, _ := json.Marshal(specs)
	wantJSON, _ := json.Marshal(want)
	if string(got) != string(wantJSON) {
		t.Errorf("specs = %s, want %s", got, wantJSON)
	}
	ctx, cancel := context.WithCancel(scopedCtx())
	cancel()
	if _, err := declaredFlakeInputsViaNix(ctx, t.TempDir(), []byte(flake), nil); err == nil {
		t.Error("a cancelled merge must not run the evaluation")
	}
}

// The evaluation evaluates the worker's own expression, so it must be
// restricted: a flake.nix that reads a file outside the flake (the working
// directory included), fetches, or loops, is refused, and nothing it read
// reaches the error; a relative path, a string spec and a url mixed with
// attributes are refused by the canonicalisation.
func TestDeclaredFlakeInputsViaNixIsRestrictedAndBounded(t *testing.T) {
	skipWithoutNixAndScope(t)
	dir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "marker")
	const secret = "DUMMY-MARKER-NOT-A-SECRET"
	if err := os.WriteFile(marker, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LEAK_ME", secret)
	prevTimeout := declaredInputsTimeout
	declaredInputsTimeout = 10 * time.Second
	t.Cleanup(func() { declaredInputsTimeout = prevTimeout })
	// harmless shapes evaluate to a name that reveals nothing: the scrubbed
	// environment, the host's system, or false.
	harmless := map[string]bool{"getEnv": true, "currentSystem": true, "pathExists outside": true}
	for name, flake := range map[string]string{
		"throw with readFile":       `{ inputs = throw (builtins.readFile ` + marker + `); outputs = _: {}; }`,
		"attrName from a file":      `{ inputs = { ${builtins.readFile ` + marker + `} = { }; }; outputs = _: {}; }`,
		"readFile in the cwd":       `{ inputs = { ${builtins.readFile ./flake.nix} = { }; }; outputs = _: {}; }`,
		"getEnv":                    `{ inputs = { ${builtins.getEnv "LEAK_ME"} = { }; }; outputs = _: {}; }`,
		"readDir outside":           `{ inputs = builtins.readDir ` + filepath.Dir(marker) + `; outputs = _: {}; }`,
		"pathExists outside":        `{ inputs = { ${toString (builtins.pathExists ` + marker + `)} = { }; }; outputs = _: {}; }`,
		"currentSystem":             `{ inputs = { ${builtins.currentSystem} = { }; }; outputs = _: {}; }`,
		"fetchurl with a hash":      `{ inputs = { ${builtins.readFile (builtins.fetchurl { url = "file://` + marker + `"; sha256 = "0000000000000000000000000000000000000000000000000000"; })} = { }; }; outputs = _: {}; }`,
		"fetchGit with a rev":       `{ inputs = { ${builtins.readFile ((builtins.fetchGit { url = "file://` + filepath.Dir(marker) + `"; rev = "0000000000000000000000000000000000000000"; }) + "/x")} = { }; }; outputs = _: {}; }`,
		"infinite recursion":        `{ inputs = let f = x: f (x + 1); in f 0; outputs = _: {}; }`,
		"unbounded list to attr":    `{ inputs = builtins.listToAttrs (map (i: { name = toString i; value = { }; }) (builtins.genList (i: i) 10000000)); outputs = _: {}; }`,
		"relative path reference":   `{ inputs.a.url = "./sub"; outputs = _: {}; }`,
		"string spec":               `{ inputs.a = "github:x/y"; outputs = _: {}; }`,
		"url mixed with attributes": `{ inputs.a = { url = "github:x/y"; owner = "z"; }; outputs = _: {}; }`,
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			specs, err := declaredFlakeInputsViaNix(scopedCtx(), dir, []byte(flake), nil)
			if err == nil {
				for n := range specs {
					if strings.Contains(n, secret) || n == "marker" || n == "true" {
						t.Fatalf("the evaluation read outside the flake: inputs = %v", specs)
					}
				}
				if !harmless[name] {
					t.Fatalf("hostile flake was not refused: inputs = %v", specs)
				}
				return
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("the refusal echoes what the flake read: %v", err)
			}
			if time.Since(start) > 2*declaredInputsTimeout {
				t.Fatalf("not bounded: took %v", time.Since(start))
			}
		})
	}
}

// The policy runs in the build worktree before the gate's command.
func TestGuardedGateRefusesALockWithALocalPathInput(t *testing.T) {
	stubDeclaredInputs(t, nil, nil)
	t.Setenv("RINGMASTER_DISABLE_SCOPE", "1")
	defer hookrun.SetConfinedGateTools("git", "sh")()
	_, _, wtPath := setupRepoWithWorktree(t, "feature")
	lock := `{"nodes":{"leak":{"flake":false,"locked":{"lastModified":1,"narHash":"sha256-A=","path":"/etc","type":"path"},"original":{"path":"/etc","type":"path"}},"root":{"inputs":{"leak":"leak"}}},"root":"root","version":7}`
	for name, body := range map[string]string{"flake.nix": "{ inputs.leak = { url = \"path:/etc\"; flake = false; }; outputs = _: {}; }", "flake.lock": lock} {
		if err := os.WriteFile(filepath.Join(wtPath, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, wtPath, "add", "flake.nix", "flake.lock")
	runGit(t, wtPath, "commit", "-m", "flake")
	marker := filepath.Join(t.TempDir(), "ran")
	hookSha := runGit(t, wtPath, "rev-parse", "HEAD")
	var buf bytes.Buffer
	rep := crap.NewReporter(&buf, crap.ReporterOptions{})
	ts := rep.TestStream(0)
	gate := sweatfileio.ConfinedPreMergeGate{Command: ": > " + marker}
	_, err := RunGuardedWithReporterContext(context.Background(), rep, ts, sweatfile.Hierarchy{}, wtPath, "feature", hookSha, gate, nil)
	ts.Finish()
	if err == nil || !strings.Contains(err.Error(), "local filesystem") {
		t.Fatalf("err = %v, want the lock policy refusal", err)
	}
	if _, serr := os.Stat(marker); serr == nil {
		t.Error("the gate command ran although the lock was refused")
	}
	if tr, ok := findPoint(testPoints(decodeRecords(t, buf.Bytes())), "guarded gate feature"); !ok || tr.OK {
		t.Errorf("refusal point = %+v, want a failing guarded gate point", tr)
	}
}
