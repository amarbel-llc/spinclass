package spawn

import (
	"strings"
	"testing"
)

// spinclass#474: a spawn's posture option. Omitted keeps the trusted key;
// unattended is a narrowing to dontAsk; attended is a widening that needs the
// trusted allow-posture-widening key and is refused for a confined worker. The
// permission floor's ask for attended is tested in internal/charlie/perms.

func TestParsePosture(t *testing.T) {
	for _, ok := range []string{"", "unattended", "attended"} {
		if got, err := ParsePosture(ok); err != nil || string(got) != ok {
			t.Errorf("ParsePosture(%q) = (%q, %v), want (%q, nil)", ok, got, err, ok)
		}
	}
	for _, bad := range []string{"keep", "Attended", "dontAsk"} {
		if _, err := ParsePosture(bad); err == nil {
			t.Errorf("ParsePosture(%q) = nil error, want a refusal", bad)
		}
	}
}

func TestSpawnPostureOmittedKeepsTheKeyAndRecordsIt(t *testing.T) {
	got := launchWithPosture(t, "", trustedDontAsk, PostureDefault)
	assertUnattended(t, got, wantDontAskArgv, true)
	if got.pinnedPosture != "unattended" {
		t.Errorf("pinned posture = %q, want unattended", got.pinnedPosture)
	}
	got = launchWithPosture(t, "", "", PostureDefault)
	assertUnattended(t, got, wantAutoArgv, false)
	if got.pinnedPosture != "attended" {
		t.Errorf("pinned posture = %q, want attended", got.pinnedPosture)
	}
}

// Unattended needs no key: it narrows whatever the sweatfile says.
func TestSpawnPostureUnattendedOverridesAnUnsetOrAutoKey(t *testing.T) {
	for _, global := range []string{"", trustedAuto} {
		got := launchWithPosture(t, "", global, PostureUnattended)
		assertUnattended(t, got, wantDontAskArgv, true)
		if got.pinnedPosture != "unattended" {
			t.Errorf("global %q: pinned posture = %q, want unattended", global, got.pinnedPosture)
		}
	}
}

// Attended over a dontAsk key, with the trusted widening key, runs as an unset
// key would (auto mode unless disable-auto-mode) and carries no marker.
func TestSpawnPostureAttendedWithTheWideningKeyDropsDontAsk(t *testing.T) {
	got := launchWithPosture(t, "", trustedDontAsk+"allow-posture-widening = true\n", PostureAttended)
	assertUnattended(t, got, wantAutoArgv, false)
	if got.pinnedPosture != "attended" {
		t.Errorf("pinned posture = %q, want attended", got.pinnedPosture)
	}
	got = launchWithPosture(t, "disable-auto-mode = true\n", trustedDontAsk+"allow-posture-widening = true\n", PostureAttended)
	assertUnattended(t, got, []string{"--", "brief"}, false)
}

func TestSpawnPostureAttendedWithoutTheWideningKeyIsRefused(t *testing.T) {
	home, repoPath := newWorkerFixture(t, "[session-entry]\n"+unattendedEntry)
	writeGlobalSweatfile(t, home, trustedDontAsk)
	_, err := LaunchDetachedOpts(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "", LaunchOptions{Posture: PostureAttended})
	if err == nil || !strings.Contains(err.Error(), "allow-posture-widening") {
		t.Fatalf("err = %v, want a refusal naming allow-posture-widening", err)
	}
	noWorktree(t, repoPath)
}

// The widening key is honoured only from trusted layers: in the repo's own
// sweatfile it is ignored and the attended spawn is refused.
func TestSpawnPostureAttendedIgnoresARepoLevelWideningKey(t *testing.T) {
	home, repoPath := newWorkerFixture(t, "[session-entry]\n"+unattendedEntry+"allow-posture-widening = true\n")
	writeGlobalSweatfile(t, home, trustedDontAsk)
	_, err := LaunchDetachedOpts(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "", LaunchOptions{Posture: PostureAttended})
	if err == nil || !strings.Contains(err.Error(), "allow-posture-widening") {
		t.Fatalf("err = %v, want a refusal naming allow-posture-widening", err)
	}
	noWorktree(t, repoPath)
}

func TestRefuseConfinedAttended(t *testing.T) {
	if err := refuseConfinedAttended(true, PostureAttended); err == nil || !strings.Contains(err.Error(), "confined") {
		t.Errorf("confined attended: err = %v, want a refusal", err)
	}
	for _, c := range []struct {
		confined bool
		posture  Posture
	}{{true, PostureDefault}, {true, PostureUnattended}, {false, PostureAttended}, {false, PostureDefault}} {
		if err := refuseConfinedAttended(c.confined, c.posture); err != nil {
			t.Errorf("confined=%v posture=%q: err = %v, want nil", c.confined, c.posture, err)
		}
	}
}
