package basebranch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/agentretry"
)

// flakySSH points origin at an ssh URL and puts an `ssh` on PATH (the fetch
// runs ssh by name) that fails like a missing agent key for its first
// `failures` connections, then serves the local bare upstream by running the
// remote command, git-upload-pack, itself (spinclass#411).
func flakySSH(t *testing.T, clone, upstream string, failures int) {
	t.Helper()
	dir := t.TempDir()
	counter := filepath.Join(dir, "runs")
	body := fmt.Sprintf(`#!/bin/sh
n=$(cat %[1]q 2>/dev/null || echo 0); n=$((n+1)); echo $n > %[1]q
if [ $n -le %[2]d ]; then echo "git@fake.invalid: Permission denied (publickey)." >&2; exit 255; fi
for last; do :; done
exec sh -c "$last"
`, counter, failures)
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	mustGit(t, clone, "remote", "set-url", "origin", "ssh://git@fake.invalid"+upstream)

	prev := agentretry.Delays
	agentretry.Delays = []time.Duration{0, 0}
	t.Cleanup(func() { agentretry.Delays = prev })
}

// A short agent outage at session creation no longer fails it: the fetch is
// retried and the session is cut from the fetched tip.
func TestFreshenRidesOutAShortAgentOutage(t *testing.T) {
	upstream, clone := fixture(t)
	flakySSH(t, clone, upstream, 2)
	tip := advanceUpstream(t, upstream)

	res, err := Freshen(context.Background(), clone, false, true)
	if err != nil {
		t.Fatalf("Freshen: %v", err)
	}
	if res.BaseSha != tip {
		t.Errorf("BaseSha = %q, want the upstream tip %q", res.BaseSha, tip)
	}
}

// An outage that outlasts the retries is still a stale base, now named as the
// agent's rather than a bare "Permission denied (publickey)".
func TestFreshenNamesTheAgentWhenTheOutageOutlastsTheRetries(t *testing.T) {
	upstream, clone := fixture(t)
	flakySSH(t, clone, upstream, 99)

	_, err := Freshen(context.Background(), clone, false, true)
	if !errors.Is(err, ErrStaleBase) {
		t.Fatalf("want ErrStaleBase, got %v", err)
	}
	for _, want := range []string{"Permission denied (publickey)", "3 attempts", "ssh-agent", "ssh-add -l"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}
