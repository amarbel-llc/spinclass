package spawn

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// TEMPORARY (spinclass#384 stopgap): DriverMaySpawn is the one place the
// "an unattended driver may not spawn" bound is relaxed. Delete with it.

const allowPrincipal = "11111111-2222-4333-8444-555555555555"

var allowClock = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

// stubAllow installs the seams: the caller's principal maps to sessionKey (or
// nothing when ""), the host has `live` unclosed workers spawned by allowPrincipal
// and one closed one, and the clock is allowClock.
func stubAllow(t *testing.T, sessionKey string, live int) {
	t.Helper()
	oldOf, oldList, oldNow := allowSessionOf, allowListSessions, allowNow
	t.Cleanup(func() { allowSessionOf, allowListSessions, allowNow = oldOf, oldList, oldNow })
	allowSessionOf = func(p string) (string, error) {
		if p == allowPrincipal && sessionKey != "" {
			return sessionKey, nil
		}
		return "", errors.New("no such principal")
	}
	// A session counts as live only while its worktree directory exists
	// (State.ResolveState), so the live ones get one and the closed one does not.
	worktree := t.TempDir()
	allowListSessions = func() ([]session.State, error) {
		var out []session.State
		for i := 0; i < live; i++ {
			out = append(out, session.State{SpawnedByPrincipal: allowPrincipal, SessionState: session.StateInactive, WorktreePath: worktree})
		}
		out = append(out,
			session.State{SpawnedByPrincipal: allowPrincipal, SessionState: session.StateInactive, WorktreePath: filepath.Join(worktree, "gone")},
			session.State{SpawnedByPrincipal: "someone-else", SessionState: session.StateInactive, WorktreePath: worktree},
			session.State{SpawnedByPrincipal: allowPrincipal, Kind: session.KindImplicit, WorktreePath: worktree},
		)
		return out, nil
	}
	allowNow = func() time.Time { return allowClock }
}

func allowDriver(t *testing.T, global, repoLayer string) (home, driver string) {
	t.Helper()
	home = t.TempDir()
	driver = makeMainCheckout(t, filepath.Join(home, "work", "driver"))
	if global != "" {
		writeSweatfileAt(t, filepath.Join(home, ".config", "spinclass", "sweatfile"), global)
	}
	if repoLayer != "" {
		writeSweatfileAt(t, filepath.Join(driver, "sweatfile"), repoLayer)
	}
	return home, driver
}

func listed(extra string) string {
	return "[permissions]\n\n[[permissions.spawn-allow]]\nprincipal = \"" + allowPrincipal + "\"\n" + extra
}

func TestDriverMaySpawnListedPrincipal(t *testing.T) {
	stubAllow(t, "circus/coordinator", 3)
	home, driver := allowDriver(t, listed("note = \"coordinator\"\n"), "")
	use, err := DriverMaySpawn(home, driver, allowPrincipal)
	if err != nil {
		t.Fatalf("a listed principal under its cap: %v", err)
	}
	if use.Live != 3 || use.Entry.MaxWorkers != 12 || use.Entry.Note != "coordinator" || use.SessionKey != "circus/coordinator" {
		t.Errorf("use = %+v, want 3 live of the default cap 12, the note and the session", use)
	}
}

func TestDriverMaySpawnRefusesWhatIsNotListed(t *testing.T) {
	other := "99999999-2222-4333-8444-555555555555"
	cases := map[string]struct {
		global, repoLayer, principal string
		sessionKey                   string
	}{
		"unlisted principal":     {global: listed(""), principal: other, sessionKey: "circus/coordinator"},
		"nothing configured":     {principal: allowPrincipal, sessionKey: "circus/coordinator"},
		"repo layer entry":       {repoLayer: listed(""), principal: allowPrincipal, sessionKey: "circus/coordinator"},
		"no live session":        {global: listed(""), principal: allowPrincipal, sessionKey: ""},
		"empty principal":        {global: listed(""), principal: "", sessionKey: "circus/coordinator"},
		"expired entry":          {global: listed("expires = \"2026-11-12T11:59:59Z\"\n"), principal: allowPrincipal, sessionKey: "x/y"},
		"unparseable expiry":     {global: listed("expires = \"tomorrow\"\n"), principal: allowPrincipal, sessionKey: "x/y"},
		"zero cap":               {global: listed("max-workers = 0\n"), principal: allowPrincipal, sessionKey: "x/y"},
		"both principal+session": {global: listed("session = \"x/y\"\n"), principal: allowPrincipal, sessionKey: "x/y"},
		"entry for another host": {global: strings.Replace(listed(""), allowPrincipal, other+"@other.example", 1), principal: allowPrincipal, sessionKey: "x/y"},
		"session entry mismatch": {global: "[permissions]\n\n[[permissions.spawn-allow]]\nsession = \"other/branch\"\n", principal: allowPrincipal, sessionKey: "x/y"},
		"the not-in-a-repo cwd":  {global: listed(""), principal: allowPrincipal, sessionKey: "x/y"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stubAllow(t, tc.sessionKey, 0)
			home, driver := allowDriver(t, tc.global, tc.repoLayer)
			cwd := driver
			if name == "the not-in-a-repo cwd" {
				cwd = t.TempDir()
			}
			if _, err := DriverMaySpawn(home, cwd, tc.principal); err == nil {
				t.Error("want a refusal that keeps the prompt")
			}
		})
	}
}

// The cap counts the principal's live workers only (not closed ones, not
// another driver's, not implicit sessions); reaching it keeps the prompt.
func TestDriverMaySpawnCap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra string
		live  int
		want  bool
	}{
		{"default cap, one under", "", 11, true},
		{"default cap reached", "", 12, false},
		{"entry cap, one under", "max-workers = 3\n", 2, true},
		{"entry cap reached", "max-workers = 3\n", 3, false},
		{"entry cap over", "max-workers = 3\n", 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubAllow(t, "x/y", tc.live)
			home, driver := allowDriver(t, listed(tc.extra), "")
			_, err := DriverMaySpawn(home, driver, allowPrincipal)
			if (err == nil) != tc.want {
				t.Errorf("err = %v, want allowed=%v", err, tc.want)
			}
		})
	}
}

// An unexpired entry passes and the same entry past its instant does not; the
// instant itself is already expired.
func TestDriverMaySpawnExpiry(t *testing.T) {
	stubAllow(t, "x/y", 0)
	home, driver := allowDriver(t, listed("expires = \"2026-11-12T12:00:01Z\"\n"), "")
	if _, err := DriverMaySpawn(home, driver, allowPrincipal); err != nil {
		t.Fatalf("one second before the expiry: %v", err)
	}
	allowNow = func() time.Time { return allowClock.Add(time.Second) }
	if _, err := DriverMaySpawn(home, driver, allowPrincipal); err == nil {
		t.Error("at the expiry instant the entry must be ignored")
	}
}

// An entry may name the session instead of the principal: it follows the
// coordinator's worktree through the frontend mapping; a qualified principal
// is reduced to its key.
func TestDriverMaySpawnSessionAndQualifiedEntries(t *testing.T) {
	stubAllow(t, "circus/coordinator", 0)
	home, driver := allowDriver(t, "[permissions]\n\n[[permissions.spawn-allow]]\nsession = \"circus/coordinator\"\n", "")
	if _, err := DriverMaySpawn(home, driver, allowPrincipal); err != nil {
		t.Errorf("a session entry: %v", err)
	}

	home, driver = allowDriver(t, strings.Replace(listed(""), allowPrincipal, allowPrincipal+"@flic.example", 1), "")
	if _, err := DriverMaySpawn(home, driver, allowPrincipal); err != nil {
		t.Errorf("a qualified principal whose key is the caller's: %v", err)
	}
}

// Two entries for the caller: the first valid one under its cap wins.
func TestDriverMaySpawnFallsThroughToALaterEntry(t *testing.T) {
	stubAllow(t, "x/y", 2)
	body := listed("max-workers = 2\n") + "\n[[permissions.spawn-allow]]\nprincipal = \"" + allowPrincipal + "\"\nmax-workers = 5\n"
	home, driver := allowDriver(t, body, "")
	use, err := DriverMaySpawn(home, driver, allowPrincipal)
	if err != nil || use.Entry.MaxWorkers != 5 {
		t.Errorf("use = %+v, err = %v; want the second entry (cap 5)", use, err)
	}
}
