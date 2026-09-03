// Package auth implements per-session forge push credentials (FDR 0028): a
// token minted by [auth].mint-command at worktree creation, stored mode-600
// under .spinclass/, wired into the worktree via worktree-scoped git config
// (a credential helper plus a forge-host-scoped ssh→https URL rewrite), mirrored
// onto the disposable landing worktree at merge time (FDR 0029), revoked by
// [auth].revoke-command at close, and swept for sessions that died without
// closing.
package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
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

func credentialPath(worktreePath string) string {
	return filepath.Join(worktreePath, ".spinclass", CredentialFile)
}

// Minted reports whether worktreePath carries a minted credential.
func Minted(worktreePath string) bool {
	_, err := os.Stat(credentialPath(worktreePath))
	return err == nil
}

func (id Identity) env(r Remote) []string {
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
	}
}

// originRemote reads origin's CONFIGURED url. Not `git remote get-url`, which
// applies url.*.insteadOf — once Inject has rewritten the forge to https in a
// worktree, that would report the https form and hide the ssh prefix the next
// Inject (the landing worktree's) has to rewrite.
func originRemote(dir string) (Remote, error) {
	raw, err := git.Run(dir, "config", "--get", "remote.origin.url")
	if err != nil {
		return Remote{}, fmt.Errorf("resolve origin remote: %w", err)
	}
	return ParseForgeRemote(raw)
}

// Mint runs [auth].mint-command in the session worktree, writes the token it
// prints as a mode-600 git-credential-store file, injects the worktree-scoped
// git config, and records the mint on the session state. Returns false with a
// nil error when no mint-command is configured.
func Mint(ctx context.Context, sf sweatfile.Sweatfile, id Identity) (bool, error) {
	cmd := sf.AuthMintCommand()
	if cmd == nil || strings.TrimSpace(*cmd) == "" {
		return false, nil
	}
	remote, err := originRemote(id.RepoPath)
	if err != nil {
		return false, fmt.Errorf("[auth] mint: %w", err)
	}
	out, err := sweatfile.RunCommandCapture(ctx, id.WorktreePath, *cmd, id.env(remote))
	if err != nil {
		return false, fmt.Errorf("[auth] mint-command failed: %w", err)
	}
	token := strings.TrimSpace(out)
	if token == "" {
		return false, errors.New("[auth] mint-command printed no token on stdout")
	}
	if err := writeCredential(id.WorktreePath, remote.Host, token); err != nil {
		return false, fmt.Errorf("[auth] write credential: %w", err)
	}
	if err := Inject(id.WorktreePath, credentialPath(id.WorktreePath), remote); err != nil {
		return false, fmt.Errorf("[auth] inject worktree config: %w", err)
	}
	st, err := session.EnsureWorktreeState(id.RepoPath, id.Branch, id.SessionKey, 0)
	if err != nil {
		return false, fmt.Errorf("[auth] record mint: %w", err)
	}
	st.Credential = &session.Credential{MintedAt: time.Now().UTC()}
	if err := session.Write(*st); err != nil {
		return false, fmt.Errorf("[auth] record mint: %w", err)
	}
	return true, nil
}

func writeCredential(worktreePath, host, token string) error {
	path := credentialPath(worktreePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line := "https://" + credentialUser + ":" + url.PathEscape(token) + "@" + host + "\n"
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, werr := f.WriteString(line); werr != nil {
		_ = f.Close()
		return werr
	}
	return f.Close()
}

// Inject points dir's worktree-scoped git config at credFile and rewrites the
// forge's ssh origin to https so fetch AND push in dir authenticate with the
// token — never the inherited ssh-agent. Worktree-scoped (extensions.
// worktreeConfig, the same mechanism as the per-worktree pre-commit hook), so
// the root checkout and every other worktree keep their own auth. Only the
// forge host is rewritten: remotes on other hosts stay as they are.
func Inject(dir, credFile string, r Remote) error {
	if sweatfile.CommonConfigHasWorktreeOverride(dir) {
		return errors.New("core.worktree is set in the shared git config; extensions.worktreeConfig would break it")
	}
	if _, err := git.Run(dir, "config", "extensions.worktreeConfig", "true"); err != nil {
		return fmt.Errorf("enabling extensions.worktreeConfig: %w", err)
	}
	if _, err := git.Run(dir, "config", "--worktree", "credential.helper", "store --file="+credFile); err != nil {
		return fmt.Errorf("setting credential.helper: %w", err)
	}
	if r.SSHPrefix != "" {
		key := "url.https://" + r.Host + "/.insteadOf"
		if _, err := git.Run(dir, "config", "--worktree", "--replace-all", key, r.SSHPrefix); err != nil {
			return fmt.Errorf("setting %s: %w", key, err)
		}
	}
	return nil
}

// MirrorInto applies the session worktree's credential wiring to another
// worktree of the same repo — the disposable landing worktree the merge pushes
// from (FDR 0029) — pointing at the session worktree's credential file. A no-op
// when the session never minted one.
func MirrorInto(sessionWorktree, dir string) error {
	if !Minted(sessionWorktree) {
		return nil
	}
	// The helper is host-agnostic (the stored line names the host), so an
	// origin that is not a forge URL (a local path, say) still gets it — only
	// the ssh→https rewrite needs a parsed remote.
	remote, err := originRemote(sessionWorktree)
	if err != nil {
		remote = Remote{}
	}
	return Inject(dir, credentialPath(sessionWorktree), remote)
}

// Revoke runs [auth].revoke-command for a session that minted a credential,
// then removes the credential file and records the revocation. Command output
// streams to w. Returns false with a nil error when there is nothing to revoke
// (no revoke-command, or no credential was minted).
func Revoke(ctx context.Context, sf sweatfile.Sweatfile, id Identity, w io.Writer) (bool, error) {
	if !Minted(id.WorktreePath) {
		return false, nil
	}
	if err := revoke(ctx, sf, id, id.WorktreePath, w); err != nil {
		return true, err
	}
	_ = os.Remove(credentialPath(id.WorktreePath))
	return true, nil
}

func revoke(ctx context.Context, sf sweatfile.Sweatfile, id Identity, dir string, w io.Writer) error {
	cmd := sf.AuthRevokeCommand()
	if cmd == nil || strings.TrimSpace(*cmd) == "" {
		return errors.New("[auth] a credential was minted but no revoke-command is configured")
	}
	// Revocation addresses the token by session id; the forge host/repo env
	// is a convenience, so an origin that is not a forge URL (a local path)
	// just leaves those two variables empty rather than blocking the revoke.
	remote, err := originRemote(id.RepoPath)
	if err != nil {
		remote = Remote{}
	}
	out, err := sweatfile.RunCommandCapture(ctx, dir, *cmd, id.env(remote))
	if w != nil && out != "" {
		_, _ = io.WriteString(w, out)
	}
	if err != nil {
		return fmt.Errorf("[auth] revoke-command failed: %w", err)
	}
	now := time.Now().UTC()
	if st, rerr := session.Read(id.RepoPath, id.Branch); rerr == nil && st.Credential != nil {
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
func SweepOrphans(ctx context.Context, sf sweatfile.Sweatfile, repoPath string, w io.Writer) (int, []error) {
	if cmd := sf.AuthRevokeCommand(); cmd == nil || strings.TrimSpace(*cmd) == "" {
		return 0, nil
	}
	states, err := session.ListAll(nil)
	if err != nil {
		return 0, []error{err}
	}
	var (
		revoked int
		errs    []error
	)
	for _, s := range states {
		if s.RepoPath != repoPath || s.Credential == nil || s.Credential.RevokedAt != nil {
			continue
		}
		if s.ResolveState() != session.StateAbandoned {
			continue
		}
		id := Identity{RepoPath: s.RepoPath, WorktreePath: s.WorktreePath, Branch: s.Branch, SessionKey: s.SessionKey}
		if rerr := revoke(ctx, sf, id, repoPath, w); rerr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.SessionKey, rerr))
			continue
		}
		revoked++
	}
	return revoked, errs
}
