package spawn

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// fakeAuthorizer answers from fixed errors and records what it was asked and
// whether a worktree already existed when the child link was issued.
type fakeAuthorizer struct {
	authErr    error
	issueErr   error
	grant      string
	authorized []Driver
	issuedFor  []string
	undone     int
	// worktreesAtIssue is the repo's .worktrees listing seen inside IssueChild.
	repoPath         string
	worktreesAtIssue []string
}

func (f *fakeAuthorizer) Authorize(driver Driver, _ ResolvedProfile) (ProfileAuthorization, error) {
	f.authorized = append(f.authorized, driver)
	if f.authErr != nil {
		return ProfileAuthorization{}, f.authErr
	}
	return ProfileAuthorization{GrantDigest: f.grant, IssueChild: func(child string) error {
		f.issuedFor = append(f.issuedFor, child)
		f.worktreesAtIssue, _ = filepath.Glob(filepath.Join(f.repoPath, ".worktrees", "*"))
		return f.issueErr
	}, Undo: func() { f.undone++ }}, nil
}

func withAuthorizer(t *testing.T, a ProfileAuthorizer) {
	t.Helper()
	prev := newProfileAuthorizer
	newProfileAuthorizer = func() ProfileAuthorizer { return a }
	t.Cleanup(func() { newProfileAuthorizer = prev })
}

func forbidAuthorizer(t *testing.T) {
	t.Helper()
	prev := newProfileAuthorizer
	newProfileAuthorizer = func() ProfileAuthorizer {
		t.Fatal("an authorizer was built although enforcement is off or no profile was named")
		return nil
	}
	t.Cleanup(func() { newProfileAuthorizer = prev })
}

// enforceInGlobalSweatfile turns [profile-rights] enforce on from the trusted
// global layer of the fixture's HOME.
func enforceInGlobalSweatfile(t *testing.T, home string) {
	t.Helper()
	path := filepath.Join(home, ".config", "spinclass", "sweatfile")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[profile-rights]\nenforce = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cheapResolver(t *testing.T) {
	t.Helper()
	withResolver(t, &fakeResolver{profiles: map[string]ResolvedProfile{
		"cheap": {Name: "cheap", Digest: "digest-cheap", KeptServers: requiredKept},
	}})
}

func noWorktrees(t *testing.T, repoPath string) {
	t.Helper()
	if matches, _ := filepath.Glob(filepath.Join(repoPath, ".worktrees", "*")); len(matches) != 0 {
		t.Errorf("worktree(s) created despite the refusal: %v", matches)
	}
}

func TestEnforcementOffNeverBuildsTheAuthorizer(t *testing.T) {
	cheapResolver(t)
	forbidAuthorizer(t)

	res, repoPath := launchWithProfile(t, modelSpawnSweatfile, "cheap")
	st, err := session.Read(repoPath, filepath.Base(res.WorktreePath))
	if err != nil {
		t.Fatal(err)
	}
	if st.AuthorizingGrant != "" {
		t.Errorf("AuthorizingGrant = %q with enforcement off", st.AuthorizingGrant)
	}
}

func TestEnforcementAuthorizeErrorRefusesBeforeCreate(t *testing.T) {
	cheapResolver(t)
	fake := &fakeAuthorizer{authErr: errors.New("no profile grant for this session")}
	withAuthorizer(t, fake)
	home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
	enforceInGlobalSweatfile(t, home)

	_, err := Launch(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "cheap", 15*time.Second)
	if err == nil || !strings.Contains(err.Error(), "no profile grant") || !strings.Contains(err.Error(), `"cheap"`) {
		t.Fatalf("err = %v, want the authorizer's refusal naming the profile", err)
	}
	if len(fake.issuedFor) != 0 {
		t.Errorf("IssueChild called after a refused Authorize: %v", fake.issuedFor)
	}
	noWorktrees(t, repoPath)
}

func TestEnforcementIssueChildErrorRefusesBeforeCreate(t *testing.T) {
	cheapResolver(t)
	fake := &fakeAuthorizer{issueErr: errors.New("link does not verify"), grant: "g1"}
	withAuthorizer(t, fake)
	home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
	fake.repoPath = repoPath
	enforceInGlobalSweatfile(t, home)

	_, err := Launch(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "cheap", 15*time.Second)
	if err == nil || !strings.Contains(err.Error(), "link does not verify") {
		t.Fatalf("err = %v", err)
	}
	noWorktrees(t, repoPath)
}

func TestEnforcementSuccessRecordsGrantAndIssuesLinkBeforeTheWorktreeExists(t *testing.T) {
	cheapResolver(t)
	home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
	fake := &fakeAuthorizer{grant: "record-digest-1", repoPath: repoPath}
	withAuthorizer(t, fake)
	enforceInGlobalSweatfile(t, home)

	const driverKey = "driver/test-session"
	stop, helloErr := helloAfterLaunch(t, repoPath, driverKey)
	res, err := Launch(home, repoPath, testDriver(driverKey), "brief", "", "", "cheap", 15*time.Second)
	stop()
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if herr := <-helloErr; herr != nil {
		t.Fatalf("hello goroutine: %v", herr)
	}

	if len(fake.issuedFor) != 1 || fake.issuedFor[0] != res.SessionKey {
		t.Errorf("IssueChild called with %v, want [%s]", fake.issuedFor, res.SessionKey)
	}
	if len(fake.worktreesAtIssue) != 0 {
		t.Errorf("worktree existed when the child link was issued: %v", fake.worktreesAtIssue)
	}
	if len(fake.authorized) != 1 || fake.authorized[0].Principal != driverKey {
		t.Errorf("Authorize driver = %+v", fake.authorized)
	}
	st, err := session.Read(repoPath, filepath.Base(res.WorktreePath))
	if err != nil {
		t.Fatal(err)
	}
	if st.AuthorizingGrant != "record-digest-1" || st.Profile != "cheap" {
		t.Errorf("state = profile %q grant %q", st.Profile, st.AuthorizingGrant)
	}
}

func TestEnforcementDoesNotAffectASpawnWithoutAProfile(t *testing.T) {
	withResolver(t, &fakeResolver{})
	forbidAuthorizer(t)
	home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
	enforceInGlobalSweatfile(t, home)

	const driverKey = "driver/test-session"
	stop, helloErr := helloAfterLaunch(t, repoPath, driverKey)
	_, err := Launch(home, repoPath, testDriver(driverKey), "brief", "", "", "", 15*time.Second)
	stop()
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if herr := <-helloErr; herr != nil {
		t.Fatalf("hello goroutine: %v", herr)
	}
}

func TestRepoLayerCannotTurnEnforcementOff(t *testing.T) {
	cheapResolver(t)
	fake := &fakeAuthorizer{authErr: errors.New("nope")}
	withAuthorizer(t, fake)
	home, repoPath := newWorkerFixture(t, modelSpawnSweatfile+"\n[profile-rights]\nenforce = false\n")
	enforceInGlobalSweatfile(t, home)

	if _, err := Launch(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "cheap", 15*time.Second); err == nil {
		t.Fatal("a repo-layer enforce = false disabled enforcement")
	}
	if len(fake.authorized) != 1 {
		t.Errorf("authorizer consulted %d times, want 1", len(fake.authorized))
	}
}

func TestRepoLayerCannotTurnEnforcementOn(t *testing.T) {
	cheapResolver(t)
	forbidAuthorizer(t)
	// Only the repo layer asks for enforcement, which an agent could also have
	// written: it is ignored, so the spawn proceeds as in the default.
	launchWithProfile(t, modelSpawnSweatfile+"\n[profile-rights]\nenforce = true\n", "cheap")
}

func TestEnforcementRefusesAPrincipallessDriver(t *testing.T) {
	cheapResolver(t)
	forbidAuthorizer(t)
	home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
	enforceInGlobalSweatfile(t, home)

	_, err := Launch(home, repoPath, Driver{SessionKey: "driver/test-session"}, "brief", "", "", "cheap", 15*time.Second)
	if err == nil || !strings.Contains(err.Error(), "no principal") {
		t.Fatalf("err = %v", err)
	}
	noWorktrees(t, repoPath)
}

func writeSweatfile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sweatfile"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const enforceFalse = "[profile-rights]\nenforce = false\n"

func TestAncestorLayerFalseCannotDisableTheGlobalTrue(t *testing.T) {
	cheapResolver(t)
	fake := &fakeAuthorizer{authErr: errors.New("nope")}
	withAuthorizer(t, fake)
	home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
	enforceInGlobalSweatfile(t, home)
	writeSweatfile(t, filepath.Dir(repoPath), enforceFalse)
	writeSweatfile(t, filepath.Dir(filepath.Dir(repoPath)), enforceFalse)

	if _, err := Launch(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "cheap", 15*time.Second); err == nil {
		t.Fatal("false ancestor layers disabled enforcement")
	}
	if len(fake.authorized) != 1 {
		t.Errorf("authorizer consulted %d times, want 1", len(fake.authorized))
	}
}

// An agent controls the target path: a symlink into a directory it owns (with
// its own enforce = false ancestor) must not escape the global true.
func TestRelocatedTargetRepoCannotEscapeTheGlobalTrue(t *testing.T) {
	cheapResolver(t)
	fake := &fakeAuthorizer{authErr: errors.New("nope")}
	withAuthorizer(t, fake)
	home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
	enforceInGlobalSweatfile(t, home)

	scratch := t.TempDir() // outside HOME, writable by the "agent"
	writeSweatfile(t, scratch, enforceFalse)
	link := filepath.Join(scratch, "relocated")
	if err := os.Symlink(repoPath, link); err != nil {
		t.Fatal(err)
	}

	if _, err := Launch(home, link, testDriver("driver/test-session"), "brief", "", "", "cheap", 15*time.Second); err == nil {
		t.Fatal("a relocated target repo escaped enforcement")
	}
	if len(fake.authorized) != 1 {
		t.Errorf("authorizer consulted %d times, want 1", len(fake.authorized))
	}
}

// A parent-scoped true covers the driver's repo but not a target outside it;
// the driver's context must carry the switch to that target.
func TestDriverRepoInsideAScopedTrueEnforcesAnOutsideTarget(t *testing.T) {
	cheapResolver(t)
	fake := &fakeAuthorizer{authErr: errors.New("nope")}
	withAuthorizer(t, fake)
	home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
	writeSweatfile(t, filepath.Join(home, "work"), "[profile-rights]\nenforce = true\n")
	driverRepo := filepath.Join(home, "work", "driverrepo")
	if err := os.MkdirAll(driverRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.MustInit(t, driverRepo)
	prev := driverCwd
	driverCwd = func() (string, error) { return filepath.Join(driverRepo, "sub"), nil }
	t.Cleanup(func() { driverCwd = prev })

	if _, err := Launch(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "cheap", 15*time.Second); err == nil {
		t.Fatal("a target outside the scoped true was not enforced")
	}
	if len(fake.authorized) != 1 {
		t.Errorf("authorizer consulted %d times, want 1", len(fake.authorized))
	}
}

func TestDriverOutsideTheScopedTrueDoesNotEnforceAnOutsideTarget(t *testing.T) {
	cheapResolver(t)
	forbidAuthorizer(t)
	home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
	writeSweatfile(t, filepath.Join(home, "work"), "[profile-rights]\nenforce = true\n")
	_ = repoPath // target and driver (home) are both outside work/
	stop, helloErr := helloAfterLaunch(t, repoPath, "driver/test-session")
	_, err := Launch(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "cheap", 15*time.Second)
	stop()
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if herr := <-helloErr; herr != nil {
		t.Fatalf("hello goroutine: %v", herr)
	}
}

const embeddedProfileSweatfile = `[session-entry]
spawn-entry = ["sh", "-c", 'printf "%s\n" "$@" > "$PWD/argv.txt"; touch "$PWD/launched"', "sh", "--profile", "sneaky", "--", "{prompt}"]
`

func TestEnforcementRefusesAnEmbeddedProfileWhenNoneWasRequested(t *testing.T) {
	cheapResolver(t)
	forbidAuthorizer(t)
	for _, entry := range []string{
		`"--profile", "sneaky"`, `"--profile=sneaky"`, `"-profile", "sneaky"`, `"-profile=sneaky"`,
	} {
		home, repoPath := newWorkerFixture(t, `[session-entry]
spawn-entry = ["sh", `+entry+`, "--", "{prompt}"]
`)
		enforceInGlobalSweatfile(t, home)
		_, err := Launch(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "", 15*time.Second)
		if err == nil || !strings.Contains(err.Error(), "spawn-entry") || !strings.Contains(err.Error(), "explicitly") {
			t.Fatalf("entry %s: err = %v, want a refusal naming the spawn-entry", entry, err)
		}
		noWorktrees(t, repoPath)
	}
}

func TestEmbeddedProfileAfterTheSeparatorIsNotAClownFlag(t *testing.T) {
	if got := embeddedProfileFlag([]string{"clown", "--", "--profile", "x"}); got != "" {
		t.Errorf("a provider-arg after -- reported as %q", got)
	}
}

func TestRequestedProfileReplacesAnEmbeddedOneInTheFinalArgv(t *testing.T) {
	cheapResolver(t)
	home, repoPath := newWorkerFixture(t, embeddedProfileSweatfile)
	enforceInGlobalSweatfile(t, home)
	fake := &fakeAuthorizer{grant: "g", repoPath: repoPath}
	withAuthorizer(t, fake)

	stop, helloErr := helloAfterLaunch(t, repoPath, "driver/test-session")
	res, err := Launch(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "cheap", 15*time.Second)
	stop()
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if herr := <-helloErr; herr != nil {
		t.Fatalf("hello goroutine: %v", herr)
	}
	// The auto-mode splice keys off the entry's provider, which the embedded
	// profile may change, so only the profile flags are pinned here.
	got := launchedArgv(t, res.WorktreePath)
	if slices.Contains(got, "sneaky") {
		t.Errorf("argv = %v, still carries the embedded profile", got)
	}
	if !slices.Equal(got[:3], []string{"--profile", "cheap", "--"}) {
		t.Errorf("argv = %v, want it to lead with exactly the authorized profile", got)
	}
}

func TestEnforcementRefusesAResolverThatRenamesTheProfile(t *testing.T) {
	withResolver(t, &fakeResolver{profiles: map[string]ResolvedProfile{
		"cheap": {Name: "other", Digest: "d", KeptServers: requiredKept},
	}})
	forbidAuthorizer(t)
	home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
	enforceInGlobalSweatfile(t, home)
	_, err := Launch(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "cheap", 15*time.Second)
	if err == nil || !strings.Contains(err.Error(), `"other"`) {
		t.Fatalf("err = %v", err)
	}
	noWorktrees(t, repoPath)
}

func TestFailedWorktreeCreationUndoesTheMintedLink(t *testing.T) {
	cheapResolver(t)
	fake := &fakeAuthorizer{grant: "g"}
	withAuthorizer(t, fake)
	home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
	enforceInGlobalSweatfile(t, home)
	// A read-only worktrees directory makes creation fail after the link has
	// been minted. (A regular file in its place would make RandomName's
	// os.Stat report ENOTDIR forever and hang the name search.)
	wtDir := filepath.Join(repoPath, ".worktrees")
	if err := os.MkdirAll(wtDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(wtDir, 0o700) })
	if _, err := Launch(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "cheap", 15*time.Second); err == nil {
		t.Fatal("worktree creation unexpectedly succeeded")
	}
	if len(fake.issuedFor) != 1 || fake.undone != 1 {
		t.Errorf("issued %v, undone %d; want one link minted and undone", fake.issuedFor, fake.undone)
	}
}

func TestFailedEntryStartUndoesTheMintedLink(t *testing.T) {
	cheapResolver(t)
	fake := &fakeAuthorizer{grant: "g"}
	withAuthorizer(t, fake)
	home, repoPath := newWorkerFixture(t, `[session-entry]
spawn-entry = ["/nonexistent/spawn-entry-binary", "--", "{prompt}"]
`)
	enforceInGlobalSweatfile(t, home)
	if _, err := Launch(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "cheap", 15*time.Second); err == nil {
		t.Fatal("a missing entry binary unexpectedly started")
	}
	if len(fake.issuedFor) != 1 || fake.undone != 1 {
		t.Errorf("issued %v, undone %d; want one link minted and undone", fake.issuedFor, fake.undone)
	}
}

func TestSuccessfulSpawnKeepsTheMintedLink(t *testing.T) {
	cheapResolver(t)
	home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
	fake := &fakeAuthorizer{grant: "g", repoPath: repoPath}
	withAuthorizer(t, fake)
	enforceInGlobalSweatfile(t, home)
	stop, helloErr := helloAfterLaunch(t, repoPath, "driver/test-session")
	_, err := Launch(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "cheap", 15*time.Second)
	stop()
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-helloErr
	if fake.undone != 0 {
		t.Errorf("link undone %d times after a successful spawn", fake.undone)
	}
}

// TestProductionAuthorizerRefusesWhileTheCardRootIsUnestablished pins the
// production default: with no confined unit (every host today, and this test's
// driver has no session at all) turning the switch on refuses every profile
// spawn, with the confinement reason, and creates nothing.
func TestProductionAuthorizerRefusesWhileTheCardRootIsUnestablished(t *testing.T) {
	cheapResolver(t)
	home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
	enforceInGlobalSweatfile(t, home)

	_, err := Launch(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "cheap", 15*time.Second)
	if err == nil {
		t.Fatal("the production authorizer allowed a spawn")
	}
	for _, want := range []string{"cannot be card-rooted", "session confinement (FDR 0035)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	noWorktrees(t, repoPath)
	// The refusal precedes opening the store: nothing was created on disk.
	if _, statErr := os.Stat(filepath.Join(home, ".local", "state", "spinclass", "grants")); statErr == nil {
		t.Error("the grant store was created before the confinement check refused")
	}
}
