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

// Without a token the line says only what spinclass knows, per kind of push URL:
// it never claims a worker cannot push, since git may hold an https credential.
func TestPushLineWithNoTokenSaysWhatThatMeansForThisSession(t *testing.T) {
	const (
		holderSSH = "for an ssh push URL, no forge is known to accept this worker's key, so expect that push to fail"
		agentSSH  = "an ssh push uses the ssh agent this session was given"
		overHTTP  = "an http(s) push works only if git already has a credential for it (spinclass does not know)"
		other     = "origin has a push URL that is neither ssh nor http(s), and spinclass does not know whether a push there works"
		unread    = "spinclass could not read origin's push URL, so it does not know whether a push works"
	)
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
		for _, url := range []struct {
			name          string
			dest          PushDest
			holder, agent string
		}{
			{"ssh", PushDest{SSH: true}, holderSSH, agentSSH},
			{"http", PushDest{HTTP: true}, overHTTP, overHTTP},
			{"both", PushDest{SSH: true, HTTP: true}, holderSSH + "; " + overHTTP, agentSSH + "; " + overHTTP},
			{"other", PushDest{Other: true}, other, other},
			{"unread", PushDest{}, unread, unread},
		} {
			p := tc.p
			p.PushTo = url.dest
			with := p.Line(true, "repo/w")
			head := "push_token: no (" + p.Reason + "); " + url.holder + ". next: " + tc.next
			if !strings.HasPrefix(with, head) || !strings.Contains(with, "`sc close repo/w`") || strings.Contains(with, "cannot push") {
				t.Errorf("%s, %s, with a holder = %q, want it to start %q and name sc close", tc.name, url.name, with, head)
			}
			without := p.Line(false, "repo/w")
			if want := "push_token: no (" + p.Reason + "); " + url.agent; without != want {
				t.Errorf("%s, %s, without a holder = %q, want %q", tc.name, url.name, without, want)
			}
			if strings.Contains(with+without, "SECRET-IN-ERROR") {
				t.Errorf("%s: the mint error's text reached the line", tc.name)
			}
		}
	}
}

// A token whose recorded expiry has passed is not a "yes".
func TestAnExpiredTokenIsNotAYes(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	prev := pushClock
	pushClock = func() time.Time { return now }
	t.Cleanup(func() { pushClock = prev })

	past, soon := now.Add(-time.Minute), now.Add(time.Minute)
	got := PushStatus{Token: true, Reused: true, Source: SourcePool, PoolSlot: "trip-3", MintedAt: past.Add(-time.Hour), ExpiresAt: &past}.Line(true, "repo/w")
	want := "push_token: expired at 2026-11-26T11:59:00Z (reused: pool slot trip-3, since 2026-11-26T10:59:00Z). next: `sc close repo/w` and start it again (a token is only made when a session is created)"
	if got != want {
		t.Errorf("expired = %q, want %q", got, want)
	}
	if got := (PushStatus{Token: true, Source: SourceMint, ExpiresAt: &soon}).Line(true, "repo/w"); !strings.HasPrefix(got, "push_token: yes (") {
		t.Errorf("not yet expired = %q", got)
	}
}

// With a token, an ssh push URL (a pushurl the rewrite does not cover) is named:
// the token is not used there.
func TestATokenLineNamesAnSSHPushURLItDoesNotCover(t *testing.T) {
	got := PushStatus{Token: true, Source: SourceMint, PushTo: PushDest{SSH: true}}.Line(false, "repo/w")
	if want := "push_token: yes (minted by [auth].mint-command; expiry unknown); an ssh push URL of origin does not use this token"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRedactURLsRemovesUserinfo(t *testing.T) {
	got := RedactURLs(`parse remote "https://bob:hunter2@h.example/o/r": bad; also ssh://git@h.example/r and git@h.example:r`)
	if want := `parse remote "https://<redacted>@h.example/o/r": bad; also ssh://<redacted>@h.example/r and git@h.example:r`; got != want {
		t.Errorf("got %q", got)
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
