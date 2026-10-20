package perms

import (
	"context"
	"encoding/json"
	"testing"

	"code.linenisgreat.com/spinclass/internal/store"
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
	resolveStoreSession = func() (store.Session, bool) {
		if resolved == nil {
			return store.Session{}, false
		}
		return *resolved, true
	}
}

func TestStoreVerdictIgnoresOtherTools(t *testing.T) {
	for _, name := range []string{
		"Bash",
		"mcp__plugin_moxy_moxy__grit_branch",
		"mcp__plugin_spinclass_spinclass__list",
		"mcp__plugin_spinclass_spinclass__grit_unknown",
	} {
		if _, _, isStore := StoreVerdict(name, map[string]any{"subcommand": "log"}); isStore {
			t.Errorf("%s must not be a store tool", name)
		}
	}
}

func TestStoreVerdictFailsClosed(t *testing.T) {
	const tool = gritToolPrefix + "branch"
	t.Run("unresolved session", func(t *testing.T) {
		recorder := &recordingBackend{verdict: store.Allow}
		stubStore(t, recorder, nil)
		verdict, _, isStore := StoreVerdict(tool, map[string]any{"subcommand": "log"})
		if !isStore || verdict != store.Ask {
			t.Errorf("got (%v, store=%v), want ask", verdict, isStore)
		}
		if recorder.op.Tool != "" {
			t.Error("backend must not be consulted without a session")
		}
	})
	t.Run("missing subcommand", func(t *testing.T) {
		stubStore(t, &recordingBackend{verdict: store.Allow}, &store.Session{Dir: "/w", Branch: "b"})
		if verdict, _, _ := StoreVerdict(tool, map[string]any{}); verdict != store.Ask {
			t.Errorf("got %v, want ask", verdict)
		}
	})
	t.Run("subcommand wrong type", func(t *testing.T) {
		stubStore(t, &recordingBackend{verdict: store.Allow}, &store.Session{Dir: "/w", Branch: "b"})
		if verdict, _, _ := StoreVerdict(tool, map[string]any{"subcommand": 4}); verdict != store.Ask {
			t.Errorf("got %v, want ask", verdict)
		}
	})
	t.Run("empty principal through the real resolver", func(t *testing.T) {
		t.Setenv("CLOWN_SESSION_ID", "")
		verdict, _, isStore := StoreVerdict(tool, map[string]any{"subcommand": "log"})
		if !isStore || verdict != store.Ask {
			t.Errorf("got (%v, store=%v), want ask", verdict, isStore)
		}
	})
}

func TestStoreVerdictPassesSessionAndOpToBackend(t *testing.T) {
	recorder := &recordingBackend{verdict: store.Allow}
	resolved := store.Session{Dir: "/w", Branch: "glad-buckeye"}
	stubStore(t, recorder, &resolved)

	verdict, _, isStore := StoreVerdict(gritToolPrefix+"branch", map[string]any{"subcommand": "delete", "name": "x"})
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
	if verdict, _, _ := StoreVerdict(gritToolPrefix+"branch", map[string]any{"subcommand": "delete", "name": "x"}); verdict != store.Ask {
		t.Errorf("backend ask must stay ask, got %v", verdict)
	}
}

func TestStoreVerdictToolsWithoutSubcommand(t *testing.T) {
	recorder := &recordingBackend{verdict: store.Allow}
	stubStore(t, recorder, &store.Session{Dir: "/w", Branch: "b"})
	for _, tool := range []string{"commit", "merge", "clone"} {
		if verdict, _, isStore := StoreVerdict(gritToolPrefix+tool, map[string]any{"message": "m"}); !isStore || verdict != store.Allow {
			t.Errorf("%s: got (%v, store=%v)", tool, verdict, isStore)
		}
		if recorder.op.Tool != tool || recorder.op.Subcommand != "" {
			t.Errorf("%s: op = %+v", tool, recorder.op)
		}
	}
}
