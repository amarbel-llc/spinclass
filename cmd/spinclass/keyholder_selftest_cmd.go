package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/0/sshsig"
)

// `sc keyholder-selftest` starts one real pair for a throwaway key, uses it,
// stops it and checks that nothing is left. It runs the same preconditions and
// the same start and stop a session launch does; it needs no terminal, no
// session and no [keyholder] mode.

const selftestNamespace = "spinclass-keyholder-selftest"

// keyholderSelftestOut is the command's output stream; tests replace it.
var keyholderSelftestOut = func() io.Writer { return os.Stdout }

// selftestResult is the --json document, and what the text form is made from.
type selftestResult struct {
	OK        bool     `json:"ok"`
	Step      string   `json:"step"` // the failed step, or "done"
	Error     string   `json:"error,omitempty"`
	Next      string   `json:"next,omitempty"`
	Session   string   `json:"session"`
	AgentSock string   `json:"agent_sock,omitempty"`
	AgentUnit string   `json:"agent_unit,omitempty"`
	FibbyUnit string   `json:"fibby_unit,omitempty"`
	LeftOver  []string `json:"left_over,omitempty"`
}

type selftestParams struct {
	Fibby string `json:"fibby"`
	Piggy string `json:"piggy"`
	JSON  bool   `json:"json"`
}

func registerKeyholderSelftestCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "keyholder-selftest",
		Title: "Start, use and stop one throwaway key holder",
		Description: command.Description{
			Short: "Prove a key holder can start on this host: start one for a throwaway key, sign with it, stop it, check nothing is left. CLI-only; no terminal needed.",
			Long: "Checks what a pair needs (the same checks `sc validate` and a spawn make), starts a real pair for a throwaway session name, checks the holder lists exactly its one key, makes one signature and verifies it, makes one signed git commit with the settings a worker gets and verifies it, stops the pair and checks that no unit and no directory is left. " +
				"It uses [keyholder] fibby and piggy as the trusted sweatfiles above the current directory set them, whether or not the mode is on; --fibby and --piggy (both) name other builds. It touches no session and does not read or change the switch beyond that. " +
				"Exit 0 and the last stdout line `keyholder-selftest: OK ...`, or exit 1 with `keyholder-selftest: FAILED at <step>: ...` as the last line and a `next:` line above it saying what to do. With --json, stdout is one JSON object {ok, step, error, next, session, agent_sock, agent_unit, fibby_unit, left_over}. Ctrl-C stops the pair before it exits.",
		},
		Params: []command.Param{
			{Name: "fibby", Type: command.String, Description: "Absolute path of a fibby build to test (with --piggy; default: [keyholder] fibby)"},
			{Name: "piggy", Type: command.String, Description: "Absolute path of a piggy build to test (with --fibby; default: [keyholder] piggy)"},
			{Name: "json", Type: command.Bool, Description: "Print one JSON object instead of the lines"},
		},
		Examples: []command.Example{
			{Description: "Check this host's key holder end to end", Command: "sc keyholder-selftest"},
		},
		RunCLI: func(ctx context.Context, args json.RawMessage) error {
			var p selftestParams
			if err := json.Unmarshal(args, &p); err != nil {
				return fmt.Errorf("keyholder-selftest: invalid arguments: %w", err)
			}
			ctx, stop := selftestContext(ctx)
			defer stop()
			return runKeyholderSelftest(ctx, p)
		},
	})
}

// selftestContext is cancelled, instead of the process dying with its pair up,
// on every way a person or a pipe ends the command: Ctrl-C, a kill, a closed
// terminal or dropped ssh (SIGHUP), and a reader that went away (`| head -1`:
// with SIGPIPE caught the write only fails).
func selftestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGPIPE)
}

// selftestLauncher builds the launcher under test: the named builds, else the
// ones a launch from here would run.
func selftestLauncher(p selftestParams) (*keyholder.Launcher, error) {
	if p.Fibby != "" || p.Piggy != "" {
		return launcherForTools(p.Fibby, p.Piggy)
	}
	home, repo, err := keyholderCallerContext()
	if err != nil {
		return nil, err
	}
	sw, err := resolveKeyholderSwitch(home, repo)
	if err != nil {
		return nil, err
	}
	l, err := keyholderLauncherFn(keyholder.Tools{Fibby: sw.Fibby, Piggy: sw.Piggy})
	var te *keyholderToolsError
	if errors.As(err, &te) {
		// The self-test runs with the mode off too: say what is wrong with the
		// settings, not that a key holder is on.
		whats := make([]string, len(te.problems))
		for i, pr := range te.problems {
			whats[i] = pr.What
		}
		return nil, errors.New(strings.Join(whats, "; "))
	}
	return l, err
}

func runKeyholderSelftest(ctx context.Context, p selftestParams) error {
	out := keyholderSelftestOut()
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Errorf("keyholder-selftest: %w", err)
	}
	res := selftestResult{Session: fmt.Sprintf("selftest/%x", raw)}
	say := func(format string, args ...any) {
		if !p.JSON {
			fmt.Fprintf(out, "keyholder-selftest: "+format+"\n", args...)
		}
	}
	selftest(ctx, p, &res, say)
	if p.JSON {
		if err := json.NewEncoder(out).Encode(res); err != nil {
			return err
		}
	} else if res.OK {
		say("OK (one key listed, one signature verified, nothing left)")
	} else {
		for _, l := range res.LeftOver {
			say("left over: %s", l)
		}
		say("next: %s", res.Next)
		say("FAILED at %s: %s", res.Step, keyholder.SafeLine(res.Error))
	}
	if !res.OK {
		return errors.New("keyholder-selftest failed")
	}
	return nil
}

// selftest runs the steps into res, printing each one that passed. Whatever
// happens after the pair started, it is stopped and the leftovers looked for.
func selftest(ctx context.Context, p selftestParams, res *selftestResult, say func(string, ...any)) {
	fail := func(step string, err error, next string) {
		res.Step, res.Error, res.Next = step, strings.Join(strings.Fields(err.Error()), " "), next
	}
	journal := "read what the units said with `journalctl --user --since -5min | grep spinclass-keyholder`, check [keyholder] fibby and piggy, then run `sc keyholder-selftest` again"

	l, err := selftestLauncher(p)
	if err != nil {
		fail("settings", err, "name both programs with --fibby and --piggy, or set [keyholder] fibby and piggy in a sweatfile above the repo")
		return
	}
	if problems := l.Preflight(ctx); len(problems) > 0 {
		for _, pr := range problems {
			say("problem: %s", pr.String())
		}
		fail("preconditions", fmt.Errorf("%s: %s", problems[0].Name, problems[0].What), problems[0].Next)
		return
	}
	say("ok preconditions")

	cleanup := fmt.Sprintf("run `sc keyholder-stop %s` at a terminal", res.Session)
	pair, err := l.Start(ctx, res.Session)
	if err != nil {
		// Start tears down what it made; look anyway.
		fail("start", err, journal)
		res.LeftOver = selftestLeftovers(context.WithoutCancel(ctx), l, res.Session)
		if len(res.LeftOver) > 0 {
			res.Next += "; also " + cleanup
		}
		return
	}
	res.AgentSock, res.AgentUnit, res.FibbyUnit = pair.AgentSock, pair.AgentUnit, pair.FibbyUnit

	// The pair is up: it is stopped on every way out, Ctrl-C included. Deferred
	// before anything more is written.
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		stopErr := l.Stop(stopCtx, res.Session)
		res.LeftOver = selftestLeftovers(stopCtx, l, res.Session)
		switch {
		case res.Step != "":
			if stopErr != nil || len(res.LeftOver) > 0 {
				res.Next += "; also " + cleanup
			}
		case stopErr != nil:
			fail("stop", stopErr, cleanup)
		case len(res.LeftOver) > 0:
			fail("leftovers", errors.New(strings.Join(res.LeftOver, ", ")), cleanup)
		default:
			say("ok stop, nothing left")
			res.Step, res.OK = "done", true
		}
	}()
	say("ok start (%s, %s)", pair.AgentUnit, pair.FibbyUnit)

	if err := selftestListsOneKey(ctx, l, pair); err != nil {
		fail("list-key", err, journal)
		return
	}
	say("ok the holder lists exactly its one key")
	if err := selftestSignAndVerify(ctx, l, pair); err != nil {
		fail("sign", err, journal)
		return
	}
	say("ok signed and verified")
	if err := selftestSignedCommit(ctx, l, pair); err != nil {
		fail("commit", err, journal)
		return
	}
	say("ok made a signed git commit as a worker would, and verified it")
	if err := ctx.Err(); err != nil {
		fail("interrupted", err, "run `sc keyholder-selftest` again")
	}
}

// selftestSignedCommit is the self-test's commit step; tests, whose fake holder
// cannot sign for git, replace it.
var selftestSignedCommit = signedCommitWithHolder

// selftestGitSigningEnv is the worker's own writer; a test hands the commit
// step one whose settings do not sign, to see the verification refuse it.
var selftestGitSigningEnv = keyholder.GitSigningEnv

// signedCommitWithHolder makes a plain `git commit` in a throwaway repository,
// with the settings a holder worker's launch gives it (the same writer), and
// verifies it. Nothing of the operator's git configuration is read.
func signedCommitWithHolder(ctx context.Context, l *keyholder.Launcher, pair keyholder.Pair) error {
	keygen, err := keyholder.SSHKeygenPin()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "keyholder-selftest-git-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + dir,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=spinclass", "GIT_AUTHOR_EMAIL=selftest@spinclass.invalid",
		"GIT_COMMITTER_NAME=spinclass", "GIT_COMMITTER_EMAIL=selftest@spinclass.invalid",
	}
	signing, err := selftestGitSigningEnv(l.Base, pair.Session, keygen, pair, env, nil)
	if err != nil {
		return err
	}
	for k, v := range signing {
		env = append(env, k+"="+v)
	}
	repo := filepath.Join(dir, "repo")
	for _, args := range [][]string{
		{"init", "-q", repo},
		{"-C", repo, "commit", "-q", "--allow-empty", "-m", "key holder self-test"},
		{"-C", repo, "verify-commit", "HEAD"},
	} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
		}
	}
	return nil
}

func selftestListsOneKey(ctx context.Context, l *keyholder.Launcher, pair keyholder.Pair) error {
	sshAdd, err := keyholder.SSHAddPin()
	if err != nil {
		return err
	}
	listed, err := l.Run.Output(ctx, []string{sshAdd, "-L"}, []string{"SSH_AUTH_SOCK=" + pair.AgentSock})
	if err != nil {
		return err
	}
	line, err := onlyKeyLine(listed)
	if err != nil {
		return err
	}
	got, want := strings.Fields(line), strings.Fields(pair.PublicKey)
	if len(got) < 2 || len(want) < 2 || got[0] != want[0] || got[1] != want[1] {
		return errors.New("the holder lists another key than the one it minted")
	}
	return nil
}

func selftestSignAndVerify(ctx context.Context, l *keyholder.Launcher, pair keyholder.Pair) error {
	keygen, err := keyholder.SSHKeygenPin()
	if err != nil {
		return err
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pair.PublicKey))
	if err != nil {
		return fmt.Errorf("parsing the holder's public key: %w", err)
	}
	dir, err := os.MkdirTemp("", "keyholder-selftest-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	keyFile, data := filepath.Join(dir, "key.pub"), filepath.Join(dir, "data")
	message := []byte("spinclass key holder self-test\n")
	if err := os.WriteFile(keyFile, []byte(pair.PublicKey+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(data, message, 0o600); err != nil {
		return err
	}
	if _, err := l.Run.Output(ctx, []string{keygen, "-Y", "sign", "-U", "-f", keyFile, "-n", selftestNamespace, data},
		[]string{"SSH_AUTH_SOCK=" + pair.AgentSock}); err != nil {
		return fmt.Errorf("signing through the holder: %w", err)
	}
	sig, err := os.ReadFile(data + ".sig")
	if err != nil {
		return fmt.Errorf("reading the signature: %w", err)
	}
	if err := sshsig.Verify(sig, message, selftestNamespace, []ssh.PublicKey{pub}); err != nil {
		return fmt.Errorf("the signature does not verify against the holder's key: %w", err)
	}
	return nil
}

// selftestLeftovers names what a stopped (or never started) pair should not
// have left: a running unit, or its directory.
func selftestLeftovers(ctx context.Context, l *keyholder.Launcher, session string) []string {
	var left []string
	h := keyholder.Hash(session)
	for _, u := range []string{"spinclass-keyholder-agent-" + h + ".service", "spinclass-keyholder-fibby-" + h + ".service"} {
		active, err := l.Run.Active(ctx, u)
		if err != nil {
			left = append(left, u+" (could not be asked about)")
		} else if active {
			left = append(left, u+" is still running")
		}
	}
	dir := keyholder.SessionDir(l.Base, session)
	if _, err := os.Lstat(dir); err == nil {
		left = append(left, "directory "+dir)
	}
	return left
}
