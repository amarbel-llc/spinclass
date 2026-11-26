package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// A record a session can write reaches the operator's terminal, the journal and
// the lifecycle log through these commands. None of them may print what the
// record says as itself: an escape sequence, a newline, a NUL or a bidirectional
// override in the record's `session` (or a fingerprint, or a key line) is shown
// escaped, with "(record altered)", and can neither end a line nor start one.

const safeRLO = "\xe2\x80\xae" // U+202E as bytes: no invisible character in this source

var alteredSessions = map[string]string{
	"escape sequence": "repo/x\x1b[31mRED\x1b[0m",
	"newline":         "repo/x\nFORGED LINE: stopped everything",
	"nul":             "repo/x\x00y",
	"override":        "repo/x" + safeRLO + "gnp.exe",
	"over long":       strings.Repeat("a", 300),
}

// rawControl returns the first control or format character in s other than the
// line ends the command itself writes.
func rawControl(s string) (rune, bool) {
	for _, r := range s {
		if r == '\n' {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return r, true
		}
	}
	return 0, false
}

// alterRecord rewrites a pair's record so its session is evil and it is old
// enough to count as an orphan (no live session owns that string).
func alterRecord(t *testing.T, l *keyholder.Launcher, realSession, evil string) {
	t.Helper()
	path := filepath.Join(l.Base, keyholder.Hash(realSession), "record.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m["session"] = evil
	m["minted_at"] = "2020-01-01T00:00:00Z"
	b, _ = json.Marshal(m)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAnAlteredRecordSessionNeverReachesTheTerminalAsItself(t *testing.T) {
	for name, evil := range alteredSessions {
		t.Run(name, func(t *testing.T) {
			fx := newStopFixture(t, "s/a")
			alterRecord(t, fx.l, "s/a", evil)
			stdout, _ := stubOperatorTerminal(t, false)
			if err := runKeyholderStop(context.Background(), keyholderStopParams{All: true, DryRun: true}); err != nil {
				t.Fatal(err)
			}
			out := stdout.String()
			if r, bad := rawControl(out); bad {
				t.Errorf("the listing carries a raw %U:\n%q", r, out)
			}
			if !strings.Contains(out, "record altered") {
				t.Errorf("the listing does not say the record was altered:\n%s", out)
			}
			// A newline in the record must not start a line of its own.
			for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
				if strings.HasPrefix(line, "FORGED") {
					t.Errorf("the record forged a line of output: %q", line)
				}
			}
		})
	}
}

func TestAnAlteredRecordSessionNeverReachesTheJournalOrTheLogAsItself(t *testing.T) {
	for name, evil := range alteredSessions {
		t.Run(name, func(t *testing.T) {
			fx := newStopFixture(t, "s/a")
			var lines []string
			fx.l.Log = func(format string, a ...any) { lines = append(lines, fmt.Sprintf(format, a...)) }
			alterRecord(t, fx.l, "s/a", evil)
			stubOperatorTerminal(t, true)
			if err := runKeyholderStop(context.Background(), keyholderStopParams{All: true}); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(fx.journal)
			if err != nil {
				t.Fatal(err)
			}
			var rec keyholderJournalRecord
			if err := json.Unmarshal([]byte(strings.TrimSpace(string(b))), &rec); err != nil {
				t.Fatalf("the journal line is not one JSON object: %q: %v", b, err)
			}
			if r, bad := rawControl(rec.Session); bad {
				t.Errorf("the journal's session carries a raw %U: %q", r, rec.Session)
			}
			if !strings.Contains(rec.Session, "record altered") {
				t.Errorf("the journal's session %q does not say the record was altered", rec.Session)
			}
			var stoppedLine string
			for _, ln := range lines {
				if r, bad := rawControl(ln); bad || strings.Contains(ln, "\n") {
					t.Errorf("the lifecycle log carries a raw %U or a line break: %q", r, ln)
				}
				if strings.Contains(ln, "stopped the pair") {
					stoppedLine = ln
				}
			}
			if !strings.Contains(stoppedLine, "record altered") {
				t.Errorf("the lifecycle log's stop line %q does not say the record was altered (lines: %q)", stoppedLine, lines)
			}
		})
	}
}

// `sc keyholder-stop SESSION` echoes the operator's own argument when no pair
// matches it: that too goes through the same guard.
func TestTheStopCommandEchoesTheNamedSessionOnlyInItsSafeForm(t *testing.T) {
	newStopFixture(t, "s/a")
	stdout, _ := stubOperatorTerminal(t, true)
	if err := runKeyholderStop(context.Background(), keyholderStopParams{Session: "s/none\x1b[2J\nFORGED"}); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if r, bad := rawControl(out); bad {
		t.Errorf("the output carries a raw %U: %q", r, out)
	}
	if !strings.Contains(out, "record altered") {
		t.Errorf("output %q does not say the name was altered", out)
	}
}

// `sc session-key` prints the fingerprint the pair's record holds (a file a
// session can write) in its refusal; and the installed certificate's own words.
func TestSessionKeyNeverPrintsARecordFingerprintOrACertificateProblemAsItself(t *testing.T) {
	evil := "SHA256:\x1b[31mRECORDED\nFORGED" + safeRLO
	e := useSessionKeySeams(t)
	e.makeSessionDir(t, "a")
	e.pair.Fingerprint = evil // the record's claim differs from the listed key
	err := runSessionKey(context.Background(), json.RawMessage(`{"session":"a"}`))
	if err == nil {
		t.Fatal("want the changed-key refusal")
	}
	if r, bad := rawControl(err.Error()); bad {
		t.Errorf("the refusal carries a raw %U: %q", r, err)
	}
	if !strings.Contains(err.Error(), "record altered") {
		t.Errorf("the refusal %q does not say the record was altered", err)
	}

	// An installed certificate that certifies a key whose printed fingerprint is
	// hostile text: the warning on stderr.
	e2 := useSessionKeySeams(t)
	e2.listing = "        Type: ssh-ed25519-cert-v01@openssh.com user certificate\n        Public key: ED25519-CERT SHA256:\x1b[31mOTHER" + safeRLO + "\n        Valid: from 2026-11-25T20:00:00 to 2026-11-26T20:00:00\n"
	e2.installCert(t, "a", khCertLine)
	if err := runSessionKey(context.Background(), json.RawMessage(`{"session":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if r, bad := rawControl(e2.errOut.String()); bad {
		t.Errorf("stderr carries a raw %U: %q", r, e2.errOut.String())
	}
}

// `sc keyholder-pair status` prints the record's public key and socket path.
func TestKeyholderPairStatusNeverPrintsARecordFieldAsItself(t *testing.T) {
	f := newKhFake(t)
	khUseFake(t, f)
	var out bytes.Buffer
	prevOut, prevRecipe := keyholderPairOut, keyholderPairRecipe
	t.Cleanup(func() { keyholderPairOut, keyholderPairRecipe = prevOut, prevRecipe })
	keyholderPairOut = func() io.Writer { return &out }
	keyholderPairRecipe = func() bool { return true }
	run := func(action string) {
		t.Helper()
		out.Reset()
		if err := runKeyholderPair(context.Background(), json.RawMessage(`{"action":"`+action+`","session":"repo/live"}`)); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	run("start")
	l, _ := keyholderLauncherFn(keyholder.Tools{})
	path := filepath.Join(keyholder.SessionDir(l.Base, "repo/live"), "record.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m["public_key"] = "ssh-ed25519 AAAAC3NzaFAKEAAA holder:reader\x1b[31m\nFORGED line" + safeRLO
	b, _ = json.Marshal(m)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	run("status")
	got := out.String()
	if r, bad := rawControl(got); bad {
		t.Errorf("status carries a raw %U: %q", r, got)
	}
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if strings.HasPrefix(line, "FORGED") {
			t.Errorf("the record forged a line of output: %q", line)
		}
	}
}
