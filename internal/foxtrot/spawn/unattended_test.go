package spawn

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
)

// unattendedEntry records the worker's argv (after the "--" provider-args
// separator) and the CLOWN_UNATTENDED it was exec'd with.
const unattendedEntry = `spawn-entry = ["sh", "-c", 'printf "%s\n" "$@" > "$PWD/argv.txt"; printf "%s" "${CLOWN_UNATTENDED-unset}" > "$PWD/marker.txt"; touch "$PWD/launched"', "sh", "--", "{prompt}"]
`

type unattendedLaunch struct {
	argv      []string
	marker    string
	pinnedEnv map[string]string
}

// launchUnattended launches an unconfined worker with the given repo-level
// [session-entry] extras and global sweatfile, returning what the worker saw.
func launchUnattended(t *testing.T, repoExtra, global string) unattendedLaunch {
	t.Helper()
	// The driver may itself be unattended: whatever it carries must never reach
	// an auto or unset child (clown would apply dontAsk settings under auto argv).
	t.Setenv(unattendedEnvVar, "1")
	home, repoPath := newWorkerFixture(t, "[session-entry]\n"+unattendedEntry+repoExtra)
	if global != "" {
		writeGlobalSweatfile(t, home, global)
	}
	const driverKey = "driver/unattended-test"
	stop, helloErr := helloAfterLaunch(t, repoPath, driverKey)
	res, err := Launch(home, repoPath, testDriver(driverKey), "brief", "", "", "", 15*time.Second)
	stop()
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if herr := <-helloErr; herr != nil {
		t.Fatalf("hello goroutine: %v", herr)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(res.WorktreePath, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	st, err := session.Read(repoPath, filepath.Base(res.WorktreePath))
	if err != nil {
		t.Fatal(err)
	}
	out := unattendedLaunch{
		argv:   strings.Split(strings.TrimSpace(read("argv.txt")), "\n"),
		marker: read("marker.txt"),
	}
	if st.Entry != nil {
		out.pinnedEnv = st.Entry.Env
	}
	return out
}

var (
	wantAutoArgv    = []string{"--", "--permission-mode", "auto", "brief"}
	wantDontAskArgv = []string{"--", "--permission-mode", "dontAsk", "brief"}
	trustedDontAsk  = "[session-entry]\nunattended-permission-mode = \"dontAsk\"\n"
	trustedAuto     = "[session-entry]\nunattended-permission-mode = \"auto\"\n"
)

func assertUnattended(t *testing.T, got unattendedLaunch, wantArgv []string, wantMarker bool) {
	t.Helper()
	if !slices.Equal(got.argv, wantArgv) {
		t.Errorf("argv = %q, want %q", got.argv, wantArgv)
	}
	wantEnv := "unset"
	if wantMarker {
		wantEnv = "1"
	}
	if got.marker != wantEnv {
		t.Errorf("exec env CLOWN_UNATTENDED = %q, want %q", got.marker, wantEnv)
	}
	pinned, has := got.pinnedEnv["CLOWN_UNATTENDED"]
	if wantMarker && pinned != "1" {
		t.Errorf("pinned Entry.Env CLOWN_UNATTENDED = %q, want 1 (clown reads it on resume)", pinned)
	}
	if !wantMarker && has {
		t.Errorf("pinned Entry.Env carries CLOWN_UNATTENDED=%q, want none", pinned)
	}
}

func TestUnconfinedSpawnUnsetKeepsTodaysBehaviour(t *testing.T) {
	assertUnattended(t, launchUnattended(t, "", ""), wantAutoArgv, false)
}

func TestUnconfinedSpawnUnsetHonoursDisableAutoMode(t *testing.T) {
	assertUnattended(t, launchUnattended(t, "disable-auto-mode = true\n", ""), []string{"--", "brief"}, false)
}

func TestUnconfinedSpawnDontAskSplicesModeAndMarker(t *testing.T) {
	assertUnattended(t, launchUnattended(t, "", trustedDontAsk), wantDontAskArgv, true)
}

func TestUnconfinedSpawnAutoKeepsAutoWithoutMarker(t *testing.T) {
	assertUnattended(t, launchUnattended(t, "", trustedAuto), wantAutoArgv, false)
}

func TestUnattendedKeyWinsOverDisableAutoMode(t *testing.T) {
	assertUnattended(t, launchUnattended(t, "disable-auto-mode = true\n", trustedDontAsk), wantDontAskArgv, true)
	assertUnattended(t, launchUnattended(t, "disable-auto-mode = true\n", trustedAuto), wantAutoArgv, false)
}

// A value the agent can write into its own repo's sweatfile must not loosen or
// tighten the child: it is ignored, today's behaviour holds.
func TestUnattendedKeyInRepoSweatfileIsIgnored(t *testing.T) {
	assertUnattended(t, launchUnattended(t, "unattended-permission-mode = \"dontAsk\"\n", ""), wantAutoArgv, false)
}

// A bad value refuses the spawn rather than guessing, and litters nothing.
func TestUnconfinedSpawnInvalidUnattendedModeIsRefused(t *testing.T) {
	home, repoPath := newWorkerFixture(t, "[session-entry]\n"+unattendedEntry)
	writeGlobalSweatfile(t, home, "[session-entry]\nunattended-permission-mode = \"bypassPermissions\"\n")
	_, err := LaunchDetachedWith(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "", ConfineDefault)
	if err == nil || !strings.Contains(err.Error(), "unattended-permission-mode") || !strings.Contains(err.Error(), "bypassPermissions") {
		t.Fatalf("err = %v, want a refusal naming unattended-permission-mode and the value", err)
	}
	noWorktree(t, repoPath)
}

// An unconfined spawn whose entry cannot take dontAsk is refused, like a
// confined one, before any worktree exists.
func TestUnconfinedDontAskRefusedWhenEntryCannotTakeIt(t *testing.T) {
	home, repoPath := newWorkerFixture(t, `[session-entry]
spawn-entry = ["sh", "-c", "true", "sh", "{prompt}"]
`)
	writeGlobalSweatfile(t, home, trustedDontAsk)
	_, err := LaunchDetachedWith(home, repoPath, testDriver("driver/test-session"), "brief", "", "", "", ConfineDefault)
	if err == nil || !strings.Contains(err.Error(), "separator") {
		t.Fatalf("err = %v, want a refusal about the missing \"--\" separator", err)
	}
	noWorktree(t, repoPath)
}

// applyUnattendedMode is the one decision both lanes share: a confined spawn is
// always dontAsk with the marker, whatever the key says.
func TestApplyUnattendedMode(t *testing.T) {
	entry := []string{"clown", "--", "--permission-mode", "auto", "brief"}
	dontAsk := []string{"clown", "--", "--permission-mode", "dontAsk", "brief"}
	cases := []struct {
		name       string
		confined   bool
		mode       string
		wantArgv   []string
		wantMarker bool
	}{
		{"confined, unset", true, "", dontAsk, true},
		{"confined, auto", true, "auto", dontAsk, true},
		{"confined, dontAsk", true, "dontAsk", dontAsk, true},
		{"unconfined, dontAsk", false, "dontAsk", dontAsk, true},
		{"unconfined, auto", false, "auto", entry, false},
		{"unconfined, unset", false, "", entry, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, marker, err := applyUnattendedMode(entry, c.confined, c.mode)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, c.wantArgv) || marker != c.wantMarker {
				t.Errorf("= (%q, %v), want (%q, %v)", got, marker, c.wantArgv, c.wantMarker)
			}
		})
	}
}

// A [session-entry].env value for the marker never reaches the worker either:
// only an applied dontAsk sets it.
func TestUserEnvCannotSetTheMarker(t *testing.T) {
	userEnv := "[session-entry.env]\nCLOWN_UNATTENDED = \"1\"\n"
	assertUnattended(t, launchUnattended(t, userEnv, ""), wantAutoArgv, false)
	assertUnattended(t, launchUnattended(t, userEnv, trustedAuto), wantAutoArgv, false)
	assertUnattended(t, launchUnattended(t, userEnv, trustedDontAsk), wantDontAskArgv, true)
}

// The exec env is built from os.Environ: an inherited marker is dropped there.
func TestWorkerEnvDropsInheritedMarker(t *testing.T) {
	t.Setenv(unattendedEnvVar, "1")
	for _, e := range workerEnvScoped(worktree.ResolvedPath{SessionKey: "w/k", AbsPath: t.TempDir()}, "d", nil, true, "") {
		if strings.HasPrefix(e, unattendedEnvVar+"=") {
			t.Errorf("inherited marker leaked into the worker env: %q", e)
		}
	}
}

func TestSetUnattendedMarker(t *testing.T) {
	in := map[string]string{"A": "b", "CLOWN_UNATTENDED": "0"}
	on := setUnattendedMarker(in, true)
	if on["CLOWN_UNATTENDED"] != "1" || on["A"] != "b" {
		t.Errorf("on: got %v", on)
	}
	off := setUnattendedMarker(in, false)
	if _, has := off["CLOWN_UNATTENDED"]; has || off["A"] != "b" {
		t.Errorf("off: got %v", off)
	}
	if in["CLOWN_UNATTENDED"] != "0" {
		t.Error("input map was mutated")
	}
	if got := setUnattendedMarker(nil, true); got["CLOWN_UNATTENDED"] != "1" {
		t.Errorf("nil input: got %v", got)
	}
}
