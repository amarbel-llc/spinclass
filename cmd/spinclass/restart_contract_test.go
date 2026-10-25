package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// fakeClownRestart mirrors the argument contract of clown's `restart` verb as
// clown/pure-teak stated it from cmd/clown/restart.go (2026-10-25, #419):
// `clown restart --session KEY [--when idle] [--reason TEXT]`. Each flag
// consumes the next token as its value; a missing value, an unknown argument, a
// missing --session, or any --when other than idle exits 2. On success it
// records the session it was given and exits 0. Update it when clown's verb
// changes (e.g. when it accepts --when now).
const fakeClownRestart = `#!/bin/sh
[ "$1" = restart ] || { echo "clown: unknown verb $1" >&2; exit 2; }
shift
session=
while [ $# -gt 0 ]; do
  case "$1" in
    --session|--when|--reason)
      [ $# -ge 2 ] || { echo "clown restart: $1 needs a value" >&2; exit 2; }
      if [ "$1" = --session ]; then session=$2; fi
      if [ "$1" = --when ] && [ "$2" != idle ]; then
        echo "clown restart: --when \"$2\" is not supported; only idle" >&2; exit 2
      fi
      shift 2 ;;
    *) echo "clown restart: unknown argument \"$1\"" >&2; exit 2 ;;
  esac
done
[ -n "$session" ] || { echo "clown restart: --session is required" >&2; exit 2; }
printf '%s\n' "$session" >>"$FAKE_CLOWN_LOG"
`

// installFakeClown puts fakeClownRestart on PATH as clown, with the real
// hand-off exec (no stub), and returns a reader of the sessions it accepted.
func installFakeClown(t *testing.T) func() []string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "clown.log")
	t.Setenv("FAKE_CLOWN_LOG", log)
	testgit.MustPutOnPath(t, "clown", fakeClownRestart)
	return func() []string {
		data, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Fields(string(data))
	}
}

// The documented hand-off template, expanded for a self-restart, is an argv
// clown accepts.
func TestRestartHandoffMeetsClownContractForSelf(t *testing.T) {
	accepted := installFakeClown(t)
	repoPath, _ := restartSelfFixture(t, true)

	if text, isErr := callRestartThis(t, `{"reason":"need new tools"}`); isErr {
		t.Fatalf("clown refused the self-restart hand-off: %q", text)
	}
	if got := accepted(); len(got) != 1 || got[0] != selfPrincipal {
		t.Errorf("clown accepted sessions %q, want [%s]", got, selfPrincipal)
	}
	if req := readKid(t, repoPath).RestartRequest; req == nil || req.When != "idle" {
		t.Errorf("restart request = %+v, want one queued for idle", req)
	}
}

func TestRestartHandoffMeetsClownContractForChild(t *testing.T) {
	accepted := installFakeClown(t)
	restartChildFixture(t, "holder", session.State{
		Holders: []string{"holder"}, HandleRights: map[string]string{"holder": "observe,restart"},
	})

	if text, isErr := callRestartChild(t, `{"child":"worker/kid","reason":"pick up new config"}`); isErr {
		t.Fatalf("clown refused the child-restart hand-off: %q", text)
	}
	if got := accepted(); len(got) != 1 || got[0] != childPrincipal {
		t.Errorf("clown accepted sessions %q, want [%s]", got, childPrincipal)
	}
}

// A forced restart never reaches clown while clown accepts only idle.
func TestForcedRestartNeverReachesClown(t *testing.T) {
	accepted := installFakeClown(t)
	restartSelfFixture(t, true)

	if text, isErr := callRestartThis(t, `{"force":true}`); !isErr {
		t.Fatalf("a forced restart must be refused, got %q", text)
	}
	if got := accepted(); len(got) != 0 {
		t.Errorf("clown was handed a forced restart: %q", got)
	}
}

// The fake enforces the contract: the old fixture's --key flag is refused, as
// real clown refuses it, so a passing contract test means something.
func TestFakeClownRefusesAnUnknownFlag(t *testing.T) {
	installFakeClown(t)
	restartSelfFixture(t, false)
	writeGlobalSweatfile(t, os.Getenv("HOME"), "[session-entry]\n"+
		`restart = ["clown", "restart", "--session", "{principal}", "--key", "{session-key}", "--when", "{when}"]`+"\n")

	text, isErr := callRestartThis(t, `{}`)
	if !isErr || !strings.Contains(text, `unknown argument "--key"`) {
		t.Fatalf("want the hand-off to fail on --key, got isErr=%v %q", isErr, text)
	}
}
