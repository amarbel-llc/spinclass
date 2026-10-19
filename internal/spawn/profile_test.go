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
	"code.linenisgreat.com/spinclass/internal/worktree"
)

// fakeResolver records the names it is asked about and answers from a table.
type fakeResolver struct {
	profiles map[string]ResolvedProfile
	asked    []string
}

func (f *fakeResolver) Resolve(name string) (ResolvedProfile, error) {
	f.asked = append(f.asked, name)
	p, ok := f.profiles[name]
	if !ok {
		return ResolvedProfile{}, ProfileNotFound(name)
	}
	return p, nil
}

// withResolver installs r as the package resolver for one test.
func withResolver(t *testing.T, r ProfileResolver) {
	t.Helper()
	prev := profileResolver
	profileResolver = r
	t.Cleanup(func() { profileResolver = prev })
}

func TestSpliceProfileFlag(t *testing.T) {
	cases := []struct {
		name  string
		entry []string
		want  []string
	}{
		{
			name:  "placed before the separator",
			entry: []string{"clown", "--clown-attach=spawn", "--", "{prompt}"},
			want:  []string{"clown", "--clown-attach=spawn", "--profile", "cheap", "--", "{prompt}"},
		},
		{
			name:  "replaces space form",
			entry: []string{"clown", "--profile", "old", "--clown-attach=spawn", "--", "{prompt}"},
			want:  []string{"clown", "--clown-attach=spawn", "--profile", "cheap", "--", "{prompt}"},
		},
		{
			name:  "replaces equals form",
			entry: []string{"clown", "--profile=old", "--", "{prompt}"},
			want:  []string{"clown", "--profile", "cheap", "--", "{prompt}"},
		},
		{
			name:  "leaves --profile after the separator alone",
			entry: []string{"clown", "--", "--profile", "provider-arg"},
			want:  []string{"clown", "--profile", "cheap", "--", "--profile", "provider-arg"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			orig := slices.Clone(c.entry)
			got, err := SpliceProfileFlag(c.entry, "cheap")
			if err != nil {
				t.Fatalf("SpliceProfileFlag: %v", err)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
			if !slices.Equal(c.entry, orig) {
				t.Errorf("input mutated: %v, was %v", c.entry, orig)
			}
		})
	}
}

func TestSpliceProfileFlagNoSeparatorErrors(t *testing.T) {
	_, err := SpliceProfileFlag([]string{"clown", "{prompt}"}, "cheap")
	if err == nil || !strings.Contains(err.Error(), `"--"`) {
		t.Fatalf("err = %v, want a missing-separator error", err)
	}
}

func TestSpliceProfileFlagEmptyProfileIsNoOp(t *testing.T) {
	entry := []string{"clown", "{prompt}"} // no separator: must not error without a profile
	got, err := SpliceProfileFlag(entry, "")
	if err != nil || !slices.Equal(got, entry) {
		t.Errorf("got %v, %v; want entry unchanged, nil", got, err)
	}
}

func TestRequiredServersKept(t *testing.T) {
	if err := RequiredServersKept([]string{"troupe", "ringmaster", "extra"}, requiredServers); err != nil {
		t.Errorf("all kept: unexpected error %v", err)
	}
	err := RequiredServersKept([]string{"troupe"}, requiredServers)
	if err == nil || !strings.Contains(err.Error(), `"ringmaster"`) {
		t.Errorf("err = %v, want it to name the missing ringmaster", err)
	}
}

func TestUnavailableResolverNamesTheMissingLibrary(t *testing.T) {
	_, err := unavailableResolver{}.Resolve("cheap")
	if err == nil || !strings.Contains(err.Error(), "clown#264") || !strings.Contains(err.Error(), "cheap") {
		t.Errorf("err = %v, want it to name the profile and clown#264", err)
	}
}

// launchedArgv returns the $@ the modelSpawnSweatfile entry recorded.
func launchedArgv(t *testing.T, worktreePath string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(worktreePath, "argv.txt"))
	if err != nil {
		t.Fatalf("reading argv.txt: %v", err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func launchWithProfile(t *testing.T, sweatfileTOML, profile string) (Result, string) {
	t.Helper()
	home, repoPath := newWorkerFixture(t, sweatfileTOML)
	const driverKey = "driver/test-session"
	stop, helloErr := helloAfterLaunch(t, repoPath, driverKey)
	res, err := Launch(home, repoPath, testDriver(driverKey), "brief", "", "", profile, 15*time.Second)
	stop()
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if herr := <-helloErr; herr != nil {
		t.Fatalf("hello goroutine: %v", herr)
	}
	return res, repoPath
}

func TestLaunchProfileSplicedAndRecorded(t *testing.T) {
	r := &fakeResolver{profiles: map[string]ResolvedProfile{
		"cheap": {Name: "cheap", Digest: "digest-cheap", KeptServers: []string{"troupe", "ringmaster"}},
	}}
	withResolver(t, r)

	res, repoPath := launchWithProfile(t, modelSpawnSweatfile, "cheap")

	want := []string{"--profile", "cheap", "--", "--enable-auto-mode", "brief"}
	if got := launchedArgv(t, res.WorktreePath); !slices.Equal(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
	}
	st, err := session.Read(repoPath, filepath.Base(res.WorktreePath))
	if err != nil {
		t.Fatal(err)
	}
	if st.Profile != "cheap" || st.ProfileDigest != "digest-cheap" {
		t.Errorf("state profile = %q/%q, want cheap/digest-cheap", st.Profile, st.ProfileDigest)
	}
}

const spawnProfileDefaultSweatfile = modelSpawnSweatfile + `spawn-profile = "review"
`

func TestLaunchUsesSweatfileSpawnProfileDefault(t *testing.T) {
	r := &fakeResolver{profiles: map[string]ResolvedProfile{
		"review": {Digest: "digest-review"},
		"cheap":  {Digest: "digest-cheap"},
	}}
	withResolver(t, r)

	res, repoPath := launchWithProfile(t, spawnProfileDefaultSweatfile, "")
	if got := launchedArgv(t, res.WorktreePath); !slices.Contains(got, "review") {
		t.Errorf("argv = %v, want the sweatfile default profile review", got)
	}
	st, err := session.Read(repoPath, filepath.Base(res.WorktreePath))
	if err != nil {
		t.Fatal(err)
	}
	if st.Profile != "review" {
		t.Errorf("state Profile = %q, want review (Name defaults to the requested name)", st.Profile)
	}

	// An explicit parameter overrides the default.
	res2, _ := launchWithProfile(t, spawnProfileDefaultSweatfile, "cheap")
	if got := launchedArgv(t, res2.WorktreePath); !slices.Contains(got, "cheap") || slices.Contains(got, "review") {
		t.Errorf("argv = %v, want explicit cheap to override the default", got)
	}
}

func TestLaunchWithoutProfileNeverCallsResolver(t *testing.T) {
	r := &fakeResolver{}
	withResolver(t, r)

	res, repoPath := launchWithProfile(t, modelSpawnSweatfile, "")
	if len(r.asked) != 0 {
		t.Errorf("resolver consulted without a profile: %v", r.asked)
	}
	want := []string{"--", "--enable-auto-mode", "brief"}
	if got := launchedArgv(t, res.WorktreePath); !slices.Equal(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
	}
	st, err := session.Read(repoPath, filepath.Base(res.WorktreePath))
	if err != nil {
		t.Fatal(err)
	}
	if st.Profile != "" || st.ProfileDigest != "" {
		t.Errorf("state profile = %q/%q, want empty", st.Profile, st.ProfileDigest)
	}
}

func TestLaunchProfileRefusalsLeaveNoWorktree(t *testing.T) {
	cases := []struct {
		name     string
		resolver ProfileResolver
		wantErr  func(error) bool
	}{
		{
			name:     "unknown profile",
			resolver: &fakeResolver{},
			wantErr:  func(err error) bool { return errors.Is(err, ErrProfileNotFound) },
		},
		{
			name: "drops a required server",
			resolver: &fakeResolver{profiles: map[string]ResolvedProfile{
				"cheap": {Digest: "d", KeptServers: []string{"troupe"}},
			}},
			wantErr: func(err error) bool { return strings.Contains(err.Error(), "ringmaster") },
		},
		{
			name:     "default resolver is unavailable",
			resolver: unavailableResolver{},
			wantErr:  func(err error) bool { return strings.Contains(err.Error(), "clown#264") },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withResolver(t, c.resolver)
			home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)

			_, err := Launch(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "cheap", 15*time.Second)
			if err == nil {
				t.Fatal("Launch() = nil error, want a refusal")
			}
			if !c.wantErr(err) {
				t.Errorf("unexpected error: %v", err)
			}
			if matches, _ := filepath.Glob(filepath.Join(repoPath, ".worktrees", "*")); len(matches) != 0 {
				t.Errorf("worktree(s) created despite the refusal: %v", matches)
			}
		})
	}
}

func TestLaunchProfileWithoutSeparatorErrorsBeforeCreate(t *testing.T) {
	withResolver(t, &fakeResolver{profiles: map[string]ResolvedProfile{"cheap": {}}})
	home, repoPath := newWorkerFixture(t, happySweatfile) // no "--" in the entry

	_, err := Launch(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "cheap", 15*time.Second)
	if err == nil || !strings.Contains(err.Error(), `"--"`) {
		t.Fatalf("err = %v, want a missing-separator error", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(repoPath, ".worktrees", "*")); len(matches) != 0 {
		t.Errorf("worktree(s) created despite the error: %v", matches)
	}
}

func envHas(env []string, key string) (string, bool) {
	for _, e := range slices.Backward(env) { // last occurrence wins, as in os/exec
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			return v, true
		}
	}
	return "", false
}

func TestWorkerEnvScrubsDesktopVarsForHarnessOnly(t *testing.T) {
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
	t.Setenv("SSH_AUTH_SOCK", "/run/agent.sock")
	t.Setenv("SSH_ASKPASS", "/usr/bin/askpass")
	rp := worktree.ResolvedPath{SessionKey: "repo/branch", AbsPath: t.TempDir()}

	harness := workerEnv(rp, "d", nil, true)
	for _, k := range harnessScrubbedEnvVars {
		if v, found := envHas(harness, k); found {
			t.Errorf("harness env still carries %s=%q", k, v)
		}
	}
	for k, want := range map[string]string{"SSH_AUTH_SOCK": "/run/agent.sock", "SSH_ASKPASS": "/usr/bin/askpass"} {
		if got, _ := envHas(harness, k); got != want {
			t.Errorf("harness %s = %q, want %q kept", k, got, want)
		}
	}

	window := workerEnv(rp, "d", nil, false)
	if got, _ := envHas(window, "DISPLAY"); got != ":0" {
		t.Errorf("window DISPLAY = %q, want :0 (a window needs a display)", got)
	}
	if got, _ := envHas(window, "DBUS_SESSION_BUS_ADDRESS"); got == "" {
		t.Error("window lost DBUS_SESSION_BUS_ADDRESS")
	}
}

func TestWorkerEnvUserEnvSetsScrubbedVarsBack(t *testing.T) {
	t.Setenv("DISPLAY", ":0")
	rp := worktree.ResolvedPath{SessionKey: "repo/branch", AbsPath: t.TempDir()}

	env := workerEnv(rp, "d", map[string]string{"DISPLAY": ":7"}, true)
	if got, found := envHas(env, "DISPLAY"); !found || got != ":7" {
		t.Errorf("DISPLAY = %q (found=%v), want the explicit [session-entry].env value :7", got, found)
	}
}

func TestDropEnvVarsMatchesWholeKeys(t *testing.T) {
	got := dropEnvVars([]string{"DISPLAY=:0", "DISPLAYX=keep", "A=DISPLAY=b"}, []string{"DISPLAY"})
	want := []string{"DISPLAYX=keep", "A=DISPLAY=b"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
