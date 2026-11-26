package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/0/sshsig/sshsigtest"
)

// selftestFake is khFakeRunner with programs that behave: the mint and
// `ssh-add -L` give one real ed25519 key, and `ssh-keygen -Y sign` signs with
// it, so the command's own verification runs for real.
type selftestFake struct {
	*khFakeRunner
	pub       string
	priv      ed25519.PrivateKey
	listed    string // what ssh-add -L prints; "" = the minted key
	badSign   bool
	commitErr error
	onSign    func()
	activeErr error
}

func newSelftestFake(t *testing.T) *selftestFake {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshsigtest.AuthorizedKey(pub)))) + " holder:test"
	return &selftestFake{khFakeRunner: newKhFake(t), pub: line, priv: priv}
}

func (f *selftestFake) Output(ctx context.Context, argv, env []string) ([]byte, error) {
	switch {
	case len(argv) >= 3 && argv[1] == "holder" && argv[2] == "mint":
		if f.mintErr != nil {
			return nil, f.mintErr
		}
		return []byte(f.pub + "\n"), nil
	case argv[0] == "/pin/ssh-add":
		if f.listed != "" {
			return []byte(f.listed + "\n"), nil
		}
		return []byte(f.pub + "\n"), nil
	case argv[0] == "/pin/ssh-keygen":
		if f.onSign != nil {
			f.onSign()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data := argv[len(argv)-1]
		msg, err := os.ReadFile(data)
		if err != nil {
			return nil, err
		}
		if f.badSign {
			msg = append(msg, 'x')
		}
		return nil, os.WriteFile(data+".sig", sshsigtest.Sign(f.priv, selftestNamespace, msg), 0o600)
	}
	return nil, nil
}

// setupSelftest points the self-test at run, f's programs standing for the pins.
func setupSelftest(t *testing.T, f *selftestFake, run keyholder.Runner) {
	t.Helper()
	home, repo := khHome(t, "")
	t.Setenv("HOME", home)
	t.Chdir(repo)
	khUseFake(t, f.khFakeRunner)
	inner := keyholderLauncherFn
	keyholderLauncherFn = func(tools keyholder.Tools) (*keyholder.Launcher, error) {
		l, err := inner(tools)
		if l != nil {
			l.Run = run
		}
		return l, err
	}
	t.Cleanup(keyholder.OverrideSSHPins("/pin/ssh-add", "/pin/ssh-keygen"))
	prev := selftestSignedCommit
	t.Cleanup(func() { selftestSignedCommit = prev })
	selftestSignedCommit = func(context.Context, *keyholder.Launcher, keyholder.Pair) error { return f.commitErr }
}

// runSelftest runs the command against f and returns its stdout and error.
func runSelftest(t *testing.T, ctx context.Context, f *selftestFake, p selftestParams) (string, error) {
	t.Helper()
	setupSelftest(t, f, f)
	var out bytes.Buffer
	prev := keyholderSelftestOut
	keyholderSelftestOut = func() io.Writer { return &out }
	t.Cleanup(func() { keyholderSelftestOut = prev })
	err := runKeyholderSelftest(ctx, p)
	return out.String(), err
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

func (f *selftestFake) stillActive() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, on := range f.active {
		if on {
			n++
		}
	}
	return n
}

func TestKeyholderSelftestStartsSignsStopsAndSaysOK(t *testing.T) {
	f := newSelftestFake(t)
	out, err := runSelftest(t, context.Background(), f, selftestParams{})
	if err != nil || lastLine(out) != "keyholder-selftest: OK (one key listed, one signature verified, nothing left)" {
		t.Fatalf("err %v, output:\n%s", err, out)
	}
	if len(f.started) != 2 || f.stillActive() != 0 {
		t.Errorf("started %v, %d unit(s) still active", f.started, f.stillActive())
	}
}

func TestKeyholderSelftestNamesTheFailedStepAndStopsThePair(t *testing.T) {
	cases := []struct {
		step string
		set  func(*selftestFake)
	}{
		{"start", func(f *selftestFake) { f.mintErr = errors.New("no card") }},
		{"list-key", func(f *selftestFake) { f.listed = "ssh-ed25519 AAAAC3NzaOTHERKEY other" }},
		{"sign", func(f *selftestFake) { f.badSign = true }},
		{"commit", func(f *selftestFake) { f.commitErr = errors.New("git commit: no signature") }},
	}
	for _, c := range cases {
		t.Run(c.step, func(t *testing.T) {
			f := newSelftestFake(t)
			c.set(f)
			out, err := runSelftest(t, context.Background(), f, selftestParams{})
			if err == nil || !strings.HasPrefix(lastLine(out), "keyholder-selftest: FAILED at "+c.step+": ") {
				t.Fatalf("err %v, output:\n%s", err, out)
			}
			if !strings.Contains(out, "keyholder-selftest: next: ") {
				t.Errorf("no next line:\n%s", out)
			}
			if f.stillActive() != 0 {
				t.Errorf("%d unit(s) still active after a failed self-test", f.stillActive())
			}
		})
	}
}

func TestKeyholderSelftestFailsWhenAUnitWillNotStop(t *testing.T) {
	f := newSelftestFake(t)
	f.stuck = map[string]bool{}
	f.onSign = func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		for u := range f.active {
			f.stuck[u] = true
		}
	}
	out, err := runSelftest(t, context.Background(), f, selftestParams{})
	if err == nil || !strings.HasPrefix(lastLine(out), "keyholder-selftest: FAILED at stop: ") {
		t.Fatalf("err %v, output:\n%s", err, out)
	}
	for _, want := range []string{"left over: ", "is still running", "sc keyholder-stop selftest/"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// Ctrl-C cancels the context; the pair is stopped all the same.
func TestKeyholderSelftestStopsThePairWhenInterrupted(t *testing.T) {
	f := newSelftestFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.onSign = cancel
	out, err := runSelftest(t, ctx, f, selftestParams{})
	if err == nil || f.stillActive() != 0 {
		t.Fatalf("err %v, %d unit(s) still active:\n%s", err, f.stillActive(), out)
	}
}

func TestKeyholderSelftestJSONIsOneObjectOnStdout(t *testing.T) {
	f := newSelftestFake(t)
	out, err := runSelftest(t, context.Background(), f, selftestParams{JSON: true})
	var res selftestResult
	if err != nil || strings.Count(out, "\n") != 1 || json.Unmarshal([]byte(out), &res) != nil {
		t.Fatalf("err %v, output:\n%s", err, out)
	}
	if !res.OK || res.Step != "done" || !strings.HasPrefix(res.Session, "selftest/") || res.AgentUnit == "" || res.FibbyUnit == "" || res.AgentSock == "" {
		t.Errorf("result %+v", res)
	}
}

// A worker launch asks the same preconditions before anything starts, with the
// switch on; with it off it asks nothing.
func TestKeyholderLaunchRefusesOnAPreconditionBeforeStartingAnything(t *testing.T) {
	home, repo := khHome(t, "[keyholder]\nmode = \"on\"\n")
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
	for name, got := range map[string]error{
		"spawn preflight": keyholderPreflight(home, repo, "repo/w"),
		"launch": func() error {
			_, err := keyholderSessionEnv(home, repo, "repo/w", io.Discard)
			return err
		}(),
	} {
		if got == nil || !strings.Contains(got.Error(), "user-manager: nobody answers") || !strings.Contains(got.Error(), "next: wake it") {
			t.Errorf("%s: %v", name, got)
		}
	}
	if len(f.started) != 0 {
		t.Errorf("units started despite a failed precondition: %v", f.started)
	}
	offHome, offRepo := khHome(t, "")
	if err := keyholderPreflight(offHome, offRepo, "repo/w"); err != nil {
		t.Errorf("switch off: %v", err)
	}
}

type unreadyRunner struct{ *khFakeRunner }

func (unreadyRunner) Preflight(context.Context) []keyholder.Problem {
	return []keyholder.Problem{{Name: "user-manager", What: "nobody answers", Next: "wake it"}}
}
