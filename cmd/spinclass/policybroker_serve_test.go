package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/policybroker"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

// The plan resolves to "no broker" whenever clown did not name a unix socket
// path, so a serve outside clown or in a unit (host:port relay form) never
// listens; nothing else is read in those cases.
func TestResolvePolicyBrokerWithoutASocketPath(t *testing.T) {
	for name, value := range map[string]string{"unset": "", "relay address": "127.0.0.1:19001", "relative": "broker.sock"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(policybroker.EnvAddr, value)
			plan := resolvePolicyBroker()
			if plan.Path != "" || plan.Reason == "" {
				t.Errorf("plan = %+v, want no broker with a reason", plan)
			}
		})
	}
}

func stubPolicyBrokerPlan(t *testing.T, plan policyBrokerPlan) {
	t.Helper()
	prev := policyBrokerResolve
	policyBrokerResolve = func() policyBrokerPlan { return plan }
	t.Cleanup(func() { policyBrokerResolve = prev })
}

func shortSocketPath(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "pb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return filepath.Join(d, "broker.sock")
}

// Serve does not listen without a configured, compilable policy: no stanza,
// an entry that does not parse, or no unmatched each leave the socket absent
// so clown relays nothing and moxy keeps its static floor.
func TestStartPolicyBrokerDeclinesWithoutAUsablePolicy(t *testing.T) {
	for name, policy := range map[string]sweatfileio.FolioPolicy{
		"unconfigured":   {},
		"bad entry":      {Configured: true, Read: []string{"pwd:../x"}, Unmatched: "ask"},
		"no unmatched":   {Configured: true, Read: []string{"pwd:"}},
		"bad unmatched":  {Configured: true, Unmatched: "allow"},
		"bad deny entry": {Configured: true, Deny: []string{"nowhere:x"}, Unmatched: "deny"},
	} {
		t.Run(name, func(t *testing.T) {
			path := shortSocketPath(t)
			stubPolicyBrokerPlan(t, policyBrokerPlan{Path: path, SessionKey: "r/b", Policy: policy, StateDir: t.TempDir(), Worktree: t.TempDir()})
			if stop := startPolicyBroker(context.Background(), "test"); stop != nil {
				stop()
				t.Fatal("serve must not listen")
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("socket should not exist, lstat: %v", err)
			}
		})
	}
	stubPolicyBrokerPlan(t, policyBrokerPlan{Reason: "no socket named"})
	if stop := startPolicyBroker(context.Background(), "test"); stop != nil {
		stop()
		t.Error("a plan without a path must not listen")
	}
}

// With a configured policy serve listens on clown's path, answers the hello
// with the exact session key, and answers checks from the policy: a read
// inside the worktree is allowed by pwd:, a blob is allowed by madder:
// (ruled 2026-11-02), anything else, a malformed blob URI included, gets the
// policy's unmatched verdict. Stop removes the socket.
func TestStartPolicyBrokerListensAndAnswersThePolicy(t *testing.T) {
	path := shortSocketPath(t)
	worktree, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stubPolicyBrokerPlan(t, policyBrokerPlan{
		Path: path, SessionKey: "spinclass/light-cedar",
		Policy:   sweatfileio.FolioPolicy{Configured: true, Read: []string{"pwd:", "madder:"}, Unmatched: "deny"},
		StateDir: t.TempDir(), Worktree: worktree,
	})
	stop := startPolicyBroker(context.Background(), "0.0.0-test")
	if stop == nil {
		t.Fatal("serve should listen")
	}
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(c)
	read := func() policybroker.Message {
		t.Helper()
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var m policybroker.Message
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	send := func(m policybroker.Message) {
		t.Helper()
		b, _ := json.Marshal(m)
		if _, err := c.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	send(policybroker.Message{V: 1, Type: "hello", Client: "test", Capabilities: []string{"check"}, Subjects: []string{"spinclass/light-cedar"}})
	if h := read(); h.Type != "hello" || h.SessionKey != "spinclass/light-cedar" || h.Broker != "spinclass serve 0.0.0-test" {
		t.Fatalf("hello = %+v", h)
	}
	for _, tc := range []struct {
		id      int64
		res     policybroker.Resource
		verdict string
	}{
		{1, policybroker.Resource{Kind: "file", URI: "file:///etc/hostname"}, "deny"},
		{2, policybroker.Resource{Kind: "file", URI: "file://" + filepath.Join(worktree, "a.go")}, "allow"},
		{3, policybroker.Resource{Kind: "blob", URI: "madder://blobs/blake2b256-x"}, "allow"},
		{4, policybroker.Resource{Kind: "blob", URI: "madder://blobs/"}, "deny"},
		{5, policybroker.Resource{Kind: "blob", URI: "file:///etc/hostname"}, "deny"},
	} {
		send(policybroker.Message{V: 1, Type: "check", ID: tc.id, Subject: "spinclass/light-cedar", Op: "read", Resource: &tc.res, DeadlineMS: 100})
		if v := read(); v.Type != "verdict" || v.ID != tc.id || v.Verdict != tc.verdict {
			t.Errorf("%s: verdict = %+v, want %s", tc.res.URI, v, tc.verdict)
		}
	}
	stop()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stop should remove the socket, lstat: %v", err)
	}
}
