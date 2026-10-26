package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// handleRightTools is every tool that acts on another session under a handle,
// with the FDR 0032 D13 right it requires.
var handleRightTools = []struct{ tool, right, args string }{
	{"close-child-session", "close", `{"child":"worker/kid"}`},
	{"restart-child-session", "restart", `{"child":"worker/kid"}`},
	{"cancel-restart", "restart", `{"child":"worker/kid"}`},
	{"grant-session-handle", "grant", `{"child":"worker/kid","to":"p9"}`},
}

// unenforcedHandleRights are rights in the vocabulary that no spinclass tool
// requires yet: recorded on grants, checked by nothing. Enforcing one moves
// it into handleRightTools.
var unenforcedHandleRights = []string{"observe", "instruct", "cap", "merge", "check"}

func callHandleTool(t *testing.T, tool, args string) (string, bool) {
	t.Helper()
	switch tool {
	case "close-child-session":
		return callCloseChild(t, args)
	case "restart-child-session":
		return callRestartChild(t, args)
	case "grant-session-handle":
		return callGrant(t, args)
	case "cancel-restart":
		res, err := handleCancelRestart(context.Background(), json.RawMessage(args), nil)
		if err != nil {
			t.Fatalf("handleCancelRestart: %v", err)
		}
		return res.Text, res.IsErr
	}
	t.Fatalf("no caller for %s", tool)
	return "", false
}

// Each handle tool refuses a holder that holds every right but the one it
// needs, and its refusal names that right.
func TestEveryHandleToolRequiresItsRight(t *testing.T) {
	for _, tc := range handleRightTools {
		t.Run(tc.tool, func(t *testing.T) {
			stubRestartHandoff(t, "", nil)
			stubNotifyPrincipal(t)
			allBut := slices.DeleteFunc(slices.Clone(session.HandleRightNames), func(r string) bool { return r == tc.right })
			restartChildFixture(t, "holder", session.State{
				Holders:      []string{"holder"},
				HandleRights: map[string]string{"holder": strings.Join(allBut, ",")},
			})

			text, isErr := callHandleTool(t, tc.tool, tc.args)
			if !isErr || !strings.Contains(text, tc.tool+" needs "+tc.right) {
				t.Fatalf("holding all but %s: want a refusal naming it, got isErr=%v %q", tc.right, isErr, text)
			}
		})
	}
}

// Every right in the vocabulary is either required by a tool or listed as
// unenforced, so a new right (or a newly enforced one) cannot go unaccounted.
func TestEveryHandleRightIsAccountedFor(t *testing.T) {
	accounted := slices.Clone(unenforcedHandleRights)
	for _, tc := range handleRightTools {
		accounted = append(accounted, tc.right)
	}
	for _, r := range session.HandleRightNames {
		if !slices.Contains(accounted, r) {
			t.Errorf("right %q is neither required by a tool nor listed as unenforced", r)
		}
	}
	for _, r := range unenforcedHandleRights {
		if !slices.Contains(session.HandleRightNames, r) {
			t.Errorf("unenforced right %q is not in the vocabulary", r)
		}
	}
}
