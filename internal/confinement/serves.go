package confinement

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ServeRecord is one running `spinclass serve`, written under
// $XDG_STATE_HOME/spinclass/serves/ so a second serve claiming the same clown
// principal is detectable (FDR 0036 section 9).
type ServeRecord struct {
	Principal string `json:"principal"`
	PID       int    `json:"pid"`
	PPID      int    `json:"ppid"`
	Started   string `json:"started"`
	// Build is the serve's version+commit, so `sc list` can flag a serve still
	// running an older build than the installed one (spinclass#399's residual).
	// Records written before the field existed lack it.
	Build string `json:"build,omitempty"`
}

func servesDir(stateDir string) string {
	return filepath.Join(stateDir, "spinclass", "serves")
}

func serveRecordName(principal string, pid int) string {
	return principal + "-" + strconv.Itoa(pid) + ".json"
}

func pidAlive(sys System, pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := sys.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	return err == nil
}

// RegisterServe records this serve and returns the function that removes the
// record. An empty principal writes nothing: such a serve stays confined
// whatever the records say. Records of dead serves are pruned on the way.
func RegisterServe(stateDir, principal, build string, pid, ppid int, now time.Time) (func(), error) {
	noop := func() {}
	if principal == "" {
		return noop, nil
	}
	if err := validPrincipalName(principal); err != nil {
		return noop, err
	}
	dir := servesDir(stateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return noop, fmt.Errorf("creating the serve records directory: %w", err)
	}
	pruneDeadServeRecords(Host{}, dir)
	raw, err := json.Marshal(ServeRecord{Principal: principal, PID: pid, PPID: ppid, Started: now.UTC().Format(time.RFC3339), Build: build})
	if err != nil {
		return noop, err
	}
	path := filepath.Join(dir, serveRecordName(principal, pid))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return noop, fmt.Errorf("writing the serve record: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return noop, fmt.Errorf("writing the serve record: %w", err)
	}
	return func() { _ = os.Remove(path) }, nil
}

func pruneDeadServeRecords(sys System, dir string) {
	names, err := sys.ReadDir(dir)
	if err != nil {
		return
	}
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		raw, err := sys.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var rec ServeRecord
		if json.Unmarshal(raw, &rec) != nil || pidAlive(sys, rec.PID) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// LiveServeBuilds maps each principal to the builds of its live serves, read
// from the serve records; a record without a build maps to "". Unreadable or
// unparseable records and dead serves are skipped: the result is display-only.
func LiveServeBuilds(sys System, stateDir string) map[string][]string {
	dir := servesDir(stateDir)
	names, err := sys.ReadDir(dir)
	if err != nil {
		return nil
	}
	builds := map[string][]string{}
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		raw, err := sys.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var rec ServeRecord
		if json.Unmarshal(raw, &rec) != nil || rec.Principal == "" || !pidAlive(sys, rec.PID) {
			continue
		}
		builds[rec.Principal] = append(builds[rec.Principal], rec.Build)
	}
	return builds
}

// ClassifyServeCaller is ClassifyCaller plus the one-serve check: a caller the
// unit records and principal mapping call unconfined is still confined when
// more than one live serve carries the principal, or when this serve's own
// record is missing or does not match. That detects a duplicated serve; it
// does not detect several peers behind one clown stdio-bridge port.
func ClassifyServeCaller(sys System, stateDir, principal string, ownPID int) (confined bool, why string) {
	if confined, why = ClassifyCaller(sys, stateDir, principal); confined {
		return confined, why
	}
	return classifyByServeRecords(sys, stateDir, principal, ownPID)
}

func classifyByServeRecords(sys System, stateDir, principal string, ownPID int) (bool, string) {
	dir := servesDir(stateDir)
	names, err := sys.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return true, "this serve has no record"
	}
	if err != nil {
		return true, fmt.Sprintf("serve records unreadable: %v", err)
	}
	ownFound, live := false, 0
	for _, name := range names {
		if !strings.HasPrefix(name, principal+"-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		raw, err := sys.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return true, fmt.Sprintf("serve record %s unreadable: %v", name, err)
		}
		var rec ServeRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return true, fmt.Sprintf("serve record %s does not parse: %v", name, err)
		}
		if rec.Principal != principal || !pidAlive(sys, rec.PID) {
			continue
		}
		live++
		if rec.PID == ownPID && name == serveRecordName(principal, ownPID) {
			ownFound = true
		}
	}
	switch {
	case !ownFound:
		return true, "this serve's own record is missing or does not match"
	case live > 1:
		return true, fmt.Sprintf("%d live serves carry principal %s", live, principal)
	}
	return false, "the only live serve carrying the principal"
}
