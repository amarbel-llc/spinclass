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

func stubServeCallerUnconfined(t *testing.T) {
	t.Helper()
	original := serveCallerConfined
	t.Cleanup(func() { serveCallerConfined = original })
	serveCallerConfined = func() (bool, string) { return false, "stubbed unconfined" }
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
}

// gatedServeHandlers lists every serve tool a confined caller may not use
// (FDR 0035, "Serve handlers and the caller"). Each gets the same refusal.
var gatedServeHandlers = []gatedServeHandler{
	{"spawn-session", "sc spawn", handleSpawnSession, `{"brief":"do"}`, `{"repo":"somewhere"}`, "brief is required"},
	{"resurrect", "sc resurrect", handleResurrect, `{"target":"worker/kid"}`, `{}`, "target is required"},
}

// The operator's terminal path is not the serve, so it is not gated: a
// terminal has no serve principal and would otherwise be refused.
func TestResurrectCLIIsNotServeGated(t *testing.T) {
	asConfinedServeCaller(t, "a-confined-principal")
	err := runResurrectCLI(context.Background(), json.RawMessage(`{}`))
	if err == nil || strings.Contains(err.Error(), "classifies as confined") || !strings.Contains(err.Error(), "target is required") {
		t.Fatalf("sc resurrect must reach its own validation, got %v", err)
	}
}

func TestGatedServeHandlersLetAnUnconfinedCallerThrough(t *testing.T) {
	for _, h := range gatedServeHandlers {
		t.Run(h.tool, func(t *testing.T) {
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
