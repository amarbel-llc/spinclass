//go:build linux

package confinement

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recipeMappingLine is the line `just debug-bridge-halves-proof` writes as the
// principal mapping: spinclass has no CLI that runs its confined-launch writer
// (RecordPrincipalFromUnit runs inside `sc spawn`), so the recipe cannot call it.
// This test keeps that line equal, once decoded, to what the real writer writes
// from a unit record, so a change to the mapping shape fails here and not in a
// live run.
const recipeMappingLine = `{"version":1,"principal":"%s","session_key":"%s","worktree":"%s","unit":"%s"}` + "\n"

func TestRecipeMappingLineIsWhatTheRealWriterWrites(t *testing.T) {
	dir := t.TempDir()
	const (
		principal = "halves-1"
		key       = "proj/calm-birch"
		wt        = "/wt/halves"
		unit      = "clown-agent-bh1a.service"
	)
	rec := fmt.Sprintf(`{"version":1,"capabilities":["bridge-v1"],"unit":%q,"principal":%q,"cgroup":"/user.slice/clown-agent.slice/%s","uid":%d,"bridge":"recorded"}`+"\n",
		unit, principal, unit, os.Getuid())
	recPath := UnitRecordPath(dir, wt)
	if err := os.MkdirAll(filepath.Dir(recPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recPath, []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := RecordPrincipalFromUnit(Host{}, dir, key, wt)
	if err != nil {
		t.Fatalf("the real writer refused the record the recipe's verb writes: %v", err)
	}
	if got != principal {
		t.Fatalf("recorded principal %q, want %q", got, principal)
	}
	real, err := os.ReadFile(PrincipalMappingPath(dir, principal))
	if err != nil {
		t.Fatal(err)
	}
	var fromReal, fromRecipe PrincipalMapping
	if err := json.Unmarshal(real, &fromReal); err != nil {
		t.Fatalf("the real mapping does not parse: %v", err)
	}
	if err := json.Unmarshal([]byte(fmt.Sprintf(recipeMappingLine, principal, key, wt, unit)), &fromRecipe); err != nil {
		t.Fatalf("the recipe's mapping line does not parse: %v", err)
	}
	if fromReal != fromRecipe {
		t.Errorf("the recipe writes %+v, the real writer writes %+v", fromRecipe, fromReal)
	}
	// And the file the recipe writes must load through the real reader.
	if err := os.WriteFile(PrincipalMappingPath(dir, "recipe-p"), []byte(fmt.Sprintf(recipeMappingLine, "recipe-p", key, wt, unit)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrincipalMapping(Host{}, dir, "recipe-p"); err != nil {
		t.Errorf("the recipe's mapping line is refused by the real reader: %v", err)
	}
	// The recipe text still carries this exact line (skipped when the justfile is not in the test's tree).
	if jf, err := os.ReadFile(filepath.Join("..", "..", "..", "justfile")); err == nil {
		want := strings.TrimSuffix(recipeMappingLine, "\n")
		if !strings.Contains(string(jf), "printf '"+want+`\n' "$principal"`) {
			t.Errorf("the recipe no longer writes the mapping line this test pins: %q", want)
		}
	}
}
