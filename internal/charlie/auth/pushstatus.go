package auth

// What a session was told about its push token: one line at every spawn, start
// and resume. Names and times only; nothing here ever holds a token.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
)

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
	Reason    string // for a person; names only
}

// StatusFromMint is the status of a session whose creation ran Mint. err is
// Mint's error when the session was created anyway (allow-no-credential); its
// text is not repeated here, the warning that precedes the line carries it.
func StatusFromMint(o MintOutcome, err error) PushStatus {
	switch {
	case err != nil:
		return PushStatus{Kind: MintFailed, Reason: "the mint failed (see the warning above)"}
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
	st, err := session.Read(repoPath, branch)
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

// Line is the one line printed for this status. ownHolder is whether the
// session runs on its own key holder: without a token it then has only a key
// no forge knows, while a session with no holder pushes with the ssh agent it
// was given.
func (p PushStatus) Line(ownHolder bool, sessionKey string) string {
	if p.Token {
		return "push_token: yes (" + p.sourceText() + "; " + p.expiryText() + ")"
	}
	if !ownHolder {
		return "push_token: no (" + p.Reason + "); pushes use the ssh agent this session was given"
	}
	next := p.next()
	again := "then `sc close " + sessionKey + "` and start it again (a token is only made when a session is created)"
	if p.Kind == NoForgeOrigin {
		return "push_token: no (" + p.Reason + "). its only ssh key is its key holder's: a push works only if this origin needs no ssh key. next: " + next + ", " + again
	}
	return "push_token: no. this worker cannot push: " + p.Reason + ", and no forge knows its key holder's key. next: " + next + ", " + again
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
