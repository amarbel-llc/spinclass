package confinement

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func serveRecordBody(principal string, pid int) string {
	return `{"principal":"` + principal + `","pid":` + itoa(pid) + `,"ppid":1,"started":"2026-10-20T00:00:00Z"}`
}

func itoa(n int) string { return strconv.Itoa(n) }

type serveFixture struct {
	principal string
	records   map[int]bool // pid -> alive
}

func (sf serveFixture) sys() *fakeSys {
	sys := callerHost(nil)
	dir := callerState + "/spinclass/serves"
	for pid, alive := range sf.records {
		name := serveRecordName(sf.principal, pid)
		sys.files[dir+"/"+name] = serveRecordBody(sf.principal, pid)
		sys.dirs[dir] = append(sys.dirs[dir], name)
		if alive {
			sys.files["/proc/"+itoa(pid)+"/stat"] = "x"
		}
	}
	return sys
}

func TestClassifyServeCaller(t *testing.T) {
	const me = "principal-a"
	for _, tc := range []struct {
		name         string
		fixture      serveFixture
		pid          int
		wantConfined bool
		wantWhy      string
	}{
		{"single live serve", serveFixture{me, map[int]bool{100: true}}, 100, false, "the only live serve"},
		{"duplicate live serve", serveFixture{me, map[int]bool{100: true, 200: true}}, 100, true, "2 live serves"},
		{"stale dead-pid duplicate is ignored", serveFixture{me, map[int]bool{100: true, 200: false}}, 100, false, "the only live serve"},
		{"own record missing", serveFixture{me, map[int]bool{200: true}}, 100, true, "own record is missing"},
		{"own record dead", serveFixture{me, map[int]bool{100: false}}, 100, true, "own record is missing"},
		{"no records at all", serveFixture{me, nil}, 100, true, "no record"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			confined, why := ClassifyServeCaller(tc.fixture.sys(), callerState, me, tc.pid)
			if confined != tc.wantConfined || !strings.Contains(why, tc.wantWhy) {
				t.Fatalf("got (%v, %q), want (%v, ...%q...)", confined, why, tc.wantConfined, tc.wantWhy)
			}
		})
	}
}

func TestClassifyServeCallerKeepsEveryConfinedVerdict(t *testing.T) {
	sys := serveFixture{"principal-a", map[int]bool{100: true}}.sys()
	sys.files[callerState+"/spinclass/units/1.json"] = callerUnitRecord("principal-a")
	sys.dirs[callerState+"/spinclass/units"] = []string{"1.json"}
	if confined, _ := ClassifyServeCaller(sys, callerState, "principal-a", 100); !confined {
		t.Error("a unit record must still confine")
	}
	if confined, _ := ClassifyServeCaller(callerHost(nil), callerState, "", 100); !confined {
		t.Error("an empty principal must stay confined")
	}
}

func TestClassifyServeCallerOtherPrincipalsRecordsDoNotCount(t *testing.T) {
	sys := serveFixture{"principal-a", map[int]bool{100: true}}.sys()
	other := serveFixture{"principal-b", map[int]bool{300: true}}.sys()
	for k, v := range other.files {
		sys.files[k] = v
	}
	sys.dirs[callerState+"/spinclass/serves"] = append(sys.dirs[callerState+"/spinclass/serves"], other.dirs[callerState+"/spinclass/serves"]...)
	if confined, why := ClassifyServeCaller(sys, callerState, "principal-a", 100); confined {
		t.Fatalf("got confined: %s", why)
	}
}

func TestRegisterServeWritesAndRemovesTheRecord(t *testing.T) {
	state := t.TempDir()
	dir := servesDir(state)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, serveRecordName("principal-a", 999999999))
	if err := os.WriteFile(stale, []byte(serveRecordBody("principal-a", 999999999)), 0o600); err != nil {
		t.Fatal(err)
	}

	remove, err := RegisterServe(state, "principal-a", os.Getpid(), os.Getppid(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, serveRecordName("principal-a", os.Getpid()))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("record mode = %v", info.Mode().Perm())
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a dead serve's record must be pruned, stat err = %v", err)
	}
	if confined, why := ClassifyServeCaller(Host{}, state, "principal-a", os.Getpid()); confined {
		t.Errorf("a single live serve: confined (%s)", why)
	}
	remove()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("record not removed, stat err = %v", err)
	}
}

func TestRegisterServeDirectoryIsPrivate(t *testing.T) {
	state := t.TempDir()
	if _, err := RegisterServe(state, "principal-a", os.Getpid(), 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(servesDir(state))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("directory mode = %v", info.Mode().Perm())
	}
}

func TestRegisterServeEmptyPrincipalWritesNothing(t *testing.T) {
	state := t.TempDir()
	remove, err := RegisterServe(state, "", os.Getpid(), 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	remove()
	if _, err := os.Stat(servesDir(state)); !os.IsNotExist(err) {
		t.Errorf("no records directory expected, stat err = %v", err)
	}
	if confined, _ := ClassifyServeCaller(Host{}, state, "", os.Getpid()); !confined {
		t.Error("an empty principal must stay confined")
	}
}
