package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// [[permissions.stop-now-allow]]: the operator's list of sessions whose stop
// now needs no prompt, read from trusted layers only and journalled per use.

func stopNowAllowFixture(t *testing.T, entry string) (wtPath string) {
	t.Helper()
	_, wtPath = restartSelfFixture(t, false)
	writeGlobalSweatfile(t, os.Getenv("HOME"), restartHandoffTOML+"\n[[permissions.stop-now-allow]]\n"+entry)
	return wtPath
}

func TestStopNowAllowListsByPrincipalAndBySession(t *testing.T) {
	for name, entry := range map[string]string{
		"principal": `principal = "` + selfPrincipal + `"` + "\n",
		"session":   `session = "worker/kid"` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			wt := stopNowAllowFixture(t, entry)
			if _, _, err := stopNowAllowedFor(os.Getenv("HOME"), wt, selfPrincipal); err != nil {
				t.Fatalf("a listed caller must be allowed: %v", err)
			}
		})
	}
}

func TestStopNowAllowIgnoresExpiredInvalidAndOtherEntries(t *testing.T) {
	for name, entry := range map[string]string{
		"expired": `principal = "` + selfPrincipal + `"` + "\nexpires = \"2000-01-01T00:00:00Z\"\n",
		"invalid": `principal = "` + selfPrincipal + `"` + "\nsession = \"worker/kid\"\n",
		"other":   `principal = "someone-else"` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			wt := stopNowAllowFixture(t, entry)
			if _, _, err := stopNowAllowedFor(os.Getenv("HOME"), wt, selfPrincipal); err == nil {
				t.Fatal("the caller must not be allowed")
			}
		})
	}
}

// A repo-layer entry is writable by the agent, so it must not bless anyone.
func TestStopNowAllowIgnoresARepoLayerEntry(t *testing.T) {
	repoPath, wt := restartSelfFixture(t, true)
	body := "[[permissions.stop-now-allow]]\nprincipal = \"" + selfPrincipal + "\"\n"
	if err := os.WriteFile(filepath.Join(repoPath, "sweatfile"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stopNowAllowedFor(os.Getenv("HOME"), wt, selfPrincipal); err == nil {
		t.Fatal("a repo-layer entry must not allow")
	}
}

func TestStopNowAllowJournalsEachUse(t *testing.T) {
	wt := stopNowAllowFixture(t, `principal = "`+selfPrincipal+`"`+"\nnote = \"canary\"\n")
	if err := journalStopNowUse(map[string]any{"child": "worker/other", "when": "now"}, wt); err != nil {
		t.Fatalf("journal: %v", err)
	}
	log, err := os.ReadFile(filepath.Join(os.Getenv("XDG_STATE_HOME"), "spinclass", stopNowAllowLogName))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"outcome":"allowed"`, selfPrincipal, `"target":"worker/other"`, `"note":"canary"`} {
		if !strings.Contains(string(log), want) {
			t.Errorf("journal line %q is missing %q", log, want)
		}
	}
}

// At the allow point the list must still admit the call, or the hook asks.
func TestStopNowAllowJournalRefusesAnUnlistedCaller(t *testing.T) {
	wt := stopNowAllowFixture(t, `principal = "someone-else"`+"\n")
	if err := journalStopNowUse(map[string]any{"when": "now"}, wt); err == nil {
		t.Fatal("an unlisted caller must make the journal fail, so the hook asks")
	}
}
