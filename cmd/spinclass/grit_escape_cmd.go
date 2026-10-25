package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	gitpkg "code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/alfa/store"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	"golang.org/x/term"
)

// Seams for command tests.
var (
	// gritEscapeIsTerminal reports whether a human is at the keyboard: stdin and
	// stdout must both be terminals, so a pipe cannot drive the hatch.
	gritEscapeIsTerminal = func() bool {
		return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	}
	// gritEscapeConfirmIn is where the typed confirmation is read from.
	gritEscapeConfirmIn = func() io.Reader { return os.Stdin }
	gritEscapeStdout    = func() io.Writer { return os.Stdout }
	gritEscapeStderr    = func() io.Writer { return os.Stderr }
	gritEscapeNow       = time.Now
)

const gritEscapeLogName = "grit-escape.log"

// registerGritEscapeCommand registers `sc grit`, the human-run escape hatch
// (FDR 0036 section 7). CLI-only by construction: RunCLI and no Run, so it is
// never an MCP tool, and nothing on the hook path reaches it.
func registerGritEscapeCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "grit",
		Title: "Run one grit store tool by hand",
		Description: command.Description{
			Short: "Run one grit store tool as the operator, outside the MCP and hook rules (escape hatch; FDR 0036)",
			Long: "Runs one grit tool (stage, commit, branch, stash, tag, worktree, remote, merge, clone) through the same backend " +
				"the grit_<tool> MCP tools use, as an unconfined caller. It exists so an operator can recover when the rules misfire " +
				"or when working outside any session: unlike the MCP tools it may write from a directory that is no session worktree. " +
				"The protections that apply to every caller (destructive operations on the default branch) still apply. " +
				"The arguments are one JSON object on the command line, e.g. sc grit branch '{\"subcommand\":\"log\",\"oneline\":true}'; " +
				"they are never read from stdin. " +
				"It refuses to run unless stdin and stdout are both terminals. " +
				"Any verb that is not plainly allowed (a write that is not on an owned branch, a floor verb) prints the verb and its " +
				"arguments to stderr and needs a typed y on the terminal, unless --yes is given. " +
				"Every use is appended as one JSON line to $XDG_STATE_HOME/spinclass/grit-escape.log (default ~/.local/state/spinclass/, " +
				"mode 0600); if that log cannot be written the operation is refused. Remote verbs stay agentless. Result text goes to stdout, errors to stderr with a nonzero exit.",
		},
		Params: []command.Param{
			{Name: "tool", Type: command.String, Description: "The grit tool: stage, commit, branch, stash, tag, worktree, remote, merge or clone"},
			{Name: "args", Type: command.String, Description: "The tool's arguments as one JSON object (default {}); never read from stdin"},
			{Name: "yes", Type: command.Bool, Description: "Skip the typed confirmation for a verb that would ask (a terminal is still required)"},
		},
		RunCLI: runGritEscape,
	})
}

type gritEscapeFlags struct {
	Tool string `json:"tool"`
	Args string `json:"args"`
	Yes  bool   `json:"yes"`
}

// gritEscapeRecord is one line of the escape log. Args hold no credentials.
type gritEscapeRecord struct {
	Time            string          `json:"time"`
	Cwd             string          `json:"cwd"`
	Tool            string          `json:"tool"`
	Subcommand      string          `json:"subcommand"`
	Args            json.RawMessage `json:"args"`
	Verdict         string          `json:"verdict"`
	ConfirmRequired bool            `json:"confirm_required"`
	Confirmed       bool            `json:"confirmed"`
	Outcome         string          `json:"outcome"`
	Error           string          `json:"error,omitempty"`
}

func runGritEscape(ctx context.Context, raw json.RawMessage) error {
	var f gritEscapeFlags
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("grit: %w", err)
	}
	if !gritEscapeIsTerminal() {
		return errors.New("grit: stdin and stdout must both be terminals; this escape hatch is for an operator at the keyboard and cannot be piped or driven by a program")
	}
	if f.Args == "" {
		f.Args = "{}"
	}
	args := json.RawMessage(f.Args)
	var object map[string]json.RawMessage
	if err := json.Unmarshal(args, &object); err != nil {
		return fmt.Errorf("grit: the arguments must be one JSON object: %w", err)
	}
	if !knownGritTool(f.Tool) {
		return fmt.Errorf("grit: unknown tool %q", f.Tool)
	}
	subcommand, err := gritSubcommand(f.Tool, args)
	if err != nil {
		return fmt.Errorf("grit: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("grit: could not get working directory: %w", err)
	}

	s := gritEscapeSession(cwd)
	op := store.Op{Tool: f.Tool, Subcommand: subcommand, Args: args}
	verdict := gritBackend.Classify(s, op)

	// Open the log before anything runs: a log that cannot be written refuses the
	// operation (fail closed).
	logFile, err := openGritEscapeLog()
	if err != nil {
		return fmt.Errorf("grit: refusing to run, the escape log cannot be written: %w", err)
	}
	defer logFile.Close()

	rec := gritEscapeRecord{
		Cwd: cwd, Tool: f.Tool, Subcommand: subcommand, Args: compactJSON(args),
		Verdict: verdict.String(), ConfirmRequired: verdict != store.Allow && !f.Yes,
	}
	finish := func(runErr error) error {
		rec.Time = gritEscapeNow().UTC().Format(time.RFC3339)
		rec.Outcome = "ok"
		if runErr != nil {
			rec.Outcome, rec.Error = "error", runErr.Error()
		}
		if werr := appendGritEscapeRecord(logFile, rec); werr != nil {
			return errors.Join(runErr, fmt.Errorf("grit: the operation ran but the escape log could not be written: %w", werr))
		}
		return runErr
	}

	// Refusals and declines are returned, not printed: the command runner
	// prints a returned error, and printing it here too doubled it (#396).
	if refusal := refuseEscapeDefaultBranchPush(s.Dir, f.Tool, subcommand, args); refusal != nil {
		return finish(refusal)
	}

	if rec.ConfirmRequired {
		fmt.Fprintf(gritEscapeStderr(), "grit %s %s %s\nverdict: ask (not plainly allowed here)\nrun it? type y to confirm: ", f.Tool, subcommand, rec.Args)
		line, _ := bufio.NewReader(gritEscapeConfirmIn()).ReadString('\n')
		if !strings.HasSuffix(line, "\n") {
			// An answer ended by EOF brings no newline of its own; end the prompt
			// line so what follows starts on a fresh one.
			fmt.Fprintln(gritEscapeStderr())
		}
		if strings.ToLower(strings.TrimSpace(line)) != "y" {
			return finish(errors.New("declined by the operator; nothing ran"))
		}
		rec.Confirmed = true
	}

	text, runErr := runGritAs(ctx, f.Tool, args, s)
	if runErr == nil && text != "" {
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		fmt.Fprint(gritEscapeStdout(), text)
	}
	return finish(runErr)
}

const gritEscapeDefaultBranchRule = "the escape hatch never force-pushes, deletes, or rewrites the default branch"

type gritEscapePushArgs struct {
	Remote         string   `json:"remote"`
	Branch         string   `json:"branch"`
	Branches       []string `json:"branches"`
	RemoteBranch   string   `json:"remote_branch"`
	Force          bool     `json:"force"`
	ForceWithLease bool     `json:"force_with_lease"`
	Delete         bool     `json:"delete"`
}

// refuseEscapeDefaultBranchPush refuses, whatever --yes or a typed y, a push
// that would force, delete or rewrite the default branch (main, master or
// origin's HEAD branch). A plain fast-forward push of it is left to the normal
// Ask. A local equivalent of the store's default-branch resolution, kept here so
// the store package stays untouched.
func refuseEscapeDefaultBranchPush(dir, tool, subcommand string, raw json.RawMessage) error {
	if tool != "remote" || (subcommand != "push" && subcommand != "push-stack") {
		return nil
	}
	var a gritEscapePushArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil
	}
	refuse := func(why string) error {
		return fmt.Errorf("refusing remote %s: %s; %s", subcommand, why, gritEscapeDefaultBranchRule)
	}
	if subcommand == "push-stack" {
		for _, name := range a.Branches {
			if escapeTouchesDefaultBranch(dir, name) {
				return refuse(fmt.Sprintf("push-stack includes the default branch %q", name))
			}
		}
		return nil
	}
	source := a.Branch
	if source == "" {
		source = "HEAD"
	}
	destination := strings.TrimPrefix(a.RemoteBranch, "refs/heads/")
	if destination == "" {
		if full, err := gitpkg.Run(dir, "rev-parse", "--symbolic-full-name", source); err == nil && strings.HasPrefix(full, "refs/heads/") {
			destination = strings.TrimPrefix(full, "refs/heads/")
		} else {
			destination = strings.TrimPrefix(source, "refs/heads/")
		}
	}
	if !escapeTouchesDefaultBranch(dir, source) && !escapeTouchesDefaultBranch(dir, destination) {
		return nil
	}
	switch {
	case a.Delete:
		return refuse("it deletes the default branch")
	case a.Force:
		return refuse("it force-pushes the default branch")
	case a.ForceWithLease:
		return refuse("it force-pushes (with lease) the default branch")
	}
	remote := a.Remote
	if remote == "" {
		remote = "origin"
	}
	tip, err := gitpkg.Run(dir, "rev-parse", "--verify", "-q", "refs/remotes/"+remote+"/"+destination)
	if err != nil || tip == "" {
		return refuse(fmt.Sprintf("the remote-tracking ref %s/%s is missing, so a non-fast-forward update cannot be ruled out", remote, destination))
	}
	if _, err := gitpkg.Run(dir, "rev-parse", "--verify", "-q", source+"^{commit}"); err != nil {
		return refuse(fmt.Sprintf("%q does not resolve to a commit, so a non-fast-forward update cannot be ruled out", source))
	}
	if _, err := gitpkg.Run(dir, "merge-base", "--is-ancestor", tip, source); err != nil {
		return refuse(fmt.Sprintf("the push would not be a fast-forward of %s/%s", remote, destination))
	}
	return nil
}

// escapeTouchesDefaultBranch resolves name (empty means HEAD) to its branch and
// also checks the literal name, as the store does.
func escapeTouchesDefaultBranch(dir, name string) bool {
	if name == "" {
		name = "HEAD"
	}
	if full, err := gitpkg.Run(dir, "rev-parse", "--symbolic-full-name", name); err == nil && strings.HasPrefix(full, "refs/heads/") {
		if escapeIsDefaultBranch(dir, strings.TrimPrefix(full, "refs/heads/")) {
			return true
		}
	}
	return escapeIsDefaultBranch(dir, strings.TrimPrefix(name, "refs/heads/"))
}

func escapeIsDefaultBranch(dir, short string) bool {
	if short == "main" || short == "master" {
		return true
	}
	out, err := gitpkg.Run(dir, "symbolic-ref", "-q", "refs/remotes/origin/HEAD")
	return err == nil && strings.TrimPrefix(out, "refs/remotes/origin/") == short
}

func knownGritTool(name string) bool {
	for _, t := range gritTools() {
		if t.name == name {
			return true
		}
	}
	return false
}

// gritEscapeSession is the caller the operator acts as: unconfined, in the
// session worktree when the cwd is in one, otherwise in the cwd with no session
// branch. NoSession is deliberately false so the read-only refusal for callers
// outside a session does not apply (the point of the hatch).
func gritEscapeSession(cwd string) store.Session {
	s := store.Session{Dir: cwd, Confinement: store.ConfinementUnconfined}
	if worktree, branch, ok := recognisedSessionWorktree(cwd); ok {
		s.Dir, s.Branch = worktree, branch
	}
	s.Identity = storeIdentityFor(s.Dir)
	return s
}

func gritEscapeLogPath() (string, error) {
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "spinclass", gritEscapeLogName), nil
}

func openGritEscapeLog() (*os.File, error) {
	path, err := gritEscapeLogPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}

func appendGritEscapeRecord(f *os.File, rec gritEscapeRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

func compactJSON(raw json.RawMessage) json.RawMessage {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return raw
	}
	return b.Bytes()
}
