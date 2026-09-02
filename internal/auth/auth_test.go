package auth

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

func TestMain(m *testing.M) {
	cleanup, err := testgit.SetHermeticEnv()
	if err != nil {
		panic(err)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupRepo builds an isolated repo with an scp-style forge origin (never
// contacted) and one worktree session on branch "feature-x". Session state is
// rooted under an isolated XDG_STATE_HOME.
func setupRepo(t *testing.T) (repoPath, wtPath string, id Identity) {
	t.Helper()
	testgit.RequireGit(t)
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("HOME", root)
	repoPath = filepath.Join(root, "repo")
	testgit.MustInit(t, repoPath)
	runGit(t, repoPath, "remote", "add", "origin", "git@forge.example.com:owner/repo.git")
	wtPath = filepath.Join(repoPath, ".worktrees", "feature-x")
	testgit.MustWorktreeAdd(t, repoPath, wtPath, "feature-x")
	id = Identity{RepoPath: repoPath, WorktreePath: wtPath, Branch: "feature-x", SessionKey: "repo/feature-x"}
	return repoPath, wtPath, id
}

func authSweatfile(mint, revoke string) sweatfile.Sweatfile {
	a := &sweatfile.Auth{}
	if mint != "" {
		a.MintCommand = &mint
	}
	if revoke != "" {
		a.RevokeCommand = &revoke
	}
	return sweatfile.Sweatfile{Auth: a}
}

func TestParseForgeRemote(t *testing.T) {
	cases := []struct {
		in   string
		want Remote
	}{
		{"git@forge.example.com:owner/repo.git", Remote{Host: "forge.example.com", OwnerRepo: "owner/repo", SSHPrefix: "git@forge.example.com:"}},
		{"forge.example.com:owner/repo", Remote{Host: "forge.example.com", OwnerRepo: "owner/repo", SSHPrefix: "forge.example.com:"}},
		{"ssh://git@forge.example.com:2222/owner/repo.git", Remote{Host: "forge.example.com", OwnerRepo: "owner/repo", SSHPrefix: "ssh://git@forge.example.com:2222/"}},
		{"https://forge.example.com/owner/repo.git", Remote{Host: "forge.example.com", OwnerRepo: "owner/repo"}},
	}
	for _, c := range cases {
		got, err := ParseForgeRemote(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: got %+v, want %+v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "/srv/git/bare.git", "file:///srv/git/bare.git"} {
		if _, err := ParseForgeRemote(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestMintNoCommandIsNoop(t *testing.T) {
	_, wtPath, id := setupRepo(t)
	minted, err := Mint(context.Background(), sweatfile.Sweatfile{}, id)
	if err != nil || minted {
		t.Fatalf("Mint with no [auth]: minted=%v err=%v", minted, err)
	}
	if Minted(wtPath) {
		t.Error("credential file written without a mint-command")
	}
}

func TestMintWritesCredentialInjectsConfigAndRecordsState(t *testing.T) {
	repoPath, wtPath, id := setupRepo(t)
	envFile := filepath.Join(t.TempDir(), "env")
	sf := authSweatfile("env | grep '^SPINCLASS_' | sort > "+envFile+"; echo tok/123", "true")

	minted, err := Mint(context.Background(), sf, id)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if !minted {
		t.Fatal("Mint reported nothing minted")
	}

	credPath := filepath.Join(wtPath, ".spinclass", CredentialFile)
	info, err := os.Stat(credPath)
	if err != nil {
		t.Fatalf("credential file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credential file mode %o, want 0600", perm)
	}
	raw, _ := os.ReadFile(credPath)
	if got, want := strings.TrimSpace(string(raw)), "https://spinclass:tok%2F123@forge.example.com"; got != want {
		t.Errorf("credential line %q, want %q", got, want)
	}

	if got := runGit(t, wtPath, "config", "--worktree", "--get", "credential.helper"); got != "store --file="+credPath {
		t.Errorf("credential.helper = %q", got)
	}
	if got := runGit(t, wtPath, "config", "--worktree", "--get", "url.https://forge.example.com/.insteadOf"); got != "git@forge.example.com:" {
		t.Errorf("insteadOf = %q", got)
	}
	// Worktree-scoped: the root checkout sees none of it.
	if out, err := exec.Command("git", "-C", repoPath, "config", "--get", "credential.helper").CombinedOutput(); err == nil {
		t.Errorf("root checkout leaked credential.helper: %s", out)
	}

	env, _ := os.ReadFile(envFile)
	for _, want := range []string{
		"SPINCLASS_SESSION_ID=repo/feature-x",
		"SPINCLASS_REPO=repo",
		"SPINCLASS_BRANCH=feature-x",
		"SPINCLASS_WORKTREE=" + wtPath,
		"SPINCLASS_FORGE_HOST=forge.example.com",
		"SPINCLASS_FORGE_REPO=owner/repo",
	} {
		if !strings.Contains(string(env), want) {
			t.Errorf("mint env missing %q:\n%s", want, env)
		}
	}

	st, err := session.Read(repoPath, "feature-x")
	if err != nil {
		t.Fatalf("session.Read: %v", err)
	}
	if st.Credential == nil || st.Credential.MintedAt.IsZero() || st.Credential.RevokedAt != nil {
		t.Errorf("state credential = %+v, want minted and unrevoked", st.Credential)
	}
}

func TestMintEmptyTokenFails(t *testing.T) {
	_, wtPath, id := setupRepo(t)
	if _, err := Mint(context.Background(), authSweatfile("true", "true"), id); err == nil {
		t.Fatal("expected an error for a mint-command that prints nothing")
	}
	if Minted(wtPath) {
		t.Error("credential file written for an empty token")
	}
}

// A later session.Write (the attach/spawn paths write their own State after
// the creation funnel minted) must not drop the mint record.
func TestSessionWriteCarriesCredentialForward(t *testing.T) {
	repoPath, wtPath, id := setupRepo(t)
	if _, err := Mint(context.Background(), authSweatfile("echo tok", "true"), id); err != nil {
		t.Fatalf("Mint: %v", err)
	}
	fresh := session.State{
		PID: 1, SessionState: session.StateActive, RepoPath: repoPath,
		WorktreePath: wtPath, Branch: "feature-x", SessionKey: "repo/feature-x",
	}
	if err := session.Write(fresh); err != nil {
		t.Fatalf("session.Write: %v", err)
	}
	st, _ := session.Read(repoPath, "feature-x")
	if st.Credential == nil {
		t.Fatal("session.Write dropped the credential record")
	}
}

func TestRevokeRunsCommandRemovesFileAndRecords(t *testing.T) {
	repoPath, wtPath, id := setupRepo(t)
	marker := filepath.Join(t.TempDir(), "revoked")
	sf := authSweatfile("echo tok", "echo revoking $SPINCLASS_SESSION_ID; echo $SPINCLASS_SESSION_ID > "+marker)
	if _, err := Mint(context.Background(), sf, id); err != nil {
		t.Fatalf("Mint: %v", err)
	}

	var out strings.Builder
	ran, err := Revoke(context.Background(), sf, id, &out)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !ran {
		t.Fatal("Revoke reported nothing to revoke")
	}
	if got, _ := os.ReadFile(marker); strings.TrimSpace(string(got)) != "repo/feature-x" {
		t.Errorf("revoke-command did not run with the session id: %q", got)
	}
	if !strings.Contains(out.String(), "revoking repo/feature-x") {
		t.Errorf("revoke output not streamed: %q", out.String())
	}
	if Minted(wtPath) {
		t.Error("credential file survived revoke")
	}
	st, _ := session.Read(repoPath, "feature-x")
	if st.Credential == nil || st.Credential.RevokedAt == nil {
		t.Errorf("state credential = %+v, want RevokedAt set", st.Credential)
	}

	// Idempotent: nothing left to revoke.
	if ran, err := Revoke(context.Background(), sf, id, nil); ran || err != nil {
		t.Errorf("second Revoke: ran=%v err=%v", ran, err)
	}
}

func TestRevokeWithoutMintedIsNoop(t *testing.T) {
	_, _, id := setupRepo(t)
	ran, err := Revoke(context.Background(), authSweatfile("echo tok", "false"), id, nil)
	if ran || err != nil {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
}

func TestSweepOrphansRevokesTombstonedSessions(t *testing.T) {
	repoPath, _, id := setupRepo(t)
	marker := filepath.Join(t.TempDir(), "swept")
	sf := authSweatfile("echo tok", "echo $SPINCLASS_SESSION_ID >> "+marker)
	if _, err := Mint(context.Background(), sf, id); err != nil {
		t.Fatalf("Mint: %v", err)
	}
	// The session dies without revoking: tombstoned, worktree removed.
	if err := session.Tombstone(repoPath, "feature-x", ""); err != nil {
		t.Fatalf("Tombstone: %v", err)
	}
	runGit(t, repoPath, "worktree", "remove", "--force", id.WorktreePath)

	revoked, errs := SweepOrphans(context.Background(), sf, repoPath, nil)
	if len(errs) > 0 {
		t.Fatalf("SweepOrphans errors: %v", errs)
	}
	if revoked != 1 {
		t.Fatalf("revoked %d, want 1", revoked)
	}
	if got, _ := os.ReadFile(marker); strings.TrimSpace(string(got)) != "repo/feature-x" {
		t.Errorf("revoke-command not run for the orphan: %q", got)
	}
	st, err := session.Read(repoPath, "feature-x")
	if err != nil {
		t.Fatalf("Read tombstone: %v", err)
	}
	if st.Credential == nil || st.Credential.RevokedAt == nil {
		t.Errorf("tombstone credential = %+v, want RevokedAt", st.Credential)
	}

	// Already swept: a second sweep finds nothing.
	if n, errs := SweepOrphans(context.Background(), sf, repoPath, nil); n != 0 || len(errs) > 0 {
		t.Errorf("second sweep: n=%d errs=%v", n, errs)
	}
}

func TestSweepOrphansLeavesLiveSessionsAlone(t *testing.T) {
	repoPath, _, id := setupRepo(t)
	sf := authSweatfile("echo tok", "false")
	if _, err := Mint(context.Background(), sf, id); err != nil {
		t.Fatalf("Mint: %v", err)
	}
	// Inactive but present (worktree exists): not an orphan.
	if n, errs := SweepOrphans(context.Background(), sf, repoPath, nil); n != 0 || len(errs) > 0 {
		t.Errorf("sweep touched a live session: n=%d errs=%v", n, errs)
	}
}

func TestMirrorIntoWiresAnotherWorktree(t *testing.T) {
	repoPath, wtPath, id := setupRepo(t)
	if _, err := Mint(context.Background(), authSweatfile("echo tok", "true"), id); err != nil {
		t.Fatalf("Mint: %v", err)
	}
	land := filepath.Join(repoPath, ".worktrees", ".land-x")
	runGit(t, repoPath, "worktree", "add", "--detach", land, "HEAD")

	if err := MirrorInto(wtPath, land); err != nil {
		t.Fatalf("MirrorInto: %v", err)
	}
	credPath := filepath.Join(wtPath, ".spinclass", CredentialFile)
	if got := runGit(t, land, "config", "--worktree", "--get", "credential.helper"); got != "store --file="+credPath {
		t.Errorf("landing credential.helper = %q", got)
	}
	if got := runGit(t, land, "config", "--worktree", "--get", "url.https://forge.example.com/.insteadOf"); got != "git@forge.example.com:" {
		t.Errorf("landing insteadOf = %q", got)
	}
}

func TestMirrorIntoWithoutMintIsNoop(t *testing.T) {
	repoPath, wtPath, _ := setupRepo(t)
	land := filepath.Join(repoPath, ".worktrees", ".land-x")
	runGit(t, repoPath, "worktree", "add", "--detach", land, "HEAD")
	if err := MirrorInto(wtPath, land); err != nil {
		t.Fatalf("MirrorInto: %v", err)
	}
	if out, err := exec.Command("git", "-C", land, "config", "--worktree", "--get", "credential.helper").CombinedOutput(); err == nil {
		t.Errorf("landing worktree got a helper without a mint: %s", out)
	}
}
