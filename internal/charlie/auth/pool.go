package auth

// Pre-minted token pool intake (FDR 0028 "Token pool"). The operator runs
// `papi forge token mint ... --batch ... --out <dir>` once, with one card tap,
// into $XDG_STATE_HOME/spinclass/token-pool. A session created while the card's
// ssh-agent is away claims one slot by renaming its token file out of the pool,
// instead of running [auth].mint-command. The pool location is fixed (not a
// sweatfile key), so no sweatfile layer, and no worker-writable file, can point
// intake at a directory of its choosing.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	poolDirName         = "token-pool"
	poolManifestName    = "manifest.json"
	poolManifestVersion = 1
	// poolClaimSuffix names the claimed token file beside the session's
	// credential in frontend state while the credential is being written.
	poolClaimSuffix = ".pool-claim"
)

// PoolDir is the pool directory: <state>/spinclass/token-pool, a sibling of the
// credentials directory (outside every worktree and every unit bind;
// confinement.ProtectedStateDirs names it).
func PoolDir() (string, error) {
	base, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "spinclass", poolDirName), nil
}

// poolPresent reports whether a pool directory exists. Any stat error other
// than absence counts as present, so the claim surfaces it rather than
// silently falling back.
func poolPresent() (string, bool) {
	dir, err := PoolDir()
	if err != nil {
		return "", false
	}
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	return dir, true
}

type poolSlot struct {
	Session  string     `json:"session"`
	Repo     string     `json:"repo"`
	ID       int64      `json:"id"`
	Deadline *time.Time `json:"deadline,omitempty"`
	File     string     `json:"file"`
}

type poolManifest struct {
	Version int        `json:"version"`
	Forge   string     `json:"forge"`
	User    string     `json:"user"`
	Tokens  []poolSlot `json:"tokens"`
}

// poolClaim is a claimed slot: the token (never logged) and where the renamed
// file sits until the credential has been written.
type poolClaim struct {
	slot    poolSlot
	apiHost string
	user    string
	token   string
	src     string // the pool path it was renamed from
	dest    string // the frontend-state path it was renamed to
}

// release puts an unused claim back in the pool.
func (c *poolClaim) release() { _ = os.Rename(c.dest, c.src) }

// discard removes the claimed file once the credential file holds the token.
func (c *poolClaim) discard() { _ = os.Remove(c.dest) }

// revokeCommand is the form pinned for a pool token: the token's forge-side
// session is the slot's, not $SPINCLASS_SESSION_ID. Like every revoke it needs
// the card, so it fails until the operator is back and is then retried.
func (c *poolClaim) revokeCommand() string {
	return "papi forge token revoke --host " + shellQuote(c.apiHost) +
		" --user " + shellQuote(c.user) + " --card-login --session " + shellQuote(c.slot.Session)
}

// escapeSession is papi's slot file stem: [A-Za-z0-9.] literal, every other
// byte _XX in uppercase hex.
func escapeSession(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "_%02X", c)
		}
	}
	return b.String()
}

// checkPoolDir refuses anything but a real directory closed to group and other.
func checkPoolDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("[auth] token-pool %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("[auth] token-pool %s is not a directory", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("[auth] token-pool %s is accessible to group or other (mode %04o); refusing it: chmod 700", dir, fi.Mode().Perm())
	}
	return nil
}

// readPoolManifest parses and validates the manifest. It holds no secrets, so
// its parse errors may be shown. forge-hosts must be set and list the
// manifest's host: a pool is a standing bearer credential, so unlike the mint
// gate an empty allow-list does not mean "any host".
func readPoolManifest(dir string, hosts []string) (poolManifest, string, error) {
	path := filepath.Join(dir, poolManifestName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return poolManifest{}, "", fmt.Errorf("[auth] token-pool manifest: %w", err)
	}
	var m poolManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return poolManifest{}, "", fmt.Errorf("[auth] token-pool manifest %s does not parse: %v", path, err)
	}
	if m.Version != poolManifestVersion {
		return poolManifest{}, "", fmt.Errorf("[auth] token-pool manifest %s has version %d, want %d", path, m.Version, poolManifestVersion)
	}
	u, err := url.Parse(m.Forge)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return poolManifest{}, "", fmt.Errorf("[auth] token-pool manifest forge %q is not an https URL", m.Forge)
	}
	if len(hosts) == 0 {
		return poolManifest{}, "", errors.New("[auth] token-pool requires [auth].forge-hosts to be set and to list the manifest's forge host")
	}
	if !slices.Contains(hosts, u.Hostname()) {
		return poolManifest{}, "", fmt.Errorf("[auth] token-pool manifest forge host %q is not in [auth].forge-hosts %v", u.Hostname(), hosts)
	}
	for _, s := range m.Tokens {
		if s.Session == "" || s.File != escapeSession(s.Session)+".token" {
			return poolManifest{}, "", fmt.Errorf("[auth] token-pool manifest slot %q: file %q is not the escaped session name", s.Session, s.File)
		}
	}
	return m, u.Host, nil
}

// claimPoolSlot claims the first slot for repo that is unclaimed and unexpired,
// by renaming its token file to dest (same filesystem as the pool, so atomic: of
// two racing claimers exactly one rename succeeds, the other sees the file gone
// and moves on). It returns nil, nil when the pool has no such slot. now is the
// deadline reference. Errors name paths and sessions, never token contents.
func claimPoolSlot(dir, repo string, hosts []string, now time.Time, dest string) (*poolClaim, error) {
	if err := checkPoolDir(dir); err != nil {
		return nil, err
	}
	m, apiHost, err := readPoolManifest(dir, hosts)
	if err != nil {
		return nil, err
	}
	for _, s := range m.Tokens {
		if s.Repo != repo {
			continue
		}
		if s.Deadline != nil && !s.Deadline.After(now) {
			continue
		}
		src := filepath.Join(dir, s.File)
		fi, err := os.Lstat(src)
		if errors.Is(err, fs.ErrNotExist) {
			continue // already claimed
		}
		if err != nil {
			return nil, fmt.Errorf("[auth] token-pool slot %s: %w", s.Session, err)
		}
		if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("[auth] token-pool slot %s: %s must be a regular file closed to group and other (mode %04o)", s.Session, src, fi.Mode().Perm())
		}
		if err := os.Rename(src, dest); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // lost the race
			}
			return nil, fmt.Errorf("[auth] token-pool slot %s: claim: %w", s.Session, err)
		}
		c := &poolClaim{slot: s, apiHost: apiHost, user: m.User, src: src, dest: dest}
		raw, rerr := os.ReadFile(dest)
		c.token = strings.TrimSpace(string(raw))
		if rerr != nil || c.token == "" {
			c.release()
			if rerr != nil {
				return nil, fmt.Errorf("[auth] token-pool slot %s: read claimed token: %w", s.Session, rerr)
			}
			return nil, fmt.Errorf("[auth] token-pool slot %s: token file is empty", s.Session)
		}
		return c, nil
	}
	return nil, nil
}

// claimFromPool claims a slot for worktreePath, into frontend state beside its
// credential.
func claimFromPool(dir string, hosts []string, repo, worktreePath string) (*poolClaim, error) {
	credPath, err := StateCredentialPath(worktreePath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(credPath), 0o700); err != nil {
		return nil, err
	}
	return claimPoolSlot(dir, repo, hosts, time.Now(), credPath+poolClaimSuffix)
}
