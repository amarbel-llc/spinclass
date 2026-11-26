package auth

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

func TestPushLineSaysTokenSourceAndExpiry(t *testing.T) {
	deadline := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	since := time.Date(2026, 11, 26, 9, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		p    PushStatus
		want string
	}{
		{"minted", StatusFromMint(MintOutcome{Minted: true, Source: SourceMint}, nil), "push_token: yes (minted by [auth].mint-command; expiry unknown)"},
		{"pool", StatusFromMint(MintOutcome{Minted: true, Source: SourcePool, PoolSlot: "trip-3", ExpiresAt: &deadline}, nil), "push_token: yes (pool slot trip-3; expires 2026-11-27T12:00:00Z)"},
		{"reused", PushStatus{Token: true, Reused: true, Source: SourceMint, MintedAt: since}, "push_token: yes (reused: minted by [auth].mint-command, since 2026-11-26T09:30:00Z; expiry unknown)"},
		{"old record", PushStatus{Token: true, Reused: true, MintedAt: since}, "push_token: yes (reused: source not recorded, since 2026-11-26T09:30:00Z; expiry unknown)"},
	} {
		for _, holder := range []bool{true, false} {
			if got := tc.p.Line(holder, "repo/w"); got != tc.want {
				t.Errorf("%s (holder %v) = %q, want %q", tc.name, holder, got, tc.want)
			}
		}
	}
}

// Without a token the truth depends on the holder: with one the worker's only
// ssh key is unknown to the forge; without one it pushes with the agent it was
// given.
func TestPushLineWithNoTokenSaysWhatThatMeansForThisSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    PushStatus
		next string
	}{
		{"no auth", StatusFromMint(MintOutcome{}, nil), "set [auth] mint-command and forge-hosts"},
		{"host", StatusFromMint(MintOutcome{Kind: HostNotListed, Skipped: "origin host github.com not in [auth].forge-hosts"}, nil), "add the origin host to [auth].forge-hosts"},
		{"pool empty", StatusFromMint(MintOutcome{Kind: PoolEmpty, Skipped: "token pool has no unclaimed, unexpired slot for o/r"}, nil), "refill the token pool"},
		{"mint failed", StatusFromMint(MintOutcome{}, errors.New("boom SECRET-IN-ERROR")), "fix the mint"},
	} {
		with := tc.p.Line(true, "repo/w")
		for _, want := range []string{"push_token: no. this worker cannot push: " + tc.p.Reason, "no forge knows its key holder's key", "next: " + tc.next, "`sc close repo/w`"} {
			if !strings.Contains(with, want) {
				t.Errorf("%s with a holder: %q lacks %q", tc.name, with, want)
			}
		}
		without := tc.p.Line(false, "repo/w")
		if want := "push_token: no (" + tc.p.Reason + "); pushes use the ssh agent this session was given"; without != want {
			t.Errorf("%s without a holder = %q, want %q", tc.name, without, want)
		}
		if strings.Contains(with+without, "SECRET-IN-ERROR") {
			t.Errorf("%s: the mint error's text reached the line", tc.name)
		}
	}
	local := PushStatus{Kind: NoForgeOrigin, Reason: "the origin is not a forge URL, or there is none"}.Line(true, "repo/w")
	if strings.Contains(local, "cannot push") || !strings.Contains(local, "a push works only if this origin needs no ssh key") {
		t.Errorf("an origin that is no forge: %q", local)
	}
}

func TestAFailedCardStepUnderAKeyHolderAgentSaysSo(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(keyholder.StateBase(), "abc", "agent", "agent.sock"))
	if got := holderAgentHint(); !strings.Contains(got, "is a key holder's and holds no card") || !strings.Contains(got, "your own shell") {
		t.Errorf("under a holder socket: %q", got)
	}
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(state, "my-agent.sock"))
	if got := holderAgentHint(); got != "" {
		t.Errorf("under the user's own agent: %q, want nothing", got)
	}
	t.Setenv("SSH_AUTH_SOCK", "")
	if got := holderAgentHint(); got != "" {
		t.Errorf("with no agent: %q, want nothing", got)
	}
}
