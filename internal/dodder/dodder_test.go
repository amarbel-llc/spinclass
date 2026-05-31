package dodder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDodder writes a stub dodder binary that:
//   - on `info-ssh_agent`, prints $FAKE_DODDER_KEY (empty => no key);
//   - on `init`, appends its argv to $FAKE_DODDER_ARGV_LOG and creates
//     the config-seed marker relative to its CWD (which dodder.Init sets
//     to the worktree).
//
// It returns the binary path and the argv-log path. key is exported into
// the process env (and thus reaches the child via ceilingEnv's
// os.Environ()).
func fakeDodder(t *testing.T, key string) (binPath, argvLog string) {
	t.Helper()
	dir := t.TempDir()
	binPath = filepath.Join(dir, "fake-dodder")
	argvLog = filepath.Join(dir, "argv.log")

	script := `#!/bin/sh
case "$1" in
  info-ssh_agent)
    printf '%s\n' "$FAKE_DODDER_KEY"
    ;;
  init)
    printf '%s\n' "$*" >> "$FAKE_DODDER_ARGV_LOG"
    mkdir -p .dodder/local/share
    : > .dodder/local/share/config-seed
    ;;
esac
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DODDER_KEY", key)
	t.Setenv("FAKE_DODDER_ARGV_LOG", argvLog)
	return binPath, argvLog
}

func readArgvLog(t *testing.T, argvLog string) string {
	t.Helper()
	data, err := os.ReadFile(argvLog)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(data)
}

func TestInit_CreatesRepoAndThreadsPrivateKey(t *testing.T) {
	wt := t.TempDir()
	bin, argvLog := fakeDodder(t, "ecdsa_p256_ssh-fakekey")

	if err := Init(wt, bin, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if !RepoReady(wt) {
		t.Errorf("expected config-seed marker after Init")
	}
	argv := readArgvLog(t, argvLog)
	if !strings.Contains(argv, "-private_key ecdsa_p256_ssh-fakekey") {
		t.Errorf("expected -private_key threaded into init argv, got: %q", argv)
	}
	// No madder store present => no -blob_store-id reuse flag.
	if strings.Contains(argv, "-blob_store-id") {
		t.Errorf("did not expect -blob_store-id without a madder store, got: %q", argv)
	}
	// repo-id derived from worktree basename is the trailing positional.
	if !strings.Contains(argv, deriveRepoID(wt)) {
		t.Errorf("expected derived repo-id %q in argv, got: %q", deriveRepoID(wt), argv)
	}
}

func TestInit_ReusesMadderStoreWhenPresent(t *testing.T) {
	wt := t.TempDir()
	bin, argvLog := fakeDodder(t, "ecdsa_p256_ssh-fakekey")

	// Pre-create the madder default store config so madder.StoreReady is true.
	cfgDir := filepath.Join(wt, ".madder", "local", "share", "blob_stores", "default")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "blob_store-config"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Init(wt, bin, "/some/madder"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	argv := readArgvLog(t, argvLog)
	if !strings.Contains(argv, "-blob_store-id .default") {
		t.Errorf("expected -blob_store-id .default reuse flag, got: %q", argv)
	}
}

func TestInit_Idempotent(t *testing.T) {
	wt := t.TempDir()
	bin, argvLog := fakeDodder(t, "ecdsa_p256_ssh-fakekey")

	// Pre-create the marker so the repo is already "ready".
	seed := filepath.Join(wt, configSeedRel)
	if err := os.MkdirAll(filepath.Dir(seed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seed, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Init(wt, bin, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if argv := readArgvLog(t, argvLog); argv != "" {
		t.Errorf("expected init NOT to run on a ready repo, but argv log has: %q", argv)
	}
}

func TestInit_HardFailsWhenNoKey(t *testing.T) {
	wt := t.TempDir()
	bin, _ := fakeDodder(t, "") // empty key => info-ssh_agent prints nothing usable

	err := Init(wt, bin, "")
	if err == nil {
		t.Fatalf("expected hard failure when agent has no key, got nil")
	}
	if RepoReady(wt) {
		t.Errorf("expected no repo created on hard failure")
	}
}

func TestInit_EmptyBinIsNoop(t *testing.T) {
	wt := t.TempDir()
	if err := Init(wt, "", ""); err != nil {
		t.Fatalf("Init with empty bin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".dodder")); !os.IsNotExist(err) {
		t.Errorf("expected no .dodder/ when dodderBin is empty, stat err=%v", err)
	}
}

func TestDeriveRepoID(t *testing.T) {
	cases := map[string]string{
		"/home/u/.worktrees/pure-willow": "pure-willow",
		"/tmp/wt":                        "wt",
		"/x/feat/login bug":              "login-bug",
		"/x/--weird--":                   "weird",
	}
	for in, want := range cases {
		if got := deriveRepoID(in); got != want {
			t.Errorf("deriveRepoID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLinkInto(t *testing.T) {
	binDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "dodder")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := LinkInto(binDir, target); err != nil {
		t.Fatalf("LinkInto: %v", err)
	}
	got, err := os.Readlink(filepath.Join(binDir, "dodder"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if got != target {
		t.Errorf("symlink target: got %q, want %q", got, target)
	}
	// Idempotent re-link.
	if err := LinkInto(binDir, target); err != nil {
		t.Fatalf("LinkInto (second): %v", err)
	}
}
