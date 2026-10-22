package perms

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/store"
	gitstore "code.linenisgreat.com/spinclass/internal/store/git"
)

// recordingBackend records the session and op Classify was called with.
type recordingBackend struct {
	store.Backend
	verdict store.Verdict
	session store.Session
	op      store.Op
}

func (r *recordingBackend) Classify(s store.Session, op store.Op) store.Verdict {
	r.session, r.op = s, op
	return r.verdict
}

func (r *recordingBackend) Do(context.Context, store.Session, store.Op) (store.Result, error) {
	return store.Result{}, nil
}

func stubStore(t *testing.T, backend store.Backend, resolved *store.Session) {
	t.Helper()
	oldBackend, oldResolve := storeBackend, resolveStoreSession
	t.Cleanup(func() { storeBackend, resolveStoreSession = oldBackend, oldResolve })
	storeBackend = backend
	resolveStoreSession = func(string) (store.Session, error) {
		if resolved == nil {
			return store.Session{}, errors.New("no frontend principal mapping")
		}
		return *resolved, nil
	}
}

func TestStoreVerdictIgnoresOtherTools(t *testing.T) {
	for _, name := range []string{
		"Bash",
		"mcp__plugin_moxy_moxy__grit_branch",
		"mcp__plugin_spinclass_spinclass__list",
		"mcp__plugin_spinclass_spinclass__grit_unknown",
	} {
		if _, _, isStore := StoreVerdict(name, map[string]any{"subcommand": "log"}, "", false); isStore {
			t.Errorf("%s must not be a store tool", name)
		}
	}
}

func TestStoreVerdictFailsClosed(t *testing.T) {
	const tool = gritToolPrefix + "branch"
	t.Run("missing subcommand", func(t *testing.T) {
		stubStore(t, &recordingBackend{verdict: store.Allow}, &store.Session{Dir: "/w", Branch: "b"})
		if verdict, _, _ := StoreVerdict(tool, map[string]any{}, "p", true); verdict != store.Ask {
			t.Errorf("got %v, want ask", verdict)
		}
	})
	t.Run("subcommand wrong type", func(t *testing.T) {
		stubStore(t, &recordingBackend{verdict: store.Allow}, &store.Session{Dir: "/w", Branch: "b"})
		if verdict, _, _ := StoreVerdict(tool, map[string]any{"subcommand": 4}, "p", true); verdict != store.Ask {
			t.Errorf("got %v, want ask", verdict)
		}
	})
}

// An unresolved session inside a confined unit (CLOWN_UNIT set) is deferred to
// the serve; outside a unit it asks. Force asks either way, and the backend is
// never consulted without a session.
func TestStoreVerdictUnresolvedSessionDependsOnTheUnitMarker(t *testing.T) {
	const tool = gritToolPrefix + "branch"
	calls := map[string]struct {
		tool  string
		input map[string]any
	}{
		"read":                           {"stage", map[string]any{"subcommand": "status"}},
		"stash with a foreign repo_path": {"stash", map[string]any{"subcommand": "save", "repo_path": "/foreign/repo"}},
		"plain commit":                   {"commit", map[string]any{"message": "m"}},
		"foreign branch delete":          {"branch", map[string]any{"subcommand": "delete", "name": "foreign"}},
	}
	for _, principal := range []string{"", "some-principal"} {
		for name, call := range calls {
			t.Run(name+" principal="+principal, func(t *testing.T) {
				recorder := &recordingBackend{verdict: store.Allow}
				stubStore(t, recorder, nil)
				verdict, reason, isStore := StoreVerdict(gritToolPrefix+call.tool, call.input, principal, true)
				if !isStore || verdict != store.Allow || !strings.Contains(reason, "deferred to the serve") {
					t.Errorf("in unit: got (%v, %q, store=%v), want allow deferred", verdict, reason, isStore)
				}
				verdict, reason, _ = StoreVerdict(gritToolPrefix+call.tool, call.input, principal, false)
				if verdict != store.Ask || !strings.Contains(reason, "not in a confined unit") {
					t.Errorf("outside a unit: got (%v, %q), want ask", verdict, reason)
				}
				if recorder.op.Tool != "" {
					t.Error("backend must not be consulted without a session")
				}
			})
		}
	}
	t.Run("empty principal through the real resolver", func(t *testing.T) {
		if verdict, _, _ := StoreVerdict(tool, map[string]any{"subcommand": "log"}, "", true); verdict != store.Allow {
			t.Errorf("in unit: got %v, want allow", verdict)
		}
		if verdict, _, _ := StoreVerdict(tool, map[string]any{"subcommand": "log"}, "", false); verdict != store.Ask {
			t.Errorf("outside a unit: got %v, want ask", verdict)
		}
	})
	t.Run("force asks for an unresolved caller, in a unit or not", func(t *testing.T) {
		stubStore(t, &recordingBackend{verdict: store.Allow}, nil)
		input := map[string]any{"subcommand": "delete", "name": "x", "force": true}
		for _, inUnit := range []bool{true, false} {
			if verdict, _, _ := StoreVerdict(tool, input, "p", inUnit); verdict != store.Ask {
				t.Errorf("inUnit=%v: got %v, want ask", inUnit, verdict)
			}
		}
	})
	t.Run("a resolved session is decided by Classify, marker or not", func(t *testing.T) {
		for _, backendVerdict := range []store.Verdict{store.Allow, store.Ask} {
			stubStore(t, &recordingBackend{verdict: backendVerdict}, &store.Session{Dir: "/w", Branch: "b"})
			for _, inUnit := range []bool{true, false} {
				if verdict, _, _ := StoreVerdict(tool, map[string]any{"subcommand": "delete", "name": "foreign"}, "p", inUnit); verdict != backendVerdict {
					t.Errorf("backend %v inUnit=%v: got %v", backendVerdict, inUnit, verdict)
				}
			}
		}
	})
	t.Run("an AlwaysAsk floor stays ahead of the store verdict", func(t *testing.T) {
		if _, ask := AlwaysAsk(grantSessionHandleTool, map[string]any{}, "/w"); !ask {
			t.Error("the floor must still ask")
		}
	})
}

func TestStoreVerdictPassesSessionAndOpToBackend(t *testing.T) {
	recorder := &recordingBackend{verdict: store.Allow}
	resolved := store.Session{Dir: "/w", Branch: "glad-buckeye"}
	stubStore(t, recorder, &resolved)

	verdict, _, isStore := StoreVerdict(gritToolPrefix+"branch", map[string]any{"subcommand": "delete", "name": "x"}, "p", false)
	if !isStore || verdict != store.Allow {
		t.Fatalf("got (%v, store=%v), want allow", verdict, isStore)
	}
	if recorder.session != resolved {
		t.Errorf("session = %+v, want %+v", recorder.session, resolved)
	}
	if recorder.op.Tool != "branch" || recorder.op.Subcommand != "delete" {
		t.Errorf("op = %+v", recorder.op)
	}
	var args map[string]any
	if err := json.Unmarshal(recorder.op.Args, &args); err != nil || args["name"] != "x" {
		t.Errorf("args = %s (%v)", recorder.op.Args, err)
	}

	recorder.verdict = store.Ask
	if verdict, _, _ := StoreVerdict(gritToolPrefix+"branch", map[string]any{"subcommand": "delete", "name": "x"}, "p", false); verdict != store.Ask {
		t.Errorf("backend ask must stay ask, got %v", verdict)
	}
}

func TestStoreVerdictToolsWithoutSubcommand(t *testing.T) {
	recorder := &recordingBackend{verdict: store.Allow}
	stubStore(t, recorder, &store.Session{Dir: "/w", Branch: "b"})
	for _, tool := range []string{"commit", "merge", "clone"} {
		if verdict, _, isStore := StoreVerdict(gritToolPrefix+tool, map[string]any{"message": "m"}, "p", false); !isStore || verdict != store.Allow {
			t.Errorf("%s: got (%v, store=%v)", tool, verdict, isStore)
		}
		if recorder.op.Tool != tool || recorder.op.Subcommand != "" {
			t.Errorf("%s: op = %+v", tool, recorder.op)
		}
	}
}

// The remote family and the tag and worktree mutations reach the backend's
// Classify with their subcommand, and the real git backend asks for the
// default branch, a delete and a force.
func TestStoreVerdictRemoteFamilyThroughTheGitBackend(t *testing.T) {
	stubStore(t, gitstore.New(), &store.Session{Dir: t.TempDir(), Branch: "glad-buckeye"})
	for _, tc := range []struct {
		tool  string
		input map[string]any
		want  store.Verdict
	}{
		{"remote", map[string]any{"subcommand": "push", "force": true}, store.Ask},
		{"remote", map[string]any{"subcommand": "push", "delete": true, "branch": "glad-buckeye"}, store.Ask},
		{"remote", map[string]any{"subcommand": "push", "branch": "main"}, store.Ask},
		{"remote", map[string]any{"subcommand": "push-stack", "branches": []any{}}, store.Ask},
		{"worktree", map[string]any{"subcommand": "remove", "path": "/elsewhere"}, store.Ask},
		{"tag", map[string]any{"subcommand": "create", "name": "v1"}, store.Ask},
		{"clone", map[string]any{"source": "x", "destination": "/elsewhere/y"}, store.Ask},
		{"remote", map[string]any{"subcommand": "fetch"}, store.Allow},
	} {
		if verdict, _, isStore := StoreVerdict(gritToolPrefix+tc.tool, tc.input, "p", false); !isStore || verdict != tc.want {
			t.Errorf("%s %v: got (%v, store=%v), want %v", tc.tool, tc.input, verdict, isStore, tc.want)
		}
	}
}
