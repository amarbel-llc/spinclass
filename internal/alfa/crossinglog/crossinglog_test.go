package crossinglog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var epoch = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func newLog(t *testing.T, mutate func(*Options)) (*Log, *fakeClock, string) {
	t.Helper()
	clock := &fakeClock{t: epoch}
	dir := filepath.Join(t.TempDir(), "spinclass", "bridge")
	o := Options{Dir: dir, Principal: "principal-1", Now: clock.Now}
	if mutate != nil {
		mutate(&o)
	}
	return Open(o), clock, dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readEntries(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %q is not JSON: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func TestLineCarriesTheContractFieldsAndNoValues(t *testing.T) {
	l, _, dir := newLog(t, nil)
	l.Record(Crossing{
		Session: "repo/branch", Service: "session", Verb: "update-this-session-description", Outcome: "ok",
		Duration: 1500 * time.Microsecond, ReqBytes: 120, RespBytes: 45,
		Args:         json.RawMessage(`{"description":"rotate sk-live-XXXX before the release","reason":"sk-live-XXXX","zeta":1}`),
		DeclaredArgs: []string{"description", "reason", "zeta"},
	})
	path := filepath.Join(dir, "principal-1.ndjson")
	raw := readFile(t, path)
	if strings.Contains(raw, "sk-live-XXXX") || strings.Contains(raw, "rotate") || strings.Contains(raw, "release") {
		t.Fatalf("an argument value reached the log:\n%s", raw)
	}
	es := readEntries(t, path)
	if len(es) != 1 {
		t.Fatalf("%d lines", len(es))
	}
	e := es[0]
	if e["ts"] != "2026-11-25T12:00:00Z" {
		t.Errorf("ts = %v", e["ts"])
	}
	for k, want := range map[string]any{
		"session": "repo/branch", "service": "session", "verb": "update-this-session-description",
		"outcome": "ok", "duration_ms": float64(1), "req_bytes": float64(120), "resp_bytes": float64(45),
	} {
		if e[k] != want {
			t.Errorf("%s = %v, want %v", k, e[k], want)
		}
	}
	names, _ := e["arg_names"].([]any)
	if fmt.Sprint(names) != "[description reason zeta]" {
		t.Errorf("arg_names = %v, want the sorted names", names)
	}
	sum, _ := e["args_sha256"].(string)
	if len(sum) != 16 {
		t.Errorf("args_sha256 = %q, want 16 hex", sum)
	}
}

var testKey = []byte("0123456789abcdef0123456789abcdef")

func TestArgsDigestIsOfTheCanonicalKeySortedCompactJSON(t *testing.T) {
	_, _, _, sumA := Redact("whoami", json.RawMessage(`{"b": 2, "a": [1, 2]}`), nil, nil, testKey)
	_, _, _, sumB := Redact("whoami", json.RawMessage(`{"a":[1,2],"b":2}`), nil, nil, testKey)
	_, _, _, sumC := Redact("whoami", json.RawMessage(`{"a":[1,2],"b":3}`), nil, nil, testKey)
	if sumA != sumB {
		t.Errorf("key order or whitespace changed the digest: %s vs %s", sumA, sumB)
	}
	if sumA == sumC {
		t.Error("a different value gave the same digest")
	}
}

// The digest is keyed: a bare sha256 of a low-entropy argument could be
// brute-forced from the file, and the same arguments must not give the same
// digest under another log's key.
func TestArgsDigestIsAKeyedHashNotABareSha256(t *testing.T) {
	args := json.RawMessage(`{"a":[1,2],"b":2}`)
	_, _, _, sum := Redact("whoami", args, nil, nil, testKey)
	_, _, _, other := Redact("whoami", args, nil, nil, []byte("another key another key another."))
	if sum == other {
		t.Errorf("two keys gave the digest %s", sum)
	}
	// First 16 hex of the bare sha256 of {"a":[1,2],"b":2}.
	if sum == "68b7e88ecdcf999e" || len(sum) != 16 {
		t.Errorf("digest %q is the bare sha256 or the wrong length", sum)
	}
}

func TestEachLogHasItsOwnRandomDigestKey(t *testing.T) {
	a, _, dirA := newLog(t, nil)
	b, _, dirB := newLog(t, nil)
	c := Crossing{Service: "session", Verb: "whoami", Outcome: "ok", Args: json.RawMessage(`{"a":1}`)}
	a.Record(c)
	b.Record(c)
	sumA := readEntries(t, filepath.Join(dirA, "principal-1.ndjson"))[0]["args_sha256"]
	sumB := readEntries(t, filepath.Join(dirB, "principal-1.ndjson"))[0]["args_sha256"]
	if sumA == sumB {
		t.Errorf("two logs wrote the same digest %v for the same arguments", sumA)
	}
	a.Record(c)
	if es := readEntries(t, filepath.Join(dirA, "principal-1.ndjson")); es[0]["args_sha256"] != es[1]["args_sha256"] {
		t.Error("one log gave two digests for the same arguments")
	}
}

// Only the names a verb declares are written; the rest of what a peer sent is
// counted, so a request cannot put its own text into a line through an
// argument name.
func TestOnlyDeclaredArgumentNamesAreLoggedTheRestAreCounted(t *testing.T) {
	l, _, dir := newLog(t, nil)
	l.Record(Crossing{
		Service: "session", Verb: "restart-this-session", Outcome: "refused",
		Args:         json.RawMessage(`{"force":true,"sk-live-XXXX":1,"ghp_abcdef":2,"posture":"x"}`),
		DeclaredArgs: []string{"force", "posture", "reason"},
	})
	path := filepath.Join(dir, "principal-1.ndjson")
	raw := readFile(t, path)
	if strings.Contains(raw, "sk-live") || strings.Contains(raw, "ghp_") {
		t.Fatalf("an undeclared argument name reached the log:\n%s", raw)
	}
	e := readEntries(t, path)[0]
	if fmt.Sprint(e["arg_names"]) != "[force posture]" {
		t.Errorf("arg_names = %v, want [force posture]", e["arg_names"])
	}
	if e["other_arg_names"] != float64(2) {
		t.Errorf("other_arg_names = %v, want 2", e["other_arg_names"])
	}
	// A verb that declares nothing logs no names at all.
	l.Record(Crossing{Service: "session", Verb: "whoami", Outcome: "ok", Args: json.RawMessage(`{"principal":"x"}`)})
	e = readEntries(t, path)[1]
	if fmt.Sprint(e["arg_names"]) != "[]" || e["other_arg_names"] != float64(1) {
		t.Errorf("whoami: arg_names %v other %v", e["arg_names"], e["other_arg_names"])
	}
}

func TestALineIsAtMost512BytesAndStillJSON(t *testing.T) {
	l, _, dir := newLog(t, nil)
	long := strings.Repeat("a", 4000)
	l.Record(Crossing{
		Session: long, Service: "session", Verb: "cancel-restart", Outcome: "ok",
		Args:         json.RawMessage(fmt.Sprintf(`{"target":%q,"session":%q}`, strings.Repeat("b", 128), strings.Repeat("c", 128))),
		DeclaredArgs: []string{"target", "session", "child"},
	})
	raw := readFile(t, filepath.Join(dir, "principal-1.ndjson"))
	lines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("%d lines:\n%s", len(lines), raw)
	}
	for i, ln := range lines {
		if len(ln)+1 > MaxLineBytes {
			t.Errorf("line %d is %d bytes, over %d", i, len(ln)+1, MaxLineBytes)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Errorf("line %d is not JSON: %v", i, err)
		}
		if m["truncated"] != true {
			t.Errorf("line %d does not say it was truncated: %v", i, m)
		}
	}
}

func TestEmptyAndOddArgumentsStillMakeALine(t *testing.T) {
	for name, args := range map[string]json.RawMessage{
		"nil": nil, "empty object": json.RawMessage(`{}`), "array": json.RawMessage(`[1,2]`),
		"scalar": json.RawMessage(`"sk-live-XXXX"`), "invalid": json.RawMessage(`{sk-live-XXXX`),
	} {
		names, _, fields, sum := Redact("whoami", args, nil, nil, testKey)
		if names == nil || len(names) != 0 {
			t.Errorf("%s: names = %#v, want an empty non-nil list", name, names)
		}
		if len(fields) != 0 || len(sum) != 16 {
			t.Errorf("%s: fields %v sum %q", name, fields, sum)
		}
	}
}

func TestSafeFieldsLogOnlyIdentifierShapedValuesOfAllowListedNames(t *testing.T) {
	safe := map[string][]string{"cancel-restart": {"target", "session", "branch", "message"}}
	long := strings.Repeat("a", 129)
	ok := strings.Repeat("a", 128)
	args := fmt.Sprintf(`{"target":"repo/some-branch.1_x","session":"with space","branch":%q,"message":"abc","child":"abc","extra":%q}`, long, ok)
	_, _, fields, _ := Redact("cancel-restart", json.RawMessage(args), safe, nil, testKey)
	if fields["target"] != "repo/some-branch.1_x" {
		t.Errorf("identifier-shaped target dropped: %v", fields)
	}
	if _, has := fields["session"]; has {
		t.Errorf("a value with a space was logged: %v", fields)
	}
	if _, has := fields["branch"]; has {
		t.Errorf("a 129-char value was logged: %v", fields)
	}
	for _, name := range []string{"message", "child", "extra"} {
		if _, has := fields[name]; has {
			t.Errorf("%s is never loggable: %v", name, fields)
		}
	}
	// A 128-char identifier is within the bound.
	_, _, fields, _ = Redact("cancel-restart", json.RawMessage(fmt.Sprintf(`{"branch":%q}`, ok)), safe, nil, testKey)
	if fields["branch"] != ok {
		t.Errorf("a 128-char identifier was dropped")
	}
	// Non-string values and other verbs log nothing.
	_, _, fields, _ = Redact("cancel-restart", json.RawMessage(`{"target":5}`), safe, nil, testKey)
	if len(fields) != 0 {
		t.Errorf("a number was logged: %v", fields)
	}
	_, _, fields, _ = Redact("whoami", json.RawMessage(`{"target":"repo/b"}`), safe, nil, testKey)
	if len(fields) != 0 {
		t.Errorf("a verb with no allow-list entry logged %v", fields)
	}
}

func TestSafeFieldValuesReachTheFileOthersDoNot(t *testing.T) {
	l, _, dir := newLog(t, func(o *Options) { o.SafeFields = map[string][]string{"cancel-restart": {"target"}} })
	l.Record(Crossing{
		Session: "s/b", Service: "session", Verb: "cancel-restart", Outcome: "refused",
		Args: json.RawMessage(`{"target":"repo/branch","reason":"fix: bump deps\n\nsk-live-XXXX"}`),
	})
	raw := readFile(t, filepath.Join(dir, "principal-1.ndjson"))
	if !strings.Contains(raw, "repo/branch") {
		t.Errorf("allow-listed identifier missing:\n%s", raw)
	}
	if strings.Contains(raw, "sk-live") || strings.Contains(raw, "bump deps") {
		t.Errorf("free text reached the log:\n%s", raw)
	}
}

func TestVerbServiceAndOutcomeCarryNoAttackerText(t *testing.T) {
	l, _, dir := newLog(t, nil)
	l.Record(Crossing{Session: "s/b", Service: "sk-live-XXXX", Verb: "sk-live XXXX; rm -rf", Outcome: "weird"})
	l.Record(Crossing{Session: "s/b", Service: "egress", Verb: "hello", Outcome: "unknown-service"})
	l.Record(Crossing{Session: "s/b", Service: "session", Verb: "hello", Outcome: "version"})
	es := readEntries(t, filepath.Join(dir, "principal-1.ndjson"))
	if es[0]["service"] != "unknown" || es[0]["verb"] != "unknown" || es[0]["outcome"] != "error" {
		t.Errorf("unsanitised: %v", es[0])
	}
	if es[1]["service"] != "egress" || es[1]["outcome"] != "refused" {
		t.Errorf("unknown-service: %v", es[1])
	}
	if es[2]["outcome"] != "version" {
		t.Errorf("version is its own outcome: %v", es[2])
	}
}

func TestDirectoryIs0700AndFileIs0600(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	l, _, dir := newLog(t, nil)
	l.Record(Crossing{Service: "session", Verb: "whoami", Outcome: "ok"})
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %o, want 700", di.Mode().Perm())
	}
	fi, err := os.Stat(filepath.Join(dir, "principal-1.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %o, want 600", fi.Mode().Perm())
	}
}

func TestPrincipalIsAFileNameSegment(t *testing.T) {
	l, _, dir := newLog(t, func(o *Options) { o.Principal = "../../evil/../x" })
	l.Record(Crossing{Service: "session", Verb: "whoami", Outcome: "ok"})
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) != 1 || strings.ContainsAny(ents[0].Name(), "/") || strings.HasPrefix(ents[0].Name(), ".") {
		t.Fatalf("entries %v %v", ents, err)
	}
}

func TestRotationAtTheSizeLimitKeepsThreeGenerations(t *testing.T) {
	l, _, dir := newLog(t, func(o *Options) { o.MaxBytes = 600 })
	for i := 0; i < 60; i++ {
		l.Record(Crossing{Session: "s/b", Service: "session", Verb: fmt.Sprintf("v%02d", i), Outcome: "ok"})
	}
	base := filepath.Join(dir, "principal-1.ndjson")
	for _, suffix := range []string{"", ".1", ".2", ".3"} {
		fi, err := os.Stat(base + suffix)
		if err != nil {
			t.Fatalf("%s missing: %v", suffix, err)
		}
		if fi.Size() > 600 {
			t.Errorf("%s is %d bytes, over the limit", suffix, fi.Size())
		}
	}
	if _, err := os.Stat(base + ".4"); !os.IsNotExist(err) {
		t.Errorf("a fourth generation exists: %v", err)
	}
	// The newest line is in the live file, older ones in higher generations.
	live := readEntries(t, base)
	if live[len(live)-1]["verb"] != "v59" {
		t.Errorf("newest line not in the live file: %v", live[len(live)-1])
	}
	g1 := readEntries(t, base+".1")
	if g1[len(g1)-1]["verb"].(string) >= live[0]["verb"].(string) {
		t.Errorf(".1 is not older than the live file")
	}
	fi, _ := os.Stat(base + ".1")
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("rotated file mode %o", fi.Mode().Perm())
	}
}

func TestDefaultsAreFourMiBThreeGenerationsFourteenDays(t *testing.T) {
	if DefaultMaxBytes != 4<<20 || DefaultGenerations != 3 || DefaultMaxAge != 14*24*time.Hour {
		t.Errorf("defaults: %d %d %v", DefaultMaxBytes, DefaultGenerations, DefaultMaxAge)
	}
}

func TestOpenDeletesFilesOlderThanFourteenDays(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spinclass", "bridge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := epoch.Add(-15 * 24 * time.Hour)
	fresh := epoch.Add(-13 * 24 * time.Hour)
	mk := func(name string, mtime time.Time) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	mk("gone.ndjson", old)
	mk("gone.ndjson.2", old)
	mk("other-principal.ndjson", old)
	mk("kept.ndjson", fresh)
	mk("kept.ndjson.1", fresh)
	mk("notes.txt", old) // not ours
	Open(Options{Dir: dir, Principal: "p", Now: (&fakeClock{t: epoch}).Now})
	for name, want := range map[string]bool{
		"gone.ndjson": false, "gone.ndjson.2": false, "other-principal.ndjson": false,
		"kept.ndjson": true, "kept.ndjson.1": true, "notes.txt": true,
	} {
		_, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != want {
			t.Errorf("%s: exists=%v, want %v", name, err == nil, want)
		}
	}
}

func TestAFailingWriteNeverFailsOrPanicsAndIsCountedAndLoggedOncePerMinute(t *testing.T) {
	var logged []string
	l, clock, _ := newLog(t, func(o *Options) {
		o.Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	})
	l.appendFn = func(string, []byte) error { return errors.New("disk full") }
	for i := 0; i < 5; i++ {
		l.Record(Crossing{Service: "session", Verb: "whoami", Outcome: "ok"}) // no return value to fail
	}
	if l.Failures() != 5 {
		t.Errorf("Failures() = %d, want 5", l.Failures())
	}
	if len(logged) != 1 {
		t.Errorf("logged %d lines inside a minute: %v", len(logged), logged)
	}
	clock.Advance(61 * time.Second)
	l.Record(Crossing{Service: "session", Verb: "whoami", Outcome: "ok"})
	if len(logged) != 2 {
		t.Errorf("a minute later logged %d lines total, want 2", len(logged))
	}
	// And the real filesystem failing (the directory is a file) is the same.
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l2 := Open(Options{Dir: filepath.Join(blocked, "bridge"), Principal: "p"})
	l2.Record(Crossing{Service: "session", Verb: "whoami", Outcome: "ok"})
	if l2.Failures() != 1 {
		t.Errorf("an unusable directory: Failures() = %d, want 1", l2.Failures())
	}
}

// The log is written under the operator's state directory by a process whose
// session can write elsewhere under the same uid: a link or a special file put
// where the log goes must not redirect or block a crossing.
func TestASymlinkedLogFileIsNotFollowed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spinclass", "bridge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "p.ndjson")); err != nil {
		t.Fatal(err)
	}
	l := Open(Options{Dir: dir, Principal: "p"})
	l.Record(Crossing{Service: "session", Verb: "whoami", Outcome: "ok"})
	if got := readFile(t, target); got != "keep\n" {
		t.Errorf("the link target was written: %q", got)
	}
	if l.Failures() != 1 {
		t.Errorf("Failures() = %d, want 1", l.Failures())
	}
}

func TestASymlinkedLogDirectoryIsRefused(t *testing.T) {
	real := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(t.TempDir(), "spinclass")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "bridge")
	if err := os.Symlink(real, dir); err != nil {
		t.Fatal(err)
	}
	l := Open(Options{Dir: dir, Principal: "p"})
	l.Record(Crossing{Service: "session", Verb: "whoami", Outcome: "ok"})
	if ents, _ := os.ReadDir(real); len(ents) != 0 {
		t.Errorf("a log was written through the directory link: %v", ents)
	}
	if l.Failures() != 1 {
		t.Errorf("Failures() = %d, want 1", l.Failures())
	}
}

func TestASpecialFileWhereTheLogGoesIsRefusedWithoutBlocking(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spinclass", "bridge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "p.ndjson"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := Open(Options{Dir: dir, Principal: "p"})
	done := make(chan struct{})
	go func() {
		l.Record(Crossing{Service: "session", Verb: "whoami", Outcome: "ok"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record blocked on a FIFO")
	}
	if l.Failures() != 1 {
		t.Errorf("Failures() = %d, want 1", l.Failures())
	}
}

func TestALooseExistingDirectoryAndFileAreTightened(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	dir := filepath.Join(t.TempDir(), "spinclass", "bridge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p.ndjson"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	l := Open(Options{Dir: dir, Principal: "p"})
	l.Record(Crossing{Service: "session", Verb: "whoami", Outcome: "ok"})
	di, _ := os.Stat(dir)
	fi, _ := os.Stat(filepath.Join(dir, "p.ndjson"))
	if di.Mode().Perm() != 0o700 || fi.Mode().Perm() != 0o600 {
		t.Errorf("dir %o file %o, want 700 and 600", di.Mode().Perm(), fi.Mode().Perm())
	}
}

func TestNilLogRecordsNothing(t *testing.T) {
	var l *Log
	l.Record(Crossing{Service: "session", Verb: "whoami", Outcome: "ok"})
	if l.Failures() != 0 {
		t.Error("nil log counted a failure")
	}
}
