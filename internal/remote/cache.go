package remote

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

// xdgStateBase returns $XDG_STATE_HOME or its fallback. Mirrors the
// unexported helper in internal/chat (itself mirroring internal/session)
// so the remotes cache lands as a sibling of the session index under the
// same root.
func xdgStateBase() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state")
}

// cachePath is the per-remote completion cache file:
// $XDG_STATE_HOME/spinclass/remotes/<name>.json.
func cachePath(name string) string {
	return filepath.Join(xdgStateBase(), "spinclass", "remotes", name+".json")
}

// WriteCache stores a remote's last-seen rows for completion to read
// without networking. The write is atomic (temp file + rename, mirroring
// chat.Send) so a concurrent completion never observes a half-written
// cache.
func WriteCache(name string, rows []session.ListRow) error {
	if rows == nil {
		rows = []session.ListRow{}
	}
	data, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("marshal remote cache: %w", err)
	}
	final := cachePath(name)
	dir := filepath.Dir(final)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create remotes cache dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*.json")
	if err != nil {
		return fmt.Errorf("create temp cache: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp cache: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("commit cache: %w", err)
	}
	return nil
}

// ReadCache loads a remote's cached rows. A missing cache (host never
// listed) is not an error: it yields empty rows, matching completion's
// possibly-stale-never-networks contract.
func ReadCache(name string) ([]session.ListRow, error) {
	data, err := os.ReadFile(cachePath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return []session.ListRow{}, nil
		}
		return nil, fmt.Errorf("read remote cache %s: %w", name, err)
	}
	var rows []session.ListRow
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("parse remote cache %s: %w", name, err)
	}
	return rows, nil
}

// ReadAllCaches maps each configured remote's name to its cached rows.
// Missing or unreadable caches are silently empty — completion degrades
// per-host, never fails.
func ReadAllCaches(remotes []sweatfile.Remote) map[string][]session.ListRow {
	out := make(map[string][]session.ListRow, len(remotes))
	for _, r := range remotes {
		rows, err := ReadCache(r.Name)
		if err != nil {
			rows = []session.ListRow{}
		}
		out[r.Name] = rows
	}
	return out
}
