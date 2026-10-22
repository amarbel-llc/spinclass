package confinement

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// prober holds the subject (the driver's unit) once, so every fact reads the
// same view.
type prober struct {
	sys System
	in  Inputs

	rec        UnitRecord
	ptyRdev    uint64 // the unit's own pty, stat'd from the frontend
	ptyFsdev   uint64 // ... and the devpts instance it lives on
	pids       []int  // every process in the unit's cgroup and its descendants
	root       string // /proc/<pid>/root of the unit's main process
	subjectErr error
	note       string // a defect in the record that did not stop the probes

	liveUnits map[string]UnitRecord // session key -> record, for sessions that have one
	liveErrs  map[string]error      // session key -> why it has none
}

func (p *prober) loadSubject() {
	p.liveUnits = map[string]UnitRecord{}
	p.liveErrs = map[string]error{}
	for _, s := range p.in.Live {
		rec, err := LoadUnitRecord(p.sys, p.in.StateDir, s.Worktree)
		if err != nil {
			p.liveErrs[s.Key] = err
			continue
		}
		p.liveUnits[s.Key] = rec
	}
	// A session with no live PID is live while its unit runs: its record loads
	// and its cgroup holds a process. Anything less leaves it out, as before.
	for _, s := range p.in.Recorded {
		if unitRunning(p.sys, p.in.StateDir, s.Worktree) {
			rec, _ := LoadUnitRecord(p.sys, p.in.StateDir, s.Worktree)
			p.liveUnits[s.Key] = rec
		}
	}
	if err := p.sharedRecords(); err != nil {
		p.subjectErr = err
		return
	}

	rec, err := LoadUnitRecord(p.sys, p.in.StateDir, p.in.Driver.Worktree)
	if err != nil {
		p.subjectErr = err
		return
	}
	p.rec = rec
	pids, err := p.cgroupTreeProcs(rec.Cgroup)
	if err != nil {
		p.subjectErr = err
		return
	}
	if len(pids) == 0 {
		p.subjectErr = fmt.Errorf("unit cgroup %s has no processes: the unit is not running", rec.Cgroup)
		return
	}
	p.pids = pids
	rootPID := pids[0]
	// The reported main pid only picks which process to look through. It is
	// advisory: the cgroup is the authority, and the loop below fails unless
	// EVERY process in it shares one mount namespace, so any member speaks for
	// the unit. A main pid that is not in the cgroup (clown reported one that
	// was not) is ignored and noted, rather than skipping every fact.
	if rec.MainPID > 0 {
		if slices.Contains(pids, rec.MainPID) {
			rootPID = rec.MainPID
		} else {
			p.note = fmt.Sprintf("the record's main pid %d is not in the unit's cgroup (ignored; looked through pid %d)", rec.MainPID, rootPID)
		}
	}
	p.root = fmt.Sprintf("/proc/%d/root", rootPID)
	// Path facts look through one process's root; they speak for the whole
	// unit only if every process shares that mount namespace.
	mntNS, err := p.sys.Readlink(fmt.Sprintf("/proc/%d/ns/mnt", rootPID))
	if err != nil {
		p.subjectErr = fmt.Errorf("reading the unit's mount namespace: %w", err)
		return
	}
	for _, pid := range pids {
		ns, err := p.sys.Readlink(fmt.Sprintf("/proc/%d/ns/mnt", pid))
		if err != nil {
			p.subjectErr = fmt.Errorf("reading pid %d's mount namespace: %w", pid, err)
			return
		}
		if ns != mntNS {
			p.subjectErr = fmt.Errorf("unit pid %d is in mount namespace %s, not the unit's %s: path facts cannot speak for it", pid, ns, mntNS)
			return
		}
	}
	// Every path probe below treats "cannot reach X in the unit" as a pass,
	// so the root itself must be readable or those passes are vacuous.
	if _, err := p.sys.ReadDir(p.root + "/"); err != nil {
		p.subjectErr = fmt.Errorf("cannot look into unit %s through %s: %w", rec.Unit, p.root, err)
		return
	}
	if rec.Pty != "" {
		rdev, fsdev, ok, err := p.sys.CharDevice(rec.Pty)
		switch {
		case err != nil:
			p.subjectErr = fmt.Errorf("the unit's reported pty %s: %w", rec.Pty, err)
		case !ok:
			p.subjectErr = fmt.Errorf("the unit's reported pty %s is not a character device", rec.Pty)
		default:
			p.ptyRdev, p.ptyFsdev = rdev, fsdev
		}
	}
}

// sharedRecords refuses records that claim another session's cgroup or serve
// socket: two sessions cannot share a unit, and F12 skips its own socket.
func (p *prober) sharedRecords() error {
	byCgroup, bySocket := map[string]string{}, map[string]string{}
	keys := make([]string, 0, len(p.liveUnits))
	for k := range p.liveUnits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r := p.liveUnits[k]
		if other, ok := byCgroup[r.Cgroup]; ok {
			return fmt.Errorf("sessions %s and %s report the same unit cgroup %s", other, k, r.Cgroup)
		}
		byCgroup[r.Cgroup] = k
		if r.ServeSocket != "" {
			if other, ok := bySocket[r.ServeSocket]; ok {
				return fmt.Errorf("sessions %s and %s report the same serve socket %s", other, k, r.ServeSocket)
			}
			bySocket[r.ServeSocket] = k
		}
	}
	return nil
}

// cgroupTreeProcs returns every pid in cg and all its descendant cgroups.
func (p *prober) cgroupTreeProcs(cg string) ([]int, error) {
	var pids []int
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if depth > 32 {
			return fmt.Errorf("cgroup tree under %s is too deep", cg)
		}
		b, err := p.sys.ReadFile(filepath.Join(dir, "cgroup.procs"))
		if err != nil {
			return fmt.Errorf("reading %s/cgroup.procs: %w", dir, err)
		}
		for _, f := range strings.Fields(string(b)) {
			n, err := strconv.Atoi(f)
			if err != nil || n <= 0 {
				return fmt.Errorf("%s/cgroup.procs has a bad pid %q", dir, f)
			}
			pids = append(pids, n)
		}
		names, err := p.sys.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("listing cgroup %s: %w", dir, err)
		}
		for _, n := range names {
			child := filepath.Join(dir, n)
			fi, err := p.sys.Lstat(child)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue // removed between list and stat
				}
				return fmt.Errorf("inspecting cgroup %s: %w", child, err)
			}
			if fi.IsDir() {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(filepath.Join("/sys/fs/cgroup", cg), 0); err != nil {
		return nil, fmt.Errorf("reading the unit's processes: %w", err)
	}
	sort.Ints(pids)
	return pids, nil
}

const maxSymlinkHops = 40

// at resolves a path as the unit sees it, under its root, one component at a
// time: a symlink is read and re-resolved inside the unit's root, never handed
// to the kernel to follow against the frontend's. The result is a host path
// whose every existing component is a non-symlink; a missing component ends
// the walk and the rest is appended unresolved (the caller then sees ENOENT).
func (p *prober) at(unitPath string) (string, error) {
	if !path.IsAbs(unitPath) {
		return "", fmt.Errorf("unit path %q is not absolute", unitPath)
	}
	rest := strings.Split(strings.TrimPrefix(path.Clean(unitPath), "/"), "/")
	cur := "/"
	hops := 0
	for len(rest) > 0 {
		comp := rest[0]
		rest = rest[1:]
		if comp == "" || comp == "." {
			continue
		}
		if comp == ".." {
			cur = path.Dir(cur)
			continue
		}
		next := path.Join(cur, comp)
		fi, err := p.sys.Lstat(p.root + next)
		if err != nil {
			// Absent or untraversable (an InaccessiblePaths node): the unit
			// cannot get past here either, and the caller's own operation on
			// the unresolved path fails the same way.
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
				return p.root + path.Join(append([]string{next}, rest...)...), nil
			}
			return "", fmt.Errorf("resolving %s in the unit: %w", unitPath, err)
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			cur = next
			continue
		}
		if hops++; hops > maxSymlinkHops {
			return "", fmt.Errorf("resolving %s in the unit: too many symlinks", unitPath)
		}
		target, err := p.sys.Readlink(p.root + next)
		if err != nil {
			return "", fmt.Errorf("resolving %s in the unit: %w", unitPath, err)
		}
		if path.IsAbs(target) {
			cur = "/"
		}
		rest = append(strings.Split(target, "/"), rest...)
	}
	if cur == "/" {
		return p.root + "/", nil
	}
	return p.root + cur, nil
}

func (p *prober) operatorRuntimeDir() string {
	return fmt.Sprintf("/run/user/%d", p.sys.Getuid())
}

func fact(id, name string, err error, okDetail string) Fact {
	if err != nil {
		return Fact{ID: id, Name: name, OK: false, Detail: err.Error()}
	}
	return Fact{ID: id, Name: name, OK: true, Detail: okDetail}
}

// needSubject turns a missing unit into the fact's failure.
func (p *prober) needSubject() error {
	if p.subjectErr != nil {
		return fmt.Errorf("no confined unit to probe: %w", p.subjectErr)
	}
	return nil
}

func (p *prober) cgroupOf(pid int) (string, error) {
	b, err := p.sys.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", err
	}
	return parseCgroupV2(b)
}

// F1: the driver's agent runs inside the active unit clown reported for its
// session. The unit is bound to the driver by principal: the record, written
// by the frontend clown that launched the unit and out of the unit's reach,
// names the driver's principal (claude's environment never carries it,
// clown#136). An agent runtime running outside every unit is F11's.
func (p *prober) f1Unit() Fact {
	const id, name = "F1", "driver runs inside a reported agent unit"
	err := func() error {
		if err := p.needSubject(); err != nil {
			return err
		}
		principal := p.in.Driver.Principal
		if principal == "" {
			return errors.New("the driver has no principal to bind its unit to")
		}
		if p.rec.Principal != principal {
			return fmt.Errorf("unit %s was launched for principal %q, not the driver's principal %s", p.rec.Unit, p.rec.Principal, principal)
		}
		return nil
	}()
	detail := fmt.Sprintf("unit %s, %d processes", p.rec.Unit, len(p.pids))
	if p.note != "" {
		detail += "; " + p.note
	}
	return fact(id, name, err, detail)
}

// environValue returns key's value in /proc/<pid>/environ ("" when unset).
func (p *prober) environValue(pid int, key string) (string, error) {
	b, err := p.sys.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return "", err
	}
	for _, kv := range strings.Split(string(b), "\x00") {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, nil
		}
	}
	return "", nil
}

// F2: piggy-agent is outside every agent unit and every agent process tree.
// Limit: SO_PEERCRED names the listener; a socket-activated agent resolves
// to the user manager.
func (p *prober) f2AgentOutside() Fact {
	const id, name = "F2", "piggy-agent outside every agent unit"
	var detail string
	err := func() error {
		if p.in.AgentSocket == "" {
			return errors.New("no agent socket (SSH_AUTH_SOCK) to identify the agent by")
		}
		pid, err := p.sys.PeerPID(p.in.AgentSocket)
		if err != nil {
			return fmt.Errorf("identifying the agent behind %s: %w", p.in.AgentSocket, err)
		}
		if pid <= 0 {
			return fmt.Errorf("agent socket %s reports no peer pid", p.in.AgentSocket)
		}
		cg, err := p.cgroupOf(pid)
		if err != nil {
			return fmt.Errorf("reading agent pid %d's cgroup: %w", pid, err)
		}
		if strings.Contains(cg, "/"+AgentSlice+"/") {
			return fmt.Errorf("agent pid %d (%s) is inside an agent unit", pid, cg)
		}
		units := map[string]UnitRecord{}
		for k, r := range p.liveUnits {
			units[k] = r
		}
		if p.subjectErr == nil {
			units[p.in.Driver.Key] = p.rec
		}
		for key, r := range units {
			if cgroupWithin(cg, r.Cgroup) {
				return fmt.Errorf("agent pid %d (%s) is inside %s's unit %s", pid, cg, key, r.Unit)
			}
		}
		for anc := pid; anc > 1; {
			ppid, comm, err := p.parentAndComm(anc)
			if err != nil {
				return fmt.Errorf("walking agent pid %d's ancestry at %d: %w", pid, anc, err)
			}
			if anc != pid {
				if slices.Contains(p.agentComms(), comm) {
					return fmt.Errorf("agent pid %d descends from agent runtime %s (pid %d)", pid, comm, anc)
				}
				acg, err := p.cgroupOf(anc)
				if err != nil {
					return fmt.Errorf("reading agent pid %d's ancestor %d's cgroup: %w", pid, anc, err)
				}
				if strings.Contains(acg, "/"+AgentSlice+"/") || p.inSomeUnit(acg) {
					return fmt.Errorf("agent pid %d descends from a process in agent unit (pid %d, %s)", pid, anc, acg)
				}
			}
			anc = ppid
		}
		detail = fmt.Sprintf("agent pid %d in %s", pid, cg)
		return nil
	}()
	return fact(id, name, err, detail)
}

// agentComms are the names of agent runtimes: the fallback list plus every
// harness a live unit record reports clown exec'd, in comm form.
func (p *prober) agentComms() []string {
	comms := slices.Clone(p.in.AgentComms)
	if len(comms) == 0 {
		comms = slices.Clone(DefaultAgentComms)
	}
	var harnesses []string
	for _, r := range p.liveUnits {
		if n := commOf(r.Harness); n != "" && !slices.Contains(comms, n) && !slices.Contains(harnesses, n) {
			harnesses = append(harnesses, n)
		}
	}
	sort.Strings(harnesses)
	return append(comms, harnesses...)
}

// maxComm is the kernel's comm length (TASK_COMM_LEN less the NUL).
const maxComm = 15

// commOf reduces a harness name or path to what /proc/<pid>/comm shows for it.
func commOf(harness string) string {
	if harness == "" {
		return ""
	}
	n := path.Base(harness)
	if n == "/" || n == "." {
		return ""
	}
	if len(n) > maxComm {
		n = n[:maxComm]
	}
	return n
}

// parentAndComm reads ppid and comm from /proc/<pid>/stat.
func (p *prober) parentAndComm(pid int) (int, string, error) {
	fields, comm, err := p.statFields(pid)
	if err != nil {
		return 0, "", err
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, "", fmt.Errorf("bad ppid %q", fields[1])
	}
	return ppid, comm, nil
}

// statFields returns the /proc/<pid>/stat fields after the comm, starting
// with state, plus the comm itself.
func (p *prober) statFields(pid int) ([]string, string, error) {
	b, err := p.sys.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return nil, "", err
	}
	s := string(b)
	open, closing := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || closing < open {
		return nil, "", fmt.Errorf("unparsable /proc/%d/stat", pid)
	}
	fields := strings.Fields(s[closing+1:])
	if len(fields) < 5 {
		return nil, "", fmt.Errorf("short /proc/%d/stat", pid)
	}
	return fields, s[open+1 : closing], nil
}

// devMajorMinor decodes a Linux dev_t (the st_rdev / tty_nr encoding).
func devMajorMinor(dev uint64) (uint64, uint64) {
	major := ((dev >> 8) & 0xfff) | ((dev >> 32) &^ 0xfff)
	minor := (dev & 0xff) | ((dev >> 12) &^ 0xff)
	return major, minor
}

// isTerminal reports whether a character device is a terminal an fd could
// write a prompt through: a virtual console, /dev/tty, the console, or a pty
// slave. /dev/ptmx (5:2) is a master multiplexer, not a terminal.
func isTerminal(dev uint64) bool {
	major, minor := devMajorMinor(dev)
	switch {
	case major == 4:
		return true
	case major == 5:
		return minor == 0 || minor == 1
	case major >= 136 && major <= 143:
		return true
	}
	return false
}

func sameDev(a, b uint64) bool {
	am, an := devMajorMinor(a)
	bm, bn := devMajorMinor(b)
	return am == bm && an == bn
}

// isOwnPty: the same device number on the same devpts instance as the pty
// clown reported. A private devpts reuses low minors, so rdev alone collides.
func (p *prober) isOwnPty(rdev, fsdev uint64) bool {
	return p.rec.Pty != "" && sameDev(rdev, p.ptyRdev) && fsdev == p.ptyFsdev
}

// F3: no unit process holds a terminal other than the unit's own pty.
func (p *prober) f3NoInheritedTTY() Fact {
	const id, name = "F3", "no inherited tty"
	err := func() error {
		if err := p.needSubject(); err != nil {
			return err
		}
		for _, pid := range p.pids {
			fds, err := p.sys.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
			if err != nil {
				return fmt.Errorf("listing pid %d's fds: %w", pid, err)
			}
			for _, fd := range fds {
				rdev, fsdev, ok, err := p.sys.CharDevice(fmt.Sprintf("/proc/%d/fd/%s", pid, fd))
				if err != nil {
					if errors.Is(err, fs.ErrNotExist) {
						continue // closed between list and stat
					}
					return fmt.Errorf("inspecting pid %d fd %s: %w", pid, fd, err)
				}
				if ok && isTerminal(rdev) && !p.isOwnPty(rdev, fsdev) {
					major, minor := devMajorMinor(rdev)
					return fmt.Errorf("pid %d fd %s is terminal %d:%d, not the unit's own pty", pid, fd, major, minor)
				}
			}
			fields, _, err := p.statFields(pid)
			if err != nil {
				return fmt.Errorf("reading pid %d's controlling tty: %w", pid, err)
			}
			ttyNr, err := strconv.ParseUint(fields[4], 10, 64)
			if err != nil {
				return fmt.Errorf("pid %d has a bad tty_nr %q", pid, fields[4])
			}
			// tty_nr carries no devpts instance; it must at least be the
			// own pty's number, and F3's fd scan above covers the instance.
			if ttyNr != 0 && (p.rec.Pty == "" || !sameDev(ttyNr, p.ptyRdev)) {
				major, minor := devMajorMinor(ttyNr)
				return fmt.Errorf("pid %d's controlling tty is %d:%d, not the unit's own pty", pid, major, minor)
			}
		}
		return nil
	}()
	return fact(id, name, err, fmt.Sprintf("%d processes checked", len(p.pids)))
}

var displayEnvVars = []string{"DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS"}

// F4: no unit process carries the display or bus variables (hygiene; the
// sockets themselves are F5, F6, F14).
func (p *prober) f4NoDisplayEnv() Fact {
	const id, name = "F4", "no display or bus variables"
	err := func() error {
		if err := p.needSubject(); err != nil {
			return err
		}
		for _, pid := range p.pids {
			b, err := p.sys.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
			if err != nil {
				return fmt.Errorf("reading pid %d's environment: %w", pid, err)
			}
			for _, kv := range strings.Split(string(b), "\x00") {
				if k, _, _ := strings.Cut(kv, "="); slices.Contains(displayEnvVars, k) {
					return fmt.Errorf("pid %d has %s set", pid, k)
				}
			}
		}
		return nil
	}()
	return fact(id, name, err, "")
}

// unreachable reports whether err means "the unit cannot get there": the
// path does not exist, is denied, or refuses. Any other error (EIO, ELOOP, a
// timeout) is not proof of absence and fails.
func unreachable(err error) bool {
	return err != nil && (errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOTDIR))
}

// denied reports whether a Writable error means the write would be refused.
func denied(err error) bool {
	return err != nil && (errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS) ||
		errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM))
}

// mustNotConnectIn probes a unit path, resolved inside the unit's root.
func (p *prober) mustNotConnectIn(label, unitPath string) error {
	host, err := p.at(unitPath)
	if err != nil {
		return err
	}
	return p.mustNotConnect(label, host)
}

func (p *prober) mustNotConnect(label, hostPath string) error {
	err := p.sys.Connect(hostPath, false)
	if err == nil {
		return fmt.Errorf("%s (%s) accepts a connection from the unit", label, strings.TrimPrefix(hostPath, p.root))
	}
	if !unreachable(err) {
		return fmt.Errorf("cannot establish that %s is unreachable: %w", label, err)
	}
	return nil
}

// F5: the operator's session bus and user manager are unreachable from the
// unit. The user manager's private socket would let the agent start an
// unconfined unit.
func (p *prober) f5NoSessionBus() Fact {
	const id, name = "F5", "session bus and user manager unreachable"
	err := func() error {
		if err := p.needSubject(); err != nil {
			return err
		}
		rt := p.operatorRuntimeDir()
		for _, s := range []struct{ label, path string }{
			{"the session bus", rt + "/bus"},
			{"the user manager", rt + "/systemd/private"},
		} {
			if err := p.mustNotConnectIn(s.label, s.path); err != nil {
				return err
			}
		}
		for _, part := range strings.Split(p.sys.Getenv("DBUS_SESSION_BUS_ADDRESS"), ";") {
			for _, kv := range strings.Split(strings.TrimPrefix(part, "unix:"), ",") {
				if v, ok := strings.CutPrefix(kv, "path="); ok {
					if err := p.mustNotConnectIn("the session bus", v); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}()
	return fact(id, name, err, "")
}

// F6: the operator's display is unusable from the unit: no wayland or X11
// socket file connects and the X cookie is unreadable. Abstract X sockets
// live in the network namespace, which F13 requires to be private.
func (p *prober) f6NoDisplay() Fact {
	const id, name = "F6", "display unusable"
	err := func() error {
		if err := p.needSubject(); err != nil {
			return err
		}
		rt := p.operatorRuntimeDir()
		names, err := p.sys.ReadDir(rt)
		if err != nil {
			return fmt.Errorf("listing the operator's runtime dir %s: %w", rt, err)
		}
		for _, n := range names {
			if strings.HasPrefix(n, "wayland-") && !strings.HasSuffix(n, ".lock") {
				if err := p.mustNotConnectIn("wayland socket "+n, rt+"/"+n); err != nil {
					return err
				}
			}
		}
		xs, err := p.sys.ReadDir("/tmp/.X11-unix")
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("listing /tmp/.X11-unix: %w", err)
		}
		for _, n := range xs {
			if err := p.mustNotConnectIn("X11 socket "+n, "/tmp/.X11-unix/"+n); err != nil {
				return err
			}
		}
		xauth := p.in.XAuthority
		if xauth == "" && p.in.OperatorHome != "" {
			xauth = filepath.Join(p.in.OperatorHome, ".Xauthority")
		}
		if xauth == "" {
			return errors.New("no X authority path or operator home to check the cookie against")
		}
		host, err := p.at(xauth)
		if err != nil {
			return err
		}
		if _, err := p.sys.ReadFile(host); err == nil {
			return fmt.Errorf("the operator's X authority %s is readable from the unit", xauth)
		} else if !unreachable(err) {
			return fmt.Errorf("cannot establish that %s is unreadable: %w", xauth, err)
		}
		return nil
	}()
	return fact(id, name, err, "")
}

type mount struct {
	majmin   string
	point    string
	fstype   string
	readOnly bool
}

func parseMountinfo(b []byte) ([]mount, error) {
	var ms []mount
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		pre, post, ok := strings.Cut(line, " - ")
		if !ok {
			return nil, fmt.Errorf("unparsable mountinfo line %q", line)
		}
		f, g := strings.Fields(pre), strings.Fields(post)
		if len(f) < 6 || len(g) < 1 {
			return nil, fmt.Errorf("short mountinfo line %q", line)
		}
		ro := slices.Contains(strings.Split(f[5], ","), "ro")
		ms = append(ms, mount{majmin: f[2], point: unescapeMount(f[4]), fstype: g[0], readOnly: ro})
	}
	if len(ms) == 0 {
		return nil, errors.New("empty mountinfo")
	}
	return ms, nil
}

func unescapeMount(s string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}

// covering returns the last-mounted, deepest mount whose point contains p.
func covering(ms []mount, p string) (mount, bool) {
	var best mount
	found := false
	for _, m := range ms {
		if m.point == p || m.point == "/" || strings.HasPrefix(p, m.point+"/") {
			if !found || len(m.point) >= len(best.point) {
				best, found = m, true
			}
		}
	}
	return best, found
}

// homeEscapeTargets are the writes FDR 0035's threat model names, relative to
// the operator's home (F7).
var homeEscapeTargets = []string{
	".bashrc", ".bash_profile", ".profile", ".zshrc", ".zshenv",
	".gitconfig", ".config", ".config/git", ".config/fish/config.fish",
	".config/spinclass/sweatfile", ".ssh", ".gnupg", ".local/bin",
	".local/state", ".local/share",
}

// nearestExistingIn walks up a unit path to its first existing ancestor and
// returns that ancestor's host path, resolved inside the unit.
func (p *prober) nearestExistingIn(unitPath string) (string, error) {
	for {
		host, err := p.at(unitPath)
		if err != nil {
			return "", err
		}
		_, err = p.sys.Lstat(host)
		if err == nil {
			return host, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := path.Dir(unitPath)
		if parent == unitPath {
			return "", fmt.Errorf("no existing ancestor of %s", unitPath)
		}
		unitPath = parent
	}
}

func (p *prober) mustBeUnwritable(label, unitPath string) error {
	host, err := p.nearestExistingIn(unitPath)
	if err != nil {
		return fmt.Errorf("resolving %s in the unit: %w", label, err)
	}
	werr := p.sys.Writable(host)
	if werr == nil {
		return fmt.Errorf("%s is writable from the unit (via %s)", label, strings.TrimPrefix(host, p.root))
	}
	if !denied(werr) {
		return fmt.Errorf("cannot establish that %s is unwritable: %w", label, werr)
	}
	return nil
}

// F7: the threat model's escape targets in the operator's home are not
// writable from the unit. A target is skipped without a write test only when
// the mount covering it in the unit is a read-only or tmpfs mount that is NOT
// the one covering it on the host (a tmpfs home on an impermanence host is
// the real home); everything else must refuse the write.
func (p *prober) f7HomeNotWritable() Fact {
	const id, name = "F7", "operator home not writable"
	err := func() error {
		if err := p.needSubject(); err != nil {
			return err
		}
		if p.in.OperatorHome == "" {
			return errors.New("no operator home to check")
		}
		unitMI, err := p.sys.ReadFile(strings.TrimSuffix(p.root, "/root") + "/mountinfo")
		if err != nil {
			return fmt.Errorf("reading the unit's mountinfo: %w", err)
		}
		unitMounts, err := parseMountinfo(unitMI)
		if err != nil {
			return err
		}
		hostMI, err := p.sys.ReadFile("/proc/self/mountinfo")
		if err != nil {
			return fmt.Errorf("reading the frontend's mountinfo: %w", err)
		}
		hostMounts, err := parseMountinfo(hostMI)
		if err != nil {
			return err
		}
		for _, t := range homeEscapeTargets {
			target := filepath.Join(p.in.OperatorHome, t)
			um, ok := covering(unitMounts, target)
			if !ok {
				return fmt.Errorf("no mount covers %s in the unit", target)
			}
			hm, _ := covering(hostMounts, target)
			if (um.readOnly || um.fstype == "tmpfs") && um.majmin != hm.majmin {
				continue
			}
			if err := p.mustBeUnwritable(target, target); err != nil {
				return err
			}
		}
		return nil
	}()
	return fact(id, name, err, "")
}

// F8a: no posh attach socket is reachable (posh/smooth-maple/bobo).
func (p *prober) f8aNoPoshSocket() Fact {
	const id, name = "F8a", "no posh attach socket reachable"
	err := func() error {
		if err := p.needSubject(); err != nil {
			return err
		}
		dirs := []string{p.operatorRuntimeDir() + "/posh"}
		if d := p.sys.Getenv("POSH_DIR"); d != "" {
			dirs = append(dirs, d)
		}
		for _, d := range dirs {
			host, err := p.at(d)
			if err != nil {
				return err
			}
			_, err = p.sys.Lstat(host)
			if err == nil {
				return fmt.Errorf("posh's directory %s is visible from the unit", d)
			}
			if !unreachable(err) {
				return fmt.Errorf("cannot establish that %s is hidden: %w", d, err)
			}
		}
		return nil
	}()
	return fact(id, name, err, "")
}

// F8b: no pty slave other than the unit's own is writable from the unit.
func (p *prober) f8bNoOperatorPTY() Fact {
	const id, name = "F8b", "no operator pty writable"
	err := func() error {
		if err := p.needSubject(); err != nil {
			return err
		}
		dir, err := p.at("/dev/pts")
		if err != nil {
			return err
		}
		names, err := p.sys.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("listing the unit's /dev/pts: %w", err)
		}
		for _, n := range names {
			if n == "ptmx" {
				continue
			}
			node := dir + "/" + n
			fi, err := p.sys.Lstat(node)
			if err != nil {
				return fmt.Errorf("inspecting /dev/pts/%s in the unit: %w", n, err)
			}
			if fi.Mode()&fs.ModeSymlink != 0 {
				return fmt.Errorf("/dev/pts/%s in the unit is a symlink", n)
			}
			rdev, fsdev, ok, err := p.sys.CharDevice(node)
			if err != nil {
				return fmt.Errorf("inspecting /dev/pts/%s in the unit: %w", n, err)
			}
			if ok && p.isOwnPty(rdev, fsdev) {
				continue
			}
			werr := p.sys.Writable(node)
			if werr == nil {
				return fmt.Errorf("/dev/pts/%s is writable from the unit", n)
			}
			if !denied(werr) {
				return fmt.Errorf("cannot establish that /dev/pts/%s is unwritable: %w", n, werr)
			}
		}
		return nil
	}()
	return fact(id, name, err, "")
}

// F9: the repo's hooks and config are not writable from the unit (either one
// runs code on the next git command). Paths resolve inside the unit, so a
// hooks dir replaced by a symlink is judged by what the unit reaches.
func (p *prober) f9HooksNotWritable() Fact {
	const id, name = "F9", "git hooks, config, refs and logs not writable; session credential unreadable"
	err := func() error {
		if err := p.needSubject(); err != nil {
			return err
		}
		if p.in.GitCommonDir == "" {
			return errors.New("the driver repo's git common dir is unknown")
		}
		// refs and logs are read-only too: a ref the unit could create or move
		// is a ref the unit could push with the session's credential, and the
		// frontend (grit) is the only writer of refs.
		targets := []string{
			filepath.Join(p.in.GitCommonDir, "hooks"), filepath.Join(p.in.GitCommonDir, "config"),
			filepath.Join(p.in.GitCommonDir, "refs"), filepath.Join(p.in.GitCommonDir, "logs"),
		}
		if p.in.HooksPath != "" {
			targets = append(targets, p.in.HooksPath)
		}
		// The frontend's git discovers the repo through the worktree's .git
		// pointer file and reads its config.worktree, both inside binds the
		// unit can otherwise write; a write to either runs code in the
		// frontend. A writable file is also a replaceable one (the rw worktree
		// bind it sits in would let the unit swap it), so an unwritable result
		// here means the read-only file bind is in force.
		if wt := p.in.Driver.Worktree; wt != "" {
			targets = append(targets,
				filepath.Join(p.in.GitCommonDir, "worktrees", filepath.Base(wt), "config.worktree"),
				filepath.Join(wt, ".git"),
			)
		}
		for _, t := range targets {
			if err := p.mustBeUnwritable(t, t); err != nil {
				return err
			}
		}
		return p.mustNotReadCredential()
	}()
	return fact(id, name, err, "")
}

// CredentialRelPath is the session's minted credential under its worktree
// (auth.CredentialFile; a test in the spawn package pins the two together).
const CredentialRelPath = ".spinclass/git-credentials"

// mustNotReadCredential: the unit lists the session credential as an
// inaccessible path, so reading it must be refused (or the file be absent when
// no credential was minted). Raw git in the unit could otherwise push with it.
func (p *prober) mustNotReadCredential() error {
	wt := p.in.Driver.Worktree
	if wt == "" {
		return nil
	}
	unitPath := filepath.Join(wt, CredentialRelPath)
	host, err := p.at(unitPath)
	if err != nil {
		return err
	}
	_, err = p.sys.ReadFile(host)
	switch {
	case err == nil:
		return fmt.Errorf("the session credential %s is readable from the unit", unitPath)
	case unreachable(err):
		return nil
	default:
		return fmt.Errorf("cannot establish that the session credential %s is unreadable: %w", unitPath, err)
	}
}

// F10: the unit has its own pid namespace and its own procfs showing only its
// own processes, so it cannot reach /proc/<operator pid>/root (FDR 0035 hard
// requirement).
func (p *prober) f10PrivatePIDs() Fact {
	const id, name = "F10", "private pid namespace and procfs"
	err := func() error {
		if err := p.needSubject(); err != nil {
			return err
		}
		ours, err := p.sys.Readlink("/proc/self/ns/pid")
		if err != nil {
			return fmt.Errorf("reading the frontend's pid namespace: %w", err)
		}
		for _, pid := range p.pids {
			theirs, err := p.sys.Readlink(fmt.Sprintf("/proc/%d/ns/pid", pid))
			if err != nil {
				return fmt.Errorf("reading pid %d's pid namespace: %w", pid, err)
			}
			if theirs == ours {
				return fmt.Errorf("pid %d shares the host pid namespace (%s)", pid, ours)
			}
		}
		procDir, err := p.at("/proc")
		if err != nil {
			return err
		}
		hostProc, err := p.sys.Stat("/proc")
		if err != nil {
			return fmt.Errorf("stat /proc: %w", err)
		}
		unitProc, err := p.sys.Stat(procDir)
		if err != nil {
			return fmt.Errorf("stat the unit's /proc: %w", err)
		}
		if sameFilesystem(hostProc, unitProc) {
			return errors.New("the unit's /proc is the host's procfs: host processes are visible to it")
		}
		// A distinct procfs can still belong to the host pid namespace (one
		// mounted before the unshare). The unit's procfs must list no more
		// processes than the unit holds.
		entries, err := p.sys.ReadDir(procDir)
		if err != nil {
			return fmt.Errorf("listing the unit's /proc: %w", err)
		}
		visible := 0
		for _, e := range entries {
			if _, err := strconv.Atoi(e); err == nil {
				visible++
			}
		}
		if visible > len(p.pids) {
			return fmt.Errorf("the unit's /proc lists %d processes but the unit holds %d: it shows processes outside the unit", visible, len(p.pids))
		}
		return nil
	}()
	return fact(id, name, err, "")
}

func sameFilesystem(a, b fs.FileInfo) bool {
	as, aok := a.Sys().(*syscall.Stat_t)
	bs, bok := b.Sys().(*syscall.Stat_t)
	if !aok || !bok {
		return true // cannot tell: fail closed
	}
	return as.Dev == bs.Dev
}

// F11: every live agent on the host is confined. One unconfined agent could
// paint the prompt the card root depends on, and an unconfined session's MCP
// servers are an escape hatch for every confined one.
//
// The census is by unit: every live session needs a record whose unit is
// running now (a non-empty cgroup under clown-agent.slice), and a process is
// confined iff its cgroup is within such a unit. Only the search for a runtime
// outside every unit still goes by name (agentComms), which a hostile process
// can evade; see docs/plans/2026-10-20-agent-census-by-unit.md.
func (p *prober) f11EveryAgentConfined() Fact {
	const id, name = "F11", "every live agent confined"
	var offenders []string
	running := 0
	comms := p.agentComms()
	err := func() error {
		keys := make([]string, 0, len(p.liveErrs))
		for k := range p.liveErrs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			offenders = append(offenders, "session "+k+" has no agent unit")
		}
		offenders = append(offenders, p.staleUnits(&running)...)
		entries, err := p.sys.ReadDir("/proc")
		if err != nil {
			return fmt.Errorf("listing /proc: %w", err)
		}
		for _, e := range entries {
			pid, err := strconv.Atoi(e)
			if err != nil {
				continue
			}
			comm, err := p.sys.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue // exited
				}
				return fmt.Errorf("reading pid %d's name: %w", pid, err)
			}
			if !slices.Contains(comms, strings.TrimSpace(string(comm))) {
				continue
			}
			cg, err := p.cgroupOf(pid)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return fmt.Errorf("reading agent pid %d's cgroup: %w", pid, err)
			}
			if !p.inSomeUnit(cg) {
				offenders = append(offenders, fmt.Sprintf("agent pid %d (%s) runs unconfined in %s", pid, p.sessionOfPID(pid), cg))
			}
		}
		if len(offenders) > 0 {
			return errors.New(strings.Join(offenders, "; "))
		}
		return nil
	}()
	return fact(id, name, err, fmt.Sprintf("%d live sessions in running units; no %s outside them", running, strings.Join(comms, ", ")))
}

// staleUnits names every live session whose recorded unit is not running: a
// record vouches for nothing once its cgroup is gone or empty, since the
// session's agent, if it has one, is not in it. running counts the rest.
func (p *prober) staleUnits(running *int) []string {
	keys := make([]string, 0, len(p.liveUnits))
	for k := range p.liveUnits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var stale []string
	for _, k := range keys {
		r := p.liveUnits[k]
		pids, err := p.cgroupTreeProcs(r.Cgroup)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			stale = append(stale, fmt.Sprintf("session %s's unit %s is gone (no cgroup %s): stale record", k, r.Unit, r.Cgroup))
		case err != nil:
			stale = append(stale, fmt.Sprintf("session %s's unit %s cannot be counted: %v", k, r.Unit, err))
		case len(pids) == 0:
			stale = append(stale, fmt.Sprintf("session %s's unit %s has no processes: stale record", k, r.Unit))
		default:
			*running++
		}
	}
	return stale
}

func (p *prober) inSomeUnit(cg string) bool {
	for _, r := range p.liveUnits {
		if cgroupWithin(cg, r.Cgroup) {
			return true
		}
	}
	return false
}

// sessionOfPID names the live session whose worktree holds pid's cwd, so an
// offender is actionable.
func (p *prober) sessionOfPID(pid int) string {
	cwd, err := p.sys.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	if err != nil {
		return "unknown session"
	}
	for _, s := range p.in.Live {
		if s.Worktree != "" && (cwd == s.Worktree || strings.HasPrefix(cwd, s.Worktree+"/")) {
			return "session " + s.Key
		}
	}
	return "cwd " + cwd
}

// F12: the unit reaches its own serve socket and no other session's.
func (p *prober) f12OnlyOwnServe() Fact {
	const id, name = "F12", "no other session's serve reachable"
	err := func() error {
		if err := p.needSubject(); err != nil {
			return err
		}
		if p.rec.ServeSocket == "" {
			return errors.New("the unit record names no per-session serve socket; serve is still on loopback, where every unit can reach every session's serve")
		}
		keys := make([]string, 0, len(p.liveUnits))
		for k := range p.liveUnits {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			r := p.liveUnits[k]
			if k == p.in.Driver.Key || r.ServeSocket == "" {
				continue
			}
			if err := p.mustNotConnectIn("session "+k+"'s serve socket", r.ServeSocket); err != nil {
				return err
			}
		}
		return nil
	}()
	return fact(id, name, err, "")
}

// F13: the unit has its own network namespace (FDR 0035 hard requirement).
// In the host's, every session's loopback MCP bridge (moxy's folio_write
// included) and every abstract unix socket is reachable from the unit.
func (p *prober) f13PrivateNetwork() Fact {
	const id, name = "F13", "private network namespace"
	err := func() error {
		if err := p.needSubject(); err != nil {
			return err
		}
		ours, err := p.sys.Readlink("/proc/self/ns/net")
		if err != nil {
			return fmt.Errorf("reading the frontend's network namespace: %w", err)
		}
		for _, pid := range p.pids {
			theirs, err := p.sys.Readlink(fmt.Sprintf("/proc/%d/ns/net", pid))
			if err != nil {
				return fmt.Errorf("reading pid %d's network namespace: %w", pid, err)
			}
			if theirs == ours {
				return fmt.Errorf("pid %d shares the host network namespace (%s): every loopback MCP bridge and abstract socket is reachable", pid, ours)
			}
		}
		return nil
	}()
	return fact(id, name, err, "")
}

// F15: the frontend's principal mapping (what the grant walk trusts) names
// the driver's session and unit, and the worktree's cached principal agrees.
// A disagreement means one side was rewritten; both values are named.
func (p *prober) f15PrincipalMapping() Fact {
	const id, name = "F15", "frontend principal mapping matches"
	err := func() error {
		if err := p.needSubject(); err != nil {
			return err
		}
		d := p.in.Driver
		if d.Principal == "" {
			return errors.New("the driver has no principal to look up")
		}
		m, err := LoadPrincipalMapping(p.sys, p.in.StateDir, d.Principal)
		if err != nil {
			return err
		}
		switch {
		case m.SessionKey != d.Key:
			return fmt.Errorf("the frontend maps principal %s to session %s, but the driver is session %s", d.Principal, m.SessionKey, d.Key)
		case m.Unit != p.rec.Unit:
			return fmt.Errorf("the frontend maps principal %s to unit %s, but the driver's unit is %s", d.Principal, m.Unit, p.rec.Unit)
		case d.RecordedPrincipal != "" && d.RecordedPrincipal != d.Principal:
			return fmt.Errorf("the worktree's session state records principal %s, the frontend knows %s", d.RecordedPrincipal, d.Principal)
		}
		return nil
	}()
	return fact(id, name, err, "")
}

// F14: the operator's runtime dir, as the unit sees it, holds nothing but the
// sockets the spec bound in. It is where the bus, the user manager, gpg-agent,
// pipewire and posh live; an allowlist catches the ones not named above.
func (p *prober) f14RuntimeDirBoundOnly() Fact {
	const id, name = "F14", "operator runtime dir holds only bound sockets"
	err := func() error {
		if err := p.needSubject(); err != nil {
			return err
		}
		dir, err := p.at(p.operatorRuntimeDir())
		if err != nil {
			return err
		}
		names, err := p.sys.ReadDir(dir)
		if err != nil {
			if unreachable(err) {
				return nil
			}
			return fmt.Errorf("listing the operator's runtime dir in the unit: %w", err)
		}
		var extra []string
		for _, n := range names {
			if !slices.Contains(p.in.RuntimeAllow, n) {
				extra = append(extra, n)
			}
		}
		if len(extra) > 0 {
			sort.Strings(extra)
			return fmt.Errorf("the unit sees %s in the operator's runtime dir beyond the bound sockets (allowed: %s)",
				strings.Join(extra, ", "), strings.Join(p.in.RuntimeAllow, ", "))
		}
		return nil
	}()
	return fact(id, name, err, "")
}
