package spawn

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/confinement"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/worktree"
)

// confinedGlobalSweatfile turns confinement on from the global (trusted) layer.
const confinedGlobalSweatfile = "[confinement]\nenabled = true\n"

// goodConfinedProfile is the one accepted form: selective, moxy dropped whole,
// just-us kept, plus the servers a worker needs to say hello.
func goodConfinedProfile() ResolvedProfile {
	return ResolvedProfile{
		Name: "locked", Digest: "digest-locked", Source: "builtin",
		KeptServers: append(slices.Clone(requiredKept), justUsServer),
	}
}

func writeGlobalSweatfile(t *testing.T, home, toml string) {
	t.Helper()
	path := filepath.Join(home, ".config", "spinclass", "sweatfile")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stopAtPreflight makes confinedPreflight refuse deterministically (the host
// looks like it is inside an agent unit). A confined spawn that gets past the
// profile checks then stops there, still before anything is created, so a test
// can tell "the profile checks passed" from "a profile check refused".
func stopAtPreflight(t *testing.T) {
	t.Helper()
	prev := confinementHost
	confinementHost = cgroupSystem{cgroup: "/user.slice/user@1000.service/" + confinement.AgentSlice + "/spinclass-agent-x.service"}
	t.Cleanup(func() { confinementHost = prev })
}

func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func noWorktree(t *testing.T, repoPath string) {
	t.Helper()
	if matches, _ := filepath.Glob(filepath.Join(repoPath, ".worktrees", "*")); len(matches) != 0 {
		t.Errorf("worktree(s) created despite the refusal: %v", matches)
	}
}

func TestCheckConfinedProfile(t *testing.T) {
	kept := func(servers ...string) []string { return append(slices.Clone(requiredKept), servers...) }
	cases := []struct {
		name    string
		rp      ResolvedProfile
		wantErr []string // substrings; nil means accepted
	}{
		{name: "moxy dropped and just-us kept", rp: goodConfinedProfile()},
		{
			name:    "user source without a base is refused",
			rp:      ResolvedProfile{Name: "locked", Source: "user", KeptServers: kept(justUsServer)},
			wantErr: []string{"declares no base builtin"},
		},
		{
			name:    "non-selective keeps everything",
			rp:      ResolvedProfile{Name: "locked", Digest: "d1", Source: "builtin"},
			wantErr: []string{`"locked"`, "d1", "keeps every server", moxyServer, justUsServer},
		},
		{
			name:    "moxy kept",
			rp:      ResolvedProfile{Name: "locked", Source: "builtin", KeptServers: kept(moxyServer, justUsServer)},
			wantErr: []string{"keeps the " + `"moxy/moxy"`, moxyServer + ", " + justUsServer},
		},
		{
			name:    "moxy kept with per-tool exclusions is still kept",
			rp:      ResolvedProfile{Name: "locked", Source: "builtin", KeptServers: kept(justUsServer, moxyServer)},
			wantErr: []string{"keeps the " + `"moxy/moxy"`, "per-tool exclusions"},
		},
		{
			name:    "just-us dropped",
			rp:      ResolvedProfile{Name: "locked", Source: "builtin", KeptServers: kept()},
			wantErr: []string{"drops the " + `"just-us/just-us"`, "run_recipe", strings.Join(requiredKept, ", ")},
		},
		{
			name:    "dynamic source",
			rp:      ResolvedProfile{Name: "locked", Digest: "d2", Source: "dynamic", KeptServers: kept(justUsServer)},
			wantErr: []string{`"dynamic"`, "d2", "until a verified grant names its digest"},
		},
		{
			name:    "unknown source",
			rp:      ResolvedProfile{Name: "locked", Digest: "d3", KeptServers: kept(justUsServer)},
			wantErr: []string{`source ""`, "d3", "until a verified grant names its digest"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withResolver(t, &fakeResolver{})
			link, err := checkConfinedProfile("locked", c.rp)
			if c.wantErr == nil {
				if link != nil {
					t.Errorf("a builtin profile returned a narrowing link %+v", link)
				}
				if err != nil {
					t.Fatalf("checkConfinedProfile = %v, want accepted", err)
				}
				return
			}
			if err == nil {
				t.Fatal("checkConfinedProfile = nil, want a refusal")
			}
			for _, want := range c.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not contain %q", err, want)
				}
			}
		})
	}
}

// narrowableBase is a builtin that keeps moxy, just-us and an extra server, so a
// user profile can narrow it by dropping moxy whole (R3).
func narrowableBase() ResolvedProfile {
	return ResolvedProfile{
		Name: "base-b", Digest: "digest-base", Source: "builtin", Provider: "claude", Model: "opus",
		KeptServers: append(slices.Clone(requiredKept), moxyServer, justUsServer, "extra/extra"),
		EnvKnown:    true,
	}
}

func narrowingUserProfile() ResolvedProfile {
	return ResolvedProfile{
		Name: "mine", Digest: "digest-mine", Source: "user", Base: "base-b", Provider: "claude", Model: "opus",
		KeptServers: append(slices.Clone(requiredKept), justUsServer),
		EnvKnown:    true,
	}
}

func TestCheckConfinedProfileUserNarrowing(t *testing.T) {
	const wantNoBase = `spawn refused for a confined worker: profile "mine" (digest digest-mine, source "user") declares no base builtin, ` +
		`so it cannot be checked as a narrowing of one; a user profile is accepted for a confined spawn only when it narrows ` +
		`a builtin it names as its base (clown does not record a base yet)`
	cases := []struct {
		name     string
		child    func(*ResolvedProfile)
		base     func(*ResolvedProfile)
		wantErr  []string
		wantExac string
	}{
		{name: "no base", child: func(c *ResolvedProfile) { c.Base = "" }, wantExac: wantNoBase},
		{name: "narrows its base", child: func(*ResolvedProfile) {}},
		{
			name:  "widens its base",
			child: func(c *ResolvedProfile) { c.KeptServers = append(c.KeptServers, "other/other") },
			wantExac: `spawn refused for a confined worker: profile "mine" widens base "base-b" by: ` +
				`adds server other/other`,
		},
		{
			name:  "several widenings are all named",
			child: func(c *ResolvedProfile) { c.Model = "haiku"; c.KeptServers = append(c.KeptServers, "other/other") },
			wantExac: `spawn refused for a confined worker: profile "mine" widens base "base-b" by: ` +
				`changes model opus to haiku; adds server other/other`,
		},
		{
			name:    "base is not a builtin",
			base:    func(b *ResolvedProfile) { b.Source = "user" },
			wantErr: []string{`base "base-b"`, `source "user"`},
		},
		{
			name:    "narrows but still keeps moxy (R3)",
			child:   func(c *ResolvedProfile) { c.KeptServers = append(c.KeptServers, moxyServer) },
			wantErr: []string{"keeps the " + `"moxy/moxy"`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base, child := narrowableBase(), narrowingUserProfile()
			if c.base != nil {
				c.base(&base)
			}
			if c.child != nil {
				c.child(&child)
			}
			r := &fakeResolver{profiles: map[string]ResolvedProfile{"base-b": base}}
			withResolver(t, r)
			link, err := checkConfinedProfile("mine", child)
			if c.wantExac == "" && c.wantErr == nil {
				if err != nil {
					t.Fatalf("checkConfinedProfile = %v, want accepted", err)
				}
				if link == nil || link.Name != "base-b" || link.Digest != "digest-base" {
					t.Errorf("link = %+v, want base-b / digest-base", link)
				}
				return
			}
			if err == nil {
				t.Fatal("checkConfinedProfile = nil, want a refusal")
			}
			if link != nil {
				t.Errorf("a refusal returned a link %+v", link)
			}
			if c.wantExac != "" && err.Error() != c.wantExac {
				t.Errorf("refusal\n got: %q\nwant: %q", err, c.wantExac)
			}
			for _, want := range c.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not contain %q", err, want)
				}
			}
		})
	}
}

// TestConfinedLaunchUserProfileNarrowing: through LaunchDetachedWith, a widening
// or base-less user profile is refused before any worktree exists, and a
// narrowing one gets past the profile checks (to the stubbed preflight).
func TestConfinedLaunchUserProfileNarrowing(t *testing.T) {
	cases := []struct {
		name    string
		child   func(*ResolvedProfile)
		wantErr string
		wantOK  bool
	}{
		{name: "no base", child: func(c *ResolvedProfile) { c.Base = "" }, wantErr: "declares no base builtin"},
		{name: "widens", child: func(c *ResolvedProfile) { c.Model = "haiku" }, wantErr: `widens base "base-b" by: changes model opus to haiku`},
		{name: "narrows", wantOK: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			child := narrowingUserProfile()
			if c.child != nil {
				c.child(&child)
			}
			r := &fakeResolver{profiles: map[string]ResolvedProfile{"mine": child, "base-b": narrowableBase()}}
			withResolver(t, r)
			stopAtPreflight(t)
			home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
			writeGlobalSweatfile(t, home, confinedGlobalSweatfile)

			_, err := LaunchDetachedWith(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "mine", ConfineRequire)
			if err == nil {
				t.Fatal("LaunchDetachedWith = nil error, want a refusal")
			}
			if c.wantOK {
				if !strings.Contains(err.Error(), "inside an agent unit") {
					t.Fatalf("err = %v, want the stubbed preflight refusal (the profile checks passed)", err)
				}
			} else {
				if strings.Contains(err.Error(), "inside an agent unit") || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want a profile refusal containing %q", err, c.wantErr)
				}
			}
			noWorktree(t, repoPath)
		})
	}
}

// TestNewWorkerStateRecordsTheNarrowingLink: the accepted narrowing is recorded,
// unsigned, in the worker's pre-hello state; without one the field is absent.
func TestNewWorkerStateRecordsTheNarrowingLink(t *testing.T) {
	rp := worktree.ResolvedPath{SessionKey: "worker/w", RepoPath: "/r", AbsPath: "/r/.worktrees/w", Branch: "w"}
	child := narrowingUserProfile()

	st := newWorkerState(rp, testDriver("driver/d"), "desc", child, "", &narrowingBase{Name: "base-b", Digest: "digest-base"})
	want := &session.ProfileNarrowingLink{
		Grantee: "worker/w", Profile: "mine", ProfileDigest: "digest-mine",
		Base: "base-b", Prev: "digest-base", Unsigned: true,
	}
	if st.ProfileNarrowing == nil || *st.ProfileNarrowing != *want {
		t.Errorf("ProfileNarrowing = %+v, want %+v", st.ProfileNarrowing, want)
	}

	if st := newWorkerState(rp, testDriver("driver/d"), "desc", goodConfinedProfile(), "", nil); st.ProfileNarrowing != nil {
		t.Errorf("a builtin profile recorded a narrowing link: %+v", st.ProfileNarrowing)
	}
}

// TestConfinedLaunchProfileRefusalsLeaveNoWorktree drives LaunchDetachedWith for a
// spawn that resolves to confined and asserts each refusal comes before any
// worktree exists, and before confinedPreflight (which stopAtPreflight would
// otherwise answer with its own refusal).
func TestConfinedLaunchProfileRefusalsLeaveNoWorktree(t *testing.T) {
	cases := []struct {
		name       string
		repoTOML   string // appended to modelSpawnSweatfile
		globalTOML string // appended to confinedGlobalSweatfile
		profile    string
		profiles   map[string]ResolvedProfile
		wantErr    []string
		wantAsked  []string
		wantWarn   bool
	}{
		{
			name:    "R1 no profile anywhere",
			wantErr: []string{"requires a clown profile", "pass profile", "[session-entry] spawn-profile", "above the repo"},
		},
		{
			name:      "R2 a repo-layer default is ignored and warned",
			repoTOML:  `spawn-profile = "locked"` + "\n",
			profiles:  map[string]ResolvedProfile{"locked": goodConfinedProfile()},
			wantErr:   []string{"requires a clown profile"},
			wantAsked: nil,
			wantWarn:  true,
		},
		{
			name:       "R2 a repo-layer default does not override a trusted one",
			repoTOML:   `spawn-profile = "everything"` + "\n",
			globalTOML: "[session-entry]\nspawn-profile = \"locked\"\n",
			profiles: map[string]ResolvedProfile{
				"locked":     {Name: "locked", Digest: "d", Source: "builtin", KeptServers: append(slices.Clone(requiredKept), moxyServer, justUsServer)},
				"everything": goodConfinedProfile(),
			},
			wantErr:   []string{`"locked"`, "keeps the " + `"moxy/moxy"`},
			wantAsked: []string{"locked"},
			wantWarn:  true,
		},
		{
			name:    "R3 moxy kept",
			profile: "locked",
			profiles: map[string]ResolvedProfile{
				"locked": {Name: "locked", Source: "builtin", KeptServers: append(slices.Clone(requiredKept), moxyServer, justUsServer)},
			},
			wantErr:   []string{"keeps the " + `"moxy/moxy"`},
			wantAsked: []string{"locked"},
		},
		{
			name:      "R3 non-selective",
			profile:   "locked",
			profiles:  map[string]ResolvedProfile{"locked": {Name: "locked", Source: "builtin"}},
			wantErr:   []string{"keeps every server"},
			wantAsked: []string{"locked"},
		},
		{
			name:    "R3 just-us dropped",
			profile: "locked",
			profiles: map[string]ResolvedProfile{
				"locked": {Name: "locked", Source: "builtin", KeptServers: slices.Clone(requiredKept)},
			},
			wantErr:   []string{"drops the " + `"just-us/just-us"`},
			wantAsked: []string{"locked"},
		},
		{
			name:    "R4 dynamic source",
			profile: "locked",
			profiles: map[string]ResolvedProfile{
				"locked": {Name: "locked", Source: "dynamic", KeptServers: append(slices.Clone(requiredKept), justUsServer)},
			},
			wantErr:   []string{"unknown or dynamic source", "verified grant"},
			wantAsked: []string{"locked"},
		},
		{
			name:    "R4 unknown source",
			profile: "locked",
			profiles: map[string]ResolvedProfile{
				"locked": {Name: "locked", KeptServers: append(slices.Clone(requiredKept), justUsServer)},
			},
			wantErr:   []string{"unknown or dynamic source", "verified grant"},
			wantAsked: []string{"locked"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &fakeResolver{profiles: c.profiles}
			withResolver(t, r)
			stopAtPreflight(t)
			warnings := captureWarnings(t)
			home, repoPath := newWorkerFixture(t, modelSpawnSweatfile+c.repoTOML)
			writeGlobalSweatfile(t, home, confinedGlobalSweatfile+c.globalTOML)

			_, err := LaunchDetachedWith(home, repoPath, testDriver("driver/test-session"), "brief", "", "", c.profile, ConfineRequire)
			if err == nil {
				t.Fatal("LaunchDetachedWith = nil error, want a refusal")
			}
			if strings.Contains(err.Error(), "inside an agent unit") {
				t.Fatalf("reached confinedPreflight; the profile checks must come first: %v", err)
			}
			for _, want := range c.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not contain %q", err, want)
				}
			}
			if !slices.Equal(r.asked, c.wantAsked) {
				t.Errorf("resolver asked %v, want %v", r.asked, c.wantAsked)
			}
			if got := strings.Contains(warnings.String(), "ignored [session-entry] spawn-profile"); got != c.wantWarn {
				t.Errorf("warned = %v, want %v (log: %q)", got, c.wantWarn, warnings.String())
			}
			if c.wantWarn && !strings.Contains(warnings.String(), filepath.Join(repoPath, "sweatfile")) {
				t.Errorf("warning does not name the ignored repo sweatfile: %q", warnings.String())
			}
			noWorktree(t, repoPath)
		})
	}
}

// TestConfinedLaunchAcceptsProfilesThatPassTheChecks shows the accepted cases get
// past the profile checks (the spawn then stops at the stubbed preflight, still
// with no worktree), for an explicit profile and for a trusted-layer default.
func TestConfinedLaunchAcceptsProfilesThatPassTheChecks(t *testing.T) {
	cases := []struct {
		name       string
		globalTOML string
		profile    string
	}{
		{name: "explicit profile", profile: "locked"},
		{name: "default from a trusted layer", globalTOML: "[session-entry]\nspawn-profile = \"locked\"\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &fakeResolver{profiles: map[string]ResolvedProfile{"locked": goodConfinedProfile()}}
			withResolver(t, r)
			stopAtPreflight(t)
			home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
			writeGlobalSweatfile(t, home, confinedGlobalSweatfile+c.globalTOML)

			_, err := LaunchDetachedWith(home, repoPath, testDriver("driver/test-session"), "brief", "", "", c.profile, ConfineRequire)
			if err == nil || !strings.Contains(err.Error(), "inside an agent unit") {
				t.Fatalf("err = %v, want the stubbed preflight refusal (the profile checks passed)", err)
			}
			if !slices.Equal(r.asked, []string{"locked"}) {
				t.Errorf("resolver asked %v, want [locked]", r.asked)
			}
			noWorktree(t, repoPath)
		})
	}
}

// TestUnconfinedLaunchIgnoresTheConfinedProfileRules: with confinement enabled
// but the spawn opted out, none of R1-R4 applies: no profile is fine, a
// repo-layer default is read as before, and a profile that keeps everything and
// reports no source is accepted.
func TestUnconfinedLaunchIgnoresTheConfinedProfileRules(t *testing.T) {
	t.Run("no profile", func(t *testing.T) {
		r := &fakeResolver{}
		withResolver(t, r)
		home, repoPath := newWorkerFixture(t, modelSpawnSweatfile)
		writeGlobalSweatfile(t, home, confinedGlobalSweatfile)
		const driverKey = "driver/test-session"
		stop, helloErr := helloAfterLaunch(t, repoPath, driverKey)
		_, err := LaunchWith(home, repoPath, testDriver(driverKey), "brief", "", "", "", ConfineOptOut, 15*time.Second)
		stop()
		if err != nil {
			t.Fatalf("LaunchWith: %v", err)
		}
		if herr := <-helloErr; herr != nil {
			t.Fatalf("hello goroutine: %v", herr)
		}
		if len(r.asked) != 0 {
			t.Errorf("resolver consulted without a profile: %v", r.asked)
		}
	})
	t.Run("repo-layer default and a permissive profile", func(t *testing.T) {
		warnings := captureWarnings(t)
		r := &fakeResolver{profiles: map[string]ResolvedProfile{"review": {Digest: "digest-review"}}}
		withResolver(t, r)
		home, repoPath := newWorkerFixture(t, spawnProfileDefaultSweatfile)
		writeGlobalSweatfile(t, home, confinedGlobalSweatfile)
		const driverKey = "driver/test-session"
		stop, helloErr := helloAfterLaunch(t, repoPath, driverKey)
		res, err := LaunchWith(home, repoPath, testDriver(driverKey), "brief", "", "", "", ConfineOptOut, 15*time.Second)
		stop()
		if err != nil {
			t.Fatalf("LaunchWith: %v", err)
		}
		if herr := <-helloErr; herr != nil {
			t.Fatalf("hello goroutine: %v", herr)
		}
		if got := launchedArgv(t, res.WorktreePath); !slices.Contains(got, "review") {
			t.Errorf("argv = %v, want the repo-layer default review", got)
		}
		if strings.Contains(warnings.String(), "spawn-profile") {
			t.Errorf("unconfined spawn warned about spawn-profile: %q", warnings.String())
		}
	})
}
