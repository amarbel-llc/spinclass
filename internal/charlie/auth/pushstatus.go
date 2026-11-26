package auth

// What a session was told about its push token: one line at every spawn, start
// and resume. Names and times only; nothing here ever holds a token.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/agentretry"
	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// Where a minted credential came from (session.Credential.Source).
const (
	SourceMint = "mint-command"
	SourcePool = "pool"
)

// NoTokenKind is why a session has no push token.
type NoTokenKind string

const (
	NoAuth          NoTokenKind = "no-auth"
	NoForgeOrigin   NoTokenKind = "no-forge-origin"
	HostNotListed   NoTokenKind = "host-not-listed"
	PoolEmpty       NoTokenKind = "pool-empty"
	MintFailed      NoTokenKind = "mint-failed"
	NoneAtCreation  NoTokenKind = "none-at-creation"
	AlreadyRevoked  NoTokenKind = "revoked"
	UnknownNoReason NoTokenKind = ""
	// MintUnrecorded and RecordUnreadable are "spinclass does not know", never
	// "no": a token was made but not set up or recorded, or the record could not
	// be read.
	MintUnrecorded   NoTokenKind = "mint-unrecorded"
	RecordUnreadable NoTokenKind = "record-unreadable"
)

// pushClock says whether a recorded expiry has passed. A variable for tests.
var pushClock = time.Now

// PushDest is the kinds of origin's push URLs as git resolves them (pushurl and
// every url rewrite applied). Only kinds are kept: a URL may carry userinfo.
type PushDest struct {
	SSH, HTTP, Other bool
}

func (d PushDest) read() bool { return d.SSH || d.HTTP || d.Other }

// PushDestinations reads the push URLs of origin as git in dir resolves them.
// The zero value means they could not be read.
func PushDestinations(dir string) PushDest {
	out, err := git.Run(dir, "remote", "get-url", "--push", "--all", "origin")
	if err != nil {
		return PushDest{}
	}
	var d PushDest
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		r, perr := ParseForgeRemote(line)
		switch {
		case strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://"):
			d.HTTP = true
		case perr == nil && r.SSHPrefix != "" && !strings.HasPrefix(line, "/") && !strings.HasPrefix(line, "."):
			d.SSH = true
		default:
			d.Other = true
		}
	}
	return d
}

var urlUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s"']*@`)

// RedactURLs removes the userinfo of every scheme URL in text.
func RedactURLs(text string) string {
	return urlUserinfo.ReplaceAllString(text, "${1}<redacted>@")
}

// mintFailure is a failed Mint with a cause short and safe enough for the push
// token line: no command output and no URL. Its text has URL userinfo removed.
type mintFailure struct {
	cause     string
	tokenMade bool
	err       error
}

func (f *mintFailure) Error() string { return RedactURLs(f.err.Error()) }
func (f *mintFailure) Unwrap() error { return f.err }

func mintFailed(cause string, err error) error {
	return &mintFailure{cause: cause, err: err}
}

// mintStoreFailed is a failure after a token was in hand. A fresh mint's token
// then exists at the forge with no record to revoke it by; a pool slot's goes
// back to the pool.
func mintStoreFailed(cause string, fromPool bool, sessionKey string, err error) error {
	if fromPool {
		return &mintFailure{cause: cause + " (the pool slot went back to the pool)", err: err}
	}
	return &mintFailure{cause: cause, tokenMade: true, err: fmt.Errorf(
		"%w (a token was made and may still be valid at the forge: revoke it by running your [auth].revoke-command with SPINCLASS_SESSION_ID=%s)", err, sessionKey)}
}

// mintCommandCause classifies a failed mint command without quoting it.
func mintCommandCause(ctx context.Context, err error) string {
	var exit *exec.ExitError
	var cause string
	switch {
	case ctx.Err() != nil:
		cause = "the mint command was stopped or timed out"
	case errors.As(err, &exit) && exit.ExitCode() >= 0:
		cause = fmt.Sprintf("the mint command exited with status %d", exit.ExitCode())
	case errors.As(err, &exit):
		cause = "the mint command was killed by a signal"
	default:
		cause = "the mint command could not be run"
	}
	if agentretry.AgentTrouble(err.Error()) {
		cause += " (the ssh agent offered no usable key or could not be reached)"
	}
	if holderAgentHint() != "" {
		cause += " (the ssh agent here is a key holder's and holds no card: run it from your own shell or a session without a key holder)"
	}
	return cause
}

// PushStatus is a session's push token: whether it has one, where it came from
// and when it expires if spinclass was told, else why it has none.
type PushStatus struct {
	Token bool
	// Reused is a token the session already had (a resume), not one made now.
	Reused    bool
	Source    string // SourceMint, SourcePool, or "" on a record made before sources
	PoolSlot  string // the pool slot's session name, for SourcePool
	MintedAt  time.Time
	ExpiresAt *time.Time // nil: spinclass was not told
	Kind      NoTokenKind
	Reason    string   // for a person; names only
	PushTo    PushDest // origin's push URLs, by kind
}

// StatusFromMint is the status of a session whose creation ran Mint. err is
// Mint's error when the session was created anyway (allow-no-credential). The
// line may be all a caller sees (a spawn's result), so it carries the cause, in
// Mint's own short words and never the error's text.
func StatusFromMint(o MintOutcome, err error) PushStatus {
	var failure *mintFailure
	switch {
	case errors.As(err, &failure) && failure.tokenMade:
		return PushStatus{Kind: MintUnrecorded, Reason: "a token was made, but " + failure.cause + ", so it may be valid at the forge with no record of it"}
	case errors.As(err, &failure):
		return PushStatus{Kind: MintFailed, Reason: "the mint failed: " + failure.cause}
	case err != nil:
		return PushStatus{Kind: MintFailed, Reason: "the mint failed: spinclass could not load [auth] or finish the mint"}
	case o.Minted:
		return PushStatus{Token: true, Source: o.Source, PoolSlot: o.PoolSlot, ExpiresAt: o.ExpiresAt}
	case o.Skipped != "":
		return PushStatus{Kind: o.Kind, Reason: o.Skipped}
	}
	return PushStatus{Kind: NoAuth, Reason: "[auth] mint-command is not set"}
}

// RecordedStatus is the status of a session that already exists, from its
// record and whether its credential file is still there. Nothing is minted.
func RecordedStatus(repoPath, branch, worktreePath string) PushStatus {
	p := recordedToken(repoPath, branch, worktreePath)
	p.PushTo = PushDestinations(worktreePath)
	return p
}

func recordedToken(repoPath, branch, worktreePath string) PushStatus {
	st, err := session.Read(repoPath, branch)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return PushStatus{Kind: RecordUnreadable, Reason: "spinclass could not read this session's record, so it does not know whether it has a token"}
	}
	if err != nil || st.Credential == nil {
		return PushStatus{Kind: NoneAtCreation, Reason: "none was minted when the session was created"}
	}
	c := st.Credential
	if c.RevokedAt != nil {
		return PushStatus{Kind: AlreadyRevoked, Reason: "its token was revoked"}
	}
	if !Minted(worktreePath) {
		return PushStatus{Kind: NoneAtCreation, Reason: "the session records a token but its credential file is gone"}
	}
	return PushStatus{Token: true, Reused: true, Source: c.Source, PoolSlot: c.PoolSlot, MintedAt: c.MintedAt, ExpiresAt: c.ExpiresAt}
}

// Line is the one line printed for this status. It says only what spinclass
// knows: whether it made a token, and the kinds of origin's push URLs. ownHolder
// is whether the session runs on its own key holder, whose key no forge is
// known to accept.
func (p PushStatus) Line(ownHolder bool, sessionKey string) string {
	again := "`sc close " + sessionKey + "` and start it again (a token is only made when a session is created)"
	if p.Token {
		if p.ExpiresAt != nil && !p.ExpiresAt.After(pushClock()) {
			return "push_token: expired at " + p.ExpiresAt.UTC().Format(time.RFC3339) + " (" + p.sourceText() + "). next: " + again
		}
		line := "push_token: yes (" + p.sourceText() + "; " + p.expiryText() + ")"
		if p.PushTo.SSH {
			line += "; an ssh push URL of origin does not use this token"
		}
		return line
	}
	switch p.Kind {
	case MintUnrecorded:
		return "push_token: unknown (" + p.Reason + "). next: revoke it by running your [auth].revoke-command with SPINCLASS_SESSION_ID=" + sessionKey + ", then " + again
	case RecordUnreadable:
		return "push_token: unknown (" + p.Reason + "). next: `sc list` shows why the record cannot be read"
	}
	line := "push_token: no (" + p.Reason + "); " + p.pushText(ownHolder)
	if !ownHolder {
		return line
	}
	return line + ". next: " + p.next() + ", then " + again
}

// pushText says what a push does without a spinclass token, one clause per kind
// of push URL.
func (p PushStatus) pushText(ownHolder bool) string {
	if !p.PushTo.read() {
		return "spinclass could not read origin's push URL, so it does not know whether a push works"
	}
	var clauses []string
	if p.PushTo.SSH {
		if ownHolder {
			clauses = append(clauses, "for an ssh push URL, no forge is known to accept this worker's key, so expect that push to fail")
		} else {
			clauses = append(clauses, "an ssh push uses the ssh agent this session was given")
		}
	}
	if p.PushTo.HTTP {
		clauses = append(clauses, "an http(s) push works only if git already has a credential for it (spinclass does not know)")
	}
	if p.PushTo.Other {
		clauses = append(clauses, "origin has a push URL that is neither ssh nor http(s), and spinclass does not know whether a push there works")
	}
	return strings.Join(clauses, "; ")
}

func (p PushStatus) sourceText() string {
	var from string
	switch p.Source {
	case SourceMint:
		from = "minted by [auth].mint-command"
	case SourcePool:
		from = "pool slot " + p.PoolSlot
	default:
		from = "source not recorded"
	}
	if !p.Reused {
		return from
	}
	return "reused: " + from + ", since " + p.MintedAt.UTC().Format(time.RFC3339)
}

func (p PushStatus) expiryText() string {
	if p.ExpiresAt == nil {
		return "expiry unknown"
	}
	return "expires " + p.ExpiresAt.UTC().Format(time.RFC3339)
}

func (p PushStatus) next() string {
	switch p.Kind {
	case HostNotListed:
		return "add the origin host to [auth].forge-hosts in a sweatfile above the repo"
	case NoForgeOrigin:
		return "give the repo a forge origin (https or ssh)"
	case PoolEmpty:
		return "refill the token pool, or set [auth].mint-command in a sweatfile above the repo"
	case MintFailed:
		return "fix the mint (is your card there?)"
	}
	return "set [auth] mint-command and forge-hosts in a sweatfile above the repo"
}

// holderAgentHint is added to a failed mint or revoke when the ssh agent of
// this process is a key holder's, which holds no card.
func holderAgentHint() string {
	sock := os.Getenv("SSH_AUTH_SOCK")
	base := keyholder.StateBase()
	if sock == "" || base == "" || !strings.HasPrefix(filepath.Clean(sock), filepath.Clean(base)+string(filepath.Separator)) {
		return ""
	}
	return fmt.Sprintf(" (the ssh agent here, %s, is a key holder's and holds no card: if the command signs with your card, run it from your own shell or a session without a key holder)", sock)
}
