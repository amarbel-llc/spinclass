package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"

	"code.linenisgreat.com/spinclass/internal/alfa/store"
)

// useRealServeClassifier undoes TestMain's unconfined default for one test.
func useRealServeClassifier(t *testing.T) {
	t.Helper()
	original := serveCallerConfined
	t.Cleanup(func() { serveCallerConfined = original })
	serveCallerConfined = classifyServeCaller
}

// writeUnitRecordNaming plants a frontend unit record naming principal, which
// is what makes ClassifyServeCaller call that principal confined.
func writeUnitRecordNaming(t *testing.T, stateDir, principal string) {
	t.Helper()
	dir := filepath.Join(stateDir, "spinclass", "units")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"principal":%q}`, principal)
	if err := os.WriteFile(filepath.Join(dir, "deadbeef.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

// asConfinedServeCaller makes this test process a recorded serve whose
// principal a unit record names, so the real classifier says "confined".
func asConfinedServeCaller(t *testing.T, principal string) {
	t.Helper()
	useRealServeClassifier(t)
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	t.Setenv("CLOWN_SESSION_ID", principal)
	removeRecord, err := registerServeRecord()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(removeRecord)
	writeUnitRecordNaming(t, stateDir, principal)
}

type gatedServeHandler struct {
	tool            string
	operatorCommand string
	handle          func(context.Context, json.RawMessage, command.Prompter) (*command.Result, error)
	args            string
	// harmlessArgs fail the handler's own validation, so an unconfined
	// caller is shown to get past the gate without the tool doing anything.
	harmlessArgs    string
	harmlessRefusal string
	// cliArgs run the same command through the CLI (`sc <cliArgs...>`), which
	// must not be serve-gated; nil when the operator path is another command.
	cliArgs    []string
	cliRefusal string
}

// gatedServeHandlers lists every serve tool a confined caller may not use
// (FDR 0035, "Serve handlers and the caller"). Each gets the same refusal.
var gatedServeHandlers = []gatedServeHandler{
	{
		tool: "spawn-session", operatorCommand: "sc spawn", handle: handleSpawnSession,
		args: `{"brief":"do"}`, harmlessArgs: `{"repo":"somewhere"}`, harmlessRefusal: "brief is required",
	},
	{
		tool: "resurrect", operatorCommand: "sc resurrect", handle: handleResurrect,
		args: `{"target":"worker/kid"}`, harmlessArgs: `{}`, harmlessRefusal: "target is required",
		cliArgs: []string{"resurrect", "--target", "nope/none"}, cliRefusal: "no spinclass session for target",
	},
	{
		tool: "session-health", operatorCommand: "sc session-health", handle: handleSessionHealth,
		args: `{}`, harmlessArgs: `{"child":"nope/none"}`, harmlessRefusal: "no held session matches",
		cliArgs: []string{"session-health", "--child", "nope/none"}, cliRefusal: "no held session matches",
	},
	{
		tool: "restart-hung-child", operatorCommand: "sc restart-hung-child", handle: handleRestartHungChild,
		args: `{"child":"worker/kid"}`, harmlessArgs: `{}`, harmlessRefusal: "child is required",
	},
	{
		tool: "session-jobs", operatorCommand: "sc session-jobs", handle: handleSessionJobs,
		args: `{"child":"worker/kid"}`, harmlessArgs: `{}`, harmlessRefusal: "child is required",
		cliArgs: []string{"session-jobs", "--child", "nope/none"}, cliRefusal: "no spinclass session for child",
	},
	{
		tool: "close-child-session", operatorCommand: "sc close", handle: handleCloseChildSession,
		args: `{"child":"worker/kid","force":true}`, harmlessArgs: `{}`, harmlessRefusal: "child is required",
		cliArgs: []string{"close-child-session", "--child", "nope/none"}, cliRefusal: "no spinclass session for child",
	},
	{
		tool: "restart-child-session", operatorCommand: "sc restart-child-session", handle: handleRestartChildSession,
		args: `{"child":"worker/kid"}`, harmlessArgs: `{}`, harmlessRefusal: "child is required",
		cliArgs: []string{"restart-child-session", "--child", "nope/none"}, cliRefusal: "no spinclass session for child",
	},
	{
		tool: "stop-child-session", operatorCommand: "sc stop-child-session", handle: handleStopChildSession,
		args: `{"child":"worker/kid"}`, harmlessArgs: `{}`, harmlessRefusal: "child is required",
		cliArgs: []string{"stop-child-session", "--child", "nope/none"}, cliRefusal: "no spinclass session for child",
	},
	{
		tool: "start-child-session", operatorCommand: "sc start-child-session", handle: handleStartChildSession,
		args: `{"child":"worker/kid","message":"m"}`, harmlessArgs: `{}`, harmlessRefusal: "child is required",
		cliArgs: []string{"start-child-session", "--child", "nope/none", "--message", "m"}, cliRefusal: "no spinclass session for child",
	},
	// cancel-restart is mixed like list-handles: cancelling the caller's own
	// request (no child) is open, since it only removes a request the caller
	// could make; naming a child rests on a handle read from agent-writable state,
	// so that form is gated. TestCancelRestartOfOneselfIsOpenToAConfinedCaller
	// covers the open half.
	{
		tool: "cancel-restart", operatorCommand: "sc cancel-restart", handle: handleCancelRestart,
		args: `{"child":"worker/kid"}`, harmlessArgs: `{"child":"nope/none"}`, harmlessRefusal: "no spinclass session for child",
		cliArgs: []string{"cancel-restart", "--child", "nope/none"}, cliRefusal: "no spinclass session for child",
	},
	{
		tool: "grant-session-handle", operatorCommand: "sc grant-session-handle", handle: handleGrantSessionHandle,
		args: `{"child":"worker/kid","to":"someone"}`, harmlessArgs: `{}`, harmlessRefusal: "child is required",
		cliArgs: []string{"grant-session-handle", "--child", "nope/none", "--to", "someone"}, cliRefusal: "nope/none",
	},
	{
		tool: "list-handles accept", operatorCommand: "sc list-handles --accept", handle: handleListHandles,
		args: `{"accept":true}`, harmlessArgs: `{"accept":true}`, harmlessRefusal: "no handles",
		cliArgs: []string{"list-handles", "--accept"},
	},
	{
		tool: "merge-this-session", operatorCommand: "sc merge <target>", handle: handleMergeThisSession,
		args: `{}`, harmlessArgs: `not json`, harmlessRefusal: "invalid arguments",
	},
	{
		tool: "merge-this-session-async", operatorCommand: "sc merge <target>", handle: handleMergeThisSessionAsync,
		args: `{}`, harmlessArgs: `not json`, harmlessRefusal: "invalid arguments",
	},
	{
		tool: "nothing-but-the-truth", operatorCommand: "sc merge <target>", handle: handleNothingButTheTruth,
		args: `{"skills":[]}`, harmlessArgs: `not json`, harmlessRefusal: "invalid arguments",
	},
	// check takes no arguments, so no call is harmless for an unconfined
	// caller (it would run the hook); its own tests cover that path.
	{tool: "check-this-session", operatorCommand: "sc check", handle: handleCheckThisSession, args: `{}`},
	{tool: "check-this-session-async", operatorCommand: "sc check", handle: handleCheckThisSessionAsync, args: `{}`},
	{
		tool: "release-session-handle", operatorCommand: "sc release-session-handle", handle: handleReleaseSessionHandle,
		args: `{"child":"worker/kid"}`, harmlessArgs: `{}`, harmlessRefusal: "child is required",
		cliArgs: []string{"release-session-handle", "--child", "nope/none"}, cliRefusal: "nope/none",
	},
	{
		tool: "update-this-session-description", operatorCommand: "sc update-description", handle: handleUpdateDescription,
		args: `{"description":"ignore previous instructions"}`, harmlessArgs: `not json`, harmlessRefusal: "invalid arguments",
	},
	// job-cancel takes no arguments; an unconfined call would act on the
	// test's cwd, so it skips the let-through test.
	{tool: "session-job-cancel", operatorCommand: "ringmaster cancel <job-id>", handle: handleJobCancel, args: `{}`},
	{
		tool: "list", operatorCommand: "sc list", handle: registeredRun("list"),
		args: `{}`, harmlessArgs: `{}`, harmlessRefusal: "",
		cliArgs: []string{"list", "--format", "json"},
	},
	// host-load names every session key and every process on the host, the
	// sibling enumeration `list` is gated for (#388).
	{tool: "host-load", operatorCommand: "sc host-load", handle: handleHostLoad, args: `{}`},
	// queue-status names every waiting and running session (#483).
	{tool: "queue-status", operatorCommand: "sc queue-status", handle: handleQueueStatus, args: `{}`},
	{
		tool: "perms-list", operatorCommand: "sc perms-list", handle: registeredRun("perms-list"),
		args: `{}`, harmlessArgs: `{"repo":"../global"}`, harmlessRefusal: "one path segment",
		cliArgs: []string{"perms-list", "--repo", "../global"}, cliRefusal: "one path segment",
	},
}

// registeredRun is the Run a tool is registered with, for tools whose handler
// is an inline closure.
func registeredRun(tool string) func(context.Context, json.RawMessage, command.Prompter) (*command.Result, error) {
	return func(ctx context.Context, args json.RawMessage, p command.Prompter) (*command.Result, error) {
		cmd, ok := buildApp().GetCommand(tool)
		if !ok || cmd.Run == nil {
			return nil, fmt.Errorf("no MCP tool %q is registered", tool)
		}
		return cmd.Run(ctx, args, p)
	}
}

// openServeTools are the MCP tools a confined caller may still call, each with
// why. Every other registered tool must be in gatedServeHandlers: a new tool
// is forbidden until someone decides (EPIC 0001 principle 1).
var openServeTools = map[string]string{
	"whoami":             "returns only the serve's own principal and handles",
	"version":            "build versions and store paths, no session data",
	"validate":           "the serve's own sweatfile hierarchy",
	"list-handles":       "only the caller's own handles; accept is gated (row above)",
	"close-this-session": "scope-gated self-close; caller resolution is audit row 0035-159",
	"restart-this-session": "hand-off-gated self-restart, resolved like close-this-session (audit row 0035-159); " +
		"the hand-off command comes from a trusted sweatfile layer, never from the caller",
	"stop-this-session": "a self-stop through the same trusted hand-off as restart-this-session, resolved the same way; " +
		"a session may stop itself with no right",
}

// ownershipVerdictServeTools are the MCP tools that classify their caller
// themselves instead of calling refuseConfinedServeCaller: the grit store
// tools (FDR 0036 section 9). Each maps to one WRITE call that a confined
// caller owning nothing (no recognised session worktree) must be refused; the
// verdict is the store backend's, so the refusal is "refusing ...".
var ownershipVerdictServeTools = map[string]string{
	"grit_stage":    `{"subcommand":"add","paths":["x"]}`,
	"grit_branch":   `{"subcommand":"create","name":"nope"}`,
	"grit_commit":   `{"message":"x"}`,
	"grit_tag":      `{"subcommand":"create","name":"nope"}`,
	"grit_worktree": `{"subcommand":"remove","path":"/nonexistent"}`,
	"grit_stash":    `{"subcommand":"save"}`,
	"grit_remote":   `{"subcommand":"push"}`,
	"grit_merge":    `{"branch":"x"}`,
	"grit_clone":    `{"source":"x","destination":"y"}`,
}

// The older serve route serves the whole registry and relies on every handler
// deciding for itself: a deny gate, not an allow-list. This test is what makes
// that safe: EVERY registered MCP tool (grit_* included, hidden ones included)
// must have EXACTLY ONE class in the three tables above (gated, declared open,
// or ownership verdict), so a new tool cannot be reachable by a confined caller
// by omission, and a table row naming a tool that no longer exists is stale.
//
// Some tools are registered only under a condition (the merge/check pair by
// [hooks].disable-merge, the *-async tools and session-job-cancel under clown,
// nothing-but-the-truth when pre-merge skills are listed), and the registry of
// THIS test process has only one side of each. So the walk also reads the
// package's own sources for every `command.Command{Name: "...", Run: ...}`
// literal, so a conditionally registered tool cannot hide from the table.
func TestEveryServeToolHasExactlyOneClass(t *testing.T) {
	gated := map[string]bool{}
	for _, h := range gatedServeHandlers {
		if strings.HasSuffix(h.tool, " accept") {
			continue // a partial gate on a tool that is also open: see below
		}
		gated[h.tool] = true
	}
	// list-handles is mixed: plain listing is open, accept is gated (its row
	// says "list-handles accept"). A partial gate is only legal on an open tool.
	for _, h := range gatedServeHandlers {
		if tool, partial := strings.CutSuffix(h.tool, " accept"); partial {
			if _, open := openServeTools[tool]; !open {
				t.Errorf("gatedServeHandlers has a partial gate %q on %q, which is not declared open (openServeTools): a tool is either gated or open, a partial gate is only for a mixed tool", h.tool, tool)
			}
		}
	}
	registered := map[string]bool{}
	for name, cmd := range buildApp().AllCommands() {
		if cmd.Run != nil {
			registered[name] = true
		}
	}
	for name := range literalServeToolNames(t) {
		registered[name] = true
	}
	for name := range registered {
		_, open := openServeTools[name]
		_, owned := ownershipVerdictServeTools[name]
		classes := 0
		for _, in := range []bool{gated[name], open, owned} {
			if in {
				classes++
			}
		}
		switch classes {
		case 0:
			t.Errorf("MCP tool %q has no class: add it to gatedServeHandlers, openServeTools or ownershipVerdictServeTools (a new tool is forbidden to a confined caller until someone decides)", name)
		case 1:
		default:
			t.Errorf("MCP tool %q is in more than one of gatedServeHandlers, openServeTools and ownershipVerdictServeTools; it has exactly one class", name)
		}
	}
	for name := range gated {
		if !registered[name] {
			t.Errorf("gatedServeHandlers names %q, which is not a registered MCP tool (a stale row)", name)
		}
	}
	for name := range openServeTools {
		if !registered[name] {
			t.Errorf("openServeTools names %q, which is not a registered MCP tool (a stale row)", name)
		}
	}
	for name := range ownershipVerdictServeTools {
		if !registered[name] {
			t.Errorf("ownershipVerdictServeTools names %q, which is not a registered MCP tool (a stale row)", name)
		}
	}
	for _, want := range []string{"resurrect", "list", "whoami", "close-child-session", "grit_stage", "grit_clone"} {
		if !registered[want] {
			t.Errorf("the walk did not see %q; the completeness check is not looking at the registered tools", want)
		}
	}
}

// Every ownership-verdict tool must really refuse a confined caller that owns
// nothing, through the registered handler and the REAL caller classification
// (not a stubbed gritCaller): a confined serve (a unit record names its
// principal) running in a directory that is no session worktree.
func TestEveryOwnershipVerdictServeToolRefusesAConfinedCallerThatOwnsNothing(t *testing.T) {
	for name, payload := range ownershipVerdictServeTools {
		t.Run(name, func(t *testing.T) {
			asConfinedServeCaller(t, "a-confined-principal")
			t.Chdir(t.TempDir())
			t.Setenv("HOME", t.TempDir())
			// The refusal below also happens for an unconfined caller that owns
			// nothing, so pin the classification itself: this serve's caller is
			// confined, by the same function every grit tool resolves through.
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if got := gritCaller(context.Background(), cwd); got.Confinement != store.ConfinementConfined || !got.NoSession {
				t.Fatalf("%s: the caller must classify as confined and owning nothing, got Confinement=%v NoSession=%v", name, got.Confinement, got.NoSession)
			}
			cmd, ok := buildApp().GetCommand(name)
			if !ok || cmd.Run == nil {
				t.Fatalf("%s is not a registered MCP tool", name)
			}
			res, err := cmd.Run(context.Background(), json.RawMessage(payload), nil)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if !res.IsErr || !strings.Contains(res.Text, "refusing") {
				t.Fatalf("%s: a confined caller owning nothing must be refused by the ownership verdict, got isErr=%v %q", name, res.IsErr, res.Text)
			}
		})
	}
}

// literalServeToolNames returns the names of every `command.Command{Name:
// "literal", Run: ...}` literal in this package's non-test sources: the tools a
// build can register on some condition, whether or not THIS process did. A
// name that is not a string literal (grit_<tool>, plugin commands, which are
// CLI-only) is not seen here; grit_* come in through the registry walk.
func literalServeToolNames(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package's own sources to find conditionally registered tools: %v", err)
	}
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parsing %s to find conditionally registered tools: %v", name, perr)
		}
		files = append(files, file)
	}
	names := map[string]bool{}
	{
		for _, file := range files {
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				sel, ok := lit.Type.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Command" {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "command" {
					return true
				}
				var name string
				var hasRun bool
				for _, el := range lit.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok {
						continue
					}
					switch key.Name {
					case "Name":
						if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING {
							name = strings.Trim(bl.Value, "\"`")
						}
					case "Run":
						hasRun = true
					}
				}
				if name != "" && hasRun {
					names[name] = true
				}
				return true
			})
		}
	}
	if len(names) < 20 {
		t.Fatalf("the source walk found only %d tool literals (%v): it is not reading the package's sources", len(names), names)
	}
	return names
}

// Plain list-handles reads only the caller's own handles, so it stays open
// to a confined caller; only accept is gated.
func TestListHandlesWithoutAcceptIsOpenToAConfinedCaller(t *testing.T) {
	asConfinedServeCaller(t, "a-confined-principal")
	res, err := handleListHandles(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil || res.IsErr || res.Text != "no handles" {
		t.Fatalf("got err=%v %+v, want the plain listing", err, res)
	}
}

// Cancelling one's own restart names no child and rests on no handle, so a
// confined caller may do it (unlike the child form, which is gated above).
func TestCancelRestartOfOneselfIsOpenToAConfinedCaller(t *testing.T) {
	asConfinedServeCaller(t, "a-confined-principal")
	res, err := handleCancelRestart(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "classifies as confined") {
		t.Fatalf("self-cancel must not be serve-gated, got %q", res.Text)
	}
}

// The terminal path is not the serve, so it is not gated: a terminal has no
// serve principal and would otherwise be refused. Run through buildApp so the
// RunCLI wiring itself is what is tested.
func TestGatedServeToolsCLIIsNotServeGated(t *testing.T) {
	for _, h := range gatedServeHandlers {
		if h.cliArgs == nil {
			continue
		}
		t.Run(h.tool, func(t *testing.T) {
			asConfinedServeCaller(t, "a-confined-principal")
			t.Setenv("HOME", t.TempDir())
			err := buildApp().RunCLI(context.Background(), h.cliArgs, nil)
			if h.cliRefusal == "" {
				if err != nil && strings.Contains(err.Error(), "classifies as confined") {
					t.Fatalf("sc %s must succeed ungated, got %v", strings.Join(h.cliArgs, " "), err)
				}
				return
			}
			if err == nil || strings.Contains(err.Error(), "classifies as confined") || !strings.Contains(err.Error(), h.cliRefusal) {
				t.Fatalf("sc %s must reach its own validation (%q), got %v", strings.Join(h.cliArgs, " "), h.cliRefusal, err)
			}
		})
	}
}

func TestGatedServeHandlersLetAnUnconfinedCallerThrough(t *testing.T) {
	for _, h := range gatedServeHandlers {
		if h.harmlessRefusal == "" {
			continue
		}
		t.Run(h.tool, func(t *testing.T) {
			useRealServeClassifier(t)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("CLOWN_SESSION_ID", "an-operator-principal")
			removeRecord, err := registerServeRecord()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(removeRecord)
			res, err := h.handle(context.Background(), json.RawMessage(h.harmlessArgs), nil)
			if err != nil {
				t.Fatalf("%s: %v", h.tool, err)
			}
			if strings.Contains(res.Text, "classifies as confined") || !strings.Contains(res.Text, h.harmlessRefusal) {
				t.Fatalf("%s: an unconfined caller must reach the tool's own validation (%q), got %q", h.tool, h.harmlessRefusal, res.Text)
			}
		})
	}
}

func TestGatedServeHandlersRefuseAConfinedCaller(t *testing.T) {
	const principal = "a-confined-principal"
	for _, h := range gatedServeHandlers {
		t.Run(h.tool, func(t *testing.T) {
			asConfinedServeCaller(t, principal)
			res, err := h.handle(context.Background(), json.RawMessage(h.args), nil)
			if err != nil {
				t.Fatalf("%s: %v", h.tool, err)
			}
			if !res.IsErr {
				t.Fatalf("%s: a confined caller must be refused, got %q", h.tool, res.Text)
			}
			for _, want := range []string{h.tool + " refused", "classifies as confined", principal, "`" + h.operatorCommand + "`"} {
				if !strings.Contains(res.Text, want) {
					t.Errorf("%s: refusal %q does not name %q", h.tool, res.Text, want)
				}
			}
		})
	}
}

// Unclassifiable counts as confined: no principal, or a principal with no
// serve record.
func TestGatedServeHandlersRefuseAnUnclassifiableCaller(t *testing.T) {
	for _, principal := range []string{"", "a-principal-with-no-records"} {
		for _, h := range gatedServeHandlers {
			t.Run(h.tool+"/"+principal, func(t *testing.T) {
				useRealServeClassifier(t)
				t.Setenv("XDG_STATE_HOME", t.TempDir())
				t.Setenv("CLOWN_SESSION_ID", principal)
				res, err := h.handle(context.Background(), json.RawMessage(h.args), nil)
				if err != nil {
					t.Fatalf("%s: %v", h.tool, err)
				}
				if !res.IsErr || !strings.Contains(res.Text, "classifies as confined") {
					t.Fatalf("%s: an unclassifiable caller must be refused, got %q", h.tool, res.Text)
				}
			})
		}
	}
}
