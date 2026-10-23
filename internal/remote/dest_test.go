package remote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

// A destination beginning with "-" is an ssh option, and options such as
// -oProxyCommand run local commands: such a remote is refused before ssh runs.
func optionLikeRemotes() []sweatfile.Remote {
	return []sweatfile.Remote{
		{Name: "devbox", SSH: "-oProxyCommand=touch SENTINEL"},
		{Name: "-oProxyCommand=touch SENTINEL"},
	}
}

func TestQueryHostRefusesAnOptionLikeDestinationWithoutRunningSSH(t *testing.T) {
	for _, r := range optionLikeRemotes() {
		argsFile := filepath.Join(t.TempDir(), "args")
		stubSSH(t, argsFile, "echo '[]'\nexit 0\n")

		_, err := QueryHost(context.Background(), r)
		if err == nil || !strings.Contains(err.Error(), `begins with "-"`) {
			t.Errorf("QueryHost(%+v) err = %v, want a refusal naming the leading \"-\"", r, err)
		}
		if _, statErr := os.Stat(argsFile); statErr == nil {
			t.Errorf("QueryHost(%+v) ran ssh for an option-like destination", r)
		}
	}
}

func TestAttachArgvRefusesAnOptionLikeDestination(t *testing.T) {
	for _, r := range optionLikeRemotes() {
		argv, err := AttachArgv(r, "crisp-catalpa")
		if err == nil || !strings.Contains(err.Error(), `begins with "-"`) || !strings.Contains(err.Error(), r.Name) {
			t.Errorf("AttachArgv(%+v) = %v, %v; want a refusal naming the remote and the leading \"-\"", r, argv, err)
		}
		if argv != nil {
			t.Errorf("AttachArgv(%+v) returned an argv despite refusing: %v", r, argv)
		}
	}
}

// The default templates end option parsing before the destination, so even a
// destination the check missed would not be read as an option.
func TestDefaultSSHArgvEndsOptionsBeforeTheDestination(t *testing.T) {
	argv, err := AttachArgv(sweatfile.Remote{Name: "devbox"}, "crisp-catalpa")
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) < 4 || argv[2] != "--" || argv[3] != "devbox" {
		t.Errorf("default attach argv = %v, want \"--\" right before the destination", argv)
	}

	argsFile := filepath.Join(t.TempDir(), "args")
	stubSSH(t, argsFile, "echo '[]'\nexit 0\n")
	if _, err := QueryHost(context.Background(), sweatfile.Remote{Name: "devbox"}); err != nil {
		t.Fatal(err)
	}
	if got := recordedArgs(t, argsFile); len(got) < 2 || got[0] != "--" || got[1] != "devbox" {
		t.Errorf("query ssh argv = %q, want \"--\" right before the destination", got)
	}
}
