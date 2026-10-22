// Package auth implements per-session forge push credentials (FDR 0028): a
// token minted by [auth].mint-command at worktree creation, stored mode-600
// in frontend state ($XDG_STATE_HOME/spinclass/credentials/, outside the
// worktree; FDR 0035), wired into the worktree via worktree-scoped git config
// (a credential helper plus a forge-host-scoped ssh→https URL rewrite), mirrored
// onto the disposable landing worktree at merge time (FDR 0029), revoked by
// [auth].revoke-command at close, and swept for sessions that died without
// closing.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/confinement"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/hookrun"
	"code.linenisgreat.com/spinclass/internal/landing"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

// CredentialFile is the git-credential-store file under <worktree>/.spinclass/.
const CredentialFile = "git-credentials"

// credentialUser is the literal username in the stored credential. Forgejo
// (Gitea lineage, services/auth/basic.go) looks a non-empty basic-auth password
// up as an access token and never checks the username against the token's
// owner, so any fixed value works; git-credential-store needs one to consider
// the credential complete.
const credentialUser = "spinclass"

// Identity is the session a credential belongs to; it becomes the SPINCLASS_*
// env the mint/revoke commands see.
type Identity struct {
	RepoPath     string
	WorktreePath string
	Branch       string
	SessionKey   string
}

// Remote is the parsed origin: the forge host the credential is scoped to,
// the owner/name the mint command scopes the token to, and the ssh URL prefix
// (empty for an https origin) the worktree config rewrites to https.
type Remote struct {
	Host      string
	OwnerRepo string
	SSHPrefix string
}

// ParseForgeRemote parses an origin URL in the scp-like (git@host:o/r.git),
// ssh:// (ssh://git@host[:port]/o/r.git), or https:// form.
func ParseForgeRemote(remote string) (Remote, error) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return Remote{}, errors.New("empty remote URL")
	}
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil {
			return Remote{}, fmt.Errorf("parse remote %q: %w", remote, err)
		}
		ownerRepo := trimRepoPath(u.Path)
		switch u.Scheme {
		case "https", "http":
			return Remote{Host: u.Hostname(), OwnerRepo: ownerRepo}, nil
		case "ssh", "git+ssh", "ssh+git":
			prefix := u.Scheme + "://"
			if u.User != nil {
				prefix += u.User.Username() + "@"
			}
			prefix += u.Host + "/"
			return Remote{Host: u.Hostname(), OwnerRepo: ownerRepo, SSHPrefix: prefix}, nil
		default:
			return Remote{}, fmt.Errorf("unsupported remote scheme %q in %q", u.Scheme, remote)
		}
	}
	// scp-like: [user@]host:path
	i := strings.Index(remote, ":")
	if i <= 0 {
		return Remote{}, fmt.Errorf("unrecognised remote URL %q", remote)
	}
	hostPart, path := remote[:i], remote[i+1:]
	host := hostPart
	if at := strings.LastIndex(hostPart, "@"); at >= 0 {
		host = hostPart[at+1:]
	}
	return Remote{Host: host, OwnerRepo: trimRepoPath(path), SSHPrefix: hostPart + ":"}, nil
}

func trimRepoPath(p string) string {
	p = strings.Trim(p, "/")
	return strings.TrimSuffix(p, ".git")
}

// The minted credential lives in frontend state, never in the worktree: a
// confined unit's tree holds no copy, so no mount over it can come undone
// (FDR 0035 "Frontend rewrites detach file mounts", option (b)). A session
// minted before the move keeps its legacy <worktree>/.spinclass/git-credentials
// until it is closed.

// Seams for tests: the XDG state base, and whether a worktree's agent unit is
// running.
var (
	stateDir              = confinement.DefaultStateDir
	unitRunning           = confinement.UnitRunningForWorktree
	commandCaptureAmbient = hookrun.CommandCaptureAmbient
)

// StateCredentialPath is where worktreePath's credential is minted: a 0600
// file under $XDG_STATE_HOME/spinclass/credentials/, named by the worktree.
func StateCredentialPath(worktreePath string) (string, error) {
	base, err := stateDir()
	if err != nil {
		return "", err
	}
	return confinement.CredentialPath(base, worktreePath), nil
}

// LegacyCredentialPath is where a credential minted before the move lives.
func LegacyCredentialPath(worktreePath string) string {
	return filepath.Join(worktreePath, ".spinclass", CredentialFile)
}

// MintedStatePath returns the frontend-state credential when it exists.
func MintedStatePath(worktreePath string) (string, bool) {
	p, err := StateCredentialPath(worktreePath)
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}

// CredentialPath is the minted credential: the frontend-state file, else, for
// a worktree that was never launched confined, a legacy worktree file. A
// confined worktree's tree is the unit's to write, so a legacy-looking file
// there may be planted and is never used: its path is the frontend one even
// when that does not exist.
func CredentialPath(worktreePath string) string {
	p, err := StateCredentialPath(worktreePath)
	if err == nil {
		if _, serr := os.Stat(p); serr == nil || launchedConfined(worktreePath) {
			return p
		}
	}
	legacy := LegacyCredentialPath(worktreePath)
	if fi, lerr := os.Lstat(legacy); lerr == nil && fi.Mode().IsRegular() {
		return legacy
	}
	if err != nil {
		return legacy
	}
	return p
}

// launchedConfined reports whether the worktree has (or had) an agent unit:
// a frontend-owned unit record or spec. Unknowable counts as confined.
func launchedConfined(worktreePath string) bool {
	base, err := stateDir()
	if err != nil {
		return true
	}
	return confinement.LaunchedConfined(base, worktreePath)
}

// Minted reports whether worktreePath has a minted credential.
func Minted(worktreePath string) bool {
	_, err := os.Stat(CredentialPath(worktreePath))
	return err == nil
}

// RemoveCredential deletes both the frontend-state and any legacy credential.
// Close calls it unconditionally, so a frontend file never outlives its session.
func RemoveCredential(worktreePath string) {
	if p, err := StateCredentialPath(worktreePath); err == nil {
		_ = os.Remove(p)
	}
	_ = os.Remove(LegacyCredentialPath(worktreePath))
}

func (id Identity) env(r Remote, originURL string) []string {
	repo, branch := id.SessionKey, id.Branch
	if i := strings.Index(id.SessionKey, "/"); i >= 0 {
		repo, branch = id.SessionKey[:i], id.SessionKey[i+1:]
	}
	return []string{
		"SPINCLASS_SESSION_ID=" + id.SessionKey,
		"SPINCLASS_REPO=" + repo,
		"SPINCLASS_BRANCH=" + branch,
		"SPINCLASS_WORKTREE=" + id.WorktreePath,
		"SPINCLASS_FORGE_HOST=" + r.Host,
		"SPINCLASS_FORGE_REPO=" + r.OwnerRepo,
		"SPINCLASS_ORIGIN_URL=" + originURL,
	}
}

// Rewrite is the worktree-scoped url.<HTTPS>.insteadOf wiring plus the host the
// credential-store line is keyed on. From lists the insteadOf values (empty:
// helper only).
type Rewrite struct {
	CredentialHost string
	HTTPS          string
	From           []string
}

// Resolution is what Resolve decided for a session's origin: the configured
// origin URL, the forge remote the mint/revoke env names, the rewrite, and
// whether a url-resolver (not the built-in mapping) produced it.
type Resolution struct {
	OriginURL string
	Forge     Remote
	Rewrite   Rewrite
	Resolved  bool
}

// record is the persisted form of a Resolution (session.CredentialRemote).
func (r Resolution) record() *session.CredentialRemote {
	return &session.CredentialRemote{
		OriginURL:      r.OriginURL,
		ForgeHost:      r.Forge.Host,
		ForgeRepo:      r.Forge.OwnerRepo,
		CredentialHost: r.Rewrite.CredentialHost,
		HTTPS:          r.Rewrite.HTTPS,
		From:           r.Rewrite.From,
		Resolved:       r.Resolved,
	}
}

// rewriteFromRecord is record's inverse for the wiring: the Rewrite a stored
// remote replays.
func rewriteFromRecord(rec *session.CredentialRemote) Rewrite {
	return Rewrite{CredentialHost: rec.CredentialHost, HTTPS: rec.HTTPS, From: rec.From}
}

// builtinRewrite is the pre-#335 string mapping: the origin's ssh prefix to
// https://<host>/. Wrong on an owner-free forge plane, which is what
// [auth].url-resolver exists to replace.
func builtinRewrite(r Remote) Rewrite {
	rw := Rewrite{CredentialHost: r.Host, HTTPS: "https://" + r.Host + "/"}
	if r.SSHPrefix != "" {
		rw.From = []string{r.SSHPrefix}
	}
	return rw
}

// readOrigin reads origin's CONFIGURED url. Not `git remote get-url`, which
// applies url.*.insteadOf — once Inject has rewritten the forge to https in a
// worktree, that would report the https form and hide the ssh prefix the next
// Inject (the landing worktree's) has to rewrite.
func readOrigin(dir string) (string, Remote, error) {
	raw, err := git.Run(dir, "config", "--get", "remote.origin.url")
	if err != nil {
		return "", Remote{}, fmt.Errorf("resolve origin remote: %w", err)
	}
	raw = strings.TrimSpace(raw)
	r, err := ParseForgeRemote(raw)
	return raw, r, err
}

// urlResolverTimeout caps one [auth].url-resolver run. A var so tests can
// shorten it.
var urlResolverTimeout = 30 * time.Second

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Resolve maps the configured origin to the rewrite the token lane wires. A
// blank resolver keeps the built-in string rewrite. Otherwise the resolver
// (a shell string; `{origin}` is replaced by the shell-quoted origin URL) runs
// on the ambient PATH from the repo root and must print one JSON object with
// `canonical_https` (required) and `canonical_ssh` (optional). Every failure
// is an error: there is deliberately no fallback to the string rewrite.
func Resolve(ctx context.Context, resolver string, id Identity, originURL string, origin Remote) (Resolution, error) {
	if strings.TrimSpace(resolver) == "" {
		return Resolution{OriginURL: originURL, Forge: origin, Rewrite: builtinRewrite(origin)}, nil
	}
	const prefix = "[auth] url-resolver"
	// The runner normalizes the script (drops blank lines), which would alter a
	// quoted origin containing one; git never yields such an origin. Normalizing
	// after substitution is safe otherwise: it is idempotent and the quoted
	// origin is never empty.
	if strings.ContainsAny(originURL, "\r\n") {
		return Resolution{}, fmt.Errorf("%s: origin URL %q contains a newline", prefix, originURL)
	}
	rctx, cancel := context.WithTimeout(ctx, urlResolverTimeout)
	defer cancel()
	script := strings.ReplaceAll(resolver, "{origin}", shellQuote(originURL))
	out, err := hookrun.CommandCaptureAmbient(rctx, id.RepoPath, script, id.env(origin, originURL))
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return Resolution{}, fmt.Errorf("%s cancelled: %v", prefix, ctx.Err())
		case errors.Is(rctx.Err(), context.DeadlineExceeded):
			return Resolution{}, fmt.Errorf("%s timed out after %s", prefix, urlResolverTimeout)
		}
		return Resolution{}, fmt.Errorf("%s failed: %w", prefix, err)
	}
	trimmed := bytes.TrimSpace([]byte(out))
	if len(trimmed) == 0 {
		return Resolution{}, fmt.Errorf("%s printed no JSON object on stdout", prefix)
	}
	var ans struct {
		CanonicalHTTPS *string `json:"canonical_https"`
		CanonicalSSH   *string `json:"canonical_ssh"`
	}
	if err := json.Unmarshal(trimmed, &ans); err != nil {
		snippet := trimmed
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return Resolution{}, fmt.Errorf("%s printed invalid JSON: %v (stdout: %s)", prefix, err, snippet)
	}
	if ans.CanonicalHTTPS == nil || strings.TrimSpace(*ans.CanonicalHTTPS) == "" {
		return Resolution{}, fmt.Errorf(`%s output has no "canonical_https"`, prefix)
	}
	canonicalHTTPS := strings.TrimSpace(*ans.CanonicalHTTPS)
	u, err := url.Parse(canonicalHTTPS)
	if err != nil || !strings.HasPrefix(canonicalHTTPS, "https://") || u.Scheme != "https" || u.Host == "" {
		return Resolution{}, fmt.Errorf("%s canonical_https %q is not an https URL", prefix, canonicalHTTPS)
	}
	switch {
	case u.User != nil:
		return Resolution{}, fmt.Errorf("%s canonical_https %q is not an https URL: must not carry userinfo", prefix, canonicalHTTPS)
	case strings.ContainsAny(canonicalHTTPS, "?#"):
		return Resolution{}, fmt.Errorf("%s canonical_https %q is not an https URL: must not carry a query or fragment", prefix, canonicalHTTPS)
	}
	canonicalSSH := ""
	if ans.CanonicalSSH != nil {
		canonicalSSH = strings.TrimSpace(*ans.CanonicalSSH)
	}
	if canonicalSSH != "" {
		if r, perr := ParseForgeRemote(canonicalSSH); perr != nil || r.SSHPrefix == "" {
			return Resolution{}, fmt.Errorf("%s canonical_ssh %q is not an ssh URL", prefix, canonicalSSH)
		}
	}
	var from []string
	for _, v := range []string{originURL, canonicalSSH} {
		if v != "" && v != canonicalHTTPS && !slices.Contains(from, v) {
			from = append(from, v)
		}
	}
	return Resolution{
		OriginURL: originURL,
		Forge:     Remote{Host: u.Hostname(), OwnerRepo: trimRepoPath(u.Path)},
		Rewrite:   Rewrite{CredentialHost: u.Host, HTTPS: canonicalHTTPS, From: from},
		Resolved:  true,
	}, nil
}

// MintOutcome reports what Mint did: Minted when a credential was written and
// wired (Resolved then carries the resolver's canonical_https, if one ran);
// Skipped (non-empty, human-readable) when a mint-command is configured but
// this session's origin host is outside [auth].forge-hosts, so the session
// keeps today's ssh behaviour; neither when no mint-command is configured.
type MintOutcome struct {
	Minted   bool
	Skipped  string
	Resolved string
}

// Mint runs [auth].mint-command (and pins [auth].revoke-command on the session
// record, see session.Credential.RevokeCommand) from sf, which the caller must
// have loaded WITHOUT the session worktree's own sweatfile: a worker can write
// that file (see DefaultBranchHierarchy). It runs the command in the session
// worktree, writes the token it
// prints as a mode-600 git-credential-store file, injects the worktree-scoped
// git config, and records the mint (and the remote it wired) on the session
// state. A configured [auth].forge-hosts allow-list gates it on the ORIGIN host
// first — the mechanism that lets one root-level entry cover a tree of repos
// without a GitHub-origin repo ever minting (and failing its creation). The
// allow-list also binds the resolver: a resolved canonical_https host outside
// it fails the mint before anything is written, so a resolver cannot widen it.
//
// The url-resolver (if any) runs BEFORE the mint: a refusal then happens before
// any token exists (nothing to revoke or orphan), the credential line and the
// insteadOf key must name the canonical host only the resolution knows, and
// the mint-command sees the canonical SPINCLASS_FORGE_HOST/_REPO.
func Mint(ctx context.Context, sf sweatfile.Sweatfile, id Identity, urlResolver string) (MintOutcome, error) {
	cmd := sf.AuthMintCommand()
	if cmd == nil || strings.TrimSpace(*cmd) == "" {
		return MintOutcome{}, nil
	}
	// A mint rewrites the worktree's config.worktree, which a running unit
	// holds as a read-only file mount whose parent is a read-write bind: a
	// rewrite from here would remove that mount (FDR 0035).
	if unitRunning(id.WorktreePath) {
		return MintOutcome{}, fmt.Errorf("[auth] refusing to mint for %s: its agent unit is running, and the mint would rewrite the unit's read-only config.worktree", id.WorktreePath)
	}
	// No origin, or an origin that is not a forge URL (a local path): there is
	// nothing a forge token could be scoped to, so — like an unlisted host —
	// this is a visible skip, never a failed creation. A shared [auth] entry
	// must not stop a remote-less scratch repo from getting a session.
	originURL, origin, err := readOrigin(id.RepoPath)
	if err != nil {
		return MintOutcome{Skipped: "no forge origin remote: " + strings.TrimSpace(err.Error())}, nil
	}
	if hosts := sf.AuthForgeHosts(); len(hosts) > 0 && !slices.Contains(hosts, origin.Host) {
		return MintOutcome{Skipped: fmt.Sprintf("origin host %s not in [auth].forge-hosts", origin.Host)}, nil
	}
	res, err := Resolve(ctx, urlResolver, id, originURL, origin)
	if err != nil {
		return MintOutcome{}, err
	}
	if hosts := sf.AuthForgeHosts(); len(hosts) > 0 && !slices.Contains(hosts, res.Forge.Host) {
		return MintOutcome{}, fmt.Errorf("[auth] url-resolver: canonical_https host %q is not in forge-hosts %v", res.Forge.Host, hosts)
	}
	out, err := hookrun.CommandCapture(ctx, id.WorktreePath, *cmd, id.env(res.Forge, res.OriginURL))
	if err != nil {
		return MintOutcome{}, fmt.Errorf("[auth] mint-command failed: %w", err)
	}
	token := strings.TrimSpace(out)
	if token == "" {
		return MintOutcome{}, errors.New("[auth] mint-command printed no token on stdout")
	}
	credFile, err := writeCredential(id.WorktreePath, res.Rewrite.CredentialHost, token)
	if err != nil {
		return MintOutcome{}, fmt.Errorf("[auth] write credential: %w", err)
	}
	if err := Inject(id.WorktreePath, credFile, res.Rewrite); err != nil {
		return MintOutcome{}, fmt.Errorf("[auth] inject worktree config: %w", err)
	}
	st, err := session.EnsureWorktreeState(id.RepoPath, id.Branch, id.SessionKey, 0)
	if err != nil {
		return MintOutcome{}, fmt.Errorf("[auth] record mint: %w", err)
	}
	// Pin the revoke-command resolved NOW, from the layers the caller vetted for
	// this mint. Every later revoke runs this string and re-reads no sweatfile.
	pinned := ""
	if rc := sf.AuthRevokeCommand(); rc != nil {
		pinned = strings.TrimSpace(*rc)
	}
	st.Credential = &session.Credential{
		MintedAt:      time.Now().UTC(),
		Remote:        res.record(),
		RevokeCommand: &pinned,
	}
	if err := session.Write(*st); err != nil {
		return MintOutcome{}, fmt.Errorf("[auth] record mint: %w", err)
	}
	outcome := MintOutcome{Minted: true}
	if res.Resolved {
		outcome.Resolved = res.Rewrite.HTTPS
	}
	return outcome, nil
}

// writeCredential writes the token into frontend state (0600, in a 0700
// directory), removes any legacy worktree copy, and returns the path. It
// writes in place (O_TRUNC, same inode).
func writeCredential(worktreePath, host, token string) (string, error) {
	path, err := StateCredentialPath(worktreePath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	line := "https://" + credentialUser + ":" + url.PathEscape(token) + "@" + host + "\n"
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if _, werr := f.WriteString(line); werr != nil {
		_ = f.Close()
		return "", werr
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	_ = os.Remove(LegacyCredentialPath(worktreePath))
	return path, nil
}

// Inject points dir's worktree-scoped git config at credFile and rewrites the
// forge's ssh origin to https so fetch AND push in dir authenticate with the
// token — never the inherited ssh-agent. Worktree-scoped (extensions.
// worktreeConfig, the same mechanism as the per-worktree pre-commit hook), so
// the root checkout and every other worktree keep their own auth. Only the
// forge host is rewritten: remotes on other hosts stay as they are.
func Inject(dir, credFile string, rw Rewrite) error {
	if git.CommonConfigHasWorktreeOverride(dir) {
		return errors.New("core.worktree is set in the shared git config; extensions.worktreeConfig would break it")
	}
	if _, err := git.Run(dir, "config", "extensions.worktreeConfig", "true"); err != nil {
		return fmt.Errorf("enabling extensions.worktreeConfig: %w", err)
	}
	if _, err := git.Run(dir, "config", "--worktree", "credential.helper", "store --file="+credFile); err != nil {
		return fmt.Errorf("setting credential.helper: %w", err)
	}
	if len(rw.From) > 0 {
		key := "url." + rw.HTTPS + ".insteadOf"
		if _, err := git.Run(dir, "config", "--worktree", "--replace-all", key, rw.From[0]); err != nil {
			return fmt.Errorf("setting %s: %w", key, err)
		}
		for _, v := range rw.From[1:] {
			if _, err := git.Run(dir, "config", "--worktree", "--add", key, v); err != nil {
				return fmt.Errorf("adding %s: %w", key, err)
			}
		}
	}
	return nil
}

// MirrorInto applies the session worktree's credential wiring to another
// worktree of the same repo — the disposable landing worktree the merge pushes
// from (FDR 0029) — pointing at the session worktree's credential file. A no-op
// when the session never minted one. The rewrite replays the remote the mint
// recorded on the session state (which may be resolver-produced), never a
// re-parse of today's origin.
func MirrorInto(repoPath, branch, sessionWorktree, dir string) error {
	if !Minted(sessionWorktree) {
		return nil
	}
	var rw Rewrite
	st, err := session.Read(repoPath, branch)
	switch {
	case err == nil && st.Credential != nil && st.Credential.Remote != nil:
		rw = rewriteFromRecord(st.Credential.Remote)
	case err == nil && st.Credential == nil:
		return errors.New("session state has no credential record for a minted worktree")
	case err == nil:
		// A record minted before #335 (Credential without Remote): necessarily
		// the built-in form. The helper is host-agnostic (the stored line names
		// the host), so an origin that is not a forge URL (a local path, say)
		// still gets it — only the ssh→https rewrite needs a parsed remote.
		if _, remote, oerr := readOrigin(sessionWorktree); oerr == nil {
			rw = builtinRewrite(remote)
		}
	case errors.Is(err, os.ErrNotExist):
		return errors.New("session state missing for a minted worktree")
	default:
		return fmt.Errorf("read session state for the stored credential remote: %w", err)
	}
	return Inject(dir, CredentialPath(sessionWorktree), rw)
}

// RevokeOutcome reports what Revoke did. Ran: the revoke-command ran (its error,
// if any, is Revoke's error). Skipped (human-readable) is why nothing ran.
// Note is a non-fatal remark worth surfacing, e.g. a legacy fallback.
type RevokeOutcome struct {
	Ran     bool
	Skipped string
	Note    string
}

// Revoke revokes the session's forge credential, deciding from the session
// RECORD (State.Credential set, RevokedAt nil), not from the credential file:
// revocation addresses the token by session id and needs no file. The command
// is the one pinned on the record at mint (see revokeCommandFor); no sweatfile
// of the session worktree is ever read. Command output streams to w. The state
// credential file is removed when the command ran, whatever its outcome (a
// failed revoke is recorded on the tombstone and retried by session id).
func Revoke(ctx context.Context, id Identity, w io.Writer) (RevokeOutcome, error) {
	st, err := session.Read(id.RepoPath, id.Branch)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return RevokeOutcome{Skipped: "no session record, so no credential to revoke"}, nil
	case err != nil:
		return RevokeOutcome{Skipped: "session record unreadable: " + err.Error()}, nil
	case st.Credential == nil:
		return RevokeOutcome{Skipped: "no credential was minted for this session"}, nil
	case st.Credential.RevokedAt != nil:
		return RevokeOutcome{Skipped: "credential already revoked"}, nil
	}
	var rr revokeResolver
	cmd, note, rerr := rr.command(st.Credential, id.RepoPath)
	if rerr != nil {
		return RevokeOutcome{Skipped: "revoke-command could not be resolved: " + rerr.Error()}, nil
	}
	if cmd == "" {
		return RevokeOutcome{Skipped: "a credential was minted but no revoke-command is pinned or configured above the worktree", Note: note}, nil
	}
	err = revoke(ctx, id, st, cmd, id.WorktreePath, w)
	RemoveCredential(id.WorktreePath)
	return RevokeOutcome{Ran: true, Note: note}, err
}

// revokeResolver resolves the revoke-command for credential records. A record
// with the command pinned at mint answers from the record. A legacy record
// (minted before the pin) falls back to the global and ancestor sweatfiles plus
// the repo layer read from the default branch (DefaultBranchHierarchy), never
// the worktree's copy; that lookup is made at most once per resolver.
type revokeResolver struct {
	legacyDone bool
	legacyCmd  string
	legacyNote string
	legacyErr  error
}

func (r *revokeResolver) command(c *session.Credential, repoPath string) (cmd, note string, err error) {
	if c.RevokeCommand != nil {
		return strings.TrimSpace(*c.RevokeCommand), "", nil
	}
	if !r.legacyDone {
		r.legacyDone = true
		home, herr := os.UserHomeDir()
		if herr != nil {
			r.legacyErr = herr
		} else if h, src, lerr := DefaultBranchHierarchy(home, repoPath); lerr != nil {
			r.legacyErr = lerr
		} else {
			if rc := h.Merged.AuthRevokeCommand(); rc != nil {
				r.legacyCmd = strings.TrimSpace(*rc)
			}
			r.legacyNote = "no revoke command stored at mint; using the global/ancestor sweatfiles and " + src
		}
	}
	return r.legacyCmd, r.legacyNote, r.legacyErr
}

// DefaultBranchHierarchy loads the sweatfile cascade for repoPath (global,
// ancestor directories, the main checkout's sweatfile) with the repo layer
// overlaid from the sweatfile COMMITTED at the default branch's landing ref,
// never from any session worktree: a worker can write its own worktree's
// sweatfile, and [auth] commands run on the frontend. The ref is the one merges
// land on and sessions are cut from: the default branch's remote-tracking ref
// when it exists, else the local branch (landing.ForMerge). src names the
// committed layer for reports. With no default branch there is no overlay.
func DefaultBranchHierarchy(home, repoPath string) (h sweatfile.Hierarchy, src string, err error) {
	var (
		data  []byte
		found bool
	)
	src = "the main checkout (no default branch to read)"
	if name, derr := git.DefaultBranch(repoPath); derr == nil {
		ref := landing.ForMerge(repoPath, name, true).Ref()
		if _, rpErr := git.RevParse(repoPath, ref); rpErr != nil {
			ref = landing.Self(repoPath, name).Ref()
		}
		data, found, err = git.FileAtRev(repoPath, ref, "sweatfile")
		if err != nil {
			return sweatfile.Hierarchy{}, "", fmt.Errorf("read sweatfile at %s: %w", ref, err)
		}
		src = "the repo sweatfile at " + ref
		h, err = sweatfileio.LoadHierarchyWithLayer(home, repoPath, ref+":sweatfile", data, found)
		return h, src, err
	}
	h, err = sweatfileio.LoadHierarchy(home, repoPath)
	return h, src, err
}

func revoke(ctx context.Context, id Identity, st *session.State, cmd, dir string, w io.Writer) error {
	// Revocation addresses the token by session id; the forge host/repo env
	// is a convenience, so an origin that is not a forge URL (a local path)
	// just leaves those two variables empty rather than blocking the revoke.
	// The stored record (what the mint saw) wins over a re-parse of today's
	// origin, so mint and revoke see the same values.
	var (
		remote    Remote
		originURL string
	)
	if st.Credential != nil && st.Credential.Remote != nil {
		r := st.Credential.Remote
		remote, originURL = Remote{Host: r.ForgeHost, OwnerRepo: r.ForgeRepo}, r.OriginURL
	} else if u, parsed, oerr := readOrigin(id.RepoPath); oerr == nil {
		remote, originURL = parsed, u
	}
	// Outside the devshell, like the url-resolver: a blocked .envrc made the
	// orphan sweep's revoke fail (round P), and the devshell is head-controlled,
	// so it must not be able to stop or steer a revocation either.
	out, err := commandCaptureAmbient(ctx, dir, cmd, id.env(remote, originURL))
	if w != nil && out != "" {
		_, _ = io.WriteString(w, out)
	}
	if err != nil {
		return fmt.Errorf("[auth] revoke-command failed: %w", err)
	}
	now := time.Now().UTC()
	if st.Credential != nil {
		c := *st.Credential
		c.RevokedAt = &now
		_ = session.UpdateCredential(id.RepoPath, id.Branch, &c)
	}
	return nil
}

// SweepOrphans revokes the credentials of this repo's sessions that ended
// without revoking — a tombstone or abandoned entry whose Credential has no
// RevokedAt (the session crashed, or its worktree was removed outside
// spinclass). Runs at the next session creation, best-effort per session: a
// failed revoke is reported and left for the next sweep (and for the issuer's
// own TTL sweep). Returns how many were revoked.
//
// Each session is revoked with the command pinned on its record at mint; a
// session with none configured is left alone. A legacy record (no pin) is
// resolved once per sweep from the default branch (see revokeResolver), and a
// remark saying so goes to w. No worktree sweatfile is read.
func SweepOrphans(ctx context.Context, repoPath string, w io.Writer) (int, []error) {
	states, err := session.ListAll(nil)
	if err != nil {
		return 0, []error{err}
	}
	var (
		revoked int
		errs    []error
		rr      revokeResolver
	)
	for i := range states {
		s := &states[i]
		if s.RepoPath != repoPath || s.Credential == nil || s.Credential.RevokedAt != nil {
			continue
		}
		if s.ResolveState() != session.StateAbandoned {
			continue
		}
		cmd, note, cerr := rr.command(s.Credential, repoPath)
		if cerr != nil {
			errs = append(errs, fmt.Errorf("%s: revoke-command could not be resolved: %w", s.SessionKey, cerr))
			continue
		}
		if cmd == "" {
			continue
		}
		if note != "" && w != nil {
			_, _ = fmt.Fprintf(w, "spinclass: %s: %s\n", s.SessionKey, note)
		}
		id := Identity{RepoPath: s.RepoPath, WorktreePath: s.WorktreePath, Branch: s.Branch, SessionKey: s.SessionKey}
		// The retry addresses the token by session id, so the file goes either way.
		rerr := revoke(ctx, id, s, cmd, repoPath, w)
		RemoveCredential(s.WorktreePath)
		if rerr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.SessionKey, rerr))
			continue
		}
		revoked++
	}
	return revoked, errs
}
