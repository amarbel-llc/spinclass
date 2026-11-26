//go:build linux

package confinement

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The line `just debug-bridge-halves-proof` writes as the principal mapping is a
// printf template in testdata/halves-mapping-line.json: the recipe reads that
// file, and this test reads the same file, so there is ONE source for both.
// Spinclass has no CLI that runs its confined-launch writer
// (RecordPrincipalFromUnit runs inside `sc spawn`), so the recipe cannot call
// it; this test keeps the template equal, once decoded, to what the real writer
// writes from a unit record, so a change to the mapping shape fails here and not
// in a live run.
const recipeMappingTemplatePath = "testdata/halves-mapping-line.json"

func recipeMappingLine(t *testing.T, principal, key, wt, unit string) []byte {
	t.Helper()
	tmpl, err := os.ReadFile(recipeMappingTemplatePath)
	if err != nil {
		t.Fatalf("the recipe's mapping template: %v", err)
	}
	return []byte(fmt.Sprintf(string(bytes.TrimSuffix(tmpl, []byte("\n"))), principal, key, wt, unit) + "\n")
}

// strictMapping decodes a mapping and refuses unknown fields, so a field the
// real struct dropped or renamed cannot hide in the template.
func strictMapping(b []byte) (PrincipalMapping, error) {
	var m PrincipalMapping
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	err := dec.Decode(&m)
	return m, err
}

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
	fromReal, err := strictMapping(real)
	if err != nil {
		t.Fatalf("the real mapping does not decode strictly: %v", err)
	}
	line := recipeMappingLine(t, principal, key, wt, unit)
	fromRecipe, err := strictMapping(line)
	if err != nil {
		t.Fatalf("the recipe's mapping line %q does not decode strictly into the real struct: %v", line, err)
	}
	if fromReal != fromRecipe {
		t.Errorf("the recipe writes %+v, the real writer writes %+v", fromRecipe, fromReal)
	}
	// Every field of the real struct must be present in the line: a template that
	// leaves one out decodes to its zero value and would otherwise slip through
	// when the real writer's value is also empty.
	if fromRecipe.Version == 0 || fromRecipe.Principal == "" || fromRecipe.SessionKey == "" || fromRecipe.Worktree == "" || fromRecipe.Unit == "" {
		t.Errorf("the recipe's mapping line leaves a field empty: %+v", fromRecipe)
	}
	// And the file the recipe writes must load through the real reader.
	if err := os.WriteFile(PrincipalMappingPath(dir, "recipe-p"), recipeMappingLine(t, "recipe-p", key, wt, unit), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrincipalMapping(Host{}, dir, "recipe-p"); err != nil {
		t.Errorf("the recipe's mapping line is refused by the real reader: %v", err)
	}
}
