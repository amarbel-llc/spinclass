package folioeval

import (
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

// spinclass#465: the proc: anchor reaches a fixed list of files below /proc and
// nothing else, at their exact depth.
func TestProcAnchorAllowsOnlyTheFixedList(t *testing.T) {
	p := compile(t, sweatfileio.FolioPolicy{Read: []string{"proc:"}, Unmatched: "deny"})

	for _, path := range []string{
		"/proc/1/stat", "/proc/12345/stat", "/proc/self/stat", "/proc/thread-self/stat",
		"/proc/42/status", "/proc/42/cgroup",
		"/proc/meminfo", "/proc/loadavg",
		"/proc/pressure/cpu", "/proc/pressure/io", "/proc/pressure/memory",
	} {
		if v, reason := p.Evaluate(roots, "read", path); v != "allow" {
			t.Errorf("read %s = %s (%s), want allow", path, v, reason)
		}
	}

	for _, path := range []string{
		// The shapes the issue names as never reachable.
		"/proc/1/environ", "/proc/1/fd", "/proc/1/fd/0", "/proc/1/mem",
		"/proc/1/maps", "/proc/1/smaps", "/proc/1/smaps_rollup",
		"/proc/1/task", "/proc/1/task/1/stat", "/proc/1/task/1/environ",
		"/proc/1/cmdline", "/proc/1/cwd", "/proc/1/root", "/proc/1/exe",
		// Depth: no prefix match below a file, none above it.
		"/proc/1/stat/x", "/proc/1", "/proc", "/proc/", "/proc/pressure", "/proc/pressure/cpu/x",
		// The first segment of a per-process entry is a pid, not any name.
		"/proc/sys/stat", "/proc/net/status", "/proc/-1/stat", "/proc/1a/stat", "/proc/Self/stat",
		// Other files under /proc and lookalikes.
		"/proc/kcore", "/proc/sys/kernel/hostname", "/procfoo/meminfo", "/prc/meminfo", "/proc/meminfo/x",
		"/proc/pressure/CPU", "/proc/pressure/cpu.x",
	} {
		if v, _ := p.Evaluate(roots, "read", path); v == "allow" {
			t.Errorf("read %s was allowed", path)
		}
	}
}

// A single-entry proc: grants just that entry.
func TestProcAnchorSingleEntry(t *testing.T) {
	p := compile(t, sweatfileio.FolioPolicy{Read: []string{"proc:meminfo", "proc:*/stat"}, Unmatched: "deny"})
	for path, want := range map[string]string{
		"/proc/meminfo":      "allow",
		"/proc/7/stat":       "allow",
		"/proc/loadavg":      "deny",
		"/proc/7/status":     "deny",
		"/proc/pressure/cpu": "deny",
	} {
		if v, _ := p.Evaluate(roots, "read", path); v != want {
			t.Errorf("read %s = %s, want %s", path, v, want)
		}
	}
}

// proc: is read-only: it grants nothing for a write, and a write list that
// carries it refuses the whole stanza.
func TestProcAnchorIsReadOnly(t *testing.T) {
	p := compile(t, sweatfileio.FolioPolicy{Read: []string{"proc:"}, Unmatched: "deny"})
	if v, _ := p.Evaluate(roots, "write", "/proc/1/stat"); v == "allow" {
		t.Error("a read-only entry allowed a write")
	}
	if c, err := Compile(sweatfileio.FolioPolicy{Configured: true, Write: []string{"proc:meminfo"}, Unmatched: "ask"}); err == nil {
		t.Errorf("Compile accepted proc: in write: %+v", c)
	}
}

// A deny of an entry beats the bare allow on the same file: the single entry
// has more literal segments.
func TestProcAnchorDenyNarrowsTheBareAllow(t *testing.T) {
	p := compile(t, sweatfileio.FolioPolicy{Read: []string{"proc:"}, Deny: []string{"proc:*/cgroup"}, Unmatched: "ask"})
	if v, _ := p.Evaluate(roots, "read", "/proc/3/cgroup"); v != "deny" {
		t.Errorf("read /proc/3/cgroup = %s, want deny", v)
	}
	if v, _ := p.Evaluate(roots, "read", "/proc/3/stat"); v != "allow" {
		t.Errorf("read /proc/3/stat = %s, want allow", v)
	}
}

// A proc: entry never matches a blob.
func TestProcAnchorDoesNotMatchABlob(t *testing.T) {
	p := compile(t, sweatfileio.FolioPolicy{Read: []string{"proc:"}, Unmatched: "deny"})
	if v, _ := p.EvaluateBlob("read", "madder://blobs/blake2b256-aaaa"); v == "allow" {
		t.Error("proc: matched a blob")
	}
}
