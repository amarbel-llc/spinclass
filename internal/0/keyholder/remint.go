package keyholder

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// NoticeInterval is the least time between two wakes about the same session's
// re-minted key: a pair that flaps must not flood its holders. Wakes inside the
// interval are counted, and the next one that goes out carries the count.
const NoticeInterval = 10 * time.Minute

// Fingerprint is the ssh-keygen -l style SHA256 fingerprint of a public key
// line ("SHA256:" and unpadded base64 of the SHA-256 of the key blob).
func Fingerprint(pub string) (string, error) {
	f := strings.Fields(pub)
	if len(f) < 2 {
		return "", fmt.Errorf("not a public key line: %q", truncate(pub, 80))
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil {
		return "", fmt.Errorf("the key blob is not base64: %w", err)
	}
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

// Remint is a pair that was lost and replaced: the session now has a different
// key, so any certificate for the old one is void and must be signed again.
type Remint struct {
	OldFingerprint string
	OldMintedAt    time.Time
	NewFingerprint string
	At             time.Time
}

// last is what outlives a lost pair. It sits NEXT TO the session's pair
// directory, not inside it, because replacing a lost pair removes that directory
// whole; it goes only when the session's pair is stopped on purpose.
type last struct {
	Fingerprint string    `json:"fingerprint"`
	MintedAt    time.Time `json:"minted_at"`
	// RemintedFrom and RemintedAt are set when this key replaced another; they
	// stay until the session is stopped, so `sc list` can keep saying so while
	// no certificate for the new key exists.
	RemintedFrom string    `json:"reminted_from,omitempty"`
	RemintedAt   time.Time `json:"reminted_at,omitempty"`
	// NoticeAt and Suppressed rate-limit the wake about a re-mint.
	NoticeAt   time.Time `json:"notice_at,omitempty"`
	Suppressed int       `json:"suppressed,omitempty"`
}

// LastPath is the sidecar file of a session's key history under base.
func LastPath(base, sessionKey string) string {
	return filepath.Join(base, Hash(sessionKey)+".last.json")
}

func readLast(path string) (last, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return last{}, false
	}
	var l last
	if json.Unmarshal(b, &l) != nil || l.Fingerprint == "" {
		return last{}, false
	}
	return l, true
}

func writeLast(path string, l last) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// noteMint records a fresh mint in the session's key history and reports the
// re-mint when it replaced a different key.
func (l *Launcher) noteMint(sessionKey string, p Pair) (*Remint, error) {
	path := LastPath(l.Base, sessionKey)
	prev, had := readLast(path)
	cur := last{Fingerprint: p.Fingerprint, MintedAt: p.MintedAt}
	var rm *Remint
	if had && prev.Fingerprint != p.Fingerprint {
		rm = &Remint{OldFingerprint: prev.Fingerprint, OldMintedAt: prev.MintedAt, NewFingerprint: p.Fingerprint, At: p.MintedAt}
		cur.RemintedFrom, cur.RemintedAt = prev.Fingerprint, p.MintedAt
		cur.NoticeAt, cur.Suppressed = prev.NoticeAt, prev.Suppressed
	} else if had {
		cur = prev
	}
	if err := writeLast(path, cur); err != nil {
		return rm, fmt.Errorf("recording the key history of %s: %w", sessionKey, err)
	}
	return rm, nil
}

// ClaimNotice says whether a wake about a re-mint of sessionKey's key may go out
// now. At most one goes out per NoticeInterval; the ones held back are counted,
// and the count of those held back since the last wake is returned with the next
// one that may go. A session with no key history has nothing to announce.
func (l *Launcher) ClaimNotice(sessionKey string, now time.Time) (send bool, suppressed int) {
	path := LastPath(l.Base, sessionKey)
	cur, ok := readLast(path)
	if !ok {
		return false, 0
	}
	if !cur.NoticeAt.IsZero() && now.Sub(cur.NoticeAt) < NoticeInterval {
		cur.Suppressed++
		_ = writeLast(path, cur)
		return false, 0
	}
	suppressed = cur.Suppressed
	cur.NoticeAt, cur.Suppressed = now.UTC(), 0
	_ = writeLast(path, cur)
	return true, suppressed
}

// Health is what `sc list` and session-health say about a session's holder.
type Health struct {
	// State is "none" (no pair was ever recorded), "ok" or "dead" (a recorded
	// pair whose units or socket are gone: the next launch or restart re-mints).
	State       string
	Fingerprint string
	// Reminted says the current key replaced another one and no certificate has
	// been installed for it yet.
	Reminted       bool
	OldFingerprint string
	RemintedAt     time.Time
	// Cert is the installed certificate as inspected (State "" when the launcher
	// has no CertLister, so only its presence is known).
	Cert CertInfo
}

// Note is the one line a listing shows for a holder that needs the operator's
// attention, or "" when there is nothing to say.
func (h Health) Note() string {
	switch {
	case h.State == "dead":
		return "keyholder: DEAD (the pair is not running; a resume or restart mints a NEW key)"
	case h.Reminted:
		return fmt.Sprintf("keyholder: RE-MINTED %s, old %s, new %s: no certificate for the new key yet (sc session-key)",
			h.RemintedAt.UTC().Format(time.RFC3339), h.OldFingerprint, h.Fingerprint)
	}
	switch h.Cert.State {
	case CertValid:
		if h.Cert.Expires == nil {
			return "keyholder: certificate valid (no expiry)"
		}
		return "keyholder: certificate valid until " + h.Cert.Expires.UTC().Format(time.RFC3339)
	case CertOtherKey:
		return fmt.Sprintf("keyholder: certificate is for ANOTHER key (%s), not this session's (%s)", h.Cert.Fingerprint, h.Fingerprint)
	case CertExpired:
		return "keyholder: certificate EXPIRED " + h.Cert.Expires.UTC().Format(time.RFC3339)
	case CertUnreadable:
		return "keyholder: certificate unusable: " + h.Cert.Problem
	}
	return ""
}

// HasState reports whether a session has ever been given a pair that is still
// on record: a pair record or a key history under base. It reads two files and
// asks systemd nothing, so a caller can skip the sessions that never had one.
func HasState(base, sessionKey string) bool {
	for _, p := range []string{filepath.Join(SessionDir(base, sessionKey), "record.json"), LastPath(base, sessionKey)} {
		if _, err := os.Lstat(p); err == nil {
			return true
		}
	}
	return false
}

// HealthOf reads a session's holder state. It starts and mints nothing.
func (l *Launcher) HealthOf(ctx context.Context, sessionKey string) (Health, error) {
	ly := l.layoutFor(sessionKey)
	rec, hasRec := readRecord(ly.record)
	hist, hasHist := readLast(LastPath(l.Base, sessionKey))
	if !hasRec && !hasHist {
		return Health{State: "none"}, nil
	}
	h := Health{State: "ok"}
	if hasHist {
		h.Fingerprint = hist.Fingerprint
	}
	if h.Fingerprint == "" && hasRec {
		h.Fingerprint = rec.Fingerprint
	}
	certPath := CertPathIn(ly.dir)
	certInstalled := true
	if _, err := os.Lstat(certPath); errors.Is(err, os.ErrNotExist) {
		certInstalled = false
	}
	if l.CertList != nil {
		now := time.Now()
		if l.Now != nil {
			now = l.Now()
		}
		h.Cert = InspectCert(ctx, certPath, h.Fingerprint, l.CertList, now)
	}
	if hasHist && hist.RemintedFrom != "" {
		// The re-mint stands until a certificate for the NEW key is installed: with
		// a CertLister that means a valid one (the old key's certificate does not
		// end it); without one, any installed file does.
		stillUncertified := !certInstalled
		if l.CertList != nil {
			stillUncertified = h.Cert.State != CertValid
		}
		if stillUncertified {
			h.Reminted, h.OldFingerprint, h.RemintedAt = true, hist.RemintedFrom, hist.RemintedAt
		}
	}
	if !hasRec {
		h.State = "dead"
		return h, nil
	}
	live, err := l.pairLive(ctx, ly)
	if err != nil {
		return h, err
	}
	if !live {
		h.State = "dead"
	}
	return h, nil
}
