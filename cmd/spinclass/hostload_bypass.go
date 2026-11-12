package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"code.linenisgreat.com/spinclass/internal/alfa/hostqueue"
)

// A merge or check runs inside the calling session's own `spinclass serve`, so
// whether it queues is decided by that serve's binary. A session started before
// the host-queue code was deployed keeps its old binary and runs every gate
// unqueued, silently (spinclass#399's stale state, with consequences). host-load
// names those sessions: a live serve whose executable lacks the admission
// predicate's wait text.

// QueueBypass is one live serve whose binary has no host-queue code.
type QueueBypass struct {
	PID     int    `json:"pid"`
	Session string `json:"session,omitempty"`
	Exe     string `json:"exe"`
}

// serveProcess is a running `spinclass serve` as the bypass scan sees it.
type serveProcess struct {
	pid     int
	session string
	exe     string
	// binary is the path the executable can be read at (/proc/<pid>/exe).
	binary string
}

// liveServeProcesses is the scan of /proc for running serves; a seam for tests.
var liveServeProcesses = scanServeProcesses

func scanServeProcesses() []serveProcess {
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	var out []serveProcess
	for _, d := range dirs {
		pid, err := strconv.Atoi(filepath.Base(d))
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join(d, "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
		if !isServeCommand(args) {
			continue
		}
		exe, err := os.Readlink(filepath.Join(d, "exe"))
		if err != nil {
			continue
		}
		p := serveProcess{pid: pid, exe: exe, binary: filepath.Join(d, "exe")}
		if env, err := os.ReadFile(filepath.Join(d, "environ")); err == nil {
			for _, kv := range strings.Split(string(env), "\x00") {
				if v, ok := strings.CutPrefix(kv, "SPINCLASS_SESSION_ID="); ok {
					p.session = v
				}
			}
		}
		out = append(out, p)
	}
	return out
}

// isServeCommand reports whether args are a `spinclass serve` / `sc serve`.
func isServeCommand(args []string) bool {
	if len(args) < 2 {
		return false
	}
	base := filepath.Base(args[0])
	if base != "sc" && !strings.Contains(base, "spinclass") {
		return false
	}
	return args[1] == "serve"
}

// binaryHasQueue reports whether the file at path contains the host-queue wait
// text, reading it in chunks.
func binaryHasQueue(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	needle := []byte(hostqueue.AdmissionMarker)
	buf := make([]byte, 1<<20)
	var carry []byte
	for {
		n, err := f.Read(buf)
		if n > 0 {
			chunk := append(carry, buf[:n]...)
			if bytes.Contains(chunk, needle) {
				return true, nil
			}
			keep := min(len(needle)-1, len(chunk))
			carry = append([]byte(nil), chunk[len(chunk)-keep:]...)
		}
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
}

// queueBypasses lists the live serves whose binary lacks the host-queue code.
// A binary that cannot be read is not reported (nothing is known about it). One
// read per distinct executable path.
func queueBypasses() []QueueBypass {
	verdict := map[string]bool{}
	var out []QueueBypass
	for _, p := range liveServeProcesses() {
		has, seen := verdict[p.exe]
		if !seen {
			var err error
			has, err = binaryHasQueue(p.binary)
			if err != nil {
				continue
			}
			verdict[p.exe] = has
		}
		if !has {
			out = append(out, QueueBypass{PID: p.pid, Session: p.session, Exe: p.exe})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// formatQueueBypass renders the warning section; empty when there is none.
func formatQueueBypass(b []QueueBypass) string {
	if len(b) == 0 {
		return ""
	}
	var s strings.Builder
	fmt.Fprintf(&s, "\nQUEUE BYPASS: %d live session(s) run a spinclass serve with no host-queue code, so their gates run unqueued; restart them", len(b))
	for _, x := range b {
		name := x.Session
		if name == "" {
			name = "?"
		}
		fmt.Fprintf(&s, "\n  %s (pid %d) %s", name, x.PID, x.Exe)
	}
	return s.String()
}
