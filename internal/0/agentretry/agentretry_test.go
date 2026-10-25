package agentretry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The failures observed on flic (spinclass#411), and ones that are not the
// agent's fault.
func TestAgentTrouble(t *testing.T) {
	for text, want := range map[string]bool{
		"papi: sign the login nonce: slot-9A sign ...: SSH agent signer: no slot-9A ecdsa key in the agent ($SSH_AUTH_SOCK)": true,
		"git@code.linenisgreat.com: Permission denied (publickey).\nfatal: Could not read from remote repository.":           true,
		"Could not open a connection to your authentication agent.":                                                          true,
		"sign_and_send_pubkey: signing failed for ECDSA \"card\" from agent: agent refused operation":                        true,
		"The agent has no identities.":                                   true,
		"fatal: repository 'https://forge.example.com/r.git/' not found": false,
		"mint-command: exit status 2: token quota exceeded":              false,
		"": false,
	} {
		if got := AgentTrouble(text); got != want {
			t.Errorf("AgentTrouble(%q) = %v, want %v", text, got, want)
		}
	}
}

func fastDelays(t *testing.T) *[]time.Duration {
	t.Helper()
	var slept []time.Duration
	prevDelays, prevSleep := Delays, sleep
	Delays = []time.Duration{2 * time.Second, 8 * time.Second}
	sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	t.Cleanup(func() { Delays, sleep = prevDelays, prevSleep })
	return &slept
}

var errAgent = errors.New("no slot-9A ecdsa key in the agent ($SSH_AUTH_SOCK)")

// A short agent outage is ridden out: retried with the bounded backoff, and
// the call succeeds once the agent is back.
func TestDoRetriesAgentTroubleUntilItClears(t *testing.T) {
	slept := fastDelays(t)
	calls := 0
	err := Do(context.Background(), func() (string, error) {
		calls++
		if calls < 3 {
			return "", errAgent
		}
		return "", nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("Do = %v after %d calls, want success on the third", err, calls)
	}
	if len(*slept) != 2 || (*slept)[0] != 2*time.Second || (*slept)[1] != 8*time.Second {
		t.Errorf("slept %v, want [2s 8s]", *slept)
	}
}

// A failure that is not the agent's is not retried and is returned as is.
func TestDoDoesNotRetryOtherFailures(t *testing.T) {
	fastDelays(t)
	calls := 0
	want := errors.New("fatal: repository not found")
	if err := Do(context.Background(), func() (string, error) { calls++; return "", want }); !errors.Is(err, want) || calls != 1 {
		t.Fatalf("Do = %v after %d calls, want the error once", err, calls)
	}
}

// An outage that outlasts the retries names the cause, never a bare
// "denied publickey", and still wraps the last error.
func TestDoNamesTheAgentWhenTheRetriesRunOut(t *testing.T) {
	fastDelays(t)
	calls := 0
	err := Do(context.Background(), func() (string, error) { calls++; return "", errAgent })
	if calls != 3 || !errors.Is(err, errAgent) {
		t.Fatalf("Do = %v after %d calls, want the agent error after 3", err, calls)
	}
	for _, want := range []string{"3 attempts", "ssh-agent", "ssh-add -l"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

// The trouble may show only in the command's output, not its error text.
func TestDoReadsTheOutputToo(t *testing.T) {
	fastDelays(t)
	calls := 0
	_ = Do(context.Background(), func() (string, error) {
		calls++
		return "git@forge: Permission denied (publickey).", errors.New("exit status 128")
	})
	if calls != 3 {
		t.Errorf("calls = %d, want 3: the publickey line is in the output", calls)
	}
}
