package spawnhandshake

// R3-L3: the hello is a file the worker writes, and its posh_session_id goes into
// the operator's spawn-window argv. A value that is not a plain id reads as "the
// worker reported none"; the spawn itself still succeeds.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAHelloWithAHostilePoshSessionIDReadsAsNoneReported(t *testing.T) {
	good := "b3bfa155-8881-4177-92f7-33862a309d2c"
	cases := map[string]string{
		"a newline":                   "abc\nrm -rf",
		"a space":                     "abc def",
		"a semicolon":                 "abc;touch x",
		"a command substitution":      "$(touch x)",
		"a backtick":                  "`touch x`",
		"a leading dash":              "--version",
		"a lone dash":                 "-",
		"an over-long id":             strings.Repeat("a", 129),
		"a NUL":                       "abc\x00def",
		"a quote":                     `a"b`,
		"a slash":                     "../../etc/passwd",
		"a non-ASCII letter":          "abc\xc3\xa9",
		"a right-to-left override id": "abc\xe2\x80\xaedef",
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			own := filepath.Join(t.TempDir(), "session-x")
			t.Setenv(DirEnv, own)
			if err := SendHello("repo/feat", "driver", id); err != nil {
				t.Fatal(err)
			}
			got, err := WaitForHelloIn(own, "driver", "repo/feat", time.Now().Add(-time.Minute), time.Second)
			if err != nil {
				t.Fatalf("the hello was refused instead of read as none reported: %v", err)
			}
			if got != "" {
				t.Errorf("a hostile posh session id %q reached the driver as %q", id, got)
			}
		})
	}
	t.Run("a plain id is passed through", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		own := filepath.Join(t.TempDir(), "session-x")
		t.Setenv(DirEnv, own)
		if err := SendHello("repo/feat", "driver", good); err != nil {
			t.Fatal(err)
		}
		got, err := WaitForHelloIn(own, "driver", "repo/feat", time.Now().Add(-time.Minute), time.Second)
		if err != nil || got != good {
			t.Errorf("got (%q, %v), want %q", got, err, good)
		}
	})
}

func TestValidPoshSessionID(t *testing.T) {
	for _, ok := range []string{"a", "A9", "b3bfa155-8881-4177-92f7-33862a309d2c", "posh_1.2-x", strings.Repeat("a", 128)} {
		if !ValidPoshSessionID(ok) {
			t.Errorf("%q was refused", ok)
		}
	}
	for _, bad := range []string{"", "-a", "a b", "a\n", "a;b", "$(x)", strings.Repeat("a", 129)} {
		if ValidPoshSessionID(bad) {
			t.Errorf("%q was accepted", bad)
		}
	}
}
