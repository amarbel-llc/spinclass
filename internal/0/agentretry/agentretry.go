// Package agentretry rides out a short outage of the operator's ssh-agent
// around the operations that need it (spinclass#411): the [auth] mint and the
// base-branch fetch at session creation. It retries only failures that look
// like the agent's, with a bounded backoff, and names that cause when the
// retries run out.
package agentretry

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Delays are the waits between attempts: len(Delays)+1 attempts in all.
var Delays = []time.Duration{2 * time.Second, 8 * time.Second}

var sleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// agentTroubleMarkers are lowercase fragments of the messages ssh, git and the
// card signer print when the agent has no usable key or cannot be reached.
var agentTroubleMarkers = []string{
	"permission denied (publickey",
	"in the agent",
	"ssh_auth_sock",
	"authentication agent",
	"agent refused operation",
	"agent has no identities",
}

// AgentTrouble reports whether failure text looks like the ssh-agent had no
// usable key or could not be reached.
func AgentTrouble(text string) bool {
	lower := strings.ToLower(text)
	for _, m := range agentTroubleMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// Hint names the cause once the retries run out.
const Hint = "the ssh-agent offered no usable key or could not be reached: is the (forwarded) agent up? `ssh-add -l` lists what it holds. This is the agent, not the credential policy; retry once it is back"

// Do runs fn, which returns the operation's output and error, retrying while
// the error or the output looks like agent trouble (AgentTrouble), waiting
// Delays between attempts. Any other failure is returned at once, unchanged.
// When agent trouble outlasts the retries, the last error is wrapped with the
// attempt count and Hint.
func Do(ctx context.Context, fn func() (string, error)) error {
	attempts := len(Delays) + 1
	var err error
	for i := 0; i < attempts; i++ {
		var out string
		out, err = fn()
		if err == nil {
			return nil
		}
		if !AgentTrouble(err.Error()) && !AgentTrouble(out) {
			return err
		}
		if i < len(Delays) {
			if serr := sleep(ctx, Delays[i]); serr != nil {
				return err
			}
		}
	}
	return fmt.Errorf("%w (after %d attempts: %s)", err, attempts, Hint)
}
