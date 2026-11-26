package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/foxtrot/spawn"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// A spawn through the wiring this binary installs refuses on a precondition
// before it creates anything. Without the preflight hook the launch hook would
// still refuse, but only after the worktree was made (and rolled back).
func TestASpawnThroughTheRealWiringRefusesBeforeCreatingAWorktree(t *testing.T) {
	testgit.RequireGit(t)
	home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n\n[session-entry]\nspawn-entry = [\"true\", \"--\", \"{prompt}\"]\n")
	testgit.MustInit(t, repo)
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Chdir(repo)
	f := newKhFake(t)
	khUseFake(t, f)
	inner := keyholderLauncherFn
	keyholderLauncherFn = func(tools keyholder.Tools) (*keyholder.Launcher, error) {
		l, err := inner(tools)
		if l != nil {
			l.Run = &unreadyRunner{f}
		}
		return l, err
	}
	driver := spawn.Driver{SessionKey: "driver/wiring", Principal: "driver/wiring"}
	_, err := spawn.LaunchDetachedWith(home, repo, driver, "brief", "", "", "", spawn.ConfineDefault)
	if err == nil || !strings.Contains(err.Error(), "user-manager: nobody answers") || strings.Contains(err.Error(), "nothing is left") {
		t.Fatalf("err = %v, want a refusal before anything was created", err)
	}
	if _, serr := os.Stat(filepath.Join(repo, ".worktrees")); serr == nil {
		t.Error("a worktree directory was made before the refusal")
	}
}

// assertRefusalShape is the one shape every "cannot start" refusal has.
func assertRefusalShape(t *testing.T, err error, wants ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("no refusal")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "the key holder is on but cannot start:") {
		t.Errorf("not the cannot-start form: %s", msg)
	}
	for _, want := range append(wants, "\n    next: ", "sc keyholder-switch off") {
		if !strings.Contains(msg, want) {
			t.Errorf("lacks %q: %s", want, msg)
		}
	}
}

func TestTheSwitchFileRefusalReadsLikeTheOtherRefusals(t *testing.T) {
	home, repo := khHome(t, "")
	pw := t.TempDir()
	prev := keyholderPasswdHomeFn
	t.Cleanup(func() { keyholderPasswdHomeFn = prev })
	keyholderPasswdHomeFn = func() (string, error) { return pw, nil }
	path := filepath.Join(pw, ".config", "spinclass", "keyholder-switch")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("on\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := resolveKeyholderSwitch(home, repo)
	assertRefusalShape(t, err, path, "[keyholder] fibby and piggy", "sc keyholder-switch default")
}

func TestAnUnsetProgramRefusesInTheSameForm(t *testing.T) {
	_, err := realKeyholderLauncher(keyholder.Tools{})
	assertRefusalShape(t, err, "[keyholder] fibby is not set", "[keyholder] piggy is not set")
}

// With the mode off the self-test must not claim a key holder is on, nor offer
// to switch it off.
func TestTheSelftestsSettingsErrorDoesNotSayTheKeyHolderIsOn(t *testing.T) {
	home, repo := khHome(t, "")
	t.Setenv("HOME", home)
	t.Chdir(repo)
	var out strings.Builder
	prev := keyholderSelftestOut
	keyholderSelftestOut = func() io.Writer { return &out }
	t.Cleanup(func() { keyholderSelftestOut = prev })
	if err := runKeyholderSelftest(context.Background(), selftestParams{}); err == nil {
		t.Fatal("the self-test passed with no programs set")
	}
	text := out.String()
	if !strings.HasPrefix(lastLine(text), "keyholder-selftest: FAILED at settings: ") || !strings.Contains(text, "[keyholder] fibby is not set") {
		t.Fatalf("output:\n%s", text)
	}
	for _, bad := range []string{"the key holder is on", "sc keyholder-switch off"} {
		if strings.Contains(text, bad) {
			t.Errorf("output says %q:\n%s", bad, text)
		}
	}
}

// neverStopping is a runner whose units stay up whatever is asked of them.
type neverStopping struct{ *selftestFake }

func (neverStopping) Stop(context.Context, string) error { return nil }

func TestAStartThatLeavesSomethingBehindNamesKeyholderStop(t *testing.T) {
	f := newSelftestFake(t)
	f.mintErr = io.ErrUnexpectedEOF
	setupSelftest(t, f, neverStopping{f})
	var res selftestResult
	res.Session = "selftest/stuck"
	selftest(context.Background(), selftestParams{}, &res, func(string, ...any) {})
	if res.Step != "start" || len(res.LeftOver) == 0 {
		t.Fatalf("result %+v", res)
	}
	if !strings.Contains(res.Next, "sc keyholder-stop selftest/stuck") {
		t.Errorf("next: %s", res.Next)
	}
}

// If writing the line after the start ends the command, the pair is already
// down to be stopped.
func TestThePairIsStoppedWhenTheFirstLineAfterStartFails(t *testing.T) {
	f := newSelftestFake(t)
	setupSelftest(t, f, f)
	res := selftestResult{Session: "selftest/pipe"}
	func() {
		defer func() { _ = recover() }()
		selftest(context.Background(), selftestParams{}, &res, func(format string, _ ...any) {
			if strings.HasPrefix(format, "ok start") {
				panic("the reader went away")
			}
		})
	}()
	if len(f.started) != 2 || f.stillActive() != 0 {
		t.Errorf("started %v, %d unit(s) still active", f.started, f.stillActive())
	}
}

// A signal the context does not catch would kill this test binary.
func TestTheSelftestCatchesAHangup(t *testing.T) {
	ctx, stop := selftestContext(context.Background())
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Error("SIGHUP did not cancel the self-test")
	}
}
