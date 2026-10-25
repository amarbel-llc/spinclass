package auth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/agentretry"
)

func noBackoff(t *testing.T) {
	t.Helper()
	prev := agentretry.Delays
	agentretry.Delays = []time.Duration{0, 0}
	t.Cleanup(func() { agentretry.Delays = prev })
}

// flakyMint is a mint-command that fails like the card signer did on flic
// (spinclass#411) for its first `failures` runs, then prints a token.
func flakyMint(t *testing.T, failures int) string {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "runs")
	return fmt.Sprintf(`n=$(cat %[1]q 2>/dev/null || echo 0); n=$((n+1)); echo $n > %[1]q
if [ $n -le %[2]d ]; then echo "papi: sign the login nonce: SSH agent signer: no slot-9A ecdsa key in the agent (\$SSH_AUTH_SOCK)" >&2; exit 1; fi
echo tok`, counter, failures)
}

// A short agent outage no longer fails the mint: it is retried and succeeds
// once the agent is back.
func TestMintRidesOutAShortAgentOutage(t *testing.T) {
	noBackoff(t)
	_, wtPath, id := setupRepo(t)
	outcome, err := Mint(context.Background(), authSweatfile(flakyMint(t, 2), "true"), id, "")
	if err != nil || !outcome.Minted {
		t.Fatalf("Mint = %+v, %v; want minted on the third try", outcome, err)
	}
	if !Minted(wtPath) {
		t.Error("no credential written")
	}
}

// An outage that outlasts the retries names the agent as the cause.
func TestMintNamesTheAgentWhenTheOutageOutlastsTheRetries(t *testing.T) {
	noBackoff(t)
	_, wtPath, id := setupRepo(t)
	_, err := Mint(context.Background(), authSweatfile(flakyMint(t, 99), "true"), id, "")
	if err == nil {
		t.Fatal("expected a failed mint")
	}
	for _, want := range []string{"[auth] mint-command failed", "3 attempts", "ssh-agent", "ssh-add -l"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if Minted(wtPath) {
		t.Error("a failed mint wrote a credential")
	}
}

// A mint failure that is not the agent's runs once.
func TestMintDoesNotRetryANonAgentFailure(t *testing.T) {
	noBackoff(t)
	_, _, id := setupRepo(t)
	counter := filepath.Join(t.TempDir(), "runs")
	cmd := fmt.Sprintf(`echo x >> %q; echo "token quota exceeded" >&2; exit 2`, counter)
	if _, err := Mint(context.Background(), authSweatfile(cmd, "true"), id, ""); err == nil {
		t.Fatal("expected a failed mint")
	}
	data, _ := os.ReadFile(counter)
	if runs := strings.Count(string(data), "x"); runs != 1 {
		t.Errorf("mint-command ran %d times, want 1", runs)
	}
}
