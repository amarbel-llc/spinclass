package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/keyholdersweep"
)

// These tests run `sc keyholder-stop SESSION` against a LIVE session: a session
// record exists for the key (worker/kid, held and spawned by "spawner"), so the
// command resolves it through the session store, as an operator's call does, and
// the pair's key history is a live session's to keep. The older SESSION tests run
// on an empty store and only reach the <repo>/<branch> fallback.

type liveStop struct {
	r        *khMintingRunner
	repo     string
	base     string
	journal  string
	notified *[]notifyCall
}

func newLiveStop(t *testing.T) liveStop {
	t.Helper()
	r := khRemintEnv(t)
	notified := stubNotifyPrincipal(t)
	repo := khSessionOn(t)
	prev := keyholdersweep.NewLauncher
	keyholdersweep.NewLauncher = keyholderStatusLauncherFn
	t.Cleanup(func() { keyholdersweep.NewLauncher = prev })
	journal := filepath.Join(t.TempDir(), "journal.ndjson")
	prevJ := keyholderStopJournal
	keyholderStopJournal = func() string { return journal }
	t.Cleanup(func() { keyholderStopJournal = prevJ })
	return liveStop{r: r, repo: repo, base: keyholderStateBaseFn(), journal: journal, notified: notified}
}

// startOther starts a second session's pair on the same fake systemd and state
// root: a session with no record of its own.
func (e liveStop) startOther(t *testing.T, key string) {
	t.Helper()
	l, err := keyholderLauncherFn(keyholder.Tools{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Start(context.Background(), key); err != nil {
		t.Fatal(err)
	}
}

func (e liveStop) agentActive(key string) bool {
	e.r.mu.Lock()
	defer e.r.mu.Unlock()
	return e.r.active["spinclass-keyholder-agent-"+keyholder.Hash(key)+".service"]
}

// history reads a session's key history file as the raw JSON object.
func (e liveStop) history(t *testing.T, key string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(keyholder.LastPath(e.base, key))
	if err != nil {
		t.Fatalf("the key history of %s: %v", key, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestKeyholderStopSessionOfALiveSessionKeepsTheHistoryStoppedAndTheNextStartIsAnnounced(t *testing.T) {
	for _, form := range []string{"the session key", "the worktree name"} {
		t.Run(form, func(t *testing.T) {
			e := newLiveStop(t)
			stdout, _ := stubOperatorTerminal(t, true)
			khStart(t, e.repo, &bytes.Buffer{}) // worker/kid: a live session with a pair
			e.startOther(t, "s/other")          // another session's pair, same root
			oldFP := khFingerprints(t, e.base)
			otherFP, _ := e.history(t, "s/other")["fingerprint"].(string)

			target := "worker/kid"
			if form == "the worktree name" {
				st, err := session.FindByTarget("worker/kid")
				if err != nil {
					t.Fatal(err)
				}
				target = filepath.Base(st.WorktreePath)
				if target == "worker/kid" || !strings.HasSuffix(st.WorktreePath, "/.worktrees/"+target) {
					t.Fatalf("the fixture's worktree %q cannot be named by its basename", st.WorktreePath)
				}
			}
			if err := runKeyholderStop(context.Background(), keyholderStopParams{Session: target}); err != nil {
				t.Fatal(err)
			}
			// (i) the named session's pair is stopped, its directory gone.
			if e.agentActive("worker/kid") {
				t.Error("the named session's holder unit is still active")
			}
			if _, err := os.Lstat(keyholder.SessionDir(e.base, "worker/kid")); !os.IsNotExist(err) {
				t.Errorf("the named session's directory survived: %v", err)
			}
			// (ii) its history stays, marked stopped, with the last fingerprint.
			h := e.history(t, "worker/kid")
			if h["stopped"] != true || h["fingerprint"] != oldFP {
				t.Errorf("history = %v, want stopped with the last fingerprint %s", h, oldFP)
			}
			// (iv) another session's pair and history are untouched.
			if !e.agentActive("s/other") {
				t.Error("stopping one session stopped another session's pair")
			}
			if oh := e.history(t, "s/other"); oh["stopped"] == true || oh["fingerprint"] != otherFP {
				t.Errorf("another session's history changed: %v", oh)
			}
			if strings.Count(stdout.String(), "stopped ") != 1 {
				t.Errorf("output:\n%s", stdout.String())
			}
			b, err := os.ReadFile(e.journal)
			if err != nil {
				t.Fatal(err)
			}
			var rec keyholderJournalRecord
			if err := json.Unmarshal([]byte(strings.TrimSpace(string(b))), &rec); err != nil || rec.Session != "worker/kid" || rec.Action != "stop" || rec.Result != "stopped" {
				t.Errorf("journal = %s (%+v, %v), want one stop of worker/kid", b, rec, err)
			}

			// (iii) the next start of that session is the function `sc resume` calls
			// for a spawned worker: it mints a NEW key and announces old and new.
			var again bytes.Buffer
			if _, _, err := keyholderLaunchEnvUndo(os.Getenv("HOME"), e.repo, "worker/kid", true, &again); err != nil {
				t.Fatalf("the next start: %v", err)
			}
			newFP := khFingerprints(t, e.base)
			if newFP == oldFP {
				t.Fatal("the next start did not mint a new key")
			}
			for _, want := range []string{"worker/kid", oldFP, newFP, "NEW key"} {
				if !strings.Contains(again.String(), want) {
					t.Errorf("stderr %q lacks %q", again.String(), want)
				}
			}
			if len(*e.notified) != 1 || !strings.Contains((*e.notified)[0].message, oldFP) || !strings.Contains((*e.notified)[0].message, newFP) {
				t.Errorf("wakes = %+v, want one carrying both fingerprints", *e.notified)
			}
			if !e.agentActive("worker/kid") {
				t.Error("the next start left no running pair")
			}
		})
	}
}

// A first start of a session nobody stopped announces nothing: only the kept,
// stopped history makes a later start a change.
func TestAFirstStartOfAStoppedWorkerSessionWithNoHistoryAnnouncesNothing(t *testing.T) {
	e := newLiveStop(t)
	var first bytes.Buffer
	if _, _, err := keyholderLaunchEnvUndo(os.Getenv("HOME"), e.repo, "worker/kid", true, &first); err != nil {
		t.Fatal(err)
	}
	if first.Len() != 0 || len(*e.notified) != 0 {
		t.Errorf("a first mint announced something: stderr %q, wakes %+v", first.String(), *e.notified)
	}
}

// The answer to "does sc resume of the session re-key and announce it": a
// session whose pair was stopped by the operator (history kept) gets a new key
// and a notice the next time anything starts its pair, and the stop that
// ENDS the history (an orphan, or sc close) makes the start silent.
func TestAStopThatEndsTheHistoryMakesTheNextStartSilentAndAKeptOneAnnouncesIt(t *testing.T) {
	e := newLiveStop(t)
	stubOperatorTerminal(t, true)
	khStart(t, e.repo, &bytes.Buffer{})
	if err := runKeyholderStop(context.Background(), keyholderStopParams{Session: "worker/kid"}); err != nil {
		t.Fatal(err)
	}
	var kept bytes.Buffer
	if _, _, err := keyholderLaunchEnvUndo(os.Getenv("HOME"), e.repo, "worker/kid", true, &kept); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(kept.String(), "NEW key") {
		t.Fatalf("a start after a kept history is not announced: %q", kept.String())
	}
	l, _ := keyholderLauncherFn(keyholder.Tools{})
	if err := l.Stop(context.Background(), "worker/kid"); err != nil { // the close path: Stop ends the history
		t.Fatal(err)
	}
	var ended bytes.Buffer
	if _, _, err := keyholderLaunchEnvUndo(os.Getenv("HOME"), e.repo, "worker/kid", true, &ended); err != nil {
		t.Fatal(err)
	}
	if ended.Len() != 0 {
		t.Errorf("a start after a stop that ended the history announced %q", ended.String())
	}
}
