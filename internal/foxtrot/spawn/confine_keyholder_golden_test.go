package spawn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
)

// offSpecInput is a fully populated input with no key holder: the shape every
// session has when [keyholder] is off. Its marshalled spec is pinned in
// testdata/confined-spec-off.golden.json, generated from the builder BEFORE the
// key holder change, so "off leaves the spec byte-for-byte alone" is a test.
func offSpecInput() unitSpecInput {
	return unitSpecInput{
		rp:           worktree.ResolvedPath{AbsPath: "/w/repo/.worktrees/feat", SessionKey: "repo/feat"},
		operatorHome: "/home/op",
		stateDir:     "/state/spinclass",
		commonDir:    "/w/repo/.git",
		sshAuthSock:  "/run/user/1000/piggy-agent.sock",
		runtimeDir:   "/run/user/1000",
		wayland:      []string{"/run/user/1000/wayland-0"},
		egress:       []string{"example.org:443"},
		env:          map[string]string{"SPINCLASS_WORKTREE": "/w/repo/.worktrees/feat", "TMPDIR": "/w/repo/.worktrees/feat/.tmp"},
		handshakeDir: "/state/spinclass/handshake/abc",
	}
}

func TestConfinedSpecWithTheKeyHolderOffMatchesTheGolden(t *testing.T) {
	got, err := json.MarshalIndent(buildUnitSpec(offSpecInput()), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	golden := filepath.Join("testdata", "confined-spec-off.golden.json")
	want, err := os.ReadFile(golden)
	if err != nil {
		// The tests run in a sandbox that cannot write the tree: the golden is
		// copied from this output once, generated from the pre-change builder.
		t.Fatalf("no golden (%v); the current spec is:\n%s", err, got)
	}
	if string(got) != string(want) {
		t.Errorf("the confined spec with the key holder off changed:\n--- got\n%s\n--- want\n%s", got, want)
	}
}
