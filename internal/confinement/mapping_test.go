//go:build linux

package confinement

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestDriverWithoutPrincipalFailsF1AndF15(t *testing.T) {
	sys, in := confinedHost()
	in.Driver.Principal = ""
	r := Run(sys, in)
	for _, id := range []string{"F1", "F15"} {
		if f := factByID(t, r, id); f.OK || !strings.Contains(f.Detail, "no principal") {
			t.Errorf("%s = %+v, want a no-principal failure", id, f)
		}
	}
}

// The frontend writes the mapping from the unit's own environment, and the
// grant walk's join reads only that, never the worktree state.
func TestRecordPrincipalFromUnitThenLookUp(t *testing.T) {
	dir := t.TempDir()
	sys, _ := confinedHost()
	sys.files[UnitRecordPath(dir, "/wt/a")] = sys.files[UnitRecordPath(stateDir, "/wt/a")]

	principal, err := RecordPrincipalFromUnit(sys, dir, "r/a", "/wt/a")
	if err != nil {
		t.Fatal(err)
	}
	if principal != "principal-a" {
		t.Fatalf("recorded %q, want the unit's CLOWN_SESSION_ID", principal)
	}
	fi, err := os.Stat(PrincipalMappingPath(dir, principal))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("mapping mode %v, want owner-only", fi.Mode().Perm())
	}

	join := SessionOfPrincipal(dir, func(string) bool { return true })
	if key, ok := join("principal-a"); !ok || key != "r/a" {
		t.Errorf("join = %q, %v", key, ok)
	}
	if _, ok := SessionOfPrincipal(dir, func(string) bool { return false })("principal-a"); ok {
		t.Error("a closed session's mapping still joined")
	}
	if _, ok := join("principal-unknown"); ok {
		t.Error("an unmapped principal joined")
	}
	if _, ok := join("../escape"); ok {
		t.Error("a path-shaped principal joined")
	}

	if err := RemovePrincipalMapping(dir, principal); err != nil {
		t.Fatal(err)
	}
	if _, ok := join("principal-a"); ok {
		t.Error("a removed mapping still joined")
	}
}

func TestRecordPrincipalFromUnitRefusesWithoutAPrincipal(t *testing.T) {
	dir := t.TempDir()
	sys, _ := confinedHost()
	sys.files[UnitRecordPath(dir, "/wt/a")] = sys.files[UnitRecordPath(stateDir, "/wt/a")]
	sys.files["/proc/100/environ"] = "PATH=/bin\x00"
	if _, err := RecordPrincipalFromUnit(sys, dir, "r/a", "/wt/a"); err == nil {
		t.Error("recorded a mapping with no principal in the unit")
	}
}

func TestMissingCapabilities(t *testing.T) {
	if got := MissingCapabilities([]string{"agent-unit"}); !slices.Equal(got, []string{"private-network", "frontend-mcp-sockets", "in-unit-moxy"}) {
		t.Errorf("got %v", got)
	}
	if got := MissingCapabilities(RequiredCapabilities); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}

func TestInsideAgentUnit(t *testing.T) {
	sys := &fakeSys{files: map[string]string{"/proc/self/cgroup": "0::" + unitCG + "\n"}}
	if in, err := InsideAgentUnit(sys); err != nil || !in {
		t.Errorf("unit cgroup: %v, %v", in, err)
	}
	sys.files["/proc/self/cgroup"] = "0::/user.slice/user-1000.slice/session-3.scope\n"
	if in, err := InsideAgentUnit(sys); err != nil || in {
		t.Errorf("login scope: %v, %v", in, err)
	}
	delete(sys.files, "/proc/self/cgroup")
	if _, err := InsideAgentUnit(sys); err == nil {
		t.Error("an unreadable cgroup was not an error")
	}
}

func TestUnitRecordPtyMustBeADevPtsPath(t *testing.T) {
	for _, pty := range []string{"/etc/passwd", "/dev/pts/../tty", "pts/1"} {
		body := `{"version":1,"unit":"u","cgroup":"` + unitCG + `","pty":"` + pty + `"}`
		if _, err := ParseUnitRecord([]byte(body)); err == nil {
			t.Errorf("accepted pty %q", pty)
		}
	}
}

func TestUnitSpecPathIsBesideTheRecord(t *testing.T) {
	spec, rec := UnitSpecPath("/s", "/wt/a"), UnitRecordPath("/s", "/wt/a")
	if !strings.HasSuffix(spec, ".spec.json") || strings.TrimSuffix(spec, ".spec.json") != strings.TrimSuffix(rec, ".json") {
		t.Errorf("spec %s, record %s", spec, rec)
	}
}
