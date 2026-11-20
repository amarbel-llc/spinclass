package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"code.linenisgreat.com/purse-first/libs/go-mcp/server"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// `sc handle-rights` is the operator's re-stamp of an existing holder's rights:
// a spawn records its spawner's rights as the vocabulary stood that day, so a
// right added later reaches an existing handle only through this verb. It only
// ever adds, needs a terminal, journals before it writes, and wakes the holder.

// preStopFullSet is the spawner's full set as spawn recorded it before the stop
// and start rights existed.
const preStopFullSet = "observe,close,grant,instruct,cap,merge,check,restart,jobs"

const fleetAdd = "jobs,stop,stop-now,start,park"

// spawnerFixture stages worker/kid spawned by "orig", who holds the pre-stop
// full set explicitly, beside "peer" holding the default grant.
func spawnerFixture(t *testing.T, mutate func(*session.State)) (repoPath string) {
	t.Helper()
	st := session.State{
		SpawnedByPrincipal: "orig",
		Holders:            []string{"orig", "peer"},
		HandleRights:       map[string]string{"orig": preStopFullSet, "peer": "observe,close"},
	}
	if mutate != nil {
		mutate(&st)
	}
	repoPath, _ = restartChildFixture(t, "operator-shell", st)
	return repoPath
}

func TestHandleRightsRefusesWithoutATerminal(t *testing.T) {
	stubOperatorHandle(t, false, "y\n", nil)
	repoPath := spawnerFixture(t, nil)
	err := runOperatorHandleRights(operatorHandleRightsParams{Target: []string{"worker/kid"}, Spawners: true, Add: "stop"})
	if err == nil || !strings.Contains(err.Error(), "terminals") {
		t.Fatalf("err = %v, want the terminal refusal", err)
	}
	if got := readKid(t, repoPath).HandleRights["orig"]; got != preStopFullSet {
		t.Errorf("rights = %q, nothing may be written", got)
	}
}

// --holder adds the named rights to that holder's entry and keeps every right
// it held; nobody else's entry, the holders or the provenance change.
func TestHandleRightsAddsToANamedHolderAndKeepsWhatItHeld(t *testing.T) {
	stdout, stderr := stubOperatorHandle(t, true, "y\n", nil)
	repoPath := handleFixture(t)

	if err := runOperatorHandleRights(operatorHandleRightsParams{Target: []string{"worker/kid"}, Holder: "peer", Add: "stop,start"}); err != nil {
		t.Fatalf("err = %v (stdout %q)", err, stdout.String())
	}
	st := readKid(t, repoPath)
	if got := st.HandleRights["peer"]; got != "observe,close,stop,start" {
		t.Errorf("peer rights = %q, want observe,close,stop,start", got)
	}
	if got := st.HandleRights["orig"]; got != "observe,close" {
		t.Errorf("orig rights = %q, another holder must not change", got)
	}
	if !slices.Equal(st.Holders, []string{"orig", "peer"}) || len(st.PendingHandles) != 0 {
		t.Errorf("holders %v pending %v; a re-stamp must not change who holds", st.Holders, st.PendingHandles)
	}
	if !strings.Contains(stdout.String(), "ok 1 - worker/kid") || !strings.Contains(stdout.String(), "add stop,start to peer") {
		t.Errorf("stdout = %q, want one ok line naming what was added", stdout.String())
	}
	for _, want := range []string{"worker/kid: peer observe,close -> observe,close,stop,start", "right stop:", "right start:", "apply to 1 session(s)? [y/N]"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, missing %q", stderr.String(), want)
		}
	}
}

// It never narrows: rights already held are a reported no-op, and the record
// is left alone, with no prompt.
func TestHandleRightsNeverRemovesAndIsANoOpWhenAllAreHeld(t *testing.T) {
	stdout, stderr := stubOperatorHandle(t, true, "", nil)
	repoPath := handleFixture(t)
	if err := runOperatorHandleRights(operatorHandleRightsParams{Target: []string{"worker/kid"}, Holder: "peer", Add: "observe"}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := readKid(t, repoPath).HandleRights["peer"]; got != "observe,close" {
		t.Errorf("rights = %q, want them unchanged", got)
	}
	if !strings.Contains(stdout.String(), "nothing to add") || strings.Contains(stderr.String(), "[y/N]") {
		t.Errorf("stdout %q stderr %q; want a no-op line and no prompt", stdout.String(), stderr.String())
	}
	if recs := readHandleJournal(t); len(recs) != 0 {
		t.Errorf("journal = %+v, a no-op journals nothing", recs)
	}
}

// --spawners re-stamps the recorded spawner only, never another holder: the
// operator's one fleet-wide run.
func TestHandleRightsSpawnersTouchesOnlyTheRecordedSpawner(t *testing.T) {
	stdout, _ := stubOperatorHandle(t, true, "y\n", nil)
	repoPath := spawnerFixture(t, nil)

	if err := runOperatorHandleRights(operatorHandleRightsParams{Target: []string{"worker/kid"}, Spawners: true, Add: fleetAdd}); err != nil {
		t.Fatalf("err = %v (stdout %q)", err, stdout.String())
	}
	st := readKid(t, repoPath)
	if got := st.HandleRights["orig"]; got != session.AllHandleRights {
		t.Errorf("spawner rights = %q, want the full current set %q", got, session.AllHandleRights)
	}
	if got := st.HandleRights["peer"]; got != "observe,close" {
		t.Errorf("peer rights = %q; --spawners must not touch a non-spawner holder", got)
	}
}

// The skips are ok lines, not failures: a spawner with no explicit entry (the
// implicit rule already gives the current set), a released spawner, and a
// session with no recorded spawner.
func TestHandleRightsSpawnersSkipsWhatItMustNotStamp(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*session.State)
		skip   string
	}{
		{"implicit spawner", func(s *session.State) { delete(s.HandleRights, "orig") }, "no explicit rights entry"},
		{"released spawner", func(s *session.State) { s.SpawnerReleased = true }, "released"},
		{"spawner no longer a holder", func(s *session.State) { s.Holders = []string{"peer"} }, "not a holder"},
		{"no recorded spawner", func(s *session.State) { s.SpawnedByPrincipal = "" }, "no recorded spawner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _ := stubOperatorHandle(t, true, "y\n", nil)
			repoPath := spawnerFixture(t, tc.mutate)
			before := readKid(t, repoPath).HandleRights
			if err := runOperatorHandleRights(operatorHandleRightsParams{Target: []string{"worker/kid"}, Spawners: true, Add: fleetAdd}); err != nil {
				t.Fatalf("err = %v (stdout %q); a skip is not a failure", err, stdout.String())
			}
			if after := readKid(t, repoPath).HandleRights; !mapsEqual(before, after) {
				t.Errorf("rights %v -> %v; a skipped session must not change", before, after)
			}
			if !strings.Contains(stdout.String(), "skipped: ") || !strings.Contains(stdout.String(), tc.skip) {
				t.Errorf("stdout = %q, want a skip naming %q", stdout.String(), tc.skip)
			}
		})
	}
}

// --holder must name a current holder; a stranger is refused and nothing is
// written. (A pending grant in an old record is promoted to a holder when the
// record is read, spinclass#480, so there is no pending case to refuse.)
func TestHandleRightsRefusesANonHolder(t *testing.T) {
	stdout, _ := stubOperatorHandle(t, true, "y\n", nil)
	repoPath := spawnerFixture(t, nil)
	err := runOperatorHandleRights(operatorHandleRightsParams{Target: []string{"worker/kid"}, Holder: "stranger", Add: "stop"})
	if err == nil || !strings.Contains(stdout.String(), "not ok 1 - worker/kid") || !strings.Contains(stdout.String(), "not a holder") {
		t.Errorf("err = %v stdout %q; want a not-ok line naming the refusal", err, stdout.String())
	}
	if _, has := readKid(t, repoPath).HandleRights["stranger"]; has {
		t.Error("an entry was written for a non-holder")
	}
}

// The journal line is written before the record: act rights-add, the new full
// set, the operator marker, tty assurance, and what was added and held before.
func TestHandleRightsJournalsTheAdditionAndWakesTheHolders(t *testing.T) {
	stubOperatorHandle(t, true, "y\n", nil)
	calls := stubNotifyPrincipal(t)
	spawnerFixture(t, nil)

	if err := runOperatorHandleRights(operatorHandleRightsParams{Target: []string{"worker/kid"}, Spawners: true, Add: fleetAdd}); err != nil {
		t.Fatalf("err = %v", err)
	}
	recs := readHandleJournal(t)
	if len(recs) != 1 {
		t.Fatalf("journal = %+v, want one line", recs)
	}
	r := recs[0]
	if r.Act != "rights-add" || r.Child != "worker/kid" || r.Principal != "orig" || r.Rights != session.AllHandleRights ||
		r.By != session.HandleOperator || r.Assurance != handleAssuranceTTY {
		t.Errorf("journal line = %+v", r)
	}
	if !strings.Contains(r.Note, "added stop,stop-now,start,park") || !strings.Contains(r.Note, "was "+preStopFullSet) {
		t.Errorf("note = %q, want what was added and what was held", r.Note)
	}
	if targets := notifyTargets(*calls); !slices.Contains(targets, "orig") || !slices.Contains(targets, "peer") {
		t.Errorf("woke %v, want the re-stamped holder and the other holders", targets)
	}
}

// A journal that cannot be written refuses the re-stamp (fail-closed, as a
// grant is).
func TestHandleRightsRefusesWhenTheJournalCannotBeWritten(t *testing.T) {
	stdout, _ := stubOperatorHandle(t, true, "y\n", nil)
	repoPath := spawnerFixture(t, nil)
	blockHandleJournal(t)
	err := runOperatorHandleRights(operatorHandleRightsParams{Target: []string{"worker/kid"}, Spawners: true, Add: "stop"})
	if err == nil || !strings.Contains(stdout.String(), "not ok 1 - worker/kid") || !strings.Contains(stdout.String(), "journal") {
		t.Fatalf("err = %v stdout %q; want the journal refusal", err, stdout.String())
	}
	if got := readKid(t, repoPath).HandleRights["orig"]; got != preStopFullSet {
		t.Errorf("rights = %q, nothing may be written without a journal line", got)
	}
}

func TestHandleRightsDryRunWritesNothingAndAsksNothing(t *testing.T) {
	stdout, stderr := stubOperatorHandle(t, true, "y\n", nil)
	repoPath := spawnerFixture(t, nil)
	if err := runOperatorHandleRights(operatorHandleRightsParams{AllOnHost: true, Spawners: true, Add: fleetAdd, DryRun: true}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := readKid(t, repoPath).HandleRights["orig"]; got != preStopFullSet {
		t.Errorf("rights = %q, a dry run must not write", got)
	}
	if !strings.Contains(stdout.String(), "(dry run)") || strings.Contains(stderr.String(), "[y/N]") {
		t.Errorf("stdout %q stderr %q; want a dry-run line and no prompt", stdout.String(), stderr.String())
	}
	if recs := readHandleJournal(t); len(recs) != 0 {
		t.Errorf("journal = %+v, a dry run journals nothing", recs)
	}
}

func TestHandleRightsDecliningTheBatchChangesNothing(t *testing.T) {
	stdout, _ := stubOperatorHandle(t, true, "n\n", nil)
	repoPath := spawnerFixture(t, nil)
	err := runOperatorHandleRights(operatorHandleRightsParams{Target: []string{"worker/kid"}, Spawners: true, Add: "stop"})
	if err == nil || !strings.Contains(stdout.String(), declinedAtThePrompt) {
		t.Errorf("err = %v stdout %q; want a nonzero exit and the declined line", err, stdout.String())
	}
	if got := readKid(t, repoPath).HandleRights["orig"]; got != preStopFullSet {
		t.Errorf("rights = %q after a decline", got)
	}
}

// The argument shape is refused before anything is judged.
func TestHandleRightsRefusesABadInvocation(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    operatorHandleRightsParams
		want string
	}{
		{"no add", operatorHandleRightsParams{Target: []string{"worker/kid"}, Spawners: true}, "--add"},
		{"unknown right", operatorHandleRightsParams{Target: []string{"worker/kid"}, Spawners: true, Add: "stop,teleport"}, "teleport"},
		{"both holder and spawners", operatorHandleRightsParams{Target: []string{"worker/kid"}, Holder: "peer", Spawners: true, Add: "stop"}, "--holder"},
		{"neither holder nor spawners", operatorHandleRightsParams{Target: []string{"worker/kid"}, Add: "stop"}, "--holder"},
		{"both targets and all-on-host", operatorHandleRightsParams{Target: []string{"worker/kid"}, AllOnHost: true, Spawners: true, Add: "stop"}, "--all-on-host"},
		{"no sessions", operatorHandleRightsParams{Spawners: true, Add: "stop"}, "--all-on-host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubOperatorHandle(t, true, "y\n", nil)
			repoPath := spawnerFixture(t, nil)
			err := runOperatorHandleRights(tc.p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a refusal naming %q", err, tc.want)
			}
			if got := readKid(t, repoPath).HandleRights["orig"]; got != preStopFullSet {
				t.Errorf("rights = %q, nothing may be written", got)
			}
		})
	}
}

// Every right past the default grant is listed in the operator prompts with
// what it lets the holder do, so a right added to the vocabulary cannot slip
// through a prompt unexplained.
func TestEveryNonDefaultRightIsExplainedInTheOperatorPrompt(t *testing.T) {
	def, _ := session.ParseHandleRights(session.DefaultHandleRights)
	for _, r := range session.HandleRightNames {
		if def[r] {
			continue
		}
		if dangerousHandleRights[r] == "" {
			t.Errorf("right %q has no explanation in dangerousHandleRights", r)
		}
	}
}

func TestHandleRightsIsCLIOnly(t *testing.T) {
	app := buildApp()
	cmd, ok := app.GetCommand("handle-rights")
	if !ok || cmd.RunCLI == nil || cmd.Run != nil {
		t.Fatalf("handle-rights: registered=%v; want a CLI-only command (RunCLI, no Run)", ok)
	}
	registry := server.NewToolRegistryV1()
	app.RegisterMCPToolsV1(registry)
	tools, err := registry.ListTools(context.Background())
	if err != nil || len(tools) == 0 {
		t.Fatalf("tools: %d, %v: the assertion would be vacuous", len(tools), err)
	}
	for _, tool := range tools {
		if tool.Name == "handle-rights" {
			t.Error("handle-rights must not be an MCP tool")
		}
	}
	var p operatorHandleRightsParams
	if err := json.Unmarshal([]byte(`{"target":["a/b"],"add":"stop","holder":"k","spawners":true,"all-on-host":true,"dry-run":true}`), &p); err != nil ||
		p.Add != "stop" || p.Holder != "k" || !p.Spawners || !p.AllOnHost || !p.DryRun {
		t.Errorf("param decoding: %+v, %v", p, err)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
