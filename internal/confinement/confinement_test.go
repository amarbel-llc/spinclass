//go:build linux

package confinement

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"
	"time"
)

type charDev struct{ rdev, fsdev uint64 }

// fakeSys is an in-memory System. Anything not described is absent: reads
// and stats fail with ErrNotExist, connections are refused as absent, writes
// are denied. So a fixture states exactly what the unit can see.
type fakeSys struct {
	files     map[string]string
	dirs      map[string][]string
	links     map[string]string // namespace links and filesystem symlinks
	symlinks  map[string]bool   // which links entries are filesystem symlinks (Lstat)
	devs      map[string]uint64 // st_dev for Stat
	chardevs  map[string]charDev
	connect   map[string]error // path → result; absent = ENOENT
	writable  map[string]bool
	writeErr  map[string]error
	peer      map[string]int
	env       map[string]string
	uid       int
	readErrOn map[string]error
}

type fakeInfo struct {
	name string
	dev  uint64
	mode fs.FileMode
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return &syscall.Stat_t{Dev: f.dev} }

func (f *fakeSys) ReadFile(path string) ([]byte, error) {
	if err, ok := f.readErrOn[path]; ok {
		return nil, err
	}
	if s, ok := f.files[path]; ok {
		return []byte(s), nil
	}
	return nil, fs.ErrNotExist
}

func (f *fakeSys) ReadDir(path string) ([]string, error) {
	if d, ok := f.dirs[path]; ok {
		return d, nil
	}
	return nil, fs.ErrNotExist
}

func (f *fakeSys) Readlink(path string) (string, error) {
	if l, ok := f.links[path]; ok {
		return l, nil
	}
	return "", fs.ErrNotExist
}

func (f *fakeSys) info(path string) (fs.FileInfo, error) {
	if _, ok := f.dirs[path]; ok {
		return fakeInfo{name: path, dev: f.devs[path], mode: fs.ModeDir}, nil
	}
	if d, ok := f.devs[path]; ok {
		return fakeInfo{name: path, dev: d, mode: fs.ModeDir}, nil
	}
	if _, ok := f.files[path]; ok {
		return fakeInfo{name: path}, nil
	}
	if _, ok := f.chardevs[path]; ok {
		return fakeInfo{name: path, mode: fs.ModeCharDevice}, nil
	}
	return nil, fs.ErrNotExist
}

func (f *fakeSys) Stat(path string) (fs.FileInfo, error) { return f.info(path) }

func (f *fakeSys) Lstat(path string) (fs.FileInfo, error) {
	if f.symlinks[path] {
		return fakeInfo{name: path, mode: fs.ModeSymlink}, nil
	}
	return f.info(path)
}

func (f *fakeSys) CharDevice(path string) (uint64, uint64, bool, error) {
	if d, ok := f.chardevs[path]; ok {
		return d.rdev, d.fsdev, true, nil
	}
	return 0, 0, false, nil
}

func (f *fakeSys) Connect(path string, abstract bool) error {
	key := path
	if abstract {
		key = "@" + path
	}
	if err, ok := f.connect[key]; ok {
		return err
	}
	return fs.ErrNotExist
}

func (f *fakeSys) Writable(path string) error {
	if err, ok := f.writeErr[path]; ok {
		return err
	}
	if f.writable[path] {
		return nil
	}
	return syscall.EACCES
}

func (f *fakeSys) PeerPID(socket string) (int, error) {
	if p, ok := f.peer[socket]; ok {
		return p, nil
	}
	return 0, syscall.ECONNREFUSED
}

func (f *fakeSys) Getuid() int              { return f.uid }
func (f *fakeSys) Getenv(key string) string { return f.env[key] }

const (
	slice     = "/user.slice/user-1000.slice/user@1000.service/clown.slice/clown-agent.slice"
	unitCG    = slice + "/agent-a.service"
	otherCG   = slice + "/agent-b.service"
	root      = "/proc/100/root"
	ptyDev    = uint64(136<<8 | 5) // pts/5
	hostPts   = uint64(33)         // st_dev of the host's devpts
	stateDir  = "/state"
	recordFor = `{"version":1,"unit":"%s","cgroup":"%s","invocation_id":"x","uid":1000,"pty":"%s","serve_socket":"%s","main_pid":%d,"principal":"%s"}`
	ptyPath   = "/dev/pts/5"
)

// record renders a unit record; a nonzero pty means the unit's own pty, which
// clown reports by path.
func record(unit, cg string, pty uint64, serve string, mainPID int) string {
	path := ""
	if pty != 0 {
		path = ptyPath
	}
	return fmt.Sprintf(recordFor, unit, cg, path, serve, mainPID, principalOfUnit(unit))
}

// principalOfUnit is the principal the launching clown records for a fixture
// unit: agent-a.service → principal-a.
func principalOfUnit(unit string) string {
	return "principal-" + strings.TrimSuffix(strings.TrimPrefix(unit, "agent-"), ".service")
}

func mapping(principal, key, worktree, unit string) string {
	return fmt.Sprintf(`{"version":1,"principal":"%s","session_key":"%s","worktree":"%s","unit":"%s"}`, principal, key, worktree, unit)
}

// confinedHost is a host where every fact holds for driver session r/a.
func confinedHost() (*fakeSys, Inputs) {
	sys := &fakeSys{
		uid: 1000,
		files: map[string]string{
			UnitRecordPath(stateDir, "/wt/a"):             record("agent-a.service", unitCG, ptyDev, "/run/user/1000/clown/agent/a/serve.sock", 100),
			UnitRecordPath(stateDir, "/wt/b"):             record("agent-b.service", otherCG, 0, "/run/user/1000/clown/agent/b/serve.sock", 0),
			PrincipalMappingPath(stateDir, "principal-a"): mapping("principal-a", "r/a", "/wt/a", "agent-a.service"),
			"/sys/fs/cgroup" + unitCG + "/cgroup.procs":   "100\n101\n",
			"/sys/fs/cgroup" + otherCG + "/cgroup.procs":  "200\n",
			"/proc/200/cgroup":                            "0::" + otherCG + "\n",
			"/proc/200/comm":                              "claude\n",
			"/proc/100/cgroup":                            "0::" + unitCG + "\n",
			"/proc/101/cgroup":                            "0::" + unitCG + "\n",
			"/proc/50/cgroup":                             "0::/user.slice/user-1000.slice/user@1000.service/app.slice/piggy-agent.service\n",
			"/proc/50/stat":                               "50 (piggy-agent) S 1 50 50 0 -1 0",
			"/proc/100/stat":                              "100 (claude) S 90 100 100 34821 -1 0",
			"/proc/101/stat":                              "101 (bash) S 100 100 100 34821 -1 0",
			"/proc/100/environ":                           "PATH=/bin\x00HOME=/tmp/home\x00",
			"/proc/101/environ":                           "PATH=/bin\x00",
			"/proc/100/comm":                              "claude\n",
			"/proc/101/comm":                              "bash\n",
			"/proc/50/comm":                               "piggy-agent\n",
			"/proc/1/comm":                                "systemd\n",
			"/proc/100/mountinfo": "1 0 8:1 / / rw - ext4 /dev/sda1 rw\n" +
				"2 1 0:40 / /home rw,nosuid - tmpfs tmpfs rw\n" +
				"3 2 8:1 /wt/a /wt/a rw - ext4 /dev/sda1 rw\n",
			"/proc/self/mountinfo":     "1 0 8:1 / / rw - ext4 /dev/sda1 rw\n",
			root + "/repo/.git/config": "",
		},
		dirs: map[string][]string{
			"/sys/fs/cgroup" + unitCG:  {"cgroup.procs", "memory.max"},
			"/sys/fs/cgroup" + otherCG: {"cgroup.procs"},
			root + "/":                 {"bin", "dev", "home", "proc", "repo"},
			"/proc/100/fd":             {"0", "1", "2"},
			"/proc/101/fd":             {"0"},
			"/run/user/1000":           {"bus", "wayland-1", "wayland-1.lock", "systemd", "clown"},
			root + "/dev":              {"pts"},
			root + "/dev/pts":          {"ptmx", "5"},
			"/proc":                    {"1", "50", "100", "101", "200", "self"},
			root + "/proc":             {"1", "2", "self"},
			root + "/repo":             {".git"},
			root + "/repo/.git":        {"hooks", "config"},
			root + "/repo/.git/hooks":  {},
		},
		links: map[string]string{
			"/proc/self/ns/pid": "pid:[4026531836]",
			"/proc/100/ns/pid":  "pid:[4026532999]",
			"/proc/101/ns/pid":  "pid:[4026532999]",
			"/proc/self/ns/net": "net:[4026531840]",
			"/proc/100/ns/net":  "net:[4026533111]",
			"/proc/101/ns/net":  "net:[4026533111]",
			"/proc/100/ns/mnt":  "mnt:[4026533222]",
			"/proc/101/ns/mnt":  "mnt:[4026533222]",
			"/proc/100/cwd":     "/wt/a",
		},
		symlinks: map[string]bool{},
		devs: map[string]uint64{
			"/proc":        22,
			root + "/proc": 77,
			"/dev/pts":     hostPts,
		},
		chardevs: map[string]charDev{
			ptyPath:             {ptyDev, hostPts},
			"/proc/100/fd/0":    {ptyDev, hostPts},
			"/proc/101/fd/0":    {ptyDev, hostPts},
			root + "/dev/pts/5": {ptyDev, hostPts},
		},
		peer: map[string]int{"/run/user/1000/agent.sock": 50},
		env:  map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus"},
	}
	sys.files[root+"/repo/.git/worktrees/a/config.worktree"] = ""
	sys.files[root+"/wt/a/.git"] = "gitdir: /repo/.git/worktrees/a\n"
	in := Inputs{
		Driver: Session{Key: "r/a", Worktree: "/wt/a", PID: 100, Principal: "principal-a", RecordedPrincipal: "principal-a"},
		Live: []Session{
			{Key: "r/a", Worktree: "/wt/a", PID: 100},
			{Key: "r/b", Worktree: "/wt/b", PID: 200},
		},
		OperatorHome: "/home/op",
		AgentSocket:  "/run/user/1000/agent.sock",
		GitCommonDir: "/repo/.git",
		StateDir:     stateDir,
		RuntimeAllow: RuntimeAllowFor("/run/user/1000", "/run/user/1000/agent.sock"),
	}
	return sys, in
}

func factByID(t *testing.T, r Report, id string) Fact {
	t.Helper()
	for _, f := range r.Facts {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("no fact %s in report", id)
	return Fact{}
}

func TestConfinedHostPasses(t *testing.T) {
	sys, in := confinedHost()
	r := Run(sys, in)
	for _, f := range r.Facts {
		if !f.OK {
			t.Errorf("%s %s failed on a confined host: %s", f.ID, f.Name, f.Detail)
		}
	}
	if err := r.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}

// Each mutation breaks exactly one fact of a confined host. Only that fact
// may fail, so every probe is shown both to catch its breach and not to
// misfire on the others.
func TestEachFactCatchesItsBreach(t *testing.T) {
	cases := []struct {
		id     string
		want   string
		mutate func(*fakeSys, *Inputs)
	}{
		{"F1", "not the driver's principal", func(s *fakeSys, in *Inputs) {
			// A consistent mapping, but the unit was launched for someone else.
			in.Driver.Principal, in.Driver.RecordedPrincipal = "principal-z", "principal-z"
			s.files[PrincipalMappingPath(stateDir, "principal-z")] = mapping("principal-z", "r/a", "/wt/a", "agent-a.service")
		}},
		{"F15", "no frontend principal mapping", func(s *fakeSys, _ *Inputs) {
			delete(s.files, PrincipalMappingPath(stateDir, "principal-a"))
		}},
		{"F15", "maps principal principal-a to session r/b", func(s *fakeSys, _ *Inputs) {
			s.files[PrincipalMappingPath(stateDir, "principal-a")] = mapping("principal-a", "r/b", "/wt/b", "agent-a.service")
		}},
		{"F15", "maps principal principal-a to unit agent-b.service", func(s *fakeSys, _ *Inputs) {
			s.files[PrincipalMappingPath(stateDir, "principal-a")] = mapping("principal-a", "r/a", "/wt/a", "agent-b.service")
		}},
		{"F15", "records principal principal-x, the frontend knows principal-a", func(_ *fakeSys, in *Inputs) {
			in.Driver.RecordedPrincipal = "principal-x"
		}},
		{"F2", "inside an agent unit", func(s *fakeSys, _ *Inputs) {
			s.files["/proc/50/cgroup"] = "0::" + unitCG + "/sub\n"
		}},
		{"F2", "descends from agent runtime", func(s *fakeSys, _ *Inputs) {
			s.files["/proc/50/stat"] = "50 (piggy-agent) S 40 50 50 0 -1 0"
			s.files["/proc/40/stat"] = "40 (claude) S 1 40 40 0 -1 0"
		}},
		{"F2", "descends from a process in agent unit", func(s *fakeSys, _ *Inputs) {
			s.files["/proc/50/stat"] = "50 (piggy-agent) S 40 50 50 0 -1 0"
			s.files["/proc/40/stat"] = "40 (sh) S 1 40 40 0 -1 0"
			s.files["/proc/40/cgroup"] = "0::" + otherCG + "\n"
		}},
		{"F3", "not the unit's own pty", func(s *fakeSys, _ *Inputs) {
			s.chardevs["/proc/100/fd/2"] = charDev{uint64(136<<8 | 1), hostPts}
		}},
		{"F3", "not the unit's own pty", func(s *fakeSys, _ *Inputs) {
			// Same number as the own pty, on a different devpts instance.
			s.chardevs["/proc/100/fd/2"] = charDev{ptyDev, 99}
		}},
		{"F3", "controlling tty", func(s *fakeSys, _ *Inputs) {
			s.files["/proc/101/stat"] = "101 (bash) S 100 100 100 34817 -1 0"
		}},
		{"F4", "DBUS_SESSION_BUS_ADDRESS", func(s *fakeSys, _ *Inputs) {
			s.files["/proc/101/environ"] = "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus\x00"
		}},
		{"F4", "DISPLAY", func(s *fakeSys, _ *Inputs) {
			// A process in a sub-cgroup of the unit is still the unit's.
			s.dirs["/sys/fs/cgroup"+unitCG] = []string{"cgroup.procs", "sub"}
			s.dirs["/sys/fs/cgroup"+unitCG+"/sub"] = []string{"cgroup.procs"}
			s.files["/sys/fs/cgroup"+unitCG+"/sub/cgroup.procs"] = "102\n"
			s.files["/proc/102/environ"] = "DISPLAY=:0\x00"
			s.files["/proc/102/stat"] = "102 (sh) S 100 100 100 0 -1 0"
			s.dirs["/proc/102/fd"] = []string{}
			s.links["/proc/102/ns/mnt"] = "mnt:[4026533222]"
			s.links["/proc/102/ns/pid"] = "pid:[4026532999]"
			s.links["/proc/102/ns/net"] = "net:[4026533111]"
		}},
		{"F5", "the session bus", func(s *fakeSys, _ *Inputs) {
			s.connect = map[string]error{root + "/run/user/1000/bus": nil}
		}},
		{"F5", "the user manager", func(s *fakeSys, _ *Inputs) {
			s.connect = map[string]error{root + "/run/user/1000/systemd/private": nil}
		}},
		{"F6", "wayland socket", func(s *fakeSys, _ *Inputs) {
			s.connect = map[string]error{root + "/run/user/1000/wayland-1": nil}
		}},
		{"F6", "X authority", func(s *fakeSys, _ *Inputs) {
			s.dirs[root+"/home"] = []string{"op"}
			s.dirs[root+"/home/op"] = []string{".Xauthority"}
			s.files[root+"/home/op/.Xauthority"] = "cookie"
		}},
		{"F7", "writable from the unit", func(s *fakeSys, _ *Inputs) {
			s.files["/proc/100/mountinfo"] = "1 0 8:1 / / rw - ext4 /dev/sda1 rw\n"
			s.dirs[root+"/home"] = []string{"op"}
			s.dirs[root+"/home/op"] = []string{".bashrc"}
			s.files[root+"/home/op/.bashrc"] = ""
			s.writable = map[string]bool{root + "/home/op/.bashrc": true}
		}},
		{"F7", "writable from the unit", func(s *fakeSys, _ *Inputs) {
			// Impermanence host: the real home is itself a tmpfs, the same
			// mount in the unit as on the host, so tmpfs proves nothing.
			s.files["/proc/self/mountinfo"] = "1 0 8:1 / / rw - ext4 /dev/sda1 rw\n2 1 0:40 / /home rw - tmpfs tmpfs rw\n"
			s.dirs[root+"/home"] = []string{"op"}
			s.dirs[root+"/home/op"] = []string{}
			s.writable = map[string]bool{root + "/home/op": true}
		}},
		{"F8a", "posh", func(s *fakeSys, _ *Inputs) {
			s.dirs[root+"/run"] = []string{"user"}
			s.dirs[root+"/run/user"] = []string{"1000"}
			// The listing omits posh so only F8a, not F14, sees it.
			s.dirs[root+"/run/user/1000"] = []string{"clown"}
			s.dirs[root+"/run/user/1000/posh"] = []string{"sock"}
		}},
		{"F8b", "/dev/pts/0 is writable", func(s *fakeSys, _ *Inputs) {
			s.dirs[root+"/dev/pts"] = []string{"ptmx", "0", "5"}
			s.chardevs[root+"/dev/pts/0"] = charDev{uint64(136<<8 | 0), 77}
			s.writable = map[string]bool{root + "/dev/pts/0": true}
		}},
		{"F9", "/repo/.git/hooks is writable", func(s *fakeSys, _ *Inputs) {
			s.writable = map[string]bool{root + "/repo/.git/hooks": true}
		}},
		{"F9", "/repo/.git/config is writable", func(s *fakeSys, _ *Inputs) {
			s.writable = map[string]bool{root + "/repo/.git/config": true}
		}},
		{"F9", "/repo/.git/refs is writable", func(s *fakeSys, _ *Inputs) {
			s.dirs[root+"/repo/.git/refs"] = []string{}
			s.writable = map[string]bool{root + "/repo/.git/refs": true}
		}},
		{"F9", "/repo/.git/logs is writable", func(s *fakeSys, _ *Inputs) {
			s.dirs[root+"/repo/.git/logs"] = []string{}
			s.writable = map[string]bool{root + "/repo/.git/logs": true}
		}},
		{"F9", "session credential /wt/a/.spinclass/git-credentials is readable", func(s *fakeSys, _ *Inputs) {
			s.files[root+"/wt/a/.spinclass/git-credentials"] = "https://x:tok@host\n"
		}},
		{"F9", "cannot establish that the session credential", func(s *fakeSys, _ *Inputs) {
			s.files[root+"/wt/a/.spinclass/git-credentials"] = ""
			s.readErrOn = map[string]error{root + "/wt/a/.spinclass/git-credentials": syscall.EIO}
		}},
		{"F9", "config.worktree is writable", func(s *fakeSys, _ *Inputs) {
			s.writable = map[string]bool{root + "/repo/.git/worktrees/a/config.worktree": true}
		}},
		{"F9", "/wt/a/.git is writable", func(s *fakeSys, _ *Inputs) {
			s.writable = map[string]bool{root + "/wt/a/.git": true}
		}},
		{"F9", "/repo/.git/hooks is writable", func(s *fakeSys, _ *Inputs) {
			// hooks replaced by a symlink: judged by where it leads INSIDE the unit.
			delete(s.dirs, root+"/repo/.git/hooks")
			s.symlinks[root+"/repo/.git/hooks"] = true
			s.links[root+"/repo/.git/hooks"] = "/scratch/hooks"
			s.dirs[root+"/scratch"] = []string{"hooks"}
			s.dirs[root+"/scratch/hooks"] = []string{}
			s.writable = map[string]bool{root + "/scratch/hooks": true}
		}},
		{"F10", "host pid namespace", func(s *fakeSys, _ *Inputs) {
			s.links["/proc/101/ns/pid"] = "pid:[4026531836]"
		}},
		{"F10", "host's procfs", func(s *fakeSys, _ *Inputs) {
			s.devs[root+"/proc"] = 22
		}},
		{"F10", "shows processes outside the unit", func(s *fakeSys, _ *Inputs) {
			s.dirs[root+"/proc"] = []string{"1", "2", "3", "4", "self"}
		}},
		{"F11", "runs unconfined", func(s *fakeSys, _ *Inputs) {
			s.dirs["/proc"] = append(s.dirs["/proc"], "400")
			s.files["/proc/400/comm"] = ".claude-wrapped\n"
			s.files["/proc/400/cgroup"] = "0::/user.slice/user-1000.slice/session-3.scope\n"
			s.links["/proc/400/cwd"] = "/wt/b/sub"
		}},
		{"F12", "accepts a connection", func(s *fakeSys, _ *Inputs) {
			s.connect = map[string]error{root + "/run/user/1000/clown/agent/b/serve.sock": nil}
		}},
		{"F13", "host network namespace", func(s *fakeSys, _ *Inputs) {
			s.links["/proc/101/ns/net"] = "net:[4026531840]"
		}},
		{"F14", "systemd", func(s *fakeSys, _ *Inputs) {
			s.dirs[root+"/run"] = []string{"user"}
			s.dirs[root+"/run/user"] = []string{"1000"}
			s.dirs[root+"/run/user/1000"] = []string{"clown", "ringmaster", "systemd"}
		}},
	}
	for _, c := range cases {
		t.Run(c.id+"/"+c.want, func(t *testing.T) {
			sys, in := confinedHost()
			c.mutate(sys, &in)
			r := Run(sys, in)
			f := factByID(t, r, c.id)
			if f.OK {
				t.Fatalf("%s passed after its breach", c.id)
			}
			if !strings.Contains(f.Detail, c.want) {
				t.Errorf("%s detail = %q, want it to mention %q", c.id, f.Detail, c.want)
			}
			for _, other := range r.Facts {
				if other.ID != c.id && !other.OK {
					t.Errorf("unrelated %s also failed: %s", other.ID, other.Detail)
				}
			}
			if r.Err() == nil {
				t.Error("Err() is nil with a failing fact")
			}
		})
	}
}

// A symlink inside the unit pointing at an absolute path is resolved against
// the unit's root, never the frontend's: a frontend-writable target must not
// make the unit's (unwritable) one look writable, and vice versa.
func TestSymlinksResolveInsideTheUnit(t *testing.T) {
	sys, in := confinedHost()
	delete(sys.dirs, root+"/repo/.git/hooks")
	sys.symlinks[root+"/repo/.git/hooks"] = true
	sys.links[root+"/repo/.git/hooks"] = "/home/op/hooks"
	sys.writable = map[string]bool{"/home/op/hooks": true} // the frontend's view
	if f := factByID(t, Run(sys, in), "F9"); !f.OK {
		t.Errorf("F9 judged the frontend's path, not the unit's: %s", f.Detail)
	}
}

// A credential file the unit lists as inaccessible exists on the host but its
// open is refused: that is the passing case, not a failure to establish.
func TestF9AcceptsAnInaccessibleCredential(t *testing.T) {
	sys, in := confinedHost()
	sys.files[root+"/wt/a/.spinclass/git-credentials"] = "https://x:tok@host\n"
	sys.readErrOn = map[string]error{root + "/wt/a/.spinclass/git-credentials": syscall.EACCES}
	if f := factByID(t, Run(sys, in), "F9"); !f.OK {
		t.Errorf("F9 = %+v, want pass for an EACCES credential", f)
	}
}

func TestSymlinkLoopFailsClosed(t *testing.T) {
	sys, in := confinedHost()
	delete(sys.dirs, root+"/repo/.git/hooks")
	sys.symlinks[root+"/repo/.git/hooks"] = true
	sys.links[root+"/repo/.git/hooks"] = "hooks"
	if f := factByID(t, Run(sys, in), "F9"); f.OK || !strings.Contains(f.Detail, "too many symlinks") {
		t.Errorf("F9 = %+v, want a symlink-loop failure", f)
	}
}

func TestF11NamesTheOffendingSession(t *testing.T) {
	sys, in := confinedHost()
	sys.dirs["/proc"] = append(sys.dirs["/proc"], "400")
	sys.files["/proc/400/comm"] = "claude\n"
	sys.files["/proc/400/cgroup"] = "0::/user.slice/user-1000.slice/session-3.scope\n"
	sys.links["/proc/400/cwd"] = "/wt/b/sub"
	f := factByID(t, Run(sys, in), "F11")
	if !strings.Contains(f.Detail, "session r/b") {
		t.Errorf("F11 detail = %q, want the session named", f.Detail)
	}
}

// A confined worker records no PID, or one from its unit's own pid namespace
// that is dead on the host (FDR 0035 "Session PIDs are untrusted"). While its
// unit runs it is still live, so its agent inside the unit is confined, not an
// offender.
func TestF11CountsARecordedSessionWhoseUnitRuns(t *testing.T) {
	sys, in := confinedHost()
	in.Live = in.Live[:1] // r/b's recorded pid is dead on the host ...
	in.Recorded = []Session{{Key: "r/b", Worktree: "/wt/b", PID: 12}}
	if f := factByID(t, Run(sys, in), "F11"); !f.OK {
		t.Errorf("F11 = %+v, want ok: r/b's agent runs inside its own running unit", f)
	}
}

// unitRunning is the liveness a confined session's ResolveState uses: a record
// that loads and a cgroup that holds a process.
func TestUnitRunning(t *testing.T) {
	sys, _ := confinedHost()
	if !unitRunning(sys, stateDir, "/wt/b") {
		t.Error("r/b's unit holds pid 200: want running")
	}
	sys.files["/sys/fs/cgroup"+otherCG+"/cgroup.procs"] = ""
	if unitRunning(sys, stateDir, "/wt/b") {
		t.Error("empty cgroup: want not running")
	}
	if unitRunning(sys, stateDir, "/wt/none") {
		t.Error("no record: want not running")
	}
}

// Once a recorded session's unit is gone its record vouches for nothing: an
// agent process that claims its cwd is an offender again, and the session
// itself is simply not live.
func TestF11RecordedSessionWhoseUnitIsGoneVouchesForNothing(t *testing.T) {
	for name, mutate := range map[string]func(*fakeSys){
		"cgroup gone": func(s *fakeSys) {
			delete(s.files, "/sys/fs/cgroup"+otherCG+"/cgroup.procs")
			delete(s.dirs, "/sys/fs/cgroup"+otherCG)
		},
		"cgroup empty": func(s *fakeSys) { s.files["/sys/fs/cgroup"+otherCG+"/cgroup.procs"] = "" },
		"no record":    func(s *fakeSys) { delete(s.files, UnitRecordPath(stateDir, "/wt/b")) },
	} {
		t.Run(name, func(t *testing.T) {
			sys, in := confinedHost()
			mutate(sys)
			in.Live = in.Live[:1]
			in.Recorded = []Session{{Key: "r/b", Worktree: "/wt/b", PID: 12}}
			f := factByID(t, Run(sys, in), "F11")
			if f.OK || !strings.Contains(f.Detail, "runs unconfined") {
				t.Errorf("F11 = %+v, want pid 200 named as unconfined", f)
			}
			if strings.Contains(f.Detail, "session r/b has no agent unit") || strings.Contains(f.Detail, "stale record") {
				t.Errorf("F11 = %+v: a session that is not live must not be reported as one", f)
			}
		})
	}
}

func TestF11FailsForALiveSessionWithoutAUnit(t *testing.T) {
	sys, in := confinedHost()
	delete(sys.files, UnitRecordPath(stateDir, "/wt/b"))
	f := factByID(t, Run(sys, in), "F11")
	if f.OK || !strings.Contains(f.Detail, "session r/b has no agent unit") {
		t.Errorf("F11 = %+v, want r/b named as unconfined", f)
	}
}

// A record whose unit is no longer running vouches for nothing: the session's
// agent, if it has one, is not in it.
func TestF11FailsForAStaleRecord(t *testing.T) {
	for name, mutate := range map[string]func(*fakeSys){
		"cgroup gone": func(s *fakeSys) {
			delete(s.files, "/sys/fs/cgroup"+otherCG+"/cgroup.procs")
			delete(s.dirs, "/sys/fs/cgroup"+otherCG)
		},
		"cgroup empty": func(s *fakeSys) { s.files["/sys/fs/cgroup"+otherCG+"/cgroup.procs"] = "" },
	} {
		t.Run(name, func(t *testing.T) {
			sys, in := confinedHost()
			mutate(sys)
			f := factByID(t, Run(sys, in), "F11")
			if f.OK || !strings.Contains(f.Detail, "session r/b") || !strings.Contains(f.Detail, "stale") {
				t.Errorf("F11 = %+v, want r/b's record named stale", f)
			}
		})
	}
}

func TestF11FailsWhenAUnitsCgroupIsUnreadable(t *testing.T) {
	sys, in := confinedHost()
	sys.readErrOn = map[string]error{"/sys/fs/cgroup" + otherCG + "/cgroup.procs": syscall.EIO}
	f := factByID(t, Run(sys, in), "F11")
	if f.OK || !strings.Contains(f.Detail, "session r/b") {
		t.Errorf("F11 = %+v, want r/b named", f)
	}
}

// clown FDR 0021's headless loop runs as juggler, not claude.
func TestF11FindsAnUnconfinedLoop(t *testing.T) {
	sys, in := confinedHost()
	sys.dirs["/proc"] = append(sys.dirs["/proc"], "400")
	sys.files["/proc/400/comm"] = "juggler\n"
	sys.files["/proc/400/cgroup"] = "0::/user.slice/user-1000.slice/session-3.scope\n"
	f := factByID(t, Run(sys, in), "F11")
	if f.OK || !strings.Contains(f.Detail, "agent pid 400") {
		t.Errorf("F11 = %+v, want pid 400 named", f)
	}
}

// A harness a unit record reports clown exec'd is searched for outside the
// units, by its basename truncated to the kernel's 15-byte comm.
func TestF11SearchesForTheRecordedHarness(t *testing.T) {
	sys, in := confinedHost()
	sys.files[UnitRecordPath(stateDir, "/wt/b")] = `{"version":1,"unit":"agent-b.service","cgroup":"` + otherCG +
		`","invocation_id":"x","uid":1000,"serve_socket":"/run/user/1000/clown/agent/b/serve.sock","principal":"principal-b","harness":"/nix/store/x-h/bin/some-long-harness-name"}`
	sys.files["/proc/200/comm"] = "some-long-harne\n" // in its unit: fine
	if f := factByID(t, Run(sys, in), "F11"); !f.OK {
		t.Fatalf("F11 failed with the harness inside its unit: %s", f.Detail)
	}
	sys.dirs["/proc"] = append(sys.dirs["/proc"], "400")
	sys.files["/proc/400/comm"] = "some-long-harne\n"
	sys.files["/proc/400/cgroup"] = "0::/user.slice/user-1000.slice/session-3.scope\n"
	f := factByID(t, Run(sys, in), "F11")
	if f.OK || !strings.Contains(f.Detail, "agent pid 400") {
		t.Errorf("F11 = %+v, want the unconfined harness named", f)
	}
}

func TestF11DetailDescribesTheCensus(t *testing.T) {
	sys, in := confinedHost()
	f := factByID(t, Run(sys, in), "F11")
	if !f.OK || !strings.Contains(f.Detail, "2 live sessions in running units") {
		t.Errorf("F11 = %+v, want the census summarised", f)
	}
}

// The record lives in the state dir, never the worktree the unit can write.
func TestRecordInTheWorktreeIsIgnored(t *testing.T) {
	sys, in := confinedHost()
	body := sys.files[UnitRecordPath(stateDir, "/wt/a")]
	delete(sys.files, UnitRecordPath(stateDir, "/wt/a"))
	sys.files["/wt/a/.spinclass/agent-unit.json"] = body
	if f := factByID(t, Run(sys, in), "F1"); f.OK {
		t.Error("a record inside the worktree was trusted")
	}
	if strings.HasPrefix(UnitRecordPath(stateDir, "/wt/a"), "/wt/a") {
		t.Error("UnitRecordPath points into the worktree")
	}
}

func TestRecordsSharingACgroupOrSocketFail(t *testing.T) {
	for name, other := range map[string]string{
		"cgroup": record("agent-b.service", unitCG, 0, "/run/user/1000/clown/agent/b/serve.sock", 0),
		"socket": record("agent-b.service", otherCG, 0, "/run/user/1000/clown/agent/a/serve.sock", 0),
	} {
		t.Run(name, func(t *testing.T) {
			sys, in := confinedHost()
			sys.files[UnitRecordPath(stateDir, "/wt/b")] = other
			if f := factByID(t, Run(sys, in), "F1"); f.OK || !strings.Contains(f.Detail, "report the same") {
				t.Errorf("F1 = %+v, want a shared-record failure", f)
			}
		})
	}
}

func TestMountNamespaceMismatchFailsClosed(t *testing.T) {
	sys, in := confinedHost()
	sys.links["/proc/101/ns/mnt"] = "mnt:[4026531841]"
	if f := factByID(t, Run(sys, in), "F7"); f.OK || !strings.Contains(f.Detail, "mount namespace") {
		t.Errorf("F7 = %+v, want a mount-namespace failure", f)
	}
}

// The reported main pid is advisory: clown reported one that was not in the
// unit's cgroup and every fact was skipped on it. The cgroup is the authority;
// the probes look through a cgroup member (all of which must share one mount
// namespace, see TestMountNamespaceMismatchFailsClosed) and say so.
func TestMainPIDOutsideTheCgroupIsIgnoredAndNoted(t *testing.T) {
	sys, in := confinedHost()
	sys.files[UnitRecordPath(stateDir, "/wt/a")] = record("agent-a.service", unitCG, ptyDev, "/run/user/1000/clown/agent/a/serve.sock", 999)
	r := Run(sys, in)
	f := factByID(t, r, "F1")
	if !f.OK || !strings.Contains(f.Detail, "main pid 999 is not in the unit's cgroup") {
		t.Errorf("F1 = %+v, want ok with the main-pid note", f)
	}
	if f7 := factByID(t, r, "F7"); strings.Contains(f7.Detail, "no confined unit to probe") {
		t.Errorf("F7 was skipped on the bad main pid: %+v", f7)
	}
}

// Today's state: no unit record. Every fact that looks into the unit fails
// rather than passing vacuously over a missing subject.
func TestNoUnitRecordFailsEveryUnitFact(t *testing.T) {
	sys, in := confinedHost()
	delete(sys.files, UnitRecordPath(stateDir, "/wt/a"))
	r := Run(sys, in)
	for _, id := range []string{"F1", "F3", "F4", "F5", "F6", "F7", "F8a", "F8b", "F9", "F10", "F11", "F12", "F13", "F14", "F15"} {
		if f := factByID(t, r, id); f.OK {
			t.Errorf("%s passed with no unit to probe", id)
		}
	}
	if r.OK() {
		t.Fatal("report passed with no unit")
	}
}

func TestUnreadableUnitRootFailsClosed(t *testing.T) {
	sys, in := confinedHost()
	delete(sys.dirs, root+"/")
	if f := factByID(t, Run(sys, in), "F5"); f.OK {
		t.Error("F5 passed although the unit's root could not be read: absence was vacuous")
	}
}

// An error that is not proof of absence (EIO, a timeout) must fail, never
// read as "unreachable".
func TestAmbiguousErrorsFailClosed(t *testing.T) {
	t.Run("connect", func(t *testing.T) {
		sys, in := confinedHost()
		sys.connect = map[string]error{root + "/run/user/1000/bus": syscall.EIO}
		if f := factByID(t, Run(sys, in), "F5"); f.OK {
			t.Error("F5 passed on EIO")
		}
	})
	t.Run("writable", func(t *testing.T) {
		sys, in := confinedHost()
		sys.writeErr = map[string]error{root + "/repo/.git/hooks": syscall.EIO}
		if f := factByID(t, Run(sys, in), "F9"); f.OK {
			t.Error("F9 passed on EIO")
		}
	})
	t.Run("peer", func(t *testing.T) {
		sys, in := confinedHost()
		delete(sys.peer, "/run/user/1000/agent.sock")
		if f := factByID(t, Run(sys, in), "F2"); f.OK {
			t.Error("F2 passed with no agent peer")
		}
	})
	t.Run("environ", func(t *testing.T) {
		sys, in := confinedHost()
		sys.readErrOn = map[string]error{"/proc/101/environ": errors.New("boom")}
		if f := factByID(t, Run(sys, in), "F4"); f.OK {
			t.Error("F4 passed on an unreadable environ")
		}
	})
}

func TestMissingServeSocketFailsF12(t *testing.T) {
	sys, in := confinedHost()
	sys.files[UnitRecordPath(stateDir, "/wt/a")] = record("agent-a.service", unitCG, ptyDev, "", 100)
	if f := factByID(t, Run(sys, in), "F12"); f.OK || !strings.Contains(f.Detail, "loopback") {
		t.Errorf("F12 = %+v, want a failure naming loopback", f)
	}
}

// A record clown rewrote after a failed launch has no unit; the error it
// carries must reach the operator instead of "names no unit".
func TestParseUnitRecordSurfacesClownsError(t *testing.T) {
	_, err := ParseUnitRecord([]byte(`{"version":1,"error":"private network setup failed: no slirp4netns"}`))
	if err == nil || !strings.Contains(err.Error(), "no slirp4netns") || strings.Contains(err.Error(), "names no unit") {
		t.Fatalf("got %v, want clown's own error text", err)
	}
}

func TestParseUnitRecordRejects(t *testing.T) {
	for name, body := range map[string]string{
		"root cgroup":    `{"version":1,"unit":"u","cgroup":"/"}`,
		"relative":       `{"version":1,"unit":"u","cgroup":"a/b"}`,
		"dotdot":         `{"version":1,"unit":"u","cgroup":"/x/clown-agent.slice/../b.service"}`,
		"login scope":    `{"version":1,"unit":"u","cgroup":"/user.slice/user-1000.slice/session-3.scope"}`,
		"not a service":  `{"version":1,"unit":"u","cgroup":"/x/clown-agent.slice/y.scope"}`,
		"slice is leaf":  `{"version":1,"unit":"u","cgroup":"/x/clown-agent.slice"}`,
		"version":        `{"version":2,"unit":"u","cgroup":"` + unitCG + `"}`,
		"no unit":        `{"version":1,"cgroup":"` + unitCG + `"}`,
		"serve relative": `{"version":1,"unit":"u","cgroup":"` + unitCG + `","serve_socket":"x.sock"}`,
	} {
		if _, err := ParseUnitRecord([]byte(body)); err == nil {
			t.Errorf("%s: accepted %s", name, body)
		}
	}
	if _, err := ParseUnitRecord([]byte(`{"version":1,"unit":"u","cgroup":"` + unitCG + `","future":1}`)); err != nil {
		t.Errorf("an unknown field was refused: %v", err)
	}
}

func TestRuntimeAllowFor(t *testing.T) {
	got := RuntimeAllowFor("/run/user/1000", "/run/user/1000/gnupg/S.gpg-agent.ssh")
	if strings.Join(got, ",") != "clown,ringmaster,gnupg" {
		t.Errorf("got %v", got)
	}
	got = RuntimeAllowFor("/run/user/1000", "/home/op/.local/state/ssh/agent.sock")
	if strings.Join(got, ",") != "clown,ringmaster" {
		t.Errorf("got %v", got)
	}
}

func TestCgroupWithinIsComponentWise(t *testing.T) {
	if cgroupWithin("/a/bc", "/a/b") {
		t.Error("/a/bc counted as within /a/b")
	}
	if !cgroupWithin("/a/b/c", "/a/b") || !cgroupWithin("/a/b", "/a/b") {
		t.Error("descendant or equal not within")
	}
}

func TestEmptyReportNeverPasses(t *testing.T) {
	if (Report{}).OK() || (Report{}).Err() == nil {
		t.Error("an empty report passed")
	}
}
