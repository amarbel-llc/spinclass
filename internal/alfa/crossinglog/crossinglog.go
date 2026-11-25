// Package crossinglog is the frontend's record of what crossed the bridge
// (bridge contract v1, "Crossing log"): one NDJSON line per crossing in
// <state>/spinclass/bridge/<principal>.ndjson.
//
// The redaction rule is normative: argument VALUES are never written. A line
// carries the service, the verb (a tool name), the sorted argument NAMES,
// sizes, and the first 16 hex of the sha256 of the arguments' canonical JSON.
// A per-verb allow-list (SafeFields) may add a few identifier-shaped values
// under "fields"; nothing from free text ever.
//
// Writing a line never fails or delays a crossing: Record returns nothing, a
// failed write is counted (Failures) and logged at most once a minute.
//
// The package imports the standard library and internal/0/bridge only.
package crossinglog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/bridge"
)

const (
	// DefaultMaxBytes is the size at which the live file rotates.
	DefaultMaxBytes = 4 << 20
	// DefaultGenerations is how many rotated files are kept (.1 to .3).
	DefaultGenerations = 3
	// DefaultMaxAge is the age past which a log file is deleted at open.
	DefaultMaxAge = 14 * 24 * time.Hour

	maxIdentifier    = 128
	maxVerb          = 64
	failureLogPeriod = time.Minute
	unknown          = "unknown"
)

// Outcomes of a crossing.
const (
	OutcomeOK           = "ok"
	OutcomeRefused      = "refused"
	OutcomeError        = "error"
	OutcomeBusy         = "busy"
	OutcomeHelloTimeout = "hello-timeout"
)

// SafeFields is the per-verb allow-list of argument names whose values may be
// logged. Only the names session, branch and target can ever be logged (see
// permittedSafeNames), and only as an identifier-shaped string of at most 128
// characters matching [A-Za-z0-9._/-]+; any other value is dropped.
var SafeFields = map[string][]string{
	"cancel-restart":                  {"session", "target"},
	"session-health":                  {"session", "target"},
	"update-this-session-description": {"target"},
}

var permittedSafeNames = map[string]bool{"session": true, "branch": true, "target": true}

// Crossing is one crossing to record. Args are the raw arguments of the call;
// their values are reduced by Redact before anything is written.
type Crossing struct {
	Session  string
	Service  string
	Verb     string
	Outcome  string
	Duration time.Duration
	// ReqBytes and RespBytes are the sizes on the wire (0 when unknown).
	ReqBytes  int
	RespBytes int
	Args      json.RawMessage
}

// Entry is one line of the file.
type Entry struct {
	TS         string            `json:"ts"`
	Session    string            `json:"session"`
	Service    string            `json:"service"`
	Verb       string            `json:"verb"`
	Outcome    string            `json:"outcome"`
	DurationMS int64             `json:"duration_ms"`
	ReqBytes   int               `json:"req_bytes"`
	RespBytes  int               `json:"resp_bytes"`
	ArgNames   []string          `json:"arg_names"`
	ArgsSHA256 string            `json:"args_sha256"`
	Fields     map[string]string `json:"fields,omitempty"`
}

// Options configure Open. Dir and Principal are required for a log that
// writes anything.
type Options struct {
	// Dir is <state>/spinclass/bridge.
	Dir string
	// Principal names the file: <principal>.ndjson. Anything outside
	// [A-Za-z0-9._-] becomes "_".
	Principal string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// MaxBytes, Generations and MaxAge override the defaults when positive.
	MaxBytes    int64
	Generations int
	MaxAge      time.Duration
	// Logf logs a failure line; nil discards.
	Logf func(format string, args ...any)
	// SafeFields overrides the package SafeFields when non-nil.
	SafeFields map[string][]string
}

// Log is the crossing log of one principal. A nil *Log records nothing.
type Log struct {
	opts Options
	path string

	mu         sync.Mutex
	failures   atomic.Int64
	lastLogged time.Time

	// appendFn writes one line; tests replace it.
	appendFn func(path string, line []byte) error
}

// Open prepares the log and deletes expired files in its directory. It never
// fails: whatever goes wrong shows up as Failures on the first Record.
func Open(o Options) *Log {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.Generations <= 0 {
		o.Generations = DefaultGenerations
	}
	if o.MaxAge <= 0 {
		o.MaxAge = DefaultMaxAge
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.SafeFields == nil {
		o.SafeFields = SafeFields
	}
	l := &Log{opts: o, path: filepath.Join(o.Dir, fileSegment(o.Principal)+".ndjson")}
	l.appendFn = l.appendLine
	l.deleteExpired()
	return l
}

// Failures counts the writes that failed.
func (l *Log) Failures() int64 {
	if l == nil {
		return 0
	}
	return l.failures.Load()
}

// Record writes one line for c. It returns nothing: a failed write is counted
// and logged, never reported to the crossing.
func (l *Log) Record(c Crossing) {
	if l == nil {
		return
	}
	names, fields, sum := Redact(c.Verb, c.Args, l.opts.SafeFields)
	now := l.opts.Now()
	e := Entry{
		TS:         now.UTC().Format(time.RFC3339Nano),
		Session:    c.Session,
		Service:    serviceLabel(c.Service),
		Verb:       verbLabel(c.Verb),
		Outcome:    normalizeOutcome(c.Outcome),
		DurationMS: c.Duration.Milliseconds(),
		ReqBytes:   c.ReqBytes,
		RespBytes:  c.RespBytes,
		ArgNames:   names,
		ArgsSHA256: sum,
		Fields:     fields,
	}
	line, err := json.Marshal(e)
	if err == nil {
		l.mu.Lock()
		err = l.appendFn(l.path, append(line, '\n'))
		l.mu.Unlock()
	}
	if err != nil {
		l.failed(now, err)
	}
}

func (l *Log) failed(now time.Time, err error) {
	l.failures.Add(1)
	l.mu.Lock()
	due := l.lastLogged.IsZero() || now.Sub(l.lastLogged) >= failureLogPeriod
	if due {
		l.lastLogged = now
	}
	l.mu.Unlock()
	if due {
		l.opts.Logf("bridge crossing log: write failed (%d so far): %v", l.failures.Load(), err)
	}
}

// appendLine appends line to path, rotating first when it would pass the size
// limit. The directory is made 0700 and the file 0600.
func (l *Log) appendLine(path string, line []byte) error {
	if l.opts.Dir == "" {
		return errors.New("no log directory")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 && fi.Size()+int64(len(line)) > l.opts.MaxBytes {
		if err := l.rotate(path); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// rotate shifts path to path.1, path.1 to path.2 and so on, dropping the
// oldest generation.
func (l *Log) rotate(path string) error {
	last := path + "." + strconv.Itoa(l.opts.Generations)
	if err := os.Remove(last); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for i := l.opts.Generations - 1; i >= 1; i-- {
		from := path + "." + strconv.Itoa(i)
		if err := os.Rename(from, path+"."+strconv.Itoa(i+1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(path, path+".1")
}

// deleteExpired removes the log files (.ndjson and .ndjson.N, any principal)
// in the directory whose modification time is older than MaxAge.
func (l *Log) deleteExpired() {
	ents, err := os.ReadDir(l.opts.Dir)
	if err != nil {
		return
	}
	cutoff := l.opts.Now().Add(-l.opts.MaxAge)
	for _, ent := range ents {
		if !ent.Type().IsRegular() || !isLogFileName(ent.Name()) {
			continue
		}
		fi, err := ent.Info()
		if err != nil || !fi.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(l.opts.Dir, ent.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			l.failures.Add(1)
		}
	}
}

func isLogFileName(name string) bool {
	if strings.HasSuffix(name, ".ndjson") {
		return true
	}
	i := strings.LastIndex(name, ".ndjson.")
	if i < 0 {
		return false
	}
	gen := name[i+len(".ndjson."):]
	n, err := strconv.Atoi(gen)
	return err == nil && n > 0
}

// Redact reduces a call's arguments to what may be logged: the sorted
// argument names (never nil), the allow-listed identifier-shaped values for
// verb, and the first 16 hex of the sha256 of the canonical (key-sorted,
// compact) JSON of the arguments. Values reach the result only through the
// allow-list.
func Redact(verb string, args json.RawMessage, safe map[string][]string) (names []string, fields map[string]string, sha string) {
	names = []string{}
	raw := []byte(args)
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = []byte("{}")
	}
	canonical := raw
	var parsed any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&parsed); err == nil && !dec.More() {
		if b, err := json.Marshal(parsed); err == nil {
			canonical = b
		}
	}
	sum := sha256.Sum256(canonical)
	sha = hex.EncodeToString(sum[:])[:16]

	obj, ok := parsed.(map[string]any)
	if !ok {
		return names, nil, sha
	}
	for k := range obj {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range safe[verb] {
		if !permittedSafeNames[name] {
			continue
		}
		s, isString := obj[name].(string)
		if !isString || !identifierShaped(s) {
			continue
		}
		if fields == nil {
			fields = map[string]string{}
		}
		fields[name] = s
	}
	return names, fields, sha
}

func identifierShaped(s string) bool {
	if s == "" || len(s) > maxIdentifier {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !identByte(s[i]) {
			return false
		}
	}
	return true
}

func identByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '/' || c == '-'
}

// serviceLabel is the closed set of reserved services; anything else is
// "unknown" so no attacker text reaches the line.
func serviceLabel(s string) string {
	for _, r := range bridge.ReservedServices {
		if r == s {
			return s
		}
	}
	return unknown
}

// verbLabel keeps a verb only when it is identifier-shaped and short.
func verbLabel(v string) string {
	if len(v) > maxVerb || !identifierShaped(v) {
		return unknown
	}
	return v
}

// normalizeOutcome maps a listener outcome onto the log's closed set.
func normalizeOutcome(o string) string {
	switch o {
	case OutcomeOK, OutcomeRefused, OutcomeBusy, OutcomeHelloTimeout:
		return o
	case bridge.CodeUnknownService:
		return OutcomeRefused
	}
	return OutcomeError
}

// fileSegment makes a principal one safe file-name segment.
func fileSegment(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || (c == '.' && i > 0) {
			b.WriteByte(c)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return unknown
	}
	return b.String()
}
