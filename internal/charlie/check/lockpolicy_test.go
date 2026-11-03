package check

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/crap/go-crap/v2/crap"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/bravo/hookrun"
)

const remoteOnlyLock = `{"nodes":{"nixpkgs":{"locked":{"lastModified":1,"narHash":"sha256-x","owner":"NixOS","repo":"nixpkgs","rev":"abc","type":"github"},"original":{"owner":"NixOS","repo":"nixpkgs","type":"github"}},"tb":{"flake":false,"locked":{"narHash":"sha256-y","type":"tarball","url":"https://example.invalid/x.tar.gz"},"original":{"type":"tarball","url":"https://example.invalid/x.tar.gz"}},"root":{"inputs":{"nixpkgs":"nixpkgs","tb":"tb"}}},"root":"root","version":7}`

const pathInputLock = `{"nodes":{"leak":{"flake":false,"locked":{"lastModified":1,"narHash":"sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","path":"/home/op/.ssh","type":"path"},"original":{"path":"/home/op/.ssh","type":"path"}},"root":{"inputs":{"leak":"leak"}}},"root":"root","version":7}`

// The guarded gate refuses a lock that would make the evaluator copy a local
// path into the store (spinclass#449 F1), and a flake with no lock at all.
// The PoC's three runs map onto: no lock (run 1, refused here as nix would);
// a wrong-narHash lock with a path input, whether or not an output references
// it (runs 2 and 3: the same lock, refused before any evaluation). The
// refusal names the node and the kind, never the path or a hash.
func TestGuardedGateLockPolicy(t *testing.T) {
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
		{"malformed lock fails closed", `{not json`, false, "could not read"},
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

// The policy runs in the build worktree before the gate's command.
func TestGuardedGateRefusesALockWithALocalPathInput(t *testing.T) {
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
