package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubClownBin writes an executable shell script that records its argv (one
// element per line) into argsFile and exits with the given status, returning
// the script path for CLOWN_BIN injection. Mirrors the helper in
// internal/chat's tests.
func stubClownBin(t *testing.T, argsFile string, ok bool) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "clown")
	exit := "1"
	if ok {
		exit = "0"
	}
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\nexit " + exit + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub clown: %v", err)
	}
	return script
}

// chatWakeEnv isolates the chatroom store and pins the session identity and
// wake mode for a handler test.
func chatWakeEnv(t *testing.T, mode string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPINCLASS_SESSION_ID", "spinclass/tester")
	t.Setenv("SPINCLASS_CHAT_WAKE", mode)
}

// storedMessageCount counts committed message files in the isolated chatroom.
func storedMessageCount(t *testing.T) int {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "spinclass", "chatroom")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read chatroom dir: %v", err)
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".json") {
			n++
		}
	}
	return n
}

func TestHandleChatSendClownModeEmitsWake(t *testing.T) {
	chatWakeEnv(t, "clown")
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("CLOWN_BIN", stubClownBin(t, argsFile, true))

	args, _ := json.Marshal(map[string]string{"message": "hi", "to": "clown/peer"})
	res, err := handleChatSend(context.Background(), args, nil)
	if err != nil {
		t.Fatalf("handleChatSend: %v", err)
	}
	if res.IsErr {
		t.Fatalf("result is error: %s", res.Text)
	}

	data, rerr := os.ReadFile(argsFile)
	if rerr != nil {
		t.Fatalf("stub clown was not invoked: %v", rerr)
	}
	argv := string(data)
	for _, want := range []string{"job", "message", "--target", "clown/peer", "--from", "spinclass/tester"} {
		if !strings.Contains(argv, want+"\n") {
			t.Fatalf("emit argv missing %q:\n%s", want, argv)
		}
	}
	if got := storedMessageCount(t); got != 1 {
		t.Fatalf("stored messages: got %d, want 1", got)
	}
}

func TestHandleChatSendEmitFailureDoesNotFailSend(t *testing.T) {
	chatWakeEnv(t, "clown")
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("CLOWN_BIN", stubClownBin(t, argsFile, false))

	args, _ := json.Marshal(map[string]string{"message": "hi", "to": "clown/peer"})
	res, err := handleChatSend(context.Background(), args, nil)
	if err != nil {
		t.Fatalf("handleChatSend: %v", err)
	}
	if res.IsErr {
		t.Fatalf("emit failure must not fail the send (store write succeeded): %s", res.Text)
	}
	if !strings.Contains(res.Text, "wake emit failed") {
		t.Fatalf("emit failure not surfaced in result text: %q", res.Text)
	}
	if got := storedMessageCount(t); got != 1 {
		t.Fatalf("stored messages: got %d, want 1", got)
	}
}

func TestHandleChatSendLegacyModeDoesNotInvokeClown(t *testing.T) {
	chatWakeEnv(t, "legacy")
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("CLOWN_BIN", stubClownBin(t, argsFile, true))

	args, _ := json.Marshal(map[string]string{"message": "hi", "to": "clown/peer"})
	res, err := handleChatSend(context.Background(), args, nil)
	if err != nil {
		t.Fatalf("handleChatSend: %v", err)
	}
	if res.IsErr {
		t.Fatalf("result is error: %s", res.Text)
	}
	if _, statErr := os.Stat(argsFile); statErr == nil {
		t.Fatal("legacy mode invoked clown")
	}
}

func TestRunChatWatchClownModeReturnsImmediately(t *testing.T) {
	chatWakeEnv(t, "clown")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runChatWatch(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runChatWatch in clown mode: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runChatWatch blocked in clown mode; want immediate return (clown job-watch owns the push path)")
	}
}
