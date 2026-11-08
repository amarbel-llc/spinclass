// Package testring builds the stub `ringmaster` executable that tests point
// $RINGMASTER_BIN at (clown RFC-0015), so the several packages that exercise
// the job-wakeup path share one builder instead of four near-identical scripts
// (spinclass#112). Like testgit it is ordinary code that only tests import.
package testring

import (
	"os"
	"path/filepath"
	"testing"
)

// Options shapes the stub. The zero value records every invocation and exits 0.
type Options struct {
	// Fail makes every call exit 1.
	Fail bool
	// Answers maps a ringmaster subcommand (the stub's $1) to the shell
	// word echoed when it is called: "job-deadbeef" for a literal, or
	// `"$3-stub"` to derive it from the arguments.
	Answers map[string]string
	// Delays maps a subcommand to a `sleep` argument run before the call is
	// recorded, to make a race certain rather than load-dependent.
	Delays map[string]string
}

// Stub is an installed stub.
type Stub struct {
	// InvocationsDir holds one file per invocation: the argv, one element per
	// line. One file each keeps concurrent callers from interleaving (a shell
	// printf is not a single write() in dash).
	InvocationsDir string
	// Script is the stub's path, already set as $RINGMASTER_BIN.
	Script string
	// ClownBin is the fake path set as $CLOWN_BIN so clown.Enabled() is true.
	ClownBin string
}

// Install writes the stub into a temp dir and points $RINGMASTER_BIN at it and
// $CLOWN_BIN at a fake clown path. invocationsDir is created by the first
// invocation, as the stub runs.
func Install(t *testing.T, o Options) Stub {
	t.Helper()
	dir := t.TempDir()
	return InstallAt(t, filepath.Join(dir, "inv"), o)
}

// InstallAt is Install recording into the given invocations directory.
func InstallAt(t *testing.T, invocationsDir string, o Options) Stub {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "ringmaster")
	body := "#!/bin/sh\n"
	for sub, secs := range o.Delays {
		body += "if [ \"$1\" = " + sub + " ]; then sleep " + secs + "; fi\n"
	}
	body += "mkdir -p " + invocationsDir + " 2>/dev/null\n" +
		"printf '%s\\n' \"$@\" > \"$(mktemp " + invocationsDir + "/inv.XXXXXX)\"\n"
	for sub, word := range o.Answers {
		body += "if [ \"$1\" = " + sub + " ]; then echo " + word + "; fi\n"
	}
	exit := "0"
	if o.Fail {
		exit = "1"
	}
	body += "exit " + exit + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub ringmaster: %v", err)
	}
	clownBin := filepath.Join(dir, "clown")
	t.Setenv("CLOWN_BIN", clownBin)
	t.Setenv("RINGMASTER_BIN", script)
	return Stub{InvocationsDir: invocationsDir, Script: script, ClownBin: clownBin}
}
