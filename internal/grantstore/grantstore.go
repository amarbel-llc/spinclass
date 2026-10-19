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
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"code.linenisgreat.com/spinclass/internal/profilegrant"
)

const (
	dirMode  = 0o700
	fileMode = 0o600
	// maxNameLen keeps a derived filename under the common 255-byte limit.
	maxNameLen = 200
)

// Store is a directory of grant records and a grantee index.
type Store struct {
	records string
	index   string
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
	s := &Store{records: dir, index: filepath.Join(dir, "index")}
	for _, d := range []string{s.records, s.index} {
		if err := os.MkdirAll(d, dirMode); err != nil {
			return nil, fmt.Errorf("grantstore: %w", err)
		}
	}
	return s, nil
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

// Put stores rec. It is idempotent for identical content and an error when a
// different record already lives under the same digest.
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
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) == string(data) {
			return nil
		}
		return fmt.Errorf("grantstore: put %s: a different record already exists under this digest", rec.Digest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("grantstore: %w", err)
	}
	return writeAtomic(s.records, path, data)
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
	name, err := granteeFilename(grantee)
	if err != nil {
		return err
	}
	rec, ok := s.Get(digest)
	if !ok {
		return fmt.Errorf("grantstore: bind %s: record %s not in store", grantee, digest)
	}
	g, err := profilegrant.ParseCanonicalText(rec.Text)
	if err != nil {
		return fmt.Errorf("grantstore: bind %s: %w", grantee, err)
	}
	if g.Grantee != grantee {
		return fmt.Errorf("grantstore: bind %s: record %s is granted to %q", grantee, digest, g.Grantee)
	}
	return writeAtomic(s.index, filepath.Join(s.index, name), []byte(digest))
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

// writeAtomic writes data via a temp file in dir and a rename, so a reader
// never sees a partial file.
func writeAtomic(dir, path string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("grantstore: %w", err)
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("grantstore: %w", err)
	}
	if err := tmp.Chmod(fileMode); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("grantstore: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("grantstore: %w", err)
	}
	return nil
}
