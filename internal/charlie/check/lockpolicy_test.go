package check

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/bravo/hookrun"
)

const remoteOnlyLock = `{"nodes":{"nixpkgs":{"locked":{"lastModified":1,"narHash":"sha256-x","owner":"NixOS","repo":"nixpkgs","rev":"abc","type":"github"},"original":{"owner":"NixOS","repo":"nixpkgs","type":"github"}},"tb":{"flake":false,"locked":{"narHash":"sha256-y","type":"tarball","url":"https://example.invalid/x.tar.gz"},"original":{"type":"tarball","url":"https://example.invalid/x.tar.gz"}},"root":{"inputs":{"nixpkgs":"nixpkgs","tb":"tb"}}},"root":"root","version":7}`

const pathInputLock = `{"nodes":{"leak":{"flake":false,"locked":{"lastModified":1,"narHash":"sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","path":"/home/op/.ssh","type":"path"},"original":{"path":"/home/op/.ssh","type":"path"}},"root":{"inputs":{"leak":"leak"}}},"root":"root","version":7}`

// stubDeclaredInputs stands in the inputs flake.nix declares: the test
// sandbox has no nix to evaluate them.
func stubDeclaredInputs(t *testing.T, names []string, err error) {
	t.Helper()
	prev := declaredFlakeInputs
	declaredFlakeInputs = func(string) ([]string, error) { return names, err }
	t.Cleanup(func() { declaredFlakeInputs = prev })
}

// An input flake.nix declares that the lock lacks is fetched while nix
// computes the lock it then refuses (spinclass#449 F1'): refused here first,
// naming the input only; an evaluation failure fails closed.
func TestGuardedGateLockPolicyRefusesAnInputTheLockLacks(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"flake.nix": "{ outputs = _: {}; }", "flake.lock": remoteOnlyLock} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stubDeclaredInputs(t, []string{"nixpkgs", "tb"}, nil)
	if err := refuseUnsafeFlakeLock(dir); err != nil {
		t.Fatalf("every declared input is in the lock: %v", err)
	}
	stubDeclaredInputs(t, []string{"nixpkgs", "leak"}, nil)
	err := refuseUnsafeFlakeLock(dir)
	if err == nil || !strings.Contains(err.Error(), "missing: leak") || strings.Contains(err.Error(), "tb") {
		t.Fatalf("err = %v, want a refusal naming only the missing input", err)
	}
	stubDeclaredInputs(t, nil, errors.New("evaluating flake.nix's inputs failed"))
	if err := refuseUnsafeFlakeLock(dir); err == nil || !strings.Contains(err.Error(), "cannot tell") {
		t.Fatalf("err = %v, want fail closed on an evaluation failure", err)
	}
}

// With a real nix the inputs are read from flake.nix itself, under restricted
// evaluation. Skipped where the sandbox has no nix, which is where every Go
// test of this repo runs (godyn; there is no go.mod to run `go test`
// outside it), so these two tests are exercised by a reviewer with a nix on
// PATH, by `just explore-guarded-gate-inputs-eval` (the same command line in
// bash, with the same shapes) and by the live canary, not by the merge gate.
func TestDeclaredFlakeInputsViaNix(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("no nix on PATH")
	}
	dir := t.TempDir()
	flake := `{ inputs.a.url = "github:x/y"; inputs.b = { url = "github:x/z"; flake = false; }; outputs = _: {}; }`
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte(flake), 0o644); err != nil {
		t.Fatal(err)
	}
	names, err := declaredFlakeInputsViaNix(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "a,b" {
		t.Errorf("names = %v, want [a b]", names)
	}
}

// The enumeration evaluates the worker's own expression, so it must be pure:
// a flake.nix that reads a file outside its directory, the environment, or
// loops, is refused, and nothing it read reaches the error. Skipped where
// the sandbox has no nix.
func TestDeclaredFlakeInputsViaNixIsPureAndBounded(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("no nix on PATH")
	}
	marker := filepath.Join(t.TempDir(), "marker")
	const secret = "DUMMY-MARKER-NOT-A-SECRET"
	if err := os.WriteFile(marker, []byte(secret), 0o600); err != nil {
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
		"throw with readFile":    `{ inputs = throw (builtins.readFile ` + marker + `); outputs = _: {}; }`,
		"attrName from a file":   `{ inputs = { ${builtins.readFile ` + marker + `} = { }; }; outputs = _: {}; }`,
		"readFile in the flake":  `{ inputs = { ${builtins.readFile ./flake.nix} = { }; }; outputs = _: {}; }`,
		"getEnv":                 `{ inputs = { ${builtins.getEnv "LEAK_ME"} = { }; }; outputs = _: {}; }`,
		"readDir outside":        `{ inputs = builtins.readDir ` + filepath.Dir(marker) + `; outputs = _: {}; }`,
		"pathExists outside":     `{ inputs = { ${toString (builtins.pathExists ` + marker + `)} = { }; }; outputs = _: {}; }`,
		"currentSystem":          `{ inputs = { ${builtins.currentSystem} = { }; }; outputs = _: {}; }`,
		"fetchurl with a hash":   `{ inputs = { ${builtins.readFile (builtins.fetchurl { url = "file://` + marker + `"; sha256 = "0000000000000000000000000000000000000000000000000000"; })} = { }; }; outputs = _: {}; }`,
		"fetchGit with a rev":    `{ inputs = { ${builtins.readFile ((builtins.fetchGit { url = "file://` + filepath.Dir(marker) + `"; rev = "0000000000000000000000000000000000000000"; }) + "/x")} = { }; }; outputs = _: {}; }`,
		"infinite recursion":     `{ inputs = let f = x: f (x + 1); in f 0; outputs = _: {}; }`,
		"unbounded list to attr": `{ inputs = builtins.listToAttrs (map (i: { name = toString i; value = { }; }) (builtins.genList (i: i) 10000000)); outputs = _: {}; }`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte(flake), 0o644); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			names, err := declaredFlakeInputsViaNix(dir)
			if err == nil {
				for _, n := range names {
					if strings.Contains(n, secret) || n == "marker" || n == "true" {
						t.Fatalf("the enumeration read outside the flake: names = %v", names)
					}
				}
				if !harmless[name] {
					t.Fatalf("hostile flake was not refused: names = %v", names)
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

// The guarded gate refuses a lock that would make the evaluator copy a local
// path into the store (spinclass#449 F1), and a flake with no lock at all.
// The PoC's three runs map onto: no lock (run 1, refused here as nix would);
// a wrong-narHash lock with a path input, whether or not an output references
// it (runs 2 and 3: the same lock, refused before any evaluation). The
// refusal names the node and the kind, never the path or a hash.
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
		{"run 1: no flake.lock", "", false, "there is none"},
		{"runs 2 and 3: wrong-narHash lock with a path input", pathInputLock, false, "leak: type path"},
		{"git+file url", `{"nodes":{"priv":{"locked":{"narHash":"sha256-A=","rev":"abc","type":"git","url":"file:///home/op/private"},"original":{"type":"git","url":"git+file:///home/op/private"}},"root":{"inputs":{"priv":"priv"}}},"root":"root","version":7}`, false, "priv: type git with a file url"},
		{"tarball file url", `{"nodes":{"tb":{"locked":{"narHash":"sha256-A=","type":"tarball","url":"file:///home/op/x.tar"},"original":{"type":"tarball","url":"file:///home/op/x.tar"}},"root":{"inputs":{"tb":"tb"}}},"root":"root","version":7}`, false, "tb: type tarball with a file url"},
		{"malformed lock fails closed", `{not json`, false, "could not parse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if !tc.noFlake {
				if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte("{ outputs = _: {}; }"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.lock != "" {
				if err := os.WriteFile(filepath.Join(dir, "flake.lock"), []byte(tc.lock), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := refuseUnsafeFlakeLock(dir)
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

// A committed flake.lock that is a symlink is refused unread: nothing of the
// target, not even a parse error's byte, reaches the record.
func TestGuardedGateLockPolicyRefusesASymlinkedLock(t *testing.T) {
	stubDeclaredInputs(t, nil, nil)
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(target, []byte("SECRET-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte("{ outputs = _: {}; }"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "flake.lock")); err != nil {
		t.Fatal(err)
	}
	err := refuseUnsafeFlakeLock(dir)
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("err = %v, want a refusal of the non-regular lock", err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("the refusal echoes the target: %v", err)
	}
}

// The policy runs in the build worktree before the gate's command.
func TestGuardedGateRefusesALockWithALocalPathInput(t *testing.T) {
	stubDeclaredInputs(t, []string{"leak"}, nil)
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
