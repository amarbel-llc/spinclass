package spawn

import (
	"errors"
	"os"
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

// allowWorker describes one session record the caller (or someone else)
// spawned. Its zero value is a worker of the caller that is running: the
// recorded state running-detached with a worktree on disk.
type allowWorker struct {
	state    string // recorded SessionState; "" is running-detached
	pid      int    // recorded PID (ResolveState reads it for an active session)
	clowns   int    // live clown harnesses in the presence index
	spawner  string // SpawnedByPrincipal; "" is the caller
	implicit bool
	gone     bool // the worktree directory no longer exists (a closed session)
}

// stubAllow installs the seams: the caller's principal maps to sessionKey (or
// nothing when ""), the host holds the given workers, and the clock is allowClock.
func stubAllow(t *testing.T, sessionKey string, workers ...allowWorker) {
	t.Helper()
	oldOf, oldList, oldNow, oldCounts := allowSessionOf, allowListSessions, allowNow, allowClownCounts
	t.Cleanup(func() {
		allowSessionOf, allowListSessions, allowNow, allowClownCounts = oldOf, oldList, oldNow, oldCounts
	})
	allowSessionOf = func(p string) (string, error) {
		if p == allowPrincipal && sessionKey != "" {
			return sessionKey, nil
		}
		return "", errors.New("no such principal")
	}
	counts := map[string]int{}
	var states []session.State
	for i, w := range workers {
		key := "w/" + string(rune('a'+i))
		dir := filepath.Join(t.TempDir(), "wt")
		if !w.gone {
			// A live worktree has a directory and a .git entry.
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		st := session.State{
			SessionKey:         key,
			SpawnedByPrincipal: w.spawner,
			SessionState:       w.state,
			PID:                w.pid,
			WorktreePath:       dir,
		}
		if st.SpawnedByPrincipal == "" {
			st.SpawnedByPrincipal = allowPrincipal
		}
		if st.SessionState == "" {
			st.SessionState = session.StateRunningDetached
		}
		if w.implicit {
			st.Kind = session.KindImplicit
		}
		counts[key] = w.clowns
		states = append(states, st)
	}
	allowListSessions = func() ([]session.State, error) { return states, nil }
	allowClownCounts = func(time.Time) map[string]int { return counts }
	allowNow = func() time.Time { return allowClock }
}

func running(n int) []allowWorker { return make([]allowWorker, n) }

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
	stubAllow(t, "circus/coordinator", running(3)...)
	home, driver := allowDriver(t, listed("note = \"coordinator\"\n"), "")
	use, err := DriverMaySpawn(home, driver, allowPrincipal)
	if err != nil {
		t.Fatalf("a listed principal under its cap: %v", err)
	}
	if use.Live != 3 || use.Entry.MaxWorkers != 12 || use.Entry.Note != "coordinator" || use.SessionKey != "circus/coordinator" {
		t.Errorf("use = %+v, want 3 running of the default cap 12, the note and the session", use)
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
			stubAllow(t, tc.sessionKey)
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

// The cap counts the principal's RUNNING workers; reaching it keeps the prompt
// (spinclass operator ruling 2026-11-12: max-workers is concurrency).
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
			stubAllow(t, "x/y", running(tc.live)...)
			home, driver := allowDriver(t, listed(tc.extra), "")
			_, err := DriverMaySpawn(home, driver, allowPrincipal)
			if (err == nil) != tc.want {
				t.Errorf("err = %v, want allowed=%v", err, tc.want)
			}
			var r *AllowRefusal
			if !tc.want && (!errors.As(err, &r) || r.Kind != RefusalCap) {
				t.Errorf("err = %v, want a cap refusal", err)
			}
		})
	}
}

// Only a worker that is running counts: idle (inactive with no harness),
// closed, another driver's and implicit sessions do not; a recorded-active
// session counts only while its PID is alive, and an inactive record with a
// live clown harness reads running-detached, as `sc list` shows it.
func TestDriverMaySpawnCountsRunningWorkersOnly(t *testing.T) {
	alive := os.Getpid()
	cases := []struct {
		name    string
		workers []allowWorker
		want    int
	}{
		{"running-detached", []allowWorker{{}}, 1},
		{"active with a live PID", []allowWorker{{state: session.StateActive, pid: alive}}, 1},
		{"active with a dead PID is idle", []allowWorker{{state: session.StateActive, pid: 0}}, 0},
		{"inactive with no harness is idle", []allowWorker{{state: session.StateInactive}}, 0},
		{"inactive with a live clown reads running", []allowWorker{{state: session.StateInactive, clowns: 1}}, 1},
		{"a closed session", []allowWorker{{gone: true}}, 0},
		{"another driver's worker", []allowWorker{{spawner: "someone-else"}}, 0},
		{"an implicit session", []allowWorker{{implicit: true}}, 0},
		{"mixed", []allowWorker{
			{},
			{state: session.StateActive, pid: alive},
			{state: session.StateInactive, clowns: 2},
			{state: session.StateInactive},
			{state: session.StateActive},
			{gone: true},
			{spawner: "x"},
			{implicit: true},
		}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubAllow(t, "x/y", tc.workers...)
			home, driver := allowDriver(t, listed(""), "")
			use, err := DriverMaySpawn(home, driver, allowPrincipal)
			if err != nil {
				t.Fatalf("under the default cap: %v", err)
			}
			if use.Live != tc.want {
				t.Errorf("running = %d, want %d", use.Live, tc.want)
			}
		})
	}

	// Many idle sessions never reach the cap: 20 inactive and closed ones under
	// a cap of 2 still pass, while 2 running ones reach it.
	idle := make([]allowWorker, 0, 21)
	for i := 0; i < 15; i++ {
		idle = append(idle, allowWorker{state: session.StateInactive})
	}
	for i := 0; i < 6; i++ {
		idle = append(idle, allowWorker{gone: true})
	}
	stubAllow(t, "x/y", idle...)
	home, driver := allowDriver(t, listed("max-workers = 2\n"), "")
	if _, err := DriverMaySpawn(home, driver, allowPrincipal); err != nil {
		t.Errorf("21 idle or closed sessions must not count against a cap of 2: %v", err)
	}
	stubAllow(t, "x/y", append(idle, running(2)...)...)
	if _, err := DriverMaySpawn(home, driver, allowPrincipal); err == nil {
		t.Error("2 running workers reach a cap of 2")
	}
}

// An unexpired entry passes and the same entry past its instant does not; the
// instant itself is already expired.
func TestDriverMaySpawnExpiry(t *testing.T) {
	stubAllow(t, "x/y")
	home, driver := allowDriver(t, listed("expires = \"2026-11-12T12:00:01Z\"\n"), "")
	if _, err := DriverMaySpawn(home, driver, allowPrincipal); err != nil {
		t.Fatalf("one second before the expiry: %v", err)
	}
	allowNow = func() time.Time { return allowClock.Add(time.Second) }
	_, err := DriverMaySpawn(home, driver, allowPrincipal)
	var r *AllowRefusal
	if !errors.As(err, &r) || r.Kind != RefusalExpired {
		t.Errorf("at the expiry instant: err = %v, want an expired refusal", err)
	}
}

// An entry may name the session instead of the principal: it follows the
// coordinator's worktree through the frontend mapping; a qualified principal
// is reduced to its key.
func TestDriverMaySpawnSessionAndQualifiedEntries(t *testing.T) {
	stubAllow(t, "circus/coordinator")
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
	stubAllow(t, "x/y", running(2)...)
	body := listed("max-workers = 2\n") + "\n[[permissions.spawn-allow]]\nprincipal = \"" + allowPrincipal + "\"\nmax-workers = 5\n"
	home, driver := allowDriver(t, body, "")
	use, err := DriverMaySpawn(home, driver, allowPrincipal)
	if err != nil || use.Entry.MaxWorkers != 5 {
		t.Errorf("use = %+v, err = %v; want the second entry (cap 5)", use, err)
	}
}

// Every reason a LISTED (or unidentifiable) caller is refused is named for the
// ask text and the journal (operator ruling 2026-11-12); a caller the list does
// not name, and a call the list would pass, get nothing added.
func TestDiagnoseSpawnAllow(t *testing.T) {
	const key = "[session-entry]\nunattended-permission-mode = \"dontAsk\"\n"
	cases := []struct {
		name       string
		global     string
		sessionKey string
		workers    []allowWorker
		unattended bool
		wantKind   string   // "" means no diagnosis
		wantIn     []string // substrings of the reason
		cap        int
		running    int
	}{
		{
			name: "cap reached", global: key + listed(""), sessionKey: "x/y", workers: running(13), unattended: true,
			wantKind: RefusalCap, wantIn: []string{"cap is reached", "13 workers running", "of 12 allowed"}, cap: 12, running: 13,
		},
		{
			name: "cap reached at an entry cap", global: key + listed("max-workers = 2\n"), sessionKey: "x/y", workers: running(2), unattended: true,
			wantKind: RefusalCap, wantIn: []string{"2 workers running, of 2 allowed"}, cap: 2, running: 2,
		},
		{
			name: "idle sessions are not a cap refusal", global: key + listed(""), sessionKey: "x/y",
			workers: append(running(1), allowWorker{state: session.StateInactive}, allowWorker{state: session.StateInactive}), unattended: true,
		},
		{
			name: "expired", global: key + listed("expires = \"2026-11-12T11:00:00Z\"\n"), sessionKey: "x/y", unattended: true,
			wantKind: RefusalExpired, wantIn: []string{"expired at 2026-11-12T11:00:00Z"},
		},
		{
			name: "no live mapping", global: key + listed(""), sessionKey: "", unattended: true,
			wantKind: RefusalNoMapping, wantIn: []string{"no live frontend mapping"},
		},
		{
			name: "worker not dontAsk", global: listed(""), sessionKey: "x/y", unattended: true,
			wantKind: RefusalWorkerNotDontAsk, wantIn: []string{"would not start in dontAsk", "unattended-permission-mode"},
		},
		{
			name: "driver attended and worker not dontAsk", global: listed(""), sessionKey: "x/y", unattended: false,
			wantKind: RefusalDriverAttended, wantIn: []string{"is attended", "not consulted", "would not start in dontAsk"},
		},
		{name: "attended driver ignores the cap", global: key + listed(""), sessionKey: "x/y", workers: running(40), unattended: false},
		{name: "listed, under cap, worker dontAsk: nothing refused", global: key + listed(""), sessionKey: "x/y", workers: running(1), unattended: true},
		{name: "unlisted caller", global: key + "[permissions]\n\n[[permissions.spawn-allow]]\nprincipal = \"someone-else\"\n", sessionKey: "x/y", unattended: true},
		{name: "no allow-list at all", global: key, sessionKey: "x/y", unattended: true},
		{name: "only invalid entries", global: key + "[permissions]\n\n[[permissions.spawn-allow]]\nprincipal = \"a\"\nsession = \"b/c\"\n", sessionKey: "x/y", unattended: true},
		{name: "no mapping but no allow-list either", global: key, sessionKey: "", unattended: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubAllow(t, tc.sessionKey, tc.workers...)
			home, driver := allowDriver(t, tc.global, "")
			r, ok := DiagnoseSpawnAllow(home, driver, allowPrincipal, "", tc.unattended)
			if tc.wantKind == "" {
				if ok {
					t.Fatalf("want nothing to say, got %+v", r)
				}
				return
			}
			if !ok || r.Kind != tc.wantKind {
				t.Fatalf("kind = %q (ok=%v), want %q: %+v", r.Kind, ok, tc.wantKind, r)
			}
			for _, w := range tc.wantIn {
				if !strings.Contains(r.Reason, w) {
					t.Errorf("reason %q lacks %q", r.Reason, w)
				}
			}
			if r.Kind == RefusalCap && (r.Cap != tc.cap || r.Running != tc.running) {
				t.Errorf("cap %d running %d, want %d and %d", r.Cap, r.Running, tc.cap, tc.running)
			}
		})
	}
}

// A caller outside any repo cannot have its trusted layers read: an unattended
// one is told so, an attended one is not bothered.
func TestDiagnoseSpawnAllowOutsideARepo(t *testing.T) {
	stubAllow(t, "x/y")
	home, _ := allowDriver(t, listed(""), "")
	r, ok := DiagnoseSpawnAllow(home, t.TempDir(), allowPrincipal, "", true)
	if !ok || r.Kind != RefusalNoRepo {
		t.Errorf("unattended outside a repo: %+v ok=%v, want a no-repo refusal", r, ok)
	}
	if _, ok := DiagnoseSpawnAllow(home, t.TempDir(), allowPrincipal, "", false); ok {
		t.Error("an attended caller outside a repo has nothing to be told")
	}
}
