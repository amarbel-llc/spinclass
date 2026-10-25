package confinement

import (
	"errors"
	"strings"
	"testing"
)

const callerState = "/state"

// unreadableDir is a System whose directory listings fail with a non-absence
// error.
type unreadableDir struct {
	*fakeSys
	err error
}

func (u unreadableDir) ReadDir(string) ([]string, error) { return nil, u.err }

func callerUnitRecord(principal string) string {
	return `{"version":1,"unit":"agent-a.service","cgroup":"` + unitCG + `","principal":"` + principal + `"}`
}

func callerMapping(principal string) string {
	return `{"version":1,"principal":"` + principal + `","session_key":"r/b","worktree":"/r/.worktrees/b","unit":"agent-a.service"}`
}

func callerUnconfinedMapping(principal string) string {
	return `{"version":1,"principal":"` + principal + `","session_key":"r/b","worktree":"/r/.worktrees/b","unit":"` + UnconfinedUnit + `"}`
}

func callerHost(units map[string]string) *fakeSys {
	sys := &fakeSys{files: map[string]string{}, dirs: map[string][]string{}}
	if units == nil {
		return sys
	}
	dir := callerState + "/spinclass/units"
	for name, body := range units {
		sys.files[dir+"/"+name] = body
		sys.dirs[dir] = append(sys.dirs[dir], name)
	}
	return sys
}

func TestClassifyCaller(t *testing.T) {
	const me = "principal-a"
	for _, tc := range []struct {
		name         string
		principal    string
		build        func() *fakeSys
		wrap         func(*fakeSys) System
		wantConfined bool
		wantWhy      string
	}{
		{
			name:      "no state at all: never launched confined",
			principal: me,
			build:     func() *fakeSys { return callerHost(nil) },
			wantWhy:   "no unit records exist",
		},
		{
			name:      "records exist for other principals only",
			principal: me,
			build: func() *fakeSys {
				return callerHost(map[string]string{"1.json": callerUnitRecord("principal-b"), "1.spec.json": "{not json"})
			},
			wantWhy: "no unit record names this principal",
		},
		{
			name:      "launch-failed record of another session is not mine",
			principal: me,
			build: func() *fakeSys {
				return callerHost(map[string]string{"1.json": `{"error":"clown could not start the unit"}`})
			},
			wantWhy: "no unit record names this principal",
		},
		{
			name:         "fixture record names the principal",
			principal:    me,
			build:        func() *fakeSys { return callerHost(map[string]string{"1.json": callerUnitRecord(me)}) },
			wantConfined: true,
			wantWhy:      "names principal principal-a",
		},
		{
			name:      "ambiguous: several records name the principal",
			principal: me,
			build: func() *fakeSys {
				return callerHost(map[string]string{"1.json": callerUnitRecord(me), "2.json": callerUnitRecord(me)})
			},
			wantConfined: true,
			wantWhy:      "ambiguous: 2 unit records",
		},
		{
			name:      "unreadable record",
			principal: me,
			build: func() *fakeSys {
				sys := callerHost(map[string]string{"1.json": callerUnitRecord("principal-b")})
				sys.readErrOn = map[string]error{callerState + "/spinclass/units/1.json": errors.New("permission denied")}
				return sys
			},
			wantConfined: true,
			wantWhy:      "unreadable",
		},
		{
			name:      "unparsable record",
			principal: me,
			build:     func() *fakeSys { return callerHost(map[string]string{"1.json": "{truncated"}) },

			wantConfined: true,
			wantWhy:      "does not parse",
		},
		{
			name:      "unit records directory unreadable",
			principal: me,
			wrap: func(sys *fakeSys) System {
				return unreadableDir{fakeSys: sys, err: errors.New("permission denied")}
			},
			build:        func() *fakeSys { return callerHost(nil) },
			wantConfined: true,
			wantWhy:      "unit records unreadable",
		},
		{
			name:      "mapping present although the record is missing",
			principal: me,
			build: func() *fakeSys {
				sys := callerHost(nil)
				sys.files[callerState+"/spinclass/principals/"+me+".json"] = callerMapping(me)
				return sys
			},
			wantConfined: true,
			wantWhy:      "maps principal principal-a",
		},
		{
			// The SessionStart hook writes this for every unconfined worktree
			// session (hooks.bindUnconfinedPrincipal); it must not confine.
			name:      "unconfined mapping and no unit record",
			principal: me,
			build: func() *fakeSys {
				sys := callerHost(nil)
				sys.files[callerState+"/spinclass/principals/"+me+".json"] = callerUnconfinedMapping(me)
				return sys
			},
			wantWhy: "no unit records exist",
		},
		{
			name:      "unconfined mapping but a unit record names the principal",
			principal: me,
			build: func() *fakeSys {
				sys := callerHost(map[string]string{"1.json": callerUnitRecord(me)})
				sys.files[callerState+"/spinclass/principals/"+me+".json"] = callerUnconfinedMapping(me)
				return sys
			},
			wantConfined: true,
			wantWhy:      "names principal principal-a",
		},
		{
			name:      "mapping unparsable",
			principal: me,
			build: func() *fakeSys {
				sys := callerHost(nil)
				sys.files[callerState+"/spinclass/principals/"+me+".json"] = "{"
				return sys
			},
			wantConfined: true,
			wantWhy:      "principal mapping unreadable",
		},
		{
			name:         "unresolved caller: empty principal",
			principal:    "",
			build:        func() *fakeSys { return callerHost(nil) },
			wantConfined: true,
			wantWhy:      "unresolved",
		},
		{
			name:         "unresolved caller: principal cannot name a file",
			principal:    "../escape",
			build:        func() *fakeSys { return callerHost(nil) },
			wantConfined: true,
			wantWhy:      "unresolved",
		},
		{
			name:      "inside an agent unit the missing records prove nothing",
			principal: me,
			build: func() *fakeSys {
				sys := callerHost(nil)
				sys.files["/proc/self/cgroup"] = "0::" + unitCG + "\n"
				return sys
			},
			wantConfined: true,
			wantWhy:      "inside a clown agent unit",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sys System = tc.build()
			if tc.wrap != nil {
				sys = tc.wrap(tc.build())
			}
			confined, why := ClassifyCaller(sys, callerState, tc.principal)
			if confined != tc.wantConfined {
				t.Fatalf("confined = %v (%s), want %v", confined, why, tc.wantConfined)
			}
			if !strings.Contains(why, tc.wantWhy) {
				t.Errorf("why = %q, want it to contain %q", why, tc.wantWhy)
			}
		})
	}
}

func TestWorktreeOfPrincipalReadsOnlyTheFrontendMapping(t *testing.T) {
	sys := callerHost(nil)
	if _, ok := WorktreeOfPrincipal(sys, callerState, "principal-a"); ok {
		t.Fatal("no mapping must not resolve a worktree")
	}
	sys.files[callerState+"/spinclass/principals/principal-a.json"] = callerMapping("principal-a")
	got, ok := WorktreeOfPrincipal(sys, callerState, "principal-a")
	if !ok || got != "/r/.worktrees/b" {
		t.Fatalf("got (%q, %v)", got, ok)
	}
}
