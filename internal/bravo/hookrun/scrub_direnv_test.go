package hookrun_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	. "code.linenisgreat.com/spinclass/internal/bravo/hookrun"
)

func envOfRepair(t *testing.T, ctx context.Context) string {
	t.Helper()
	cmd := "env"
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{Repair: &cmd}}
	var out bytes.Buffer
	if err := Repair(ctx, sf, t.TempDir(), &out); err != nil {
		t.Fatalf("Repair: %v", err)
	}
	return out.String()
}

// The regenerate phase runs its hook without the orchestrator's direnv state;
// every other repair keeps the inherited environment, as before.
func TestWithScrubbedDirenvDropsDirenvStateFromTheHooksEnvironment(t *testing.T) {
	t.Setenv("DIRENV_DIR", "-/somewhere")
	t.Setenv("DIRENV_DIFF", "x")
	t.Setenv("IN_NIX_SHELL", "impure")
	t.Setenv("KEEP_ME", "yes")

	scrubbed := envOfRepair(t, WithScrubbedDirenv(context.Background()))
	for _, name := range []string{"DIRENV_DIR=", "DIRENV_DIFF=", "IN_NIX_SHELL="} {
		if strings.Contains(scrubbed, name) {
			t.Errorf("%s reached a scrubbed hook:\n%s", name, scrubbed)
		}
	}
	if !strings.Contains(scrubbed, "KEEP_ME=yes") {
		t.Error("the scrub dropped an unrelated variable")
	}

	plain := envOfRepair(t, context.Background())
	if !strings.Contains(plain, "DIRENV_DIR=") || !strings.Contains(plain, "IN_NIX_SHELL=impure") {
		t.Errorf("an ordinary repair no longer inherits the environment:\n%s", plain)
	}
}
