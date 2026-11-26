package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

const khPathsOnly = "[keyholder]\nfibby = \"/nix/store/aaa-fibby/bin/fibby\"\npiggy = \"/nix/store/bbb-piggy/bin/piggy\"\n"

// khSwitchWord writes the deploy-free switch file under dir (a home) with the
// given raw body.
func khSwitchWord(t *testing.T, dir, body string) string {
	t.Helper()
	path := sweatfileio.KeyholderSwitchPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// off is the kill switch: it beats a layer's on, and the file is read at the
// account database's home even though the caller's HOME is another directory.
func TestSwitchFileOffBeatsALayersOnAndIsReadAtThePasswdHome(t *testing.T) {
	callerHome, _, engRepo := khTree(t, khOn, "", "")
	passwd := t.TempDir()
	khPasswdHomeIs(t, passwd, nil)
	if sw, err := resolveKeyholderSwitch(callerHome, engRepo); err != nil || !sw.On || sw.Source() != "layer" {
		t.Fatalf("control (no file): (%+v, %v), want on from a layer", sw, err)
	}
	path := khSwitchWord(t, passwd, "off\n")
	sw, err := resolveKeyholderSwitch(callerHome, engRepo)
	if err != nil {
		t.Fatal(err)
	}
	if sw.On || sw.File != sweatfileio.KeyholderSwitchOff || sw.FilePath != path || sw.Source() != "switch file" {
		t.Errorf("file off, layer on, caller home elsewhere = %+v, want off from the switch file at %s", sw, path)
	}
}

// The off file also stops a launch: nothing is built or started.
func TestSwitchFileOffStartsNoPairForANewLaunch(t *testing.T) {
	home, repo := khHome(t, khOn)
	passwd := t.TempDir()
	khPasswdHomeIs(t, passwd, nil)
	khSwitchWord(t, passwd, "off\n")
	f := newKhFake(t)
	built := khUseFake(t, f)
	env, err := keyholderSessionEnv(home, repo, "repo/a", &bytes.Buffer{})
	if err != nil || env != nil || len(f.started) != 0 || *built != 0 {
		t.Fatalf("file off: (%v, %v), started %v, built %d: want nothing", env, err, f.started, *built)
	}
}

// on turns it on from the file, with the programs still from the trusted layers.
func TestSwitchFileOnTurnsItOnWithTheProgramsFromTheLayers(t *testing.T) {
	home, workRepo, _ := khTree(t, khPathsOnly, "", "")
	passwd := t.TempDir()
	khPasswdHomeIs(t, passwd, nil)
	if sw, err := resolveKeyholderSwitch(home, workRepo); err != nil || sw.On {
		t.Fatalf("control: paths but no mode = (%+v, %v), want off", sw, err)
	}
	khSwitchWord(t, passwd, "on")
	sw, err := resolveKeyholderSwitch(home, workRepo)
	if err != nil || !sw.On || sw.Fibby != "/nix/store/aaa-fibby/bin/fibby" || sw.Piggy != "/nix/store/bbb-piggy/bin/piggy" || sw.Source() != "switch file" {
		t.Errorf("file on with programs = (%+v, %v), want on from the switch file with the layers' programs", sw, err)
	}
}

// on with no trusted layer supplying fibby and piggy refuses, and says so.
func TestSwitchFileOnWithoutProgramsRefuses(t *testing.T) {
	home, workRepo, _ := khTree(t, "", "", "")
	passwd := t.TempDir()
	khPasswdHomeIs(t, passwd, nil)
	path := khSwitchWord(t, passwd, "on\n")
	_, err := resolveKeyholderSwitch(home, workRepo)
	if err == nil || !strings.Contains(err.Error(), "fibby and piggy") || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "sc keyholder-switch default") {
		t.Fatalf("file on without programs = %v, want a refusal naming fibby and piggy, the path and the way out", err)
	}
	// And through a real launch: refused, nothing started.
	h, repo := khHome(t, "")
	f := newKhFake(t)
	khUseFake(t, f)
	if env, lerr := keyholderSessionEnv(h, repo, "repo/a", &bytes.Buffer{}); lerr == nil || env != nil || len(f.started) != 0 {
		t.Errorf("a launch under file-on-without-programs = (%v, %v), started %v: want a refusal", env, lerr, f.started)
	}
}

// A file that is not one of the three words, or exists and cannot be read,
// refuses every launch.
func TestAGarbageOrUnreadableSwitchFileRefusesLaunches(t *testing.T) {
	home, repo := khHome(t, khOn)
	passwd := t.TempDir()
	khPasswdHomeIs(t, passwd, nil)
	path := khSwitchWord(t, passwd, "maybe\n")
	f := newKhFake(t)
	khUseFake(t, f)
	env, err := keyholderSessionEnv(home, repo, "repo/a", &bytes.Buffer{})
	if err == nil || env != nil || len(f.started) != 0 || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "remove the file") {
		t.Fatalf("a garbage file = (%v, %v), started %v: want a refusal naming the path and the way out", env, err, f.started)
	}
	if os.Getuid() == 0 {
		return
	}
	if err := os.WriteFile(path, []byte("off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, err := resolveKeyholderSwitch(home, repo); err == nil {
		t.Error("an unreadable switch file that exists did not refuse")
	}
}

// With no file the answer is exactly the layers': the same struct, for every
// layout, down to the zero File fields. default changes nothing but says so.
func TestAbsentSwitchFileLeavesTheLayersExactlyAsTheyWere(t *testing.T) {
	for name, layout := range map[string][3]string{
		"nothing set":        {"", "", ""},
		"global on":          {khOn, "", ""},
		"global off":         {khOff, "", ""},
		"nearer off":         {khOn, khOff, ""},
		"paths without mode": {khPathsOnly, "", ""},
	} {
		home, workRepo, _ := khTree(t, layout[0], layout[1], layout[2])
		khPasswdHomeIs(t, t.TempDir(), nil)
		want, werr := resolveKeyholderLayersAndPasswd(home, workRepo)
		got, gerr := resolveKeyholderSwitch(home, workRepo)
		if (werr == nil) != (gerr == nil) || !reflect.DeepEqual(want, got) {
			t.Errorf("%s: file absent changed the answer: layers (%+v, %v), with the switch (%+v, %v)", name, want, werr, got, gerr)
		}
		if got.File != "" || got.FilePath != "" {
			t.Errorf("%s: an absent file left traces: %+v", name, got)
		}
	}
	home, workRepo, _ := khTree(t, khOn, "", "")
	passwd := t.TempDir()
	khPasswdHomeIs(t, passwd, nil)
	khSwitchWord(t, passwd, "default\n")
	sw, err := resolveKeyholderSwitch(home, workRepo)
	if err != nil || !sw.On || sw.File != sweatfileio.KeyholderSwitchDefault || sw.Source() != "layer" {
		t.Errorf("default with a layer on = (%+v, %v), want on from the layer and File default", sw, err)
	}
}

// The environment cannot move the file: switch files at the places $HOME and
// $XDG_CONFIG_HOME name are not read; only the account database's home counts.
func TestTheEnvironmentCannotRedirectTheSwitchFile(t *testing.T) {
	home, workRepo, _ := khTree(t, khOn, "", "")
	envHome, envConfig := t.TempDir(), t.TempDir()
	khSwitchWord(t, envHome, "off\n")
	if err := os.MkdirAll(filepath.Join(envConfig, "spinclass"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envConfig, "spinclass", "keyholder-switch"), []byte("off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", envHome)
	t.Setenv("XDG_CONFIG_HOME", envConfig)
	t.Setenv("SPINCLASS_KEYHOLDER_SWITCH", filepath.Join(envHome, ".config", "spinclass", "keyholder-switch"))
	khPasswdHomeIs(t, t.TempDir(), nil)
	sw, err := resolveKeyholderSwitch(home, workRepo)
	if err != nil || !sw.On || sw.File != "" {
		t.Fatalf("switch files at the environment's places = (%+v, %v), want them ignored (on from the layer, no file)", sw, err)
	}
}

// An account database that cannot answer leaves the file unread: the layers'
// own handling of that failure is unchanged.
func TestNoPasswdHomeMeansTheSwitchFileIsNotRead(t *testing.T) {
	var sw keyholderSwitch
	khPasswdHomeIs(t, "", os.ErrNotExist)
	if err := applyKeyholderSwitchFile(&sw); err != nil || sw.File != "" {
		t.Errorf("no passwd home = (%+v, %v), want untouched", sw, err)
	}
}

func switchCmdFixture(t *testing.T) (passwd string, journal string) {
	t.Helper()
	passwd = t.TempDir()
	khPasswdHomeIs(t, passwd, nil)
	journal = filepath.Join(t.TempDir(), "journal.ndjson")
	prev := keyholderStopJournal
	keyholderStopJournal = func() string { return journal }
	t.Cleanup(func() { keyholderStopJournal = prev })
	return passwd, journal
}

// Setting it: atomic file, journalled with old and new, and the output says that
// no running pair is touched and states the limit once.
func TestKeyholderSwitchSetsTheFileJournalsAndSaysItStopsNoPair(t *testing.T) {
	passwd, journal := switchCmdFixture(t)
	stdout, _ := stubOperatorTerminal(t, true)
	if err := runKeyholderSwitch(context.Background(), keyholderSwitchParams{Value: "off"}); err != nil {
		t.Fatal(err)
	}
	v, present, err := sweatfileio.ReadKeyholderSwitch(passwd)
	if err != nil || !present || v != sweatfileio.KeyholderSwitchOff {
		t.Fatalf("after `off` the file reads (%q, %v, %v)", v, present, err)
	}
	out := stdout.String()
	for _, want := range []string{"absent -> off", "stops no running pair", "sc keyholder-stop --all", "#515"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "#515") != 1 || strings.Count(out, "sc keyholder-stop --all") != 1 {
		t.Errorf("the limit and the stop advice are said once, got:\n%s", out)
	}
	if err := runKeyholderSwitch(context.Background(), keyholderSwitchParams{Value: "default"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(journal)
	if err != nil {
		t.Fatalf("no journal: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("journal has %d lines, want 2: %s", len(lines), b)
	}
	var rec keyholderJournalRecord
	if err := json.Unmarshal([]byte(lines[1]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Action != "switch" || rec.Old != "off" || rec.New != "default" || rec.Actor != "tty" || rec.Uid != os.Getuid() {
		t.Errorf("journal record = %+v, want a switch off -> default by this uid at a tty", rec)
	}
}

// The operator's act: with no terminal nothing is written.
func TestKeyholderSwitchRefusesWithoutATerminalAndWritesNothing(t *testing.T) {
	passwd, journal := switchCmdFixture(t)
	stubOperatorTerminal(t, false)
	err := runKeyholderSwitch(context.Background(), keyholderSwitchParams{Value: "off"})
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("no terminal = %v, want a refusal naming the terminals", err)
	}
	if _, serr := os.Lstat(sweatfileio.KeyholderSwitchPath(passwd)); serr == nil {
		t.Error("a refused set wrote the file")
	}
	if _, serr := os.Stat(journal); serr == nil {
		t.Error("a refused set wrote a journal entry")
	}
}

// A value that is not one of the three words is refused before anything is
// written.
func TestKeyholderSwitchRefusesAWordItDoesNotKnow(t *testing.T) {
	passwd, _ := switchCmdFixture(t)
	stubOperatorTerminal(t, true)
	if err := runKeyholderSwitch(context.Background(), keyholderSwitchParams{Value: "maybe"}); err == nil || !strings.Contains(err.Error(), "on, off or default") {
		t.Fatalf("a bad word = %v, want a refusal naming the three words", err)
	}
	if _, serr := os.Lstat(sweatfileio.KeyholderSwitchPath(passwd)); serr == nil {
		t.Error("a refused word wrote the file")
	}
}

// The bare command prints the effective mode and its source, needs no terminal.
func TestBareKeyholderSwitchPrintsTheEffectiveModeAndItsSource(t *testing.T) {
	passwd, _ := switchCmdFixture(t)
	stdout, _ := stubOperatorTerminal(t, false)
	if err := runKeyholderSwitch(context.Background(), keyholderSwitchParams{}); err != nil {
		t.Fatal(err)
	}
	if out := stdout.String(); !strings.Contains(out, "key holder: off (source: none)") || !strings.Contains(out, sweatfileio.KeyholderSwitchPath(passwd)) {
		t.Errorf("no file, no layer: %q, want 'off (source: none)' and the path", out)
	}
	khSwitchWord(t, passwd, "off\n")
	stdout.Reset()
	if err := runKeyholderSwitch(context.Background(), keyholderSwitchParams{}); err != nil {
		t.Fatal(err)
	}
	if out := stdout.String(); !strings.Contains(out, "key holder: off (source: switch file)") || !strings.Contains(out, "says off") {
		t.Errorf("file off: %q, want 'off (source: switch file)'", out)
	}
	khSwitchWord(t, passwd, "garbage\n")
	stdout.Reset()
	if err := runKeyholderSwitch(context.Background(), keyholderSwitchParams{}); err != nil {
		t.Fatal(err)
	}
	if out := stdout.String(); !strings.Contains(out, "REFUSED") || !strings.Contains(out, "sc keyholder-switch default") {
		t.Errorf("garbage file: %q, want the refusal and the way out", out)
	}
}

// host-load has something to say only when the key holder is on, a switch file
// exists, or a launch would be refused: a host that never used it renders as before.
func TestHostLoadShowsTheKeyHolderModeOnlyWhenThereIsSomethingToSay(t *testing.T) {
	passwd, _ := switchCmdFixture(t)
	if (keyholderEffective{Source: "none"}).worthShowing() {
		t.Error("a host that never used the key holder has something to show")
	}
	khSwitchWord(t, passwd, "off\n")
	e := describeKeyholderSwitch()
	if !e.worthShowing() || e.Source != "switch file" || e.On {
		t.Errorf("file off = %+v, want shown, from the switch file, off", e)
	}
}

func TestKeyholderSwitchIsACLIOnlyCommand(t *testing.T) {
	cmd, ok := buildApp().GetCommand("keyholder-switch")
	if !ok {
		t.Fatal("sc keyholder-switch is not registered")
	}
	if cmd.Run != nil || cmd.RunCLI == nil {
		t.Fatalf("must be RunCLI-only (Run=%v RunCLI=%v)", cmd.Run != nil, cmd.RunCLI != nil)
	}
}
