package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// spinclass#480: a grant is effective when recorded, so the recipient must be
// live, every grant and release is journaled, and the recipient and the
// child's other holders are woken.

// stubHandleRecipientLive makes every grant recipient count as live (or not,
// when err is non-nil).
func stubHandleRecipientLive(t *testing.T, err error) {
	t.Helper()
	orig := handleRecipientLive
	handleRecipientLive = func(string) error { return err }
	t.Cleanup(func() { handleRecipientLive = orig })
}

// stubHandleGrantEffects stubs what a grant does outside the child's record:
// the recipient counts as live and wakes are recorded instead of sent. The
// recorder is safe for concurrent grants (stubNotifyPrincipal's is not); read
// the calls only after every grant has returned.
func stubHandleGrantEffects(t *testing.T) *[]notifyCall {
	t.Helper()
	stubHandleRecipientLive(t, nil)
	var (
		mu    sync.Mutex
		calls []notifyCall
	)
	orig := notifyPrincipalFn
	notifyPrincipalFn = func(_ context.Context, target, from, message string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, notifyCall{target, from, message})
		return nil
	}
	t.Cleanup(func() { notifyPrincipalFn = orig })
	return &calls
}

func handleJournalPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.Getenv("XDG_STATE_HOME"), "spinclass", "handles", handleJournalName)
}

// readHandleJournal returns the journal's records, none when it does not exist.
func readHandleJournal(t *testing.T) []handleJournalRecord {
	t.Helper()
	f, err := os.Open(handleJournalPath(t))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var recs []handleJournalRecord
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r handleJournalRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("journal line %q does not parse: %v", sc.Text(), err)
		}
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return recs
}

// blockHandleJournal puts a file where the journal's directory belongs, so no
// journal line can be written.
func blockHandleJournal(t *testing.T) {
	t.Helper()
	dir := filepath.Dir(handleJournalPath(t))
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func notifyTargets(calls []notifyCall) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.target)
	}
	return out
}

func TestGrantSessionHandleRefusesARecipientThatIsNotLive(t *testing.T) {
	const caller = "caller-principal"
	wakes := stubHandleGrantEffects(t)
	repoPath, _ := childHandleFixture(t, caller, session.State{
		Holders:      []string{caller},
		HandleRights: map[string]string{caller: "observe,close,grant"},
	})
	stubHandleRecipientLive(t, errors.New("p9 is not a live principal on this host"))

	text, isErr := callGrant(t, `{"child":"worker/kid","to":"p9"}`)
	if !isErr || !strings.Contains(text, "p9 is not a live principal") {
		t.Fatalf("want the not-live refusal, got isErr=%v: %s", isErr, text)
	}
	if st := readKid(t, repoPath); !slices.Equal(st.Holders, []string{caller}) {
		t.Errorf("Holders = %v, a refused grant must record nothing", st.Holders)
	}
	if recs := readHandleJournal(t); len(recs) != 0 {
		t.Errorf("journal = %+v, a refused grant must journal nothing", recs)
	}
	if len(*wakes) != 0 {
		t.Errorf("wakes = %+v, a refused grant must wake nobody", *wakes)
	}
}

func TestGrantSessionHandleJournalsAndWakesTheRecipientAndOtherHolders(t *testing.T) {
	const caller = "caller-principal"
	wakes := stubHandleGrantEffects(t)
	repoPath, _ := childHandleFixture(t, caller, session.State{
		Holders:      []string{caller, "sibling"},
		HandleRights: map[string]string{caller: "observe,close,grant"},
	})

	text, isErr := callGrant(t, `{"child":"worker/kid","to":"p9"}`)
	if isErr {
		t.Fatalf("grant failed: %s", text)
	}
	if !strings.Contains(text, "effective now") || strings.Contains(text, "pending") {
		t.Errorf("result %q should say the grant is effective now", text)
	}
	if st := readKid(t, repoPath); !st.IsHolder("p9") {
		t.Errorf("Holders = %v, want p9 holding at once", st.Holders)
	}

	recs := readHandleJournal(t)
	if len(recs) != 1 {
		t.Fatalf("journal = %+v, want exactly one line", recs)
	}
	r := recs[0]
	if r.Act != "grant" || r.Child != "worker/kid" || r.Principal != "p9" || r.Rights != "observe,close" ||
		r.By != caller || r.Assurance != handleAssuranceSession || r.Time.IsZero() {
		t.Errorf("journal record = %+v, want a grant of observe,close to p9 by %s with session assurance", r, caller)
	}

	targets := notifyTargets(*wakes)
	slices.Sort(targets)
	if !slices.Equal(targets, []string{"p9", "sibling"}) {
		t.Errorf("woke %v, want the recipient and the other holder, never the granter", targets)
	}
	for _, c := range *wakes {
		if c.target == "p9" && !strings.Contains(c.message, "you now hold worker/kid") {
			t.Errorf("recipient's wake %q should tell it what it now holds", c.message)
		}
	}
}

func TestGrantSessionHandleRefusedWhenTheJournalCannotBeWritten(t *testing.T) {
	const caller = "caller-principal"
	wakes := stubHandleGrantEffects(t)
	repoPath, _ := childHandleFixture(t, caller, session.State{
		Holders:      []string{caller},
		HandleRights: map[string]string{caller: "observe,close,grant"},
	})
	before := readKid(t, repoPath)
	blockHandleJournal(t)

	text, isErr := callGrant(t, `{"child":"worker/kid","to":"p9"}`)
	if !isErr || !strings.Contains(text, "could not be journaled") {
		t.Fatalf("want the journal refusal, got isErr=%v: %s", isErr, text)
	}
	after := readKid(t, repoPath)
	if !slices.Equal(after.Holders, before.Holders) || after.HandleRights["p9"] != "" {
		t.Errorf("holders %v rights %v; the child must be unchanged", after.Holders, after.HandleRights)
	}
	if len(*wakes) != 0 {
		t.Errorf("wakes = %+v, a refused grant must wake nobody", *wakes)
	}
}

func TestReleaseSessionHandleJournalsAndAJournalFailureDoesNotStopIt(t *testing.T) {
	t.Run("journaled", func(t *testing.T) {
		const caller = "caller-principal"
		repoPath, _ := childHandleFixture(t, caller, session.State{Holders: []string{caller, "sibling"}})

		if text, isErr := callRelease(t, `{"child":"worker/kid"}`); isErr || strings.Contains(text, "warning") {
			t.Fatalf("release: isErr=%v %s", isErr, text)
		}
		if st := readKid(t, repoPath); st.IsHolder(caller) {
			t.Errorf("Holders = %v, the caller released", st.Holders)
		}
		recs := readHandleJournal(t)
		if len(recs) != 1 || recs[0].Act != "release" || recs[0].Principal != caller || recs[0].By != caller ||
			recs[0].Child != "worker/kid" || recs[0].Assurance != handleAssuranceSession {
			t.Errorf("journal = %+v, want one release of %s by itself", recs, caller)
		}
	})

	t.Run("journal unwritable", func(t *testing.T) {
		const caller = "caller-principal"
		repoPath, _ := childHandleFixture(t, caller, session.State{Holders: []string{caller, "sibling"}})
		blockHandleJournal(t)

		text, isErr := callRelease(t, `{"child":"worker/kid"}`)
		if isErr {
			t.Fatalf("a release must stand when it cannot be journaled, got error: %s", text)
		}
		if !strings.Contains(text, "warning: the release stands but was not journaled") {
			t.Errorf("result %q should carry the journal warning", text)
		}
		if st := readKid(t, repoPath); st.IsHolder(caller) {
			t.Errorf("Holders = %v, the release must still be written", st.Holders)
		}
	})
}

func TestOperatorHandleGrantJournalsWithTTYAssuranceAndWakesTheRecipient(t *testing.T) {
	stdout, _ := stubOperatorHandle(t, true, "y\n", coordinatorLive)
	wakes := stubNotifyPrincipal(t)
	repoPath := handleFixture(t)

	if err := runOperatorHandle(operatorHandleGrant, operatorHandleParams{Target: []string{"worker/kid"}, To: handleRecipient}); err != nil {
		t.Fatalf("err = %v (stdout %q)", err, stdout.String())
	}
	if !readKid(t, repoPath).IsHolder(handleRecipient) {
		t.Fatal("the recipient must hold at once")
	}

	recs := readHandleJournal(t)
	if len(recs) != 1 {
		t.Fatalf("journal = %+v, want exactly one line", recs)
	}
	r := recs[0]
	if r.Act != "grant" || r.Child != "worker/kid" || r.Principal != handleRecipient || r.Rights != "observe,close" ||
		r.By != session.HandleOperator || r.Assurance != handleAssuranceTTY {
		t.Errorf("journal record = %+v, want a grant to %s by operator with tty assurance", r, handleRecipient)
	}

	targets := notifyTargets(*wakes)
	slices.Sort(targets)
	if !slices.Equal(targets, []string{handleRecipient, "orig", "peer"}) {
		t.Errorf("woke %v, want the recipient and the existing holders", targets)
	}
}

func TestOperatorHandleGrantRefusedWhenTheJournalCannotBeWritten(t *testing.T) {
	stubOperatorHandle(t, true, "y\n", coordinatorLive)
	wakes := stubNotifyPrincipal(t)
	repoPath := handleFixture(t)
	blockHandleJournal(t)

	if err := runOperatorHandle(operatorHandleGrant, operatorHandleParams{Target: []string{"worker/kid"}, To: handleRecipient}); err == nil {
		t.Fatal("a grant that cannot be journaled must fail")
	}
	if readKid(t, repoPath).IsHolder(handleRecipient) {
		t.Error("a grant that cannot be journaled must not be written")
	}
	if len(*wakes) != 0 {
		t.Errorf("wakes = %+v, want none", *wakes)
	}
}
