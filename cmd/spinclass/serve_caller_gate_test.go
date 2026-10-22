package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/purse-first/libs/go-mcp/command"
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
		tool: "close-child-session", operatorCommand: "sc close", handle: handleCloseChildSession,
		args: `{"child":"worker/kid","force":true}`, harmlessArgs: `{}`, harmlessRefusal: "child is required",
		cliArgs: []string{"close-child-session", "--child", "nope/none"}, cliRefusal: "no spinclass session for child",
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
				if err != nil {
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
