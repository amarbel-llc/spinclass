package hookrun

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

// The scope prefix (systemd-run --user --scope …) runs under the gate's
// scrubbed environment, so it must still find the user manager: the variables
// it needs are kept, and the hook starts inside the scope.
func TestConfinedPreMergeScopePrefixSeesTheUserBusVariables(t *testing.T) {
	restore := SetConfinedGateTools("git", "sh")
	defer restore()
	out := filepath.Join(t.TempDir(), "prefix-env")
	marker := filepath.Join(t.TempDir(), "ran")
	// A stand-in for systemd-run that records its own environment, then runs
	// its payload exactly as systemd-run would.
	prefix := []string{"sh", "-c", `export -p > "$0"; exec "$@"`, out}
	ctx := fakeScope(t, prefix)
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/4242")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/4242/bus")

	require := true
	sf := sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{RequireHookScope: &require}}
	var buf bytes.Buffer
	if err := ConfinedPreMergeInDir(ctx, sf, ": > "+marker, t.TempDir(), nil, &buf); err != nil {
		t.Fatalf("scoped confined gate: %v\n%s", err, buf.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the hook did not run inside the scope")
	}
	env, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`XDG_RUNTIME_DIR="/run/user/4242"`, `DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/4242/bus"`} {
		if !strings.Contains(string(env), want) {
			t.Errorf("the scope prefix lacks %s:\n%s", want, env)
		}
	}
	if strings.Contains(string(env), "export SSH_AUTH_SOCK=") {
		t.Error("the scope prefix inherited SSH_AUTH_SOCK")
	}
}
