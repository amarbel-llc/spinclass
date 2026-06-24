package direnv_test

import (
	"reflect"
	"testing"

	"github.com/amarbel-llc/spinclass/internal/direnv"
	"github.com/amarbel-llc/spinclass/internal/embeds"
)

// pinDirenv sets the build-time direnv pin to path and restores the prior pins
// on cleanup. embeds.Set is a positional (madder, direnv, dodder) triple, so the
// other two are read and restored unchanged.
func pinDirenv(t *testing.T, path string) {
	t.Helper()
	prevMadder, prevDirenv, prevDodder := embeds.MadderBin(), embeds.DirenvBin(), embeds.DodderBin()
	embeds.Set(prevMadder, path, prevDodder)
	t.Cleanup(func() { embeds.Set(prevMadder, prevDirenv, prevDodder) })
}

// Resolve prefers the build-time pin over a PATH lookup.
func TestResolvePrefersPin(t *testing.T) {
	pinDirenv(t, "/pinned/direnv")
	got, ok := direnv.Resolve()
	if !ok {
		t.Fatalf("Resolve() ok = false, want true with a pin set")
	}
	if got != "/pinned/direnv" {
		t.Errorf("Resolve() = %q, want the pinned path", got)
	}
}

// WrapExec prepends `direnv exec <dir>` to the command argv. `direnv exec`
// loads DIR's .envrc but does not cd, so the caller's cmd.Dir governs cwd.
func TestWrapExec(t *testing.T) {
	got := direnv.WrapExec("/bin/direnv", "/work/tree", []string{"sh", "-c", "echo hi"})
	want := []string{"/bin/direnv", "exec", "/work/tree", "sh", "-c", "echo hi"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WrapExec() = %v, want %v", got, want)
	}
}
