package spawn

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/session"
)

// fakeAuthorizer answers from fixed errors and records what it was asked and
// whether a worktree already existed when the child link was issued.
type fakeAuthorizer struct {
	authErr    error
	issueErr   error
	grant      string
	authorized []Driver
	issuedFor  []string
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
	}}, nil
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

// TestProductionAuthorizerRefusesWhileTheCardRootIsUnestablished pins the
// production default: turning the switch on today refuses every profile spawn,
// with the reason, and creates nothing.
func TestProductionAuthorizerRefusesWhileTheCardRootIsUnestablished(t *testing.T) {
	cheapResolver(t)
	home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
	enforceInGlobalSweatfile(t, home)

	_, err := Launch(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "cheap", 15*time.Second)
	if err == nil {
		t.Fatal("the production authorizer allowed a spawn")
	}
	for _, want := range []string{"cannot be card-rooted", "spinclass#338", "clown#244", "posh"} {
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
