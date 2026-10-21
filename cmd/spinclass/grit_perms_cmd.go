package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/store"
)

// Seams for command tests.
var (
	gritPermsStdin  = func() io.Reader { return os.Stdin }
	gritPermsStdout = func() io.Writer { return os.Stdout }
	// gritPermsSession resolves the calling session from its principal, as the
	// PreToolUse hook does. False means none could be resolved.
	gritPermsSession = func() (store.Session, bool) {
		key, ok := session.SessionKeyOfPrincipal(os.Getenv("CLOWN_SESSION_ID"))
		if !ok {
			return store.Session{}, false
		}
		st, err := session.FindByTarget(key)
		if err != nil || st.Kind == session.KindImplicit || st.WorktreePath == "" || st.Branch == "" {
			return store.Session{}, false
		}
		return store.Session{Dir: st.WorktreePath, Branch: st.Branch}, true
	}
)

// registerGritPermsCommand registers `sc grit-perms <tool>`, the dynamic-perms
// helper of the moxin export (FDR 0036 section 7). CLI-only: RunCLI, no Run.
func registerGritPermsCommand(app *command.App) {
	app.AddCommand(&command.Command{
		Name:  "grit-perms",
		Title: "Print the permission verdict for one grit tool invocation",
		Description: command.Description{
			Short: "Read one grit tool invocation as JSON on stdin and print allow or ask (dynamic-perms helper; FDR 0036)",
			Long: "Reads the invocation arguments of the named grit tool (stage, commit, branch, stash, tag, worktree, remote, merge or clone) " +
				"as one JSON object on stdin and prints allow or ask, using the same classification as the grit_<tool> MCP tools. " +
				"Exit status 0 is allow; anything else is ask (moxy's dynamic-perms convention), including unreadable input, so it fails closed. " +
				"When the calling session cannot be resolved from the principal, the static classification applies: reads allow, writes and floor verbs ask.",
		},
		Params: []command.Param{
			{Name: "tool", Type: command.String, Required: true, Description: "The grit tool: stage, commit, branch, stash, tag, worktree, remote, merge or clone"},
		},
		RunCLI: runGritPerms,
	})
}

type gritPermsFlags struct {
	Tool string `json:"tool"`
}

func runGritPerms(_ context.Context, raw json.RawMessage) error {
	var f gritPermsFlags
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("grit-perms: %w", err)
	}
	verdict, err := gritPermsVerdict(f.Tool)
	if err != nil {
		_, _ = fmt.Fprintln(gritPermsStdout(), store.Ask)
		return fmt.Errorf("grit-perms: %w", err)
	}
	_, _ = fmt.Fprintln(gritPermsStdout(), verdict)
	if verdict != store.Allow {
		return errors.New("ask")
	}
	return nil
}

func gritPermsVerdict(tool string) (store.Verdict, error) {
	if !knownGritTool(tool) {
		return store.Ask, fmt.Errorf("unknown tool %q", tool)
	}
	input, err := io.ReadAll(gritPermsStdin())
	if err != nil {
		return store.Ask, fmt.Errorf("could not read stdin: %w", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(input, &object); err != nil {
		return store.Ask, fmt.Errorf("stdin must be one JSON object: %w", err)
	}
	subcommand, err := gritSubcommand(tool, input)
	if err != nil {
		return store.Ask, err
	}
	s, ok := gritPermsSession()
	if !ok {
		s = store.Session{NoSession: true}
	}
	return gritBackend.Classify(s, store.Op{Tool: tool, Subcommand: subcommand, Args: input}), nil
}
