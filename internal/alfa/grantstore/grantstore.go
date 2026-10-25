// Package grantstore is the on-disk store for profile-grant records (FDR 0034,
// Phase 2): records by digest, plus an index of each grantee's current record.
// Every read re-verifies content against its digest, so a tampered file is
// treated as absent rather than trusted; authority still comes from the
// card-signed root, never from the store's contents.
package grantstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"code.linenisgreat.com/spinclass/internal/0/profilegrant"
	"code.linenisgreat.com/spinclass/internal/0/trustedhome"
)

const (
	dirMode  = 0o700
	fileMode = 0o600
	// maxNameLen keeps a derived filename under the common 255-byte limit.
	maxNameLen = 200
)

// Store is a directory of grant records and a grantee index, plus the
// profile transition approvals (spinclass#407) in a transitions subdirectory.
type Store struct {
	records     string
	index       string
	transitions string
}

// TrustedDir is the grant store enforcement reads: under the passwd home's
// .local/state, never the caller's environment, and refusing an
// XDG_STATE_HOME that points elsewhere (trustedhome, audit row 34-31).
func TrustedDir() (string, error) {
	return trustedhome.Resolve("XDG_STATE_HOME", filepath.Join(".local", "state"), "spinclass", "grants")
}

// DefaultDir is $XDG_STATE_HOME/spinclass/grants, falling back to
// ~/.local/state/spinclass/grants (the same base the session index uses).
func DefaultDir() (string, error) {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "spinclass", "grants"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("grantstore: resolving home: %w", err)
	}
	return filepath.Join(home, ".local", "state", "spinclass", "grants"), nil
}

// Open creates dir (and its index subdirectory) if needed and returns a Store.
func Open(dir string) (*Store, error) {
	s := &Store{records: dir, index: filepath.Join(dir, "index"), transitions: filepath.Join(dir, "transitions")}
	for _, d := range []string{s.records, s.index, s.transitions} {
		// A symlinked store directory would redirect every record somewhere
		// the operator never looked.
		if info, err := os.Lstat(d); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("grantstore: %s is a symlink; refusing to use it as the store", d)
		}
		if err := os.MkdirAll(d, dirMode); err != nil {
			return nil, fmt.Errorf("grantstore: %w", err)
		}
		// MkdirAll leaves a pre-existing directory's mode alone.
		if err := os.Chmod(d, dirMode); err != nil {
			return nil, fmt.Errorf("grantstore: %w", err)
		}
	}
	return s, nil
}

// OpenExisting is Open for callers that must not create the store: a missing
// dir yields an error satisfying errors.Is(err, fs.ErrNotExist) and nothing is
// created.
func OpenExisting(dir string) (*Store, error) {
	if _, err := os.Lstat(dir); err != nil {
		return nil, err
	}
	return Open(dir)
}

// checkDigestName admits only [A-Za-z0-9._@+-], no leading dot, so a digest can
// never name a path outside the store.
func checkDigestName(digest string) error {
	if digest == "" || len(digest) > maxNameLen {
		return fmt.Errorf("digest %q: empty or too long", digest)
	}
	if digest[0] == '.' {
		return fmt.Errorf("digest %q: leading dot", digest)
	}
	for _, r := range digest {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("._@+-", r):
		default:
			return fmt.Errorf("digest %q: disallowed character %U", digest, r)
		}
	}
	return nil
}

// granteeFilename validates a grantee with the grant identity allowlist and
// maps it to a flat filename. The allowlist admits '/', because a grantee may
// be a session key (<repo>/<branch>); '%' is not in the allowlist, so encoding
// '/' as "%2F" is unambiguous and the result can never be a path.
func granteeFilename(grantee string) (string, error) {
	if err := profilegrant.ValidIdentity("grantee", grantee); err != nil {
		return "", err
	}
	name := strings.ReplaceAll(grantee, "/", "%2F")
	if len(name) > maxNameLen || name[0] == '.' {
		return "", fmt.Errorf("grantee %q: too long or leading dot", grantee)
	}
	return name, nil
}

// verified reports whether rec is internally consistent: digest names the
// text, and the text parses.
func verified(rec profilegrant.Record) (profilegrant.Grant, error) {
	want, err := profilegrant.DigestOf(rec.Text)
	if err != nil {
		return profilegrant.Grant{}, fmt.Errorf("digest of text: %w", err)
	}
	if rec.Digest != want {
		return profilegrant.Grant{}, fmt.Errorf("digest %q does not match text (hashes to %q)", rec.Digest, want)
	}
	g, err := profilegrant.ParseCanonicalText(rec.Text)
	if err != nil {
		return profilegrant.Grant{}, fmt.Errorf("parse: %w", err)
	}
	return g, nil
}

// cardSigned reports whether rec is a root record carrying a signature.
func cardSigned(rec profilegrant.Record) bool { return rec.Root && len(rec.Sig) > 0 }

// Put stores rec. The digest covers the text only, so signature and root flag
// are not part of the key: creating a record is atomic and no-clobber (a
// first-come unsigned copy must not be able to race a genuine one), identical
// content is idempotent, and an existing record is replaced only by an
// UPGRADE to a card-signed root. A downgrade or lateral change is an error.
// An existing file that fails verification counts as absent (Get ignores it).
func (s *Store) Put(rec profilegrant.Record) error {
	if err := checkDigestName(rec.Digest); err != nil {
		return err
	}
	if _, err := verified(rec); err != nil {
		return fmt.Errorf("grantstore: put %s: %w", rec.Digest, err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	path := filepath.Join(s.records, rec.Digest)

	tmp, err := writeTemp(s.records, data)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	// Link, not rename: link fails with EEXIST instead of replacing.
	err = os.Link(tmp, path)
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("grantstore: %w", err)
	}

	existing, ok := s.Get(rec.Digest)
	if ok {
		if existingData, rerr := json.Marshal(existing); rerr == nil && string(existingData) == string(data) {
			return nil
		}
		if !cardSigned(rec) || cardSigned(existing) {
			return fmt.Errorf("grantstore: put %s: a different record already exists under this digest and this is not an upgrade to a card-signed root", rec.Digest)
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("grantstore: %w", err)
	}
	return nil
}

// Get returns the record under digest, re-verified. A missing, unreadable,
// corrupted or tampered file is simply not found.
func (s *Store) Get(digest string) (profilegrant.Record, bool) {
	if err := checkDigestName(digest); err != nil {
		slog.Debug("grantstore: get rejected", "err", err)
		return profilegrant.Record{}, false
	}
	data, err := os.ReadFile(filepath.Join(s.records, digest))
	if err != nil {
		return profilegrant.Record{}, false
	}
	var rec profilegrant.Record
	if err := json.Unmarshal(data, &rec); err != nil {
		slog.Debug("grantstore: unreadable record", "digest", digest, "err", err)
		return profilegrant.Record{}, false
	}
	if rec.Digest != digest {
		slog.Debug("grantstore: record stored under the wrong digest", "digest", digest, "claimed", rec.Digest)
		return profilegrant.Record{}, false
	}
	if _, err := verified(rec); err != nil {
		slog.Debug("grantstore: record failed verification", "digest", digest, "err", err)
		return profilegrant.Record{}, false
	}
	return rec, true
}

// Resolver adapts the store to profilegrant.Walk.
func (s *Store) Resolver() profilegrant.Resolver { return s.Get }

// Bind records digest as grantee's current record. The record must already be
// in the store and name grantee as its grantee.
func (s *Store) Bind(grantee, digest string) error {
	path, err := s.bindingPath(grantee, digest)
	if err != nil {
		return err
	}
	return writeAtomic(s.index, path, []byte(digest))
}

// bindingPath checks that digest may be bound to grantee and returns
// grantee's index file.
func (s *Store) bindingPath(grantee, digest string) (string, error) {
	name, err := granteeFilename(grantee)
	if err != nil {
		return "", err
	}
	rec, ok := s.Get(digest)
	if !ok {
		return "", fmt.Errorf("grantstore: bind %s: record %s not in store", grantee, digest)
	}
	g, err := profilegrant.ParseCanonicalText(rec.Text)
	if err != nil {
		return "", fmt.Errorf("grantstore: bind %s: %w", grantee, err)
	}
	if g.Grantee != grantee {
		return "", fmt.Errorf("grantstore: bind %s: record %s is granted to %q", grantee, digest, g.Grantee)
	}
	return filepath.Join(s.index, name), nil
}

// ErrAlreadyBound is what BindNew's error satisfies when grantee already has
// a binding.
var ErrAlreadyBound = errors.New("grantee already has a binding")

// BindNew is Bind for a grantee that must not have a binding yet: an atomic
// create-if-absent, so of concurrent callers exactly one wins (spinclass#377).
// Any existing index file counts as a binding, even one Current would ignore
// as stale or tampered, because the key has been used.
func (s *Store) BindNew(grantee, digest string) error {
	path, err := s.bindingPath(grantee, digest)
	if err != nil {
		return err
	}
	tmp, err := writeTemp(s.index, []byte(digest))
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	// Link, not rename: link fails with EEXIST instead of replacing.
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("grantstore: bind %s: %w", grantee, ErrAlreadyBound)
		}
		return fmt.Errorf("grantstore: %w", err)
	}
	return nil
}

// Current returns grantee's bound record, re-verified like Get.
func (s *Store) Current(grantee string) (profilegrant.Record, bool) {
	name, err := granteeFilename(grantee)
	if err != nil {
		slog.Debug("grantstore: current rejected", "err", err)
		return profilegrant.Record{}, false
	}
	data, err := os.ReadFile(filepath.Join(s.index, name))
	if err != nil {
		return profilegrant.Record{}, false
	}
	rec, ok := s.Get(strings.TrimSpace(string(data)))
	if !ok {
		return profilegrant.Record{}, false
	}
	// A swapped index file must not hand grantee someone else's record.
	if g, err := profilegrant.ParseCanonicalText(rec.Text); err != nil || g.Grantee != grantee {
		slog.Debug("grantstore: index points at a record for another grantee", "grantee", grantee)
		return profilegrant.Record{}, false
	}
	return rec, true
}

// Unbind removes grantee's binding; absent is not an error.
func (s *Store) Unbind(grantee string) error {
	name, err := granteeFilename(grantee)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.index, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("grantstore: %w", err)
	}
	return nil
}

// PutTransition stores a profile transition approval under its digest. Like
// Put it is no-clobber: identical content is idempotent and any other record
// under an existing digest is refused. Only internal consistency is checked;
// the signature and redemption window are checked by
// profilegrant.VerifyTransition on use.
func (s *Store) PutTransition(rec profilegrant.TransitionRecord) error {
	if err := checkDigestName(rec.Digest); err != nil {
		return err
	}
	if _, err := profilegrant.CheckTransitionRecord(rec); err != nil {
		return fmt.Errorf("grantstore: put transition %s: %w", rec.Digest, err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	tmp, err := writeTemp(s.transitions, data)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	err = os.Link(tmp, filepath.Join(s.transitions, rec.Digest))
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("grantstore: %w", err)
	}
	if existing, ok := s.getTransition(rec.Digest); ok {
		if existingData, merr := json.Marshal(existing); merr == nil && string(existingData) == string(data) {
			return nil
		}
	}
	return fmt.Errorf("grantstore: put transition %s: a different record already exists under this digest", rec.Digest)
}

// FindTransitions returns the stored approvals for exactly this change, each
// re-checked for consistency (a tampered file is skipped). The caller verifies
// signature and redemption window.
func (s *Store) FindTransitions(profile, from, to string) []profilegrant.TransitionRecord {
	entries, err := os.ReadDir(s.transitions)
	if err != nil {
		return nil
	}
	var out []profilegrant.TransitionRecord
	for _, e := range entries {
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		rec, ok := s.getTransition(e.Name())
		if !ok {
			continue
		}
		if tr, err := profilegrant.CheckTransitionRecord(rec); err == nil && tr.Covers(profile, from, to) {
			out = append(out, rec)
		}
	}
	return out
}

func (s *Store) getTransition(digest string) (profilegrant.TransitionRecord, bool) {
	if err := checkDigestName(digest); err != nil {
		return profilegrant.TransitionRecord{}, false
	}
	data, err := os.ReadFile(filepath.Join(s.transitions, digest))
	if err != nil {
		return profilegrant.TransitionRecord{}, false
	}
	var rec profilegrant.TransitionRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.Digest != digest {
		slog.Debug("grantstore: unreadable transition record", "digest", digest, "err", err)
		return profilegrant.TransitionRecord{}, false
	}
	if _, err := profilegrant.CheckTransitionRecord(rec); err != nil {
		slog.Debug("grantstore: transition record failed verification", "digest", digest, "err", err)
		return profilegrant.TransitionRecord{}, false
	}
	return rec, true
}

// writeTemp writes data to a fresh mode-0600 temp file in dir and returns its
// name.
func writeTemp(dir string, data []byte) (string, error) {
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("grantstore: %w", err)
	}
	name := tmp.Name()
	fail := func(err error) (string, error) {
		_ = tmp.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("grantstore: %w", err)
	}
	if err := tmp.Chmod(fileMode); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("grantstore: %w", err)
	}
	return name, nil
}

// writeAtomic writes data via a temp file in dir and a rename, so a reader
// never sees a partial file.
func writeAtomic(dir, path string, data []byte) error {
	name, err := writeTemp(dir, data)
	if err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("grantstore: %w", err)
	}
	return nil
}
