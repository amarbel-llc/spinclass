package session

import (
	"slices"
	"testing"
)

func TestStripInheritedSessionIDs(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"CLOWN_SESSION_ID=driver/key",
		"SPINCLASS_SESSION_ID=worker/key",
		"CLAUDE_SESSION_ID=abc-123",
		"CLOWN_UNIT=clown-agent-driver.service",
		"CLOWN_BIN=/nix/store/x/bin/clown",
		"HOME=/home/u",
	}
	got := StripInheritedSessionIDs(in)
	want := []string{
		"PATH=/usr/bin",
		"SPINCLASS_SESSION_ID=worker/key",
		"CLOWN_BIN=/nix/store/x/bin/clown",
		"HOME=/home/u",
	}
	if !slices.Equal(got, want) {
		t.Errorf("StripInheritedSessionIDs:\n got %v\nwant %v", got, want)
	}
}

// The parent's troupe credentials never reach a child (spinclass#169): its
// clown mints the child's own; the child's user name is left for clown.
func TestStripInheritedTroupeCredentials(t *testing.T) {
	in := []string{
		"TROUPE_XMPP_USER=child-principal",
		"TROUPE_XMPP_PASSWORD_FILE=/run/user/1000/troupe/accounts/parent.pass",
		"TROUPE_XMPP_PASSWORD=hunter2",
		"PATH=/usr/bin",
	}
	want := []string{"TROUPE_XMPP_USER=child-principal", "PATH=/usr/bin"}
	if got := StripInheritedSessionIDs(in); !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A parent's profile pin never reaches a child (#407): CLOWN_PROFILE_DIGEST is
// an approval and CLOWN_PROFILE_SESSION binds it to one session, and an
// inherited CLOWN_PROFILE would apply the parent's profile to a child that
// asked for none. The launcher sets the child's own pin explicitly.
func TestStripInheritedProfilePins(t *testing.T) {
	in := []string{
		"CLOWN_PROFILE=fable-design",
		"CLOWN_PROFILE_DIGEST=00ff",
		"CLOWN_PROFILE_SESSION=1d3a5c7e-9b0f-4d2a-8e6c-0a1b2c3d4e5f",
		"CLOWN_PROFILES_PATH=/keep",
		"PATH=/usr/bin",
	}
	want := []string{"CLOWN_PROFILES_PATH=/keep", "PATH=/usr/bin"}
	if got := StripInheritedSessionIDs(in); !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A prefix-only match must not drop a var whose name merely starts with the
// stripped name (e.g. CLOWN_SESSION_ID_EXTRA), and the empty input is safe.
func TestStripInheritedSessionIDsExactPrefix(t *testing.T) {
	if got := StripInheritedSessionIDs(nil); len(got) != 0 {
		t.Errorf("nil input: got %v, want empty", got)
	}
	in := []string{"CLOWN_SESSION_IDX=keep", "SPINCLASS_SESSION_ID=k"}
	got := StripInheritedSessionIDs(in)
	if !slices.Equal(got, in) {
		t.Errorf("must keep non-exact vars: got %v, want %v", got, in)
	}
}
