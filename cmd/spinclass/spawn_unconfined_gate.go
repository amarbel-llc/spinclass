package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
	"golang.org/x/term"
)

// The spawn-session MCP tool's `unconfined` opt-out always asks through the
// PreToolUse floor (perms.AlwaysAsk). `sc spawn --unconfined` never reaches that
// hook, so the CLI asks the human itself: a terminal on stdin and stdout, a
// typed confirmation, and a log line per attempt. Under the same uid this is a
// speed bump, not a boundary (FDR 0035 "A confined spawn is not always-ask").

// Seams for command tests.
var (
	unconfinedSpawnIsTerminal = func() bool {
		return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	}
	unconfinedSpawnConfirmIn = func() io.Reader { return os.Stdin }
	unconfinedSpawnStderr    = func() io.Writer { return os.Stderr }
	unconfinedSpawnNow       = time.Now
)

const (
	unconfinedSpawnLogName = "spawn-unconfined.log"
	// unconfinedSpawnConfirmWord is what the operator must type. A whole word
	// rather than y, so a stray keystroke or a scripted "y" does not approve it.
	unconfinedSpawnConfirmWord = "unconfined"
	unconfinedSpawnNoTerminal  = "--unconfined needs a human at a terminal: stdin and stdout must both be terminals, " +
		"so a pipe, a recipe or a program cannot opt a worker out of confinement"
)

// unconfinedSpawnRecord is one line of the log. The brief is not recorded.
type unconfinedSpawnRecord struct {
	Time    string `json:"time"`
	Cwd     string `json:"cwd"`
	Repo    string `json:"repo"`
	Outcome string `json:"outcome"`
}

// confirmUnconfinedSpawn returns nil only when a human at a terminal typed the
// confirmation word. Every attempt, refused or confirmed, is logged; a log that
// cannot be written refuses the spawn.
func confirmUnconfinedSpawn(repo string) error {
	cwd, _ := os.Getwd()
	logFile, err := openUnconfinedSpawnLog()
	if err != nil {
		return fmt.Errorf("spawn refused: --unconfined is logged and the log cannot be written: %w", err)
	}
	defer logFile.Close()
	record := func(outcome string, refusal error) error {
		rec := unconfinedSpawnRecord{
			Time: unconfinedSpawnNow().UTC().Format(time.RFC3339), Cwd: cwd, Repo: repo, Outcome: outcome,
		}
		if werr := appendUnconfinedSpawnRecord(logFile, rec); werr != nil {
			return errors.Join(refusal, fmt.Errorf("spawn refused: the --unconfined log could not be written: %w", werr))
		}
		return refusal
	}

	if !unconfinedSpawnIsTerminal() {
		return record("refused: no terminal", errors.New(unconfinedSpawnNoTerminal))
	}
	target := repo
	if target == "" {
		target = "this repo"
	}
	fmt.Fprintf(unconfinedSpawnStderr(),
		"spawn an UNCONFINED worker in %s? It runs outside any agent unit, with this user's full access (FDR 0035).\n"+
			"type %s to confirm: ", target, unconfinedSpawnConfirmWord)
	line, _ := bufio.NewReader(unconfinedSpawnConfirmIn()).ReadString('\n')
	if strings.TrimSpace(line) != unconfinedSpawnConfirmWord {
		fmt.Fprintln(unconfinedSpawnStderr(), "declined; nothing was spawned")
		return record("declined", errors.New("spawn refused: --unconfined was not confirmed; nothing was spawned"))
	}
	return record("confirmed", nil)
}

func openUnconfinedSpawnLog() (*os.File, error) {
	stateDir, err := confinement.DefaultStateDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(stateDir, "spinclass", unconfinedSpawnLogName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}

func appendUnconfinedSpawnRecord(f *os.File, rec unconfinedSpawnRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}
